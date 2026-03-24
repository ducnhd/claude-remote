package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"sync"

	"github.com/creack/pty"
	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

type RingBuffer struct {
	data []byte
	cap  int
	mu   sync.Mutex
}

func NewRingBuffer(cap int) *RingBuffer {
	return &RingBuffer{cap: cap}
}

func (rb *RingBuffer) Write(p []byte) {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	rb.data = append(rb.data, p...)
	if len(rb.data) > rb.cap {
		rb.data = rb.data[len(rb.data)-rb.cap:]
	}
}

func (rb *RingBuffer) Bytes() []byte {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	out := make([]byte, len(rb.data))
	copy(out, rb.data)
	return out
}

func (rb *RingBuffer) Clear() {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	rb.data = rb.data[:0]
}

type TerminalManager struct {
	cmd     string
	args    []string
	ptmx    *os.File
	process *exec.Cmd
	buffer  *RingBuffer
	clients map[*websocket.Conn]bool
	mu      sync.Mutex
	running bool
}

func NewTerminalManager(cmd string, args []string) *TerminalManager {
	return &TerminalManager{
		cmd:     cmd,
		args:    args,
		buffer:  NewRingBuffer(64 * 1024),
		clients: make(map[*websocket.Conn]bool),
	}
}

func (tm *TerminalManager) StartInDir(dir string) error {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	if tm.running {
		return nil
	}
	tm.buffer.Clear()
	c := exec.Command(tm.cmd, tm.args...)
	c.Env = os.Environ()
	if dir != "" {
		c.Dir = dir
	}
	ptmx, err := pty.Start(c)
	if err != nil {
		return fmt.Errorf("start pty: %w", err)
	}
	tm.ptmx = ptmx
	tm.process = c
	tm.running = true

	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				data := make([]byte, n)
				copy(data, buf[:n])
				tm.buffer.Write(data)
				tm.broadcast(data)
			}
			if err != nil {
				break
			}
		}
		tm.mu.Lock()
		tm.running = false
		tm.mu.Unlock()
	}()

	go func() {
		tm.process.Wait()
		tm.mu.Lock()
		tm.running = false
		if tm.ptmx != nil {
			tm.ptmx.Close()
		}
		tm.mu.Unlock()
	}()

	return nil
}

func (tm *TerminalManager) Stop() {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	if tm.process != nil && tm.process.Process != nil {
		tm.process.Process.Kill()
	}
	if tm.ptmx != nil {
		tm.ptmx.Close()
	}
	tm.running = false
}

func (tm *TerminalManager) broadcast(data []byte) {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	for conn := range tm.clients {
		if err := conn.WriteMessage(websocket.BinaryMessage, data); err != nil {
			conn.Close()
			delete(tm.clients, conn)
		}
	}
}

func (tm *TerminalManager) Resize(rows, cols uint16) error {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	if tm.ptmx == nil {
		return fmt.Errorf("no active session")
	}
	return pty.Setsize(tm.ptmx, &pty.Winsize{Rows: rows, Cols: cols})
}

func (tm *TerminalManager) WebSocketHandler() func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Printf("websocket upgrade: %v", err)
			return
		}
		defer func() {
			tm.mu.Lock()
			delete(tm.clients, conn)
			tm.mu.Unlock()
			conn.Close()
		}()

		if !tm.running {
			conn.WriteMessage(websocket.TextMessage, []byte("Waiting for Claude to start...\r\n"))
		}

		tm.mu.Lock()
		tm.clients[conn] = true
		tm.mu.Unlock()

		if buf := tm.buffer.Bytes(); len(buf) > 0 {
			conn.WriteMessage(websocket.BinaryMessage, buf)
		}

		for {
			msgType, msg, err := conn.ReadMessage()
			if err != nil {
				break
			}
			tm.mu.Lock()
			ptmx := tm.ptmx
			tm.mu.Unlock()

			if ptmx == nil {
				break
			}

			switch msgType {
			case websocket.BinaryMessage:
				ptmx.Write(msg)
			case websocket.TextMessage:
				if len(msg) > 0 && msg[0] == '{' {
					tm.handleControlMessage(msg)
				} else {
					ptmx.Write(msg)
				}
			}
		}
	}
}

func (tm *TerminalManager) handleControlMessage(msg []byte) {
	var ctrl struct {
		Type string `json:"type"`
		Rows uint16 `json:"rows"`
		Cols uint16 `json:"cols"`
	}
	if err := json.Unmarshal(msg, &ctrl); err != nil {
		return
	}
	if ctrl.Type == "resize" && ctrl.Rows > 0 && ctrl.Cols > 0 {
		tm.Resize(ctrl.Rows, ctrl.Cols)
	}
}
