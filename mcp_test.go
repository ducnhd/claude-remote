package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	neturl "net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setupMCPServer(t *testing.T) *Server {
	t.Helper()
	return setupMCPServerDir(t, t.TempDir())
}

func setupMCPServerDir(t *testing.T, dir string) *Server {
	t.Helper()
	cfg := &Config{Port: 8822, AllowedDirs: []string{dir}, ClaudePath: "echo", DataDir: dir}
	auth := NewAuth(cfg.SecretPath())
	auth.GenerateSecret()
	s := NewServer(cfg, auth)
	s.registerRoutes()
	return s
}

func mcpPost(t *testing.T, s *Server, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.localHandler().ServeHTTP(w, req)
	return w
}

func TestMCPInitialize(t *testing.T) {
	s := setupMCPServer(t)
	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`
	w := mcpPost(t, s, body)

	if w.Code != 200 {
		t.Fatalf("want 200, got %d", w.Code)
	}

	var resp jsonrpcResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	result, ok := resp.Result.(map[string]interface{})
	if !ok {
		t.Fatal("result not a map")
	}
	if _, ok := result["capabilities"]; !ok {
		t.Error("missing capabilities in response")
	}
	info, ok := result["serverInfo"].(map[string]interface{})
	if !ok {
		t.Fatal("missing serverInfo")
	}
	if info["name"] != "claude-remote" {
		t.Errorf("want name=claude-remote, got %v", info["name"])
	}
}

func TestMCPToolsList(t *testing.T) {
	s := setupMCPServer(t)
	body := `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`
	w := mcpPost(t, s, body)

	if w.Code != 200 {
		t.Fatalf("want 200, got %d", w.Code)
	}

	var resp jsonrpcResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	result, _ := resp.Result.(map[string]interface{})
	tools, ok := result["tools"].([]interface{})
	if !ok {
		t.Fatal("tools not an array")
	}

	names := map[string]bool{}
	for _, tool := range tools {
		m, _ := tool.(map[string]interface{})
		if name, ok := m["name"].(string); ok {
			names[name] = true
		}
	}
	if !names["handoff"] {
		t.Error("missing handoff tool")
	}
	if !names["status"] {
		t.Error("missing status tool")
	}
}

func TestMCPToolCallStatus(t *testing.T) {
	s := setupMCPServer(t)
	body := `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"status","arguments":{}}}`
	w := mcpPost(t, s, body)

	if w.Code != 200 {
		t.Fatalf("want 200, got %d", w.Code)
	}

	var resp jsonrpcResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	result, _ := resp.Result.(map[string]interface{})
	content, ok := result["content"].([]interface{})
	if !ok || len(content) == 0 {
		t.Fatal("missing content in result")
	}
	first, _ := content[0].(map[string]interface{})
	text, _ := first["text"].(string)
	if !strings.Contains(text, "claude-remote") {
		t.Errorf("status text should contain 'claude-remote', got: %s", text)
	}
}

func TestMCPToolCallHandoff(t *testing.T) {
	dir := t.TempDir()
	s := setupMCPServerDir(t, dir)
	body := `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"handoff","arguments":{"dir":"` + dir + `"}}}`
	w := mcpPost(t, s, body)

	if w.Code != 200 {
		t.Fatalf("want 200, got %d", w.Code)
	}

	var resp jsonrpcResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	result, _ := resp.Result.(map[string]interface{})
	content, ok := result["content"].([]interface{})
	if !ok || len(content) == 0 {
		t.Fatal("missing content in result")
	}
	first, _ := content[0].(map[string]interface{})
	text, _ := first["text"].(string)
	if !strings.Contains(text, "/handoff?") {
		t.Errorf("handoff text should contain '/handoff?', got: %s", text)
	}
	if !strings.Contains(text, "token=") {
		t.Errorf("handoff text should contain 'token=', got: %s", text)
	}
}

func TestMCPRejectsNonLocalhost(t *testing.T) {
	s := setupMCPServer(t)
	body := `{"jsonrpc":"2.0","id":5,"method":"initialize","params":{}}`
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	req.RemoteAddr = "192.168.1.100:12345"
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.localHandler().ServeHTTP(w, req)

	if w.Code != 403 {
		t.Errorf("want 403, got %d", w.Code)
	}
}

func TestMCPRejectsGET(t *testing.T) {
	s := setupMCPServer(t)
	req := httptest.NewRequest(http.MethodGet, "/mcp", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	w := httptest.NewRecorder()
	s.localHandler().ServeHTTP(w, req)

	if w.Code != 405 {
		t.Errorf("want 405, got %d", w.Code)
	}
}

// A handoff QR must survive directories containing spaces and "&".
func TestMCPToolCallHandoffEscapesDir(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "My Project & Co")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	s := setupMCPServerDir(t, root)
	body := `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"handoff","arguments":{"dir":` +
		mustJSON(t, dir) + `}}}`
	w := mcpPost(t, s, body)

	var resp jsonrpcResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	result, _ := resp.Result.(map[string]interface{})
	content, ok := result["content"].([]interface{})
	if !ok || len(content) == 0 {
		t.Fatalf("missing content: %s", w.Body.String())
	}
	first, _ := content[0].(map[string]interface{})
	text, _ := first["text"].(string)
	line := ""
	for _, l := range strings.Split(text, "\n") {
		if strings.Contains(l, "/handoff?") {
			line = strings.TrimSpace(l)
		}
	}
	if line == "" {
		t.Fatalf("no handoff URL in output: %s", text)
	}
	u, err := neturl.Parse(line)
	if err != nil {
		t.Fatalf("handoff URL is not parseable: %v (%s)", err, line)
	}
	resolvedDir, _ := filepath.EvalSymlinks(dir)
	if got := u.Query().Get("dir"); got != resolvedDir {
		t.Errorf("dir round-trip failed: got %q want %q", got, resolvedDir)
	}
}

// Directories outside the allowlist must not produce a handoff token.
func TestMCPToolCallHandoffRejectsOutsideDir(t *testing.T) {
	s := setupMCPServer(t)
	body := `{"jsonrpc":"2.0","id":6,"method":"tools/call","params":{"name":"handoff","arguments":{"dir":"/etc"}}}`
	w := mcpPost(t, s, body)

	var resp jsonrpcResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Error == nil {
		t.Fatalf("want error for disallowed dir, got %s", w.Body.String())
	}
}

func mustJSON(t *testing.T, v string) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The tunnel forwards public traffic from 127.0.0.1; MCP must still refuse it,
// otherwise anyone with the public URL could mint handoff tokens.
func TestMCPRejectsTunnelForwardedRequest(t *testing.T) {
	s := setupMCPServer(t)
	req := httptest.NewRequest(http.MethodPost, "/mcp",
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
	req.RemoteAddr = "127.0.0.1:12345"
	req.Header.Set("CF-Connecting-IP", "203.0.113.9")
	w := httptest.NewRecorder()
	s.localHandler().ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("want 403 for forwarded request, got %d", w.Code)
	}
}

// /mcp must not be reachable at all on the public mux.
func TestMCPNotOnPublicMux(t *testing.T) {
	s := setupMCPServer(t)
	req := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(`{}`))
	req.RemoteAddr = "127.0.0.1:12345"
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)
	if w.Code == http.StatusOK {
		t.Fatal("/mcp must not be served on the public listener")
	}
}
