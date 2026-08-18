package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestRingBuffer(t *testing.T) {
	rb := NewRingBuffer(1024)
	rb.Write([]byte("hello"))
	rb.Write([]byte(" world"))
	data := rb.Bytes()
	if string(data) != "hello world" {
		t.Errorf("want 'hello world', got %q", string(data))
	}
}

func TestRingBufferOverflow(t *testing.T) {
	rb := NewRingBuffer(10)
	rb.Write([]byte("12345678901234567890")) // 20 bytes, cap 10
	data := rb.Bytes()
	if len(data) != 10 {
		t.Errorf("buffer should be exactly 10 bytes, got %d", len(data))
	}
	if string(data) != "1234567890" {
		t.Errorf("want last 10 bytes '1234567890', got %q", string(data))
	}
}

func TestRingBufferReplay(t *testing.T) {
	rb := NewRingBuffer(1024)
	rb.Write([]byte("line1\n"))
	rb.Write([]byte("line2\n"))
	data := rb.Bytes()
	if string(data) != "line1\nline2\n" {
		t.Errorf("replay buffer should contain all writes, got %q", string(data))
	}
}

func TestTerminalSpawnEcho(t *testing.T) {
	tm := NewTerminalManager("echo", []string{"hello from pty"})
	if err := tm.StartInDir(""); err != nil {
		t.Fatal(err)
	}
	defer tm.Stop()

	time.Sleep(500 * time.Millisecond)
	output := string(tm.buffer.Bytes())
	if !strings.Contains(output, "hello from pty") {
		t.Errorf("want output containing 'hello from pty', got %q", output)
	}
}

func TestTerminalDir(t *testing.T) {
	tm := NewTerminalManager("echo", []string{"dir test"})
	if err := tm.StartInDir("/tmp"); err != nil {
		t.Fatal(err)
	}
	defer tm.Stop()

	if tm.dir != "/tmp" {
		t.Errorf("want tm.dir == /tmp, got %q", tm.dir)
	}
}

func TestStartWithResume(t *testing.T) {
	// Use "echo" as a stand-in; "--continue" is just another arg for echo
	tm := NewTerminalManager("echo", nil)
	if err := tm.StartWithResume("/tmp"); err != nil {
		t.Fatal(err)
	}
	defer tm.Stop()

	if tm.dir != "/tmp" {
		t.Errorf("want tm.dir == /tmp, got %q", tm.dir)
	}

	time.Sleep(300 * time.Millisecond)
	output := string(tm.buffer.Bytes())
	if !strings.Contains(output, "--continue") {
		t.Errorf("want output containing '--continue', got %q", output)
	}
}

func TestTerminalWebSocket(t *testing.T) {
	tm := NewTerminalManager("cat", nil) // cat echoes input
	// Pre-start terminal before connecting WebSocket
	if err := tm.StartInDir(""); err != nil {
		t.Fatal(err)
	}
	defer tm.Stop()
	handler := tm.WebSocketHandler()

	server := httptest.NewServer(http.HandlerFunc(handler))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws/term"
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()

	ws.WriteMessage(websocket.BinaryMessage, []byte("test input\n"))
	time.Sleep(300 * time.Millisecond)

	ws.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, msg, err := ws.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(msg), "test input") {
		t.Errorf("want echo of 'test input', got %q", string(msg))
	}
}

// Regression: the pty reader and the request goroutine both write to the
// same websocket connection. Without a per-connection write mutex,
// gorilla/websocket panics with "concurrent write to websocket connection".
func TestWebSocketConcurrentWrites(t *testing.T) {
	tm := NewTerminalManager("sh", []string{"-c", "for i in $(seq 1 200); do echo line-$i; done; sleep 1"})
	srv := httptest.NewServer(http.HandlerFunc(tm.WebSocketHandler()))
	defer srv.Close()
	defer tm.Stop()

	if err := tm.StartInDir(t.TempDir()); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			conn, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http")+"/ws/term", nil)
			if err != nil {
				return
			}
			defer conn.Close()
			conn.SetReadDeadline(time.Now().Add(2 * time.Second))
			for j := 0; j < 5; j++ {
				if _, _, err := conn.ReadMessage(); err != nil {
					return
				}
			}
		}()
	}
	wg.Wait()
}

// Regression: a restart must not let the previous session's exit goroutine
// tear down the new pty.
func TestRestartKeepsNewSessionAlive(t *testing.T) {
	dir := t.TempDir()
	tm := NewTerminalManager("sh", []string{"-c", "sleep 5"})
	if err := tm.StartInDir(dir); err != nil {
		t.Fatal(err)
	}
	tm.Stop()
	if err := tm.StartInDir(dir); err != nil {
		t.Fatal(err)
	}
	defer tm.Stop()

	time.Sleep(300 * time.Millisecond)
	if !tm.Running() {
		t.Error("new session was killed by the previous session's goroutines")
	}
	if err := tm.Resize(40, 100); err != nil {
		t.Errorf("resize on the new session failed: %v", err)
	}
}

// Only recognized control frames are intercepted; anything else is input.
func TestHandleControlMessage(t *testing.T) {
	tm := NewTerminalManager("echo", nil)
	if !tm.handleControlMessage([]byte(`{"type":"resize","rows":40,"cols":80}`)) {
		t.Error("resize frame should be handled as control")
	}
	if tm.handleControlMessage([]byte(`{"type":"unknown"}`)) {
		t.Error("unknown JSON type must be passed through to the pty")
	}
	if tm.handleControlMessage([]byte("hello world")) {
		t.Error("plain text must be passed through to the pty")
	}
}

// Regression: launchd provides no TERM/LANG, and a TUI started without them
// renders but never processes keystrokes — the phone appears unable to send.
func TestChildEnvSuppliesTerminalVars(t *testing.T) {
	for _, key := range []string{"TERM", "COLORTERM", "LANG"} {
		t.Setenv(key, "")
	}
	env := childEnv()
	get := func(key string) string {
		for _, kv := range env {
			if strings.HasPrefix(kv, key+"=") {
				return strings.TrimPrefix(kv, key+"=")
			}
		}
		return ""
	}
	if got := get("TERM"); got == "" {
		t.Error("TERM must be set for the child process")
	}
	if got := get("LANG"); got == "" {
		t.Error("LANG must be set so UTF-8 output renders")
	}
	if got := get("COLORTERM"); got == "" {
		t.Error("COLORTERM should be set")
	}
	// No duplicate keys: os/exec would keep the last, but the ambiguity is
	// not worth carrying.
	seen := map[string]bool{}
	for _, kv := range env {
		key := strings.SplitN(kv, "=", 2)[0]
		if seen[key] {
			t.Errorf("duplicate env key %q", key)
		}
		seen[key] = true
	}
}

// An existing TERM from an interactive shell must win over the fallback.
func TestChildEnvKeepsExistingTerm(t *testing.T) {
	t.Setenv("TERM", "screen-256color")
	for _, kv := range childEnv() {
		if kv == "TERM=screen-256color" {
			return
		}
	}
	t.Error("childEnv overwrote an existing TERM")
}

// launchd's PATH lacks the user bin dirs claude and its tools live in.
func TestChildEnvWidensPath(t *testing.T) {
	t.Setenv("PATH", "/usr/bin:/bin")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	var path string
	for _, kv := range childEnv() {
		if strings.HasPrefix(kv, "PATH=") {
			path = strings.TrimPrefix(kv, "PATH=")
		}
	}
	for _, want := range []string{filepath.Join(home, ".local", "bin"), "/opt/homebrew/bin", "/usr/bin"} {
		if !pathHasDir(path, want) {
			t.Errorf("PATH is missing %s: %s", want, path)
		}
	}
}
