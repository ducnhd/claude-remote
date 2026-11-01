package main

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os/exec"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"github.com/pion/ice/v4"
	"github.com/pion/interceptor"
	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
)

// The screen video mode: claude-remote-screencap (screencap/main.swift)
// captures one display with ScreenCaptureKit and encodes H.264, and this
// relays it to the phone over WebRTC. Screen Sharing over noVNC still carries
// input; it just stops being asked for pixels. Only host candidates are used:
// the phone reaches the Mac over Tailscale, so no STUN/TURN is needed.

// screencapPath is swapped out in tests.
var screencapPath = func() string { return filepath.Join(execDir(), "claude-remote-screencap") }

// videoIncludeLoopback lets tests connect a peer on the same machine.
var videoIncludeLoopback = false

var screencapArgs = []string{"--width", "1920", "--fps", "60", "--bitrate", "6000000"}

const (
	maxOfferBytes   = 64 * 1024
	maxFrameBytes   = 8 << 20
	videoStartLimit = 8 * time.Second
	// A viewer that never finishes ICE must not leave the capture running.
	videoConnectLimit = 20 * time.Second
)

// validDisplay keeps the display choice to the names screencap understands.
var validDisplay = regexp.MustCompile(`^(|builtin|main|[0-9]{1,10})$`)

// errScreenLocked means ScreenCaptureKit cannot capture: the Mac is locked.
var errScreenLocked = errors.New("the Mac is locked")

type VideoManager struct {
	mu  sync.Mutex
	cur *videoSession
}

type videoSession struct {
	pc     *webrtc.PeerConnection
	track  *webrtc.TrackLocalStaticSample
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	events chan []byte

	keyMu sync.Mutex
	once  sync.Once
	done  chan struct{}
}

func NewVideoManager() *VideoManager { return &VideoManager{} }

// screencapEvent is one JSON line screencap writes on stderr.
type screencapEvent struct {
	Event   string `json:"event"`
	Locked  bool   `json:"locked"`
	Message string `json:"message"`
}

// Start launches a capture for a browser offer and returns the SDP answer
// with the capture geometry. Only one viewer streams at a time: a new offer
// replaces the old session.
func (m *VideoManager) Start(ctx context.Context, offerSDP, display string) (string, json.RawMessage, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cur != nil {
		m.cur.Close()
		m.cur = nil
	}

	vs := &videoSession{events: make(chan []byte, 32), done: make(chan struct{})}
	args := append([]string{"stream", "--display", display}, screencapArgs...)
	vs.cmd = exec.Command(screencapPath(), args...)
	vs.cmd.Env = childEnv()
	stdout, err := vs.cmd.StdoutPipe()
	if err != nil {
		return "", nil, err
	}
	stderr, err := vs.cmd.StderrPipe()
	if err != nil {
		return "", nil, err
	}
	if vs.stdin, err = vs.cmd.StdinPipe(); err != nil {
		return "", nil, err
	}
	if err := vs.cmd.Start(); err != nil {
		return "", nil, fmt.Errorf("start screencap: %w", err)
	}
	go func() {
		vs.cmd.Wait()
		vs.Close()
	}()

	geometry, err := vs.awaitReady(ctx, stderr)
	if err != nil {
		vs.Close()
		return "", nil, err
	}
	answer, err := vs.negotiate(offerSDP)
	if err != nil {
		vs.Close()
		return "", nil, err
	}
	go vs.pump(stdout)
	go func() {
		select {
		case <-vs.done:
		case <-time.After(videoConnectLimit):
			if vs.pc.ConnectionState() != webrtc.PeerConnectionStateConnected {
				log.Printf("video: viewer never connected, stopping capture")
				vs.Close()
			}
		}
	}()
	m.cur = vs
	log.Printf("video: capture started (display %q)", display)
	return answer, geometry, nil
}

// Stop ends the current session, if any.
func (m *VideoManager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cur != nil {
		m.cur.Close()
		m.cur = nil
	}
}

// awaitReady waits until screencap has passed its permission and lock checks.
// Its first line is the geometry; a "locked":false event means capture is
// starting, an error event means it will not.
func (vs *videoSession) awaitReady(ctx context.Context, stderr io.Reader) (json.RawMessage, error) {
	type result struct {
		geometry json.RawMessage
		err      error
	}
	ready := make(chan result, 1)
	go func() {
		sc := bufio.NewScanner(stderr)
		var geometry json.RawMessage
		sent := false
		send := func(r result) {
			if !sent {
				sent = true
				ready <- r
			}
		}
		for sc.Scan() {
			line := append([]byte(nil), sc.Bytes()...)
			var ev screencapEvent
			if json.Unmarshal(line, &ev) != nil {
				log.Printf("screencap: %s", line)
				continue
			}
			switch {
			case ev.Event == "geometry":
				geometry = line
			case ev.Event == "error":
				log.Printf("screencap error: %s", ev.Message)
				if ev.Locked || ev.Message == errScreenLocked.Error() {
					send(result{err: errScreenLocked})
				} else {
					send(result{err: errors.New(ev.Message)})
				}
			case ev.Event == "locked" && ev.Locked:
				send(result{err: errScreenLocked})
			case ev.Event == "locked":
				send(result{geometry: geometry})
			}
			if sent {
				select {
				case vs.events <- line:
				default: // a slow page loses events rather than stalling capture
				}
			}
		}
		send(result{err: errors.New("screencap exited before capturing")})
		close(vs.events)
	}()
	select {
	case r := <-ready:
		return r.geometry, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(videoStartLimit):
		return nil, errors.New("screencap did not start in time")
	}
}

func newVideoAPI() (*webrtc.API, error) {
	me := &webrtc.MediaEngine{}
	// screencap encodes Constrained Baseline; advertise exactly that so the
	// browser never expects B-frames or High-profile tools.
	for i, fmtp := range []string{
		"level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42e01f",
		"level-asymmetry-allowed=1;packetization-mode=1;profile-level-id=42001f",
	} {
		err := me.RegisterCodec(webrtc.RTPCodecParameters{
			RTPCodecCapability: webrtc.RTPCodecCapability{
				MimeType: webrtc.MimeTypeH264, ClockRate: 90000, SDPFmtpLine: fmtp,
				RTCPFeedback: []webrtc.RTCPFeedback{{Type: "nack"}, {Type: "nack", Parameter: "pli"}, {Type: "ccm", Parameter: "fir"}},
			},
			PayloadType: webrtc.PayloadType(102 + 2*i),
		}, webrtc.RTPCodecTypeVideo)
		if err != nil {
			return nil, err
		}
	}
	reg := &interceptor.Registry{}
	if err := webrtc.RegisterDefaultInterceptors(me, reg); err != nil {
		return nil, err
	}
	se := webrtc.SettingEngine{}
	se.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4})
	// Plain IP candidates: the phone cannot resolve .local names over Tailscale.
	se.SetICEMulticastDNSMode(ice.MulticastDNSModeDisabled)
	se.SetIncludeLoopbackCandidate(videoIncludeLoopback)
	return webrtc.NewAPI(webrtc.WithMediaEngine(me), webrtc.WithInterceptorRegistry(reg), webrtc.WithSettingEngine(se)), nil
}

func (vs *videoSession) negotiate(offerSDP string) (string, error) {
	api, err := newVideoAPI()
	if err != nil {
		return "", err
	}
	pc, err := api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		return "", err
	}
	vs.pc = pc
	track, err := webrtc.NewTrackLocalStaticSample(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeH264, ClockRate: 90000}, "screen", "claude-remote")
	if err != nil {
		return "", err
	}
	sender, err := pc.AddTrack(track)
	if err != nil {
		return "", err
	}
	vs.track = track
	go vs.readRTCP(sender)

	pc.OnDataChannel(func(dc *webrtc.DataChannel) {
		dc.OnOpen(func() {
			for line := range vs.events {
				if dc.SendText(string(line)) != nil {
					return
				}
			}
		})
	})
	pc.OnConnectionStateChange(func(st webrtc.PeerConnectionState) {
		log.Printf("video: connection %s", st)
		switch st {
		case webrtc.PeerConnectionStateConnected:
			// Frames sent before ICE finished were dropped, the first
			// keyframe with them.
			vs.requestKeyframe()
		case webrtc.PeerConnectionStateFailed, webrtc.PeerConnectionStateClosed:
			vs.Close()
		}
	})

	if err := pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: offerSDP}); err != nil {
		return "", fmt.Errorf("bad offer: %w", err)
	}
	answer, err := pc.CreateAnswer(nil)
	if err != nil {
		return "", fmt.Errorf("answer: %w", err)
	}
	gathered := webrtc.GatheringCompletePromise(pc)
	if err := pc.SetLocalDescription(answer); err != nil {
		return "", err
	}
	// Host candidates only, so gathering takes milliseconds; answering with
	// all of them at once avoids a trickle-ICE channel.
	select {
	case <-gathered:
	case <-time.After(3 * time.Second):
	}
	return pc.LocalDescription().SDP, nil
}

// readRTCP answers keyframe requests and logs what the phone reports about
// loss every 10 s, since broken pictures on the phone mostly mean lost packets.
func (vs *videoSession) readRTCP(sender *webrtc.RTPSender) {
	var plis, nacks, lost int
	var maxFrac uint8
	last := time.Now()
	for {
		pkts, _, err := sender.ReadRTCP()
		if err != nil {
			return
		}
		for _, p := range pkts {
			switch p := p.(type) {
			case *rtcp.PictureLossIndication, *rtcp.FullIntraRequest:
				plis++
				vs.requestKeyframe()
			case *rtcp.TransportLayerNack:
				for _, n := range p.Nacks {
					nacks += len(n.PacketList())
				}
			case *rtcp.ReceiverReport:
				for _, r := range p.Reports {
					lost = int(r.TotalLost)
					maxFrac = max(maxFrac, r.FractionLost)
				}
			}
		}
		if time.Since(last) >= 10*time.Second && (plis > 0 || nacks > 0 || maxFrac > 0) {
			log.Printf("video: phone reports lost=%d total, worst %.1f%%, nacked=%d, keyframe requests=%d",
				lost, float64(maxFrac)*100/256, nacks, plis)
			plis, nacks, maxFrac = 0, 0, 0
			last = time.Now()
		}
	}
}

func (vs *videoSession) requestKeyframe() {
	vs.keyMu.Lock()
	defer vs.keyMu.Unlock()
	io.WriteString(vs.stdin, "k\n")
}

// pump forwards screencap's length-prefixed access units to the track.
func (vs *videoSession) pump(stdout io.Reader) {
	r := bufio.NewReaderSize(stdout, 256*1024)
	var hdr [4]byte
	prev := time.Time{}
	for {
		if _, err := io.ReadFull(r, hdr[:]); err != nil {
			return
		}
		n := binary.BigEndian.Uint32(hdr[:])
		if n == 0 || n > maxFrameBytes {
			log.Printf("video: bad frame length %d", n)
			vs.Close()
			return
		}
		au := make([]byte, n)
		if _, err := io.ReadFull(r, au); err != nil {
			return
		}
		now := time.Now()
		d := time.Second / 60
		if !prev.IsZero() {
			d = now.Sub(prev)
		}
		prev = now
		if err := vs.track.WriteSample(media.Sample{Data: au, Duration: d}); err != nil && !errors.Is(err, io.ErrClosedPipe) {
			log.Printf("video: write sample: %v", err)
		}
	}
}

// Close stops capture and hangs up. Safe to call more than once.
func (vs *videoSession) Close() {
	vs.once.Do(func() {
		close(vs.done)
		if vs.stdin != nil {
			vs.stdin.Close() // screencap exits on EOF
		}
		if vs.cmd != nil && vs.cmd.Process != nil {
			p := vs.cmd.Process
			time.AfterFunc(2*time.Second, func() { p.Kill() })
		}
		if vs.pc != nil {
			go vs.pc.Close()
		}
		log.Printf("video: capture stopped")
	})
}

// handleScreenOffer answers a WebRTC offer for the screen video. Origin is
// checked on top of jsonPost's preflight: the answer starts screen capture.
func (s *Server) handleScreenOffer(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeJSONError(w, http.StatusForbidden, "forbidden")
		return
	}
	if !jsonPost(w, r) {
		return
	}
	var body struct {
		SDP     string `json:"sdp"`
		Display string `json:"display"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, maxOfferBytes)).Decode(&body); err != nil || body.SDP == "" {
		writeJSONError(w, http.StatusBadRequest, "invalid offer")
		return
	}
	if !validDisplay.MatchString(body.Display) {
		writeJSONError(w, http.StatusBadRequest, "invalid display")
		return
	}
	answer, geometry, err := s.video.Start(r.Context(), body.SDP, body.Display)
	if errors.Is(err, errScreenLocked) {
		writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error(), "locked": true})
		return
	}
	if err != nil {
		log.Printf("video offer: %v", err)
		writeJSONError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	log.Printf("video viewer from %s", clientIP(r))
	writeJSON(w, http.StatusOK, map[string]any{"type": "answer", "sdp": answer, "geometry": geometry})
}

// handleScreenStop ends the video session; the page calls it when it leaves
// video mode so capture never outlives the viewer.
func (s *Server) handleScreenStop(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		writeJSONError(w, http.StatusForbidden, "forbidden")
		return
	}
	if !jsonPost(w, r) {
		return
	}
	s.video.Stop()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleScreenState reports whether the Mac is locked, so a page that fell
// back to VNC knows when video can resume.
func (s *Server) handleScreenState(w http.ResponseWriter, r *http.Request) {
	if out, ok := localScreenState(); ok {
		w.Header().Set("Content-Type", "application/json")
		w.Write(out)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, screencapPath(), "state").Output()
	if err != nil || !json.Valid(out) {
		log.Printf("screencap state: %v", err)
		writeJSONError(w, http.StatusServiceUnavailable, "screen video is not installed")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(out)
}
