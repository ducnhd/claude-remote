package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// fakeVNC stands in for Screen Sharing: it greets like an RFB server and then
// echoes whatever it receives.
func fakeVNC(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				c.Write([]byte("RFB 003.889\n"))
				buf := make([]byte, 1024)
				for {
					n, err := c.Read(buf)
					if err != nil {
						return
					}
					c.Write(buf[:n])
				}
			}(c)
		}
	}()
	return ln.Addr().String()
}

func withVNCAddr(t *testing.T, addr string) {
	old := vncAddr
	vncAddr = addr
	t.Cleanup(func() { vncAddr = old })
}

func vncServer(t *testing.T) *httptest.Server {
	s := &Server{}
	srv := httptest.NewServer(http.HandlerFunc(s.handleVNC))
	t.Cleanup(srv.Close)
	return srv
}

func dialVNC(srv *httptest.Server, origin string) (*websocket.Conn, *http.Response, error) {
	h := http.Header{}
	if origin != "" {
		h.Set("Origin", origin)
	}
	d := websocket.Dialer{Subprotocols: []string{"binary"}}
	return d.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), h)
}

func TestVNCPipesBytes(t *testing.T) {
	withVNCAddr(t, fakeVNC(t))
	srv := vncServer(t)

	ws, _, err := dialVNC(srv, srv.URL)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer ws.Close()
	if ws.Subprotocol() != "binary" {
		t.Errorf("subprotocol = %q, want binary", ws.Subprotocol())
	}

	_, greet, err := ws.ReadMessage()
	if err != nil || string(greet) != "RFB 003.889\n" {
		t.Fatalf("greeting = %q, %v", greet, err)
	}
	if err := ws.WriteMessage(websocket.BinaryMessage, []byte("ping")); err != nil {
		t.Fatal(err)
	}
	_, echo, err := ws.ReadMessage()
	if err != nil || string(echo) != "ping" {
		t.Fatalf("echo = %q, %v", echo, err)
	}
}

func TestVNCRejectsForeignOrigin(t *testing.T) {
	withVNCAddr(t, fakeVNC(t))
	srv := vncServer(t)

	for _, origin := range []string{"", "https://evil.example"} {
		_, resp, err := dialVNC(srv, origin)
		if err == nil {
			t.Fatalf("origin %q: expected rejection", origin)
		}
		if resp == nil || resp.StatusCode != http.StatusForbidden {
			t.Errorf("origin %q: status = %v, want 403", origin, resp)
		}
	}
}

func TestVNCScreenSharingOff(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close() // nothing listens here any more
	withVNCAddr(t, addr)
	srv := vncServer(t)

	_, resp, err := dialVNC(srv, srv.URL)
	if err == nil {
		t.Fatal("expected failure when Screen Sharing is off")
	}
	if resp == nil || resp.StatusCode != http.StatusBadGateway {
		t.Errorf("status = %v, want 502", resp)
	}
}

func TestClipboardRejectsFormPost(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodPost, "/api/clipboard", strings.NewReader("text=x"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	s.handleClipboard(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestScreenWakeAndLock(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ran")
	oldWake, oldLock := wakeCmd, lockCmd
	wakeCmd = appendCommand(marker, "wake")
	lockCmd = appendCommand(marker, "lock")
	t.Cleanup(func() { wakeCmd, lockCmd = oldWake, oldLock })

	s := &Server{}
	for _, h := range []http.HandlerFunc{s.handleWake, s.handleLock} {
		req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("{}"))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
		}
	}
	// wake runs in the background, so wait for both commands to land.
	var got string
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		b, _ := os.ReadFile(marker)
		if got = strings.ReplaceAll(string(b), "\r\n", "\n"); strings.Count(got, "\n") >= 2 {
			break
		}
	}
	if !strings.Contains(got, "wake\n") || !strings.Contains(got, "lock\n") {
		t.Errorf("commands ran = %q", got)
	}
}

func TestScreenLockRejectsGet(t *testing.T) {
	old := lockCmd
	lockCmd = []string{"/usr/bin/false"} // must never run
	t.Cleanup(func() { lockCmd = old })
	s := &Server{}
	rec := httptest.NewRecorder()
	s.handleLock(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}
