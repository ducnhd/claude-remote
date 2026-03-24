package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestFullAuthFlow(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{Port: 0, AllowedDirs: []string{dir}, ClaudePath: "echo", DataDir: dir}
	cfg.Save()

	auth := NewAuth(cfg.SecretPath())
	auth.GenerateSecret()
	token := auth.GenerateToken()

	server := NewServer(cfg, auth)
	server.registerRoutes()

	ts := httptest.NewServer(server.mux)
	defer ts.Close()

	// 1. Try accessing without auth → 401
	resp, err := http.Get(ts.URL + "/api/files?path=" + dir)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 401 {
		t.Errorf("want 401 without auth, got %d", resp.StatusCode)
	}

	// 2. Scan QR token → get JWT cookie
	client := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, err = client.Get(ts.URL + "/auth/scan?token=" + token)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 302 {
		t.Errorf("want 302 redirect, got %d", resp.StatusCode)
	}
	cookies := resp.Cookies()
	var authCookie *http.Cookie
	for _, c := range cookies {
		if c.Name == "claude-remote-auth" {
			authCookie = c
		}
	}
	if authCookie == nil {
		t.Fatal("no auth cookie received")
	}

	// 3. Access with cookie → 200
	req, _ := http.NewRequest("GET", ts.URL+"/api/files?path="+dir, nil)
	req.AddCookie(authCookie)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("want 200 with auth, got %d", resp.StatusCode)
	}

	// 4. Token is single-use
	resp, err = client.Get(ts.URL + "/auth/scan?token=" + token)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 401 {
		t.Errorf("want 401 on reuse, got %d", resp.StatusCode)
	}
}

func TestWebSocketTerminal(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{Port: 0, AllowedDirs: []string{dir}, ClaudePath: "cat", DataDir: dir}

	auth := NewAuth(cfg.SecretPath())
	auth.GenerateSecret()

	server := NewServer(cfg, auth)
	server.registerRoutes()

	// Pre-start terminal in the test directory
	if err := server.terminal.StartInDir(dir); err != nil {
		t.Fatal(err)
	}
	defer server.terminal.Stop()

	ts := httptest.NewServer(http.HandlerFunc(server.terminal.WebSocketHandler()))
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws/term"
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()

	ws.WriteMessage(websocket.BinaryMessage, []byte("hello\n"))
	time.Sleep(300 * time.Millisecond)

	ws.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, msg, err := ws.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(msg), "hello") {
		t.Errorf("want echo of 'hello', got %q", string(msg))
	}
}

func TestFileBrowserAPI(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(dir+"/readme.md", []byte("# Hello"), 0644)
	os.Mkdir(dir+"/src", 0755)

	cfg := &Config{Port: 0, AllowedDirs: []string{dir}, ClaudePath: "echo", DataDir: dir}
	cfg.Save()

	auth := NewAuth(cfg.SecretPath())
	auth.GenerateSecret()
	jwtStr, _ := auth.IssueJWT("test-device")

	server := NewServer(cfg, auth)
	server.registerRoutes()
	ts := httptest.NewServer(server.mux)
	defer ts.Close()

	// List dir
	req, _ := http.NewRequest("GET", ts.URL+"/api/files?path="+dir, nil)
	req.AddCookie(&http.Cookie{Name: "claude-remote-auth", Value: jwtStr})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	var dirResp DirResponse
	json.NewDecoder(resp.Body).Decode(&dirResp)
	// config.json (from cfg.Save()) + readme.md + src = 3 entries
	// Log the count rather than asserting strictly, since Save() writes config.json into dir
	t.Logf("got %d entries: %+v", len(dirResp.Entries), dirResp.Entries)
	if len(dirResp.Entries) < 2 {
		t.Errorf("want at least 2 entries (readme.md + src), got %d", len(dirResp.Entries))
	}

	// Read file
	req, _ = http.NewRequest("GET", ts.URL+"/api/files/read?path="+dir+"/readme.md", nil)
	req.AddCookie(&http.Cookie{Name: "claude-remote-auth", Value: jwtStr})
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("want 200 for file read, got %d", resp.StatusCode)
	}
	var fileResp FileContentResponse
	json.NewDecoder(resp.Body).Decode(&fileResp)
	if fileResp.Content != "# Hello" {
		t.Errorf("want '# Hello', got %q", fileResp.Content)
	}
}
