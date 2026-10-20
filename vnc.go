package main

import (
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
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
				if err != io.EOF {
					log.Printf("VNC read: %v", err)
				}
				return
			}
		}
	}()

	<-done
}
