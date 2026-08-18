package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/creack/pty"
	"github.com/gorilla/websocket"
)

const (
	// writeTimeout bounds how long a single client may stall the pty reader.
	writeTimeout = 10 * time.Second
	// pingInterval keeps the connection alive through proxies such as
	// cloudflared, which drop idle WebSockets after a couple of minutes.
	pingInterval = 30 * time.Second
	// pongTimeout is how long a client may go silent before we drop it.
	pongTimeout = 90 * time.Second
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

// client wraps a websocket connection with its own write mutex.
// gorilla/websocket panics on concurrent writes to the same connection,
// and both the pty broadcast goroutine and the request goroutine write.
type client struct {
	conn *websocket.Conn
	mu   sync.Mutex
}

func (c *client) write(msgType int, data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.conn.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil {
		return err
	}
	return c.conn.WriteMessage(msgType, data)
}

func (c *client) ping() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.conn.SetWriteDeadline(time.Now().Add(writeTimeout)); err != nil {
		return err
	}
	return c.conn.WriteMessage(websocket.PingMessage, nil)
}

type TerminalManager struct {
	cmd     string
	args    []string
	dir     string
	ptmx    *os.File
	process *exec.Cmd
	buffer  *RingBuffer
	clients map[*client]bool
	mu      sync.Mutex
	// outMu serializes buffer appends, broadcasts and backlog replay so a
	// joining client never sees duplicated or missing output.
	outMu   sync.Mutex
	running bool
	// gen identifies the current pty session; goroutines from an older
	// session must not clobber state belonging to a newer one.
	gen uint64
}

func NewTerminalManager(cmd string, args []string) *TerminalManager {
	return &TerminalManager{
		cmd:     cmd,
		args:    args,
		buffer:  NewRingBuffer(64 * 1024),
		clients: make(map[*client]bool),
	}
}

func (tm *TerminalManager) StartInDir(dir string) error {
	return tm.start(dir, tm.args)
}

func (tm *TerminalManager) StartWithResume(dir string) error {
	return tm.start(dir, []string{"--continue"})
}

func (tm *TerminalManager) start(dir string, args []string) error {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	if tm.running {
		return nil
	}
	tm.buffer.Clear()
	tm.dir = dir
	c := exec.Command(tm.cmd, args...)
	c.Env = childEnv()
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
	tm.gen++

	tm.startIO(tm.gen, ptmx, c)
	return nil
}

// childEnv builds the environment for the claude process.
//
// launchd starts the server with no TERM and no LANG, and passing that
// environment straight through leaves the TUI unable to interpret keystrokes:
// the terminal draws, the kernel echoes raw characters, but nothing the phone
// types ever reaches the application. PATH is widened for the same reason the
// config needs an absolute claude_path — launchd's PATH is minimal.
func childEnv() []string {
	env := map[string]string{}
	var order []string
	set := func(k, v string) {
		if _, seen := env[k]; !seen {
			order = append(order, k)
		}
		env[k] = v
	}
	for _, kv := range os.Environ() {
		if i := strings.IndexByte(kv, '='); i > 0 {
			set(kv[:i], kv[i+1:])
		}
	}
	fallback := func(k, v string) {
		if env[k] == "" {
			set(k, v)
		}
	}
	fallback("TERM", "xterm-256color")
	fallback("COLORTERM", "truecolor")
	fallback("LANG", "en_US.UTF-8")

	if home, err := os.UserHomeDir(); err == nil && home != "" {
		fallback("HOME", home)
		path := env["PATH"]
		for _, dir := range []string{
			filepath.Join(home, ".local", "bin"),
			filepath.Join(home, "bin"),
			"/opt/homebrew/bin",
			"/usr/local/bin",
		} {
			if !pathHasDir(path, dir) {
				path = dir + ":" + path
			}
		}
		set("PATH", strings.TrimSuffix(path, ":"))
	}

	out := make([]string, 0, len(order))
	for _, k := range order {
		out = append(out, k+"="+env[k])
	}
	return out
}

func pathHasDir(path, dir string) bool {
	for _, p := range strings.Split(path, ":") {
		if p == dir {
			return true
		}
	}
	return false
}

// startIO launches goroutines to read pty output and wait for process exit.
// Must be called with tm.mu held and tm.running == true.
func (tm *TerminalManager) startIO(gen uint64, ptmx *os.File, proc *exec.Cmd) {
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := ptmx.Read(buf)
			if n > 0 {
				data := make([]byte, n)
				copy(data, buf[:n])
				tm.publish(data)
			}
			if err != nil {
				break
			}
		}
		tm.markStopped(gen, nil)
	}()

	go func() {
		if err := proc.Wait(); err != nil {
			log.Printf("claude process exited: %v", err)
		}
		tm.markStopped(gen, ptmx)
	}()
}

// markStopped clears session state only if gen is still the current session.
func (tm *TerminalManager) markStopped(gen uint64, ptmx *os.File) {
	tm.mu.Lock()
	stale := tm.gen != gen
	if !stale {
		tm.running = false
		tm.ptmx = nil
		tm.process = nil
	}
	tm.mu.Unlock()
	if ptmx != nil {
		ptmx.Close()
	}
}

// publish appends output to the backlog and fans it out to every client.
func (tm *TerminalManager) publish(data []byte) {
	tm.outMu.Lock()
	defer tm.outMu.Unlock()
	tm.buffer.Write(data)
	for _, c := range tm.snapshotClients() {
		if err := c.write(websocket.BinaryMessage, data); err != nil {
			tm.removeClient(c)
			c.conn.Close()
		}
	}
}

func (tm *TerminalManager) snapshotClients() []*client {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	out := make([]*client, 0, len(tm.clients))
	for c := range tm.clients {
		out = append(out, c)
	}
	return out
}

func (tm *TerminalManager) removeClient(c *client) {
	tm.mu.Lock()
	delete(tm.clients, c)
	tm.mu.Unlock()
}

func (tm *TerminalManager) Running() bool {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	return tm.running
}

func (tm *TerminalManager) Stop() {
	tm.mu.Lock()
	proc, ptmx := tm.process, tm.ptmx
	tm.process, tm.ptmx = nil, nil
	tm.running = false
	tm.gen++ // invalidate goroutines of the session being torn down
	tm.mu.Unlock()

	if proc != nil && proc.Process != nil {
		// The Wait goroutine started by startIO reaps the process; calling
		// Wait a second time here would race inside os/exec.
		proc.Process.Kill()
	}
	if ptmx != nil {
		ptmx.Close()
	}
}

func (tm *TerminalManager) Resize(rows, cols uint16) error {
	tm.mu.Lock()
	ptmx := tm.ptmx
	tm.mu.Unlock()
	if ptmx == nil {
		return fmt.Errorf("no active session")
	}
	return pty.Setsize(ptmx, &pty.Winsize{Rows: rows, Cols: cols})
}

func (tm *TerminalManager) WebSocketHandler() func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Printf("websocket upgrade: %v", err)
			return
		}
		c := &client{conn: conn}
		defer func() {
			tm.removeClient(c)
			conn.Close()
		}()

		// Keepalive: browsers answer ping frames automatically. Without
		// this a tunnelled connection dies silently after a few idle
		// minutes and the phone shows a frozen session.
		conn.SetReadDeadline(time.Now().Add(pongTimeout))
		conn.SetPongHandler(func(string) error {
			return conn.SetReadDeadline(time.Now().Add(pongTimeout))
		})
		pingDone := make(chan struct{})
		defer close(pingDone)
		go func() {
			ticker := time.NewTicker(pingInterval)
			defer ticker.Stop()
			for {
				select {
				case <-pingDone:
					return
				case <-ticker.C:
					if err := c.ping(); err != nil {
						conn.Close()
						return
					}
				}
			}
		}()

		if !tm.Running() {
			c.write(websocket.TextMessage, []byte("Waiting for Claude to start...\r\n"))
		}

		// Register and replay the backlog atomically with respect to
		// publish(), so no output is duplicated or dropped.
		tm.outMu.Lock()
		tm.mu.Lock()
		tm.clients[c] = true
		tm.mu.Unlock()
		backlog := tm.buffer.Bytes()
		tm.outMu.Unlock()

		if len(backlog) > 0 {
			if err := c.write(websocket.BinaryMessage, backlog); err != nil {
				return
			}
		}

		for {
			msgType, msg, err := conn.ReadMessage()
			if err != nil {
				break
			}
			conn.SetReadDeadline(time.Now().Add(pongTimeout))
			tm.mu.Lock()
			ptmx := tm.ptmx
			tm.mu.Unlock()

			switch msgType {
			case websocket.TextMessage:
				if tm.handleControlMessage(msg) {
					continue
				}
				fallthrough
			case websocket.BinaryMessage:
				if ptmx == nil {
					c.write(websocket.TextMessage, []byte("\r\nNo active session.\r\n"))
					continue
				}
				if _, err := ptmx.Write(msg); err != nil {
					log.Printf("pty write: %v", err)
				}
			}
		}
	}
}

// handleControlMessage returns true if msg was a recognized control frame.
// Anything else is passed through to the pty as user input.
func (tm *TerminalManager) handleControlMessage(msg []byte) bool {
	var ctrl struct {
		Type string `json:"type"`
		Rows uint16 `json:"rows"`
		Cols uint16 `json:"cols"`
	}
	if err := json.Unmarshal(msg, &ctrl); err != nil {
		return false
	}
	if ctrl.Type != "resize" {
		return false
	}
	if ctrl.Rows > 0 && ctrl.Cols > 0 {
		tm.Resize(ctrl.Rows, ctrl.Cols)
	}
	return true
}
