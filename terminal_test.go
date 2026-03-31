package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
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
