package main

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

// vncAddr is macOS Screen Sharing. It is only ever dialled on loopback, so
// the VNC port itself never has to be reachable from the network.
var vncAddr = "127.0.0.1:5900"

// vncUpgrader rejects cross-site pages: the WebSocket is authenticated by the
// session cookie alone, and a foreign page must not be able to ride it.
var vncUpgrader = websocket.Upgrader{
	Subprotocols:    []string{"binary"},
	ReadBufferSize:  32 * 1024,
	WriteBufferSize: 32 * 1024,
	CheckOrigin:     sameOrigin,
}

func sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false
	}
	u, err := url.Parse(origin)
	return err == nil && u.Host == r.Host
}

// handleVNC bridges the browser's noVNC WebSocket to Screen Sharing. noVNC
// speaks raw RFB over binary frames, so this is a plain byte pipe; the macOS
// login (Apple Remote Desktop auth) still happens end to end in the browser.
func (s *Server) handleVNC(w http.ResponseWriter, r *http.Request) {
	if !sameOrigin(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	conn, err := net.DialTimeout("tcp", vncAddr, 3*time.Second)
	if err != nil {
		log.Printf("VNC dial %s: %v", vncAddr, err)
		writeJSONError(w, http.StatusBadGateway,
			"Screen Sharing is off: enable it in System Settings → General → Sharing")
		return
	}
	ws, err := vncUpgrader.Upgrade(w, r, nil)
	if err != nil {
		conn.Close()
		return
	}
	log.Printf("VNC session opened from %s", clientIP(r))
	pipeVNC(ws, conn)
	log.Printf("VNC session closed")
}

// pbcopyPath is swapped out in tests.
var pbcopyPath = "/usr/bin/pbcopy"

const maxClipboardBytes = 64 * 1024

// handleClipboard puts text on the Mac clipboard so the screen page can paste
// it with ⌘V. Screen Sharing drops Unicode keysyms, so Vietnamese (or any
// non-ASCII text) typed key by key never arrives; a paste always does.
func (s *Server) handleClipboard(w http.ResponseWriter, r *http.Request) {
	// Requiring JSON forces a CORS preflight, so a foreign page cannot fire
	// this as a simple form post riding the session cookie.
	if r.Method != http.MethodPost || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		writeJSONError(w, http.StatusBadRequest, "POST application/json only")
		return
	}
	var body struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, maxClipboardBytes+1)).Decode(&body); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON or text too long")
		return
	}
	cmd := exec.Command(pbcopyPath)
	// pbcopy reads stdin in the locale's encoding; launchd gives us no LANG,
	// which would turn every non-ASCII character into garbage.
	cmd.Env = childEnv()
	cmd.Stdin = strings.NewReader(body.Text)
	if out, err := cmd.CombinedOutput(); err != nil {
		log.Printf("pbcopy: %v %s", err, out)
		writeJSONError(w, http.StatusInternalServerError, "could not set the clipboard")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func pipeVNC(ws *websocket.Conn, conn net.Conn) {
	defer ws.Close()
	defer conn.Close()
	done := make(chan struct{}, 2)

	go func() {
		defer func() { done <- struct{}{} }()
		for {
			_, data, err := ws.ReadMessage()
			if err != nil {
				return
			}
			if _, err := conn.Write(data); err != nil {
				return
			}
		}
	}()

	go func() {
		defer func() { done <- struct{}{} }()
		buf := make([]byte, 64*1024)
		for {
			n, err := conn.Read(buf)
			if n > 0 {
				ws.SetWriteDeadline(time.Now().Add(writeTimeout))
				if werr := ws.WriteMessage(websocket.BinaryMessage, buf[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				// net.ErrClosed is the other goroutine closing on a normal hang-up.
				if err != io.EOF && !errors.Is(err, net.ErrClosed) {
					log.Printf("VNC read: %v", err)
				}
				return
			}
		}
	}()

	<-done
}
