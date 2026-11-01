package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
)

// fakeScreencap installs a shell script in place of claude-remote-screencap.
// It records that it ran, prints the given stderr lines, then loops over
// frames.bin (length-prefixed access units) on stdout.
func fakeScreencap(t *testing.T, stderrLines ...string) (dir string) {
	t.Helper()
	skipWithoutSh(t)
	dir = t.TempDir()
	// One tiny "IDR" access unit: SPS, PPS and a slice, each Annex-B.
	au := []byte{0, 0, 0, 1, 0x67, 0x42, 0xe0, 0x1f, 0, 0, 0, 1, 0x68, 0xce, 0x3c, 0x80, 0, 0, 0, 1, 0x65, 0x88, 0x84, 0x00}
	var frame bytes.Buffer
	binary.Write(&frame, binary.BigEndian, uint32(len(au)))
	frame.Write(au)
	if err := os.WriteFile(filepath.Join(dir, "frames.bin"), frame.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	var sb strings.Builder
	sb.WriteString("#!/bin/sh\n")
	sb.WriteString("cd \"$(dirname \"$0\")\"\n")
	sb.WriteString("echo \"$@\" >> ran.txt\n")
	sb.WriteString("if [ \"$1\" = state ]; then echo '{\"locked\":false,\"displays\":[]}'; exit 0; fi\n")
	for _, l := range stderrLines {
		sb.WriteString("echo '" + l + "' >&2\n")
	}
	sb.WriteString("while :; do cat frames.bin; sleep 0.03; done\n")
	path := filepath.Join(dir, "screencap")
	if err := os.WriteFile(path, []byte(sb.String()), 0o700); err != nil {
		t.Fatal(err)
	}
	old := screencapPath
	screencapPath = func() string { return path }
	t.Cleanup(func() { screencapPath = old })
	return dir
}

const (
	geometryLine = `{"event":"geometry","display":{"id":1,"frame":{"x":0,"y":0,"w":1512,"h":982}},"desktop":{"x":0,"y":-176,"w":3432,"h":1158},"width":1920,"height":1246}`
	unlockedLine = `{"event":"locked","locked":false}`
	lockedLine   = `{"event":"locked","locked":true}`
)

func videoTestServer(t *testing.T) (*Server, string) {
	t.Helper()
	dir := t.TempDir()
	cfg := &Config{Port: 0, AllowedDirs: []string{dir}, ClaudePath: stubClaude, DataDir: dir}
	auth := NewAuth(cfg.SecretPath())
	auth.GenerateSecret()
	s := NewServer(cfg, auth)
	s.registerRoutes()
	t.Cleanup(s.video.Stop)
	jwt, err := auth.IssueJWT("test-device")
	if err != nil {
		t.Fatal(err)
	}
	return s, jwt
}

func offerRequest(path, body, origin, cookie, contentType string) *http.Request {
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: authCookieName, Value: cookie})
	}
	return req
}

func ranScreencap(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, "ran.txt"))
	return err == nil
}

func TestScreenOfferRejections(t *testing.T) {
	const okBody = `{"sdp":"v=0"}`
	cases := []struct {
		name, body, origin, ctype string
		auth                      bool
		want                      int
	}{
		{"no cookie", okBody, "http://example.com", "application/json", false, http.StatusUnauthorized},
		{"foreign origin", okBody, "https://evil.example", "application/json", true, http.StatusForbidden},
		{"no origin", okBody, "", "application/json", true, http.StatusForbidden},
		{"form post", okBody, "http://example.com", "text/plain", true, http.StatusBadRequest},
		{"empty sdp", `{"sdp":""}`, "http://example.com", "application/json", true, http.StatusBadRequest},
		{"flag as display", `{"sdp":"v=0","display":"--fps"}`, "http://example.com", "application/json", true, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := fakeScreencap(t, geometryLine, unlockedLine)
			s, jwt := videoTestServer(t)
			if !tc.auth {
				jwt = ""
			}
			w := httptest.NewRecorder()
			s.mux.ServeHTTP(w, offerRequest("/api/screen/offer", tc.body, tc.origin, jwt, tc.ctype))
			if w.Code != tc.want {
				t.Fatalf("want %d, got %d: %s", tc.want, w.Code, w.Body)
			}
			if ranScreencap(dir) {
				t.Error("a rejected offer must not start screen capture")
			}
		})
	}
}

func TestScreenOfferWhileLocked(t *testing.T) {
	fakeScreencap(t, geometryLine, lockedLine, `{"event":"error","message":"the Mac is locked"}`)
	s, jwt := videoTestServer(t)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, offerRequest("/api/screen/offer", `{"sdp":"v=0"}`, "http://example.com", jwt, "application/json"))
	if w.Code != http.StatusConflict {
		t.Fatalf("want 409, got %d: %s", w.Code, w.Body)
	}
	var resp struct{ Locked bool }
	json.Unmarshal(w.Body.Bytes(), &resp)
	if !resp.Locked {
		t.Errorf("response should say locked: %s", w.Body)
	}
}

func TestScreenOfferPermissionError(t *testing.T) {
	fakeScreencap(t, geometryLine, `{"event":"error","message":"Screen Recording permission is missing"}`)
	s, jwt := videoTestServer(t)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, offerRequest("/api/screen/offer", `{"sdp":"v=0"}`, "http://example.com", jwt, "application/json"))
	if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "permission") {
		t.Fatalf("want 503 naming the permission, got %d: %s", w.Code, w.Body)
	}
}

// TestScreenOfferStreamsVideo runs the whole path: a pion peer plays the
// browser, offers over the protected API, and must receive H.264 RTP plus
// the screencap events on its data channel.
func TestScreenOfferStreamsVideo(t *testing.T) {
	videoIncludeLoopback = true
	t.Cleanup(func() { videoIncludeLoopback = false })
	dir := fakeScreencap(t, geometryLine, unlockedLine)
	s, jwt := videoTestServer(t)
	srv := httptest.NewServer(s.mux)
	t.Cleanup(srv.Close)

	api, err := newVideoAPI()
	if err != nil {
		t.Fatal(err)
	}
	pc, err := api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	defer pc.Close()
	if _, err := pc.AddTransceiverFromKind(webrtc.RTPCodecTypeVideo,
		webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly}); err != nil {
		t.Fatal(err)
	}
	dc, err := pc.CreateDataChannel("events", nil)
	if err != nil {
		t.Fatal(err)
	}
	gotEvent := make(chan string, 4)
	dc.OnMessage(func(m webrtc.DataChannelMessage) { gotEvent <- string(m.Data) })
	gotRTP := make(chan string, 1)
	pc.OnTrack(func(tr *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		if _, _, err := tr.ReadRTP(); err == nil {
			gotRTP <- tr.Codec().MimeType
		}
	})

	offer, err := pc.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	gathered := webrtc.GatheringCompletePromise(pc)
	pc.SetLocalDescription(offer)
	<-gathered
	body, _ := json.Marshal(map[string]string{"sdp": pc.LocalDescription().SDP})
	req, _ := http.NewRequest("POST", srv.URL+"/api/screen/offer", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", srv.URL)
	req.AddCookie(&http.Cookie{Name: authCookieName, Value: jwt})
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var ans struct {
		SDP      string          `json:"sdp"`
		Geometry json.RawMessage `json:"geometry"`
	}
	if res.StatusCode != 200 {
		t.Fatalf("offer: %d", res.StatusCode)
	}
	json.NewDecoder(res.Body).Decode(&ans)
	if !strings.Contains(string(ans.Geometry), `"desktop"`) {
		t.Errorf("answer lacks geometry: %s", ans.Geometry)
	}
	if !strings.Contains(ans.SDP, "a=candidate") {
		t.Errorf("answer must carry its candidates (no trickle): %s", ans.SDP)
	}
	if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: ans.SDP}); err != nil {
		t.Fatal(err)
	}

	select {
	case mime := <-gotRTP:
		if mime != webrtc.MimeTypeH264 {
			t.Errorf("codec %s, want H264", mime)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no video RTP arrived")
	}
	select {
	case ev := <-gotEvent:
		if !strings.Contains(ev, `"locked"`) {
			t.Errorf("unexpected event %s", ev)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no event on the data channel")
	}

	// Stopping must end capture; the fake exits when killed.
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, offerRequest("/api/screen/stop", `{}`, "http://example.com", jwt, "application/json"))
	if w.Code != 200 {
		t.Fatalf("stop: %d %s", w.Code, w.Body)
	}
	ran, _ := os.ReadFile(filepath.Join(dir, "ran.txt"))
	if !strings.HasPrefix(string(ran), "stream --display  --width 1920") {
		t.Errorf("screencap args: %q", ran)
	}
}

func TestScreenStopRejectsForeignOrigin(t *testing.T) {
	s, jwt := videoTestServer(t)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, offerRequest("/api/screen/stop", `{}`, "https://evil.example", jwt, "application/json"))
	if w.Code != http.StatusForbidden {
		t.Fatalf("foreign origin: want 403, got %d", w.Code)
	}
}

func TestScreenState(t *testing.T) {
	fakeScreencap(t)
	s, jwt := videoTestServer(t)

	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, httptest.NewRequest("GET", "/api/screen/state", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("no cookie: want 401, got %d", w.Code)
	}

	req := httptest.NewRequest("GET", "/api/screen/state", nil)
	req.AddCookie(&http.Cookie{Name: authCookieName, Value: jwt})
	w = httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"locked":false`) {
		t.Fatalf("got %d %s", w.Code, w.Body)
	}
}
