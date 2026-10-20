package main

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
