# MCP Handoff Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add MCP Streamable HTTP server to claude-remote so Claude Code can call a `handoff` tool to generate a QR code for continuing sessions on phone.

**Architecture:** Add `/mcp` endpoint (JSON-RPC over HTTP, localhost-only) to existing server with two tools: `handoff` and `status`. Add `/handoff` endpoint for QR token exchange. Extend auth with short-lived handoff tokens. Add mode selector screen to web UI. All existing routes and behavior unchanged.

**Tech Stack:** Go 1.25, existing deps (gorilla/websocket, golang-jwt, skip2/go-qrcode, creack/pty). No new dependencies.

---

## File Structure

| File | Action | Responsibility |
|------|--------|----------------|
| `mcp.go` | Create | MCP Streamable HTTP handler, JSON-RPC routing, tool implementations |
| `mcp_test.go` | Create | Tests for MCP protocol, tools, localhost restriction |
| `auth.go` | Modify | Add handoff token generation/validation (short-lived, one-time) |
| `auth_test.go` | Modify | Tests for handoff tokens |
| `terminal.go` | Modify | Add `dir` field, `StartWithResume()` method |
| `terminal_test.go` | Modify | Test for StartWithResume |
| `server.go` | Modify | Register `/mcp` + `/handoff` routes, add handleHandoff, expose hostname/TLS detection |
| `server_test.go` | Modify | Tests for /handoff and /mcp routing |
| `main.go` | Modify | Move `detectTailscaleHost` to server.go (make it a method) |
| `static/index.html` | Modify | Add `#screen-handoff` div |
| `static/app.js` | Modify | URL param detection, mode selector logic, resume start |
| `static/style.css` | Modify | Styles for handoff screen |
| `.claude/skills/remote.md` | Create | Skill file for `/remote` command |

---

### Task 1: Handoff tokens in auth.go

**Files:**
- Modify: `auth.go`
- Modify: `auth_test.go`

- [ ] **Step 1: Write failing tests for handoff tokens**

Add to `auth_test.go`:

```go
func TestHandoffTokenGenerate(t *testing.T) {
	a := &Auth{}
	a.handoffTokens = make(map[string]time.Time)
	token := a.GenerateHandoffToken()
	if len(token) != 64 {
		t.Errorf("want 64 hex chars, got %d", len(token))
	}
}

func TestHandoffTokenValidateOnce(t *testing.T) {
	a := &Auth{}
	a.handoffTokens = make(map[string]time.Time)
	token := a.GenerateHandoffToken()
	if !a.ValidateHandoffToken(token) {
		t.Error("first use should succeed")
	}
	if a.ValidateHandoffToken(token) {
		t.Error("second use should fail (single-use)")
	}
}

func TestHandoffTokenExpired(t *testing.T) {
	a := &Auth{}
	a.handoffTokens = make(map[string]time.Time)
	token := a.GenerateHandoffToken()
	// Manually expire it
	a.mu.Lock()
	a.handoffTokens[token] = time.Now().Add(-6 * time.Minute)
	a.mu.Unlock()
	if a.ValidateHandoffToken(token) {
		t.Error("expired token should fail")
	}
}

func TestHandoffTokenInvalid(t *testing.T) {
	a := &Auth{}
	a.handoffTokens = make(map[string]time.Time)
	if a.ValidateHandoffToken("bogus") {
		t.Error("invalid token should fail")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /Users/datducnguyenhuu/Desktop/claude-remote && go test -run TestHandoff -v`
Expected: compilation error — `handoffTokens` field and methods don't exist yet

- [ ] **Step 3: Implement handoff tokens in auth.go**

Add `handoffTokens` field to `Auth` struct and two new methods. Do NOT change any existing fields or methods.

In `auth.go`, add field to struct:

```go
type Auth struct {
	secret         []byte
	secretPath     string
	pendingToken   string
	jwtExpiry      time.Duration
	mu             sync.Mutex
	handoffTokens  map[string]time.Time // NEW: token -> expiry time
}
```

Initialize the map in `NewAuth`:

```go
func NewAuth(secretPath string) *Auth {
	return &Auth{
		secretPath:    secretPath,
		jwtExpiry:     90 * 24 * time.Hour,
		handoffTokens: make(map[string]time.Time),
	}
}
```

Add two new methods at end of file:

```go
func (a *Auth) GenerateHandoffToken() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	b := make([]byte, 32)
	rand.Read(b)
	token := hex.EncodeToString(b)
	a.handoffTokens[token] = time.Now().Add(5 * time.Minute)
	// Cleanup expired tokens
	now := time.Now()
	for k, exp := range a.handoffTokens {
		if now.After(exp) {
			delete(a.handoffTokens, k)
		}
	}
	return token
}

func (a *Auth) ValidateHandoffToken(token string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	exp, ok := a.handoffTokens[token]
	if !ok {
		return false
	}
	delete(a.handoffTokens, token) // single-use
	return time.Now().Before(exp)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /Users/datducnguyenhuu/Desktop/claude-remote && go test -run TestHandoff -v`
Expected: all 4 tests PASS

- [ ] **Step 5: Run all existing tests to verify no regression**

Run: `cd /Users/datducnguyenhuu/Desktop/claude-remote && go test ./... -v -count=1`
Expected: all existing tests still PASS

- [ ] **Step 6: Commit**

```bash
cd /Users/datducnguyenhuu/Desktop/claude-remote
git add auth.go auth_test.go
git commit -m "feat(auth): add short-lived handoff tokens for phone QR exchange"
```

---

### Task 2: Track dir and add StartWithResume in terminal.go

**Files:**
- Modify: `terminal.go`
- Modify: `terminal_test.go`

- [ ] **Step 1: Write failing test for StartWithResume**

Add to `terminal_test.go`:

```go
func TestTerminalDir(t *testing.T) {
	dir := t.TempDir()
	tm := NewTerminalManager("echo", []string{"hello"})
	if err := tm.StartInDir(dir); err != nil {
		t.Fatal(err)
	}
	defer tm.Stop()
	tm.mu.Lock()
	got := tm.dir
	tm.mu.Unlock()
	if got != dir {
		t.Errorf("want dir %q, got %q", dir, got)
	}
}

func TestStartWithResume(t *testing.T) {
	dir := t.TempDir()
	// Use "echo" as a stand-in for claude binary — just verify it spawns
	tm := NewTerminalManager("echo", nil)
	if err := tm.StartWithResume(dir); err != nil {
		t.Fatal(err)
	}
	defer tm.Stop()
	tm.mu.Lock()
	running := tm.running
	gotDir := tm.dir
	tm.mu.Unlock()
	if !running {
		t.Error("expected running after StartWithResume")
	}
	if gotDir != dir {
		t.Errorf("want dir %q, got %q", dir, gotDir)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /Users/datducnguyenhuu/Desktop/claude-remote && go test -run "TestTerminalDir|TestStartWithResume" -v`
Expected: compilation error — `dir` field and `StartWithResume` don't exist

- [ ] **Step 3: Add dir field and StartWithResume method**

In `terminal.go`, add `dir` field to `TerminalManager` struct:

```go
type TerminalManager struct {
	cmd     string
	args    []string
	ptmx    *os.File
	process *exec.Cmd
	buffer  *RingBuffer
	clients map[*websocket.Conn]bool
	mu      sync.Mutex
	running bool
	dir     string // NEW: working directory of current session
}
```

Set `dir` in existing `StartInDir` (add one line, don't change anything else):

```go
func (tm *TerminalManager) StartInDir(dir string) error {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	if tm.running {
		return nil
	}
	tm.buffer.Clear()
	tm.dir = dir // NEW: track directory
	c := exec.Command(tm.cmd, tm.args...)
	// ... rest unchanged ...
```

Add new method at end of file (after `handleControlMessage`):

```go
func (tm *TerminalManager) StartWithResume(dir string) error {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	if tm.running {
		return nil
	}
	tm.buffer.Clear()
	tm.dir = dir
	c := exec.Command(tm.cmd, "--continue")
	c.Env = os.Environ()
	if dir != "" {
		c.Dir = dir
	}
	ptmx, err := pty.Start(c)
	if err != nil {
		return fmt.Errorf("start pty resume: %w", err)
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
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /Users/datducnguyenhuu/Desktop/claude-remote && go test -run "TestTerminalDir|TestStartWithResume" -v`
Expected: PASS

- [ ] **Step 5: Run all tests**

Run: `cd /Users/datducnguyenhuu/Desktop/claude-remote && go test ./... -v -count=1`
Expected: all PASS

- [ ] **Step 6: Commit**

```bash
cd /Users/datducnguyenhuu/Desktop/claude-remote
git add terminal.go terminal_test.go
git commit -m "feat(terminal): track working dir and add StartWithResume for --continue"
```

---

### Task 3: Move detectTailscaleHost to server.go and expose TLS detection

**Files:**
- Modify: `main.go`
- Modify: `server.go`

- [ ] **Step 1: Move detectTailscaleHost from main.go to server.go**

Cut `detectTailscaleHost()` function from `main.go` and paste it at the bottom of `server.go`. The function signature stays identical — it's a package-level function, so all existing callers (`main.go:cmdSetup`, `main.go:cmdStatus`) still work.

- [ ] **Step 2: Add TLS detection helper to server.go**

Add at end of `server.go`:

```go
func detectProto(dataDirs ...string) string {
	for _, dir := range dataDirs {
		matches, _ := filepath.Glob(filepath.Join(dir, "*.crt"))
		if len(matches) > 0 {
			return "https"
		}
	}
	return "http"
}
```

- [ ] **Step 3: Verify build and all tests pass**

Run: `cd /Users/datducnguyenhuu/Desktop/claude-remote && go build ./... && go test ./... -v -count=1`
Expected: builds clean, all tests PASS

- [ ] **Step 4: Commit**

```bash
cd /Users/datducnguyenhuu/Desktop/claude-remote
git add main.go server.go
git commit -m "refactor: move detectTailscaleHost to server.go, add detectProto helper"
```

---

### Task 4: MCP Streamable HTTP handler (mcp.go)

**Files:**
- Create: `mcp.go`
- Create: `mcp_test.go`

- [ ] **Step 1: Write failing tests for MCP handler**

Create `mcp_test.go`:

```go
package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type jsonRPCRequest struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      interface{} `json:"id,omitempty"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
}

type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      interface{}     `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   json.RawMessage `json:"error,omitempty"`
}

func setupMCPServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	cfg := &Config{Port: 8822, AllowedDirs: []string{dir}, ClaudePath: "echo", DataDir: dir}
	auth := NewAuth(cfg.SecretPath())
	auth.GenerateSecret()
	s := NewServer(cfg, auth)
	s.registerRoutes()
	return s
}

func mcpPost(t *testing.T, s *Server, body jsonRPCRequest) *httptest.ResponseRecorder {
	t.Helper()
	data, _ := json.Marshal(body)
	req := httptest.NewRequest("POST", "/mcp", bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "127.0.0.1:12345"
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)
	return w
}

func TestMCPInitialize(t *testing.T) {
	s := setupMCPServer(t)
	w := mcpPost(t, s, jsonRPCRequest{
		JSONRPC: "2.0", ID: 1, Method: "initialize",
		Params: map[string]interface{}{
			"protocolVersion": "2025-03-26",
			"clientInfo":      map[string]string{"name": "claude-code"},
			"capabilities":    map[string]interface{}{},
		},
	})
	if w.Code != 200 {
		t.Fatalf("want 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp jsonRPCResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Error != nil {
		t.Fatalf("unexpected error: %s", string(resp.Error))
	}
}

func TestMCPToolsList(t *testing.T) {
	s := setupMCPServer(t)
	w := mcpPost(t, s, jsonRPCRequest{JSONRPC: "2.0", ID: 2, Method: "tools/list"})
	if w.Code != 200 {
		t.Fatalf("want 200, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "handoff") {
		t.Error("tools/list should contain handoff tool")
	}
	if !strings.Contains(body, "status") {
		t.Error("tools/list should contain status tool")
	}
}

func TestMCPToolCallStatus(t *testing.T) {
	s := setupMCPServer(t)
	w := mcpPost(t, s, jsonRPCRequest{
		JSONRPC: "2.0", ID: 3, Method: "tools/call",
		Params: map[string]interface{}{
			"name":      "status",
			"arguments": map[string]interface{}{},
		},
	})
	if w.Code != 200 {
		t.Fatalf("want 200, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "claude-remote") {
		t.Error("status should contain service name")
	}
}

func TestMCPToolCallHandoff(t *testing.T) {
	s := setupMCPServer(t)
	w := mcpPost(t, s, jsonRPCRequest{
		JSONRPC: "2.0", ID: 4, Method: "tools/call",
		Params: map[string]interface{}{
			"name":      "handoff",
			"arguments": map[string]interface{}{"dir": "/tmp/test"},
		},
	})
	if w.Code != 200 {
		t.Fatalf("want 200, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "/handoff?") {
		t.Error("handoff should return URL with /handoff endpoint")
	}
	if !strings.Contains(body, "token=") {
		t.Error("handoff should return URL with token param")
	}
}

func TestMCPRejectsNonLocalhost(t *testing.T) {
	s := setupMCPServer(t)
	data, _ := json.Marshal(jsonRPCRequest{JSONRPC: "2.0", ID: 1, Method: "initialize"})
	req := httptest.NewRequest("POST", "/mcp", bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "192.168.1.100:12345" // non-localhost
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)
	if w.Code != 403 {
		t.Errorf("want 403 for non-localhost, got %d", w.Code)
	}
}

func TestMCPRejectsGET(t *testing.T) {
	s := setupMCPServer(t)
	req := httptest.NewRequest("GET", "/mcp", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)
	if w.Code != 405 {
		t.Errorf("want 405 for GET, got %d", w.Code)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /Users/datducnguyenhuu/Desktop/claude-remote && go test -run TestMCP -v`
Expected: compilation error — `/mcp` handler doesn't exist

- [ ] **Step 3: Create mcp.go**

```go
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"

	"github.com/skip2/go-qrcode"
)

type jsonrpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      interface{}     `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type jsonrpcResponse struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      interface{} `json:"id"`
	Result  interface{} `json:"result,omitempty"`
	Error   interface{} `json:"error,omitempty"`
}

type mcpToolDef struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema interface{} `json:"inputSchema"`
}

type mcpContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type mcpToolResult struct {
	Content []mcpContent `json:"content"`
}

func (s *Server) handleMCP(w http.ResponseWriter, r *http.Request) {
	// Localhost-only check
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	ip := net.ParseIP(host)
	if ip == nil || (!ip.IsLoopback()) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	var req jsonrpcRequest
	if err := json.Unmarshal(body, &req); err != nil {
		s.mcpError(w, nil, -32700, "parse error")
		return
	}

	switch req.Method {
	case "initialize":
		s.mcpRespond(w, req.ID, map[string]interface{}{
			"protocolVersion": "2025-03-26",
			"serverInfo": map[string]string{
				"name":    "claude-remote",
				"version": version,
			},
			"capabilities": map[string]interface{}{
				"tools": map[string]interface{}{},
			},
		})
	case "notifications/initialized":
		// No response needed for notifications
		w.WriteHeader(http.StatusNoContent)
	case "tools/list":
		s.mcpRespond(w, req.ID, map[string]interface{}{
			"tools": []mcpToolDef{
				{
					Name:        "handoff",
					Description: "Generate QR code to continue this Claude session on phone",
					InputSchema: map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"dir": map[string]string{
								"type":        "string",
								"description": "Working directory path",
							},
							"mode": map[string]interface{}{
								"type":        "string",
								"enum":        []string{"attach", "continue", "choose"},
								"default":     "choose",
								"description": "Session mode: attach to running PTY, continue with --continue, or let user choose",
							},
						},
						"required": []string{"dir"},
					},
				},
				{
					Name:        "status",
					Description: "Check claude-remote service status and running session",
					InputSchema: map[string]interface{}{
						"type":       "object",
						"properties": map[string]interface{}{},
					},
				},
			},
		})
	case "tools/call":
		s.handleMCPToolCall(w, req)
	default:
		s.mcpError(w, req.ID, -32601, fmt.Sprintf("method not found: %s", req.Method))
	}
}

func (s *Server) handleMCPToolCall(w http.ResponseWriter, req jsonrpcRequest) {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		s.mcpError(w, req.ID, -32602, "invalid params")
		return
	}

	switch params.Name {
	case "handoff":
		s.mcpToolHandoff(w, req.ID, params.Arguments)
	case "status":
		s.mcpToolStatus(w, req.ID)
	default:
		s.mcpError(w, req.ID, -32602, fmt.Sprintf("unknown tool: %s", params.Name))
	}
}

func (s *Server) mcpToolHandoff(w http.ResponseWriter, id interface{}, argsRaw json.RawMessage) {
	var args struct {
		Dir  string `json:"dir"`
		Mode string `json:"mode"`
	}
	if err := json.Unmarshal(argsRaw, &args); err != nil {
		s.mcpError(w, id, -32602, "invalid arguments")
		return
	}
	if args.Mode == "" {
		args.Mode = "choose"
	}

	hostname := detectTailscaleHost()
	home, _ := os.UserHomeDir()
	proto := detectProto(s.config.DataDir, home+"/Desktop", "/var/run/tailscale")

	token := s.auth.GenerateHandoffToken()
	url := fmt.Sprintf("%s://%s:%d/handoff?token=%s&dir=%s&mode=%s",
		proto, hostname, s.config.Port, token,
		strings.ReplaceAll(args.Dir, " ", "%20"),
		args.Mode,
	)

	// Generate QR as ASCII art
	qr, err := qrcode.New(url, qrcode.Medium)
	var qrText string
	if err == nil {
		qrText = qr.ToSmallString(false)
	} else {
		qrText = "[QR generation failed]"
	}

	text := fmt.Sprintf("%s\nScan QR or open:\n%s\n\nToken expires in 5 minutes.", qrText, url)

	s.mcpRespond(w, id, mcpToolResult{
		Content: []mcpContent{{Type: "text", Text: text}},
	})
}

func (s *Server) mcpToolStatus(w http.ResponseWriter, id interface{}) {
	s.terminal.mu.Lock()
	running := s.terminal.running
	dir := s.terminal.dir
	clientCount := len(s.terminal.clients)
	s.terminal.mu.Unlock()

	text := fmt.Sprintf("claude-remote v%s\nSession: ", version)
	if running {
		text += fmt.Sprintf("running\nDir: %s\nClients: %d connected", dir, clientCount)
	} else {
		text += "idle (no active session)"
	}

	s.mcpRespond(w, id, mcpToolResult{
		Content: []mcpContent{{Type: "text", Text: text}},
	})
}

func (s *Server) mcpRespond(w http.ResponseWriter, id interface{}, result interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(jsonrpcResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
	})
}

func (s *Server) mcpError(w http.ResponseWriter, id interface{}, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(jsonrpcResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error:   map[string]interface{}{"code": code, "message": msg},
	})
}
```

- [ ] **Step 4: Register /mcp route in server.go**

In `server.go`, in `registerRoutes()`, add the MCP route BEFORE the protected mux. Insert after the `/health` line:

```go
func (s *Server) registerRoutes() {
	s.mux.HandleFunc("/auth/scan", s.handleAuthScan)
	s.mux.HandleFunc("/health", s.handleHealth)
	s.mux.HandleFunc("/mcp", s.handleMCP) // NEW: MCP endpoint (localhost-only, no JWT)

	protected := http.NewServeMux()
	// ... rest unchanged ...
```

- [ ] **Step 5: Run MCP tests**

Run: `cd /Users/datducnguyenhuu/Desktop/claude-remote && go test -run TestMCP -v`
Expected: all 6 tests PASS

- [ ] **Step 6: Run all tests**

Run: `cd /Users/datducnguyenhuu/Desktop/claude-remote && go test ./... -v -count=1`
Expected: all PASS

- [ ] **Step 7: Commit**

```bash
cd /Users/datducnguyenhuu/Desktop/claude-remote
git add mcp.go mcp_test.go server.go
git commit -m "feat(mcp): add MCP Streamable HTTP handler with handoff and status tools"
```

---

### Task 5: /handoff endpoint and resume support in server.go

**Files:**
- Modify: `server.go`
- Modify: `server_test.go`

- [ ] **Step 1: Write failing tests**

Add to `server_test.go`:

```go
func TestHandoffValidToken(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{Port: 8822, AllowedDirs: []string{dir}, ClaudePath: "echo", DataDir: dir}
	auth := NewAuth(cfg.SecretPath())
	auth.GenerateSecret()
	token := auth.GenerateHandoffToken()
	s := NewServer(cfg, auth)
	s.registerRoutes()

	req := httptest.NewRequest("GET", "/handoff?token="+token+"&dir="+dir+"&mode=choose", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)
	if w.Code != 302 {
		t.Errorf("want 302 redirect, got %d: %s", w.Code, w.Body.String())
	}
	loc := w.Header().Get("Location")
	if loc == "" {
		t.Fatal("expected Location header")
	}
	if !strings.Contains(loc, "dir=") {
		t.Error("redirect should contain dir param")
	}
	if !strings.Contains(loc, "mode=choose") {
		t.Error("redirect should contain mode param")
	}
	// Should set auth cookie
	found := false
	for _, c := range w.Result().Cookies() {
		if c.Name == "claude-remote-auth" {
			found = true
		}
	}
	if !found {
		t.Error("expected auth cookie")
	}
}

func TestHandoffInvalidToken(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{Port: 8822, AllowedDirs: []string{dir}, ClaudePath: "echo", DataDir: dir}
	auth := NewAuth(cfg.SecretPath())
	auth.GenerateSecret()
	s := NewServer(cfg, auth)
	s.registerRoutes()

	req := httptest.NewRequest("GET", "/handoff?token=bogus&dir="+dir+"&mode=choose", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)
	if w.Code != 401 {
		t.Errorf("want 401, got %d", w.Code)
	}
}

func TestClaudeStartWithResume(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{Port: 8822, AllowedDirs: []string{dir}, ClaudePath: "echo", DataDir: dir}
	auth := NewAuth(cfg.SecretPath())
	auth.GenerateSecret()
	jwt, _ := auth.IssueJWT("test-device")
	s := NewServer(cfg, auth)
	s.registerRoutes()

	body := `{"dir":"` + dir + `","resume":true}`
	req := httptest.NewRequest("POST", "/api/claude/start", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: "claude-remote-auth", Value: jwt})
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Errorf("want 200, got %d: %s", w.Code, w.Body.String())
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /Users/datducnguyenhuu/Desktop/claude-remote && go test -run "TestHandoff|TestClaudeStartWithResume" -v`
Expected: FAIL — `/handoff` handler doesn't exist, `resume` field not handled

- [ ] **Step 3: Add /handoff handler and update handleClaudeStart**

In `server.go`, register the route in `registerRoutes()`:

```go
s.mux.HandleFunc("/handoff", s.handleHandoff) // NEW: after /mcp line
```

Add the handler:

```go
func (s *Server) handleHandoff(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if token == "" || !s.auth.ValidateHandoffToken(token) {
		http.Error(w, `{"error":"invalid or expired handoff token"}`, http.StatusUnauthorized)
		return
	}

	dir := r.URL.Query().Get("dir")
	mode := r.URL.Query().Get("mode")
	if mode == "" {
		mode = "choose"
	}

	// Issue JWT cookie (same logic as handleAuthScan)
	deviceID := fmt.Sprintf("handoff-%d", time.Now().UnixNano())
	jwt, err := s.auth.IssueJWT(deviceID)
	if err != nil {
		http.Error(w, `{"error":"failed to issue token"}`, http.StatusInternalServerError)
		return
	}
	sameSite := http.SameSiteLaxMode
	if s.useTLS {
		sameSite = http.SameSiteStrictMode
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "claude-remote-auth",
		Value:    jwt,
		Path:     "/",
		MaxAge:   90 * 24 * 3600,
		HttpOnly: true,
		Secure:   s.useTLS,
		SameSite: sameSite,
	})

	// Redirect to UI with params
	redirect := fmt.Sprintf("/?dir=%s&mode=%s", dir, mode)
	http.Redirect(w, r, redirect, http.StatusFound)
}
```

Update `handleClaudeStart` to accept `resume` field — change the request struct and add the branch:

```go
func (s *Server) handleClaudeStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Dir    string `json:"dir"`
		Resume bool   `json:"resume"` // NEW
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request"}`, http.StatusBadRequest)
		return
	}
	// Validate directory is within allowed dirs
	resolved, err := filepath.EvalSymlinks(req.Dir)
	if err != nil {
		http.Error(w, `{"error":"invalid directory"}`, http.StatusBadRequest)
		return
	}
	allowed := false
	for _, d := range s.config.AllowedDirs {
		ad, _ := filepath.EvalSymlinks(d)
		if strings.HasPrefix(resolved, ad) {
			allowed = true
			break
		}
	}
	if !allowed {
		http.Error(w, `{"error":"directory not allowed"}`, http.StatusForbidden)
		return
	}
	// Stop existing session if running
	s.terminal.Stop()
	// Start new session — resume or fresh
	var startErr error
	if req.Resume {
		startErr = s.terminal.StartWithResume(resolved) // NEW
	} else {
		startErr = s.terminal.StartInDir(resolved) // existing
	}
	if startErr != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, `{"error":"failed to start: %s"}`, startErr.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"status":"started","dir":"%s"}`, resolved)
}
```

Add `"time"` to imports in `server.go` if not already there.

- [ ] **Step 4: Run tests**

Run: `cd /Users/datducnguyenhuu/Desktop/claude-remote && go test -run "TestHandoff|TestClaudeStartWithResume" -v`
Expected: all PASS

- [ ] **Step 5: Run all tests**

Run: `cd /Users/datducnguyenhuu/Desktop/claude-remote && go test ./... -v -count=1`
Expected: all PASS

- [ ] **Step 6: Commit**

```bash
cd /Users/datducnguyenhuu/Desktop/claude-remote
git add server.go server_test.go
git commit -m "feat(server): add /handoff endpoint and resume support in /api/claude/start"
```

---

### Task 6: Web UI — handoff mode selector screen

**Files:**
- Modify: `static/index.html`
- Modify: `static/style.css`
- Modify: `static/app.js`

- [ ] **Step 1: Add handoff screen to index.html**

Insert after `<!-- Screen 1: Folder Picker -->` closing div and before `<!-- Screen 2: Claude Chat -->`:

```html
  <!-- Screen 3: Handoff Mode Selector (NEW) -->
  <div id="screen-handoff" class="screen">
    <div class="picker-title">Tiếp tục từ máy tính</div>
    <div id="handoff-dir" style="padding: 8px 12px; font-size: 13px; color: #888;"></div>
    <div id="handoff-options">
      <button id="btn-attach" class="handoff-btn">
        <span class="handoff-icon">🔗</span>
        <span class="handoff-label">Attach session</span>
        <span class="handoff-desc">Kết nối vào terminal đang chạy trên máy tính</span>
      </button>
      <button id="btn-continue" class="handoff-btn">
        <span class="handoff-icon">📋</span>
        <span class="handoff-label">Continue session</span>
        <span class="handoff-desc">Tạo session mới với đầy đủ lịch sử hội thoại</span>
      </button>
      <button id="btn-new-folder" class="handoff-btn">
        <span class="handoff-icon">📁</span>
        <span class="handoff-label">Chọn thư mục khác</span>
        <span class="handoff-desc">Bỏ qua, về trang chọn thư mục</span>
      </button>
    </div>
  </div>
```

- [ ] **Step 2: Add handoff styles to style.css**

Append to end of `style.css`:

```css
/* Handoff Mode Selector */
#handoff-options {
  display: flex; flex-direction: column; gap: 12px;
  padding: 16px 12px; flex: 1;
}
.handoff-btn {
  display: flex; flex-direction: column; gap: 4px;
  padding: 16px; border: 1px solid #333; border-radius: 12px;
  background: #16213e; color: #eee; cursor: pointer;
  text-align: left;
}
.handoff-btn:active { background: #1e3a5f; border-color: #60a5fa; }
.handoff-icon { font-size: 24px; }
.handoff-label { font-size: 16px; font-weight: 600; }
.handoff-desc { font-size: 13px; color: #888; }
```

- [ ] **Step 3: Add handoff logic to app.js**

Add at the TOP of the IIFE (after variable declarations, before `quickDirs`):

```js
  // --- Handoff URL param detection ---
  const urlParams = new URLSearchParams(location.search);
  const handoffDir = urlParams.get('dir');
  const handoffMode = urlParams.get('mode');
```

Add handoff button handlers BEFORE the `// --- Init ---` section:

```js
  // --- Handoff Mode Selector ---
  function showHandoffScreen(dir, mode) {
    // Clean URL params without reload
    history.replaceState({}, '', '/');

    if (mode === 'attach') {
      // Go straight to chat, attach to running session
      attachToSession(dir);
      return;
    }
    if (mode === 'continue') {
      startContinueSession(dir);
      return;
    }
    // mode === 'choose': show selector
    document.getElementById('handoff-dir').textContent = dir;
    showScreen('screen-handoff');
  }

  function attachToSession(dir) {
    showScreen('screen-chat');
    document.getElementById('chat-dir').textContent = dir;
    document.getElementById('output-text').innerHTML = '';
    initTerminal();
    connectWS();
  }

  async function startContinueSession(dir) {
    showScreen('screen-chat');
    document.getElementById('chat-dir').textContent = dir;
    document.getElementById('output-text').innerHTML = '';
    try {
      const resp = await fetch('/api/claude/start', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ dir: dir, resume: true })
      });
      const data = await resp.json();
      if (data.error) {
        alert('Lỗi: ' + data.error);
        showScreen('screen-picker');
        return;
      }
    } catch (e) {
      alert('Lỗi kết nối: ' + e.message);
      showScreen('screen-picker');
      return;
    }
    initTerminal();
    connectWS();
  }

  if (document.getElementById('btn-attach')) {
    document.getElementById('btn-attach').addEventListener('click', () => {
      attachToSession(handoffDir || selectedDir);
    });
  }
  if (document.getElementById('btn-continue')) {
    document.getElementById('btn-continue').addEventListener('click', () => {
      startContinueSession(handoffDir || selectedDir);
    });
  }
  if (document.getElementById('btn-new-folder')) {
    document.getElementById('btn-new-folder').addEventListener('click', () => {
      history.replaceState({}, '', '/');
      showScreen('screen-picker');
    });
  }
```

Modify the `// --- Init ---` section at the bottom to handle handoff:

```js
  // --- Init ---
  if (handoffDir && handoffMode) {
    // Handoff flow: came from QR scan
    showHandoffScreen(handoffDir, handoffMode);
  } else {
    // Existing flow: folder picker or auto-attach
    initPicker();
    fetch('/api/claude/status').then(r => r.json()).then(data => {
      if (data.running) {
        showScreen('screen-chat');
        document.getElementById('chat-dir').textContent = 'Phiên đang chạy';
        initTerminal();
        connectWS();
      }
    }).catch(() => {});
  }
```

- [ ] **Step 4: Test manually — verify existing flow unbroken**

Open `http://localhost:8822/` (no params) → should show folder picker as before.

- [ ] **Step 5: Test manually — verify handoff flow**

Open `http://localhost:8822/?dir=/tmp&mode=choose` → should show mode selector with 3 buttons.

- [ ] **Step 6: Commit**

```bash
cd /Users/datducnguyenhuu/Desktop/claude-remote
git add static/index.html static/app.js static/style.css
git commit -m "feat(ui): add handoff mode selector screen for phone session transfer"
```

---

### Task 7: Skill file for /remote command

**Files:**
- Create: `.claude/skills/remote.md`

- [ ] **Step 1: Create skill file**

```bash
mkdir -p /Users/datducnguyenhuu/Desktop/claude-remote/.claude/skills
```

Create `.claude/skills/remote.md`:

```markdown
---
name: remote
description: Generate QR code to continue current Claude session on phone via claude-remote
user-invocable: true
---

When the user invokes /remote, follow these steps:

1. Call the MCP tool `status` from the `claude-remote` server to check if the service is running.
   - If the service is not reachable, tell the user: "claude-remote service is not running. Start it with: claude-remote serve"

2. Call the MCP tool `handoff` from the `claude-remote` server with:
   - `dir`: set to the current working directory
   - `mode`: set to "choose"

3. Display the QR code result exactly as returned (it contains ASCII art QR + URL).

4. Tell the user: "Quét QR bằng điện thoại để tiếp tục session. Token hết hạn sau 5 phút."
```

- [ ] **Step 2: Commit**

```bash
cd /Users/datducnguyenhuu/Desktop/claude-remote
git add .claude/skills/remote.md
git commit -m "feat: add /remote skill for phone handoff QR generation"
```

---

### Task 8: Integration test and final verification

**Files:**
- All files from previous tasks

- [ ] **Step 1: Run full test suite**

Run: `cd /Users/datducnguyenhuu/Desktop/claude-remote && go test ./... -v -count=1`
Expected: all tests PASS

- [ ] **Step 2: Run with race detector**

Run: `cd /Users/datducnguyenhuu/Desktop/claude-remote && go test -race ./... -v -count=1`
Expected: no races detected

- [ ] **Step 3: Build binary**

Run: `cd /Users/datducnguyenhuu/Desktop/claude-remote && go build -o claude-remote .`
Expected: builds successfully

- [ ] **Step 4: Verify MCP registration command works**

Run: `claude mcp add --transport http claude-remote http://localhost:8822/mcp`
Expected: MCP server registered

- [ ] **Step 5: Update CLAUDE.md with MCP info**

Add to the API Endpoints table in CLAUDE.md:

```
| `/mcp` | POST | Localhost | MCP Streamable HTTP (JSON-RPC) |
| `/handoff` | GET | Token | Handoff token exchange → set cookie → redirect |
```

Add to Commands section:

```
| `/remote` | Skill: generate QR to continue session on phone |
```

- [ ] **Step 6: Final commit**

```bash
cd /Users/datducnguyenhuu/Desktop/claude-remote
git add CLAUDE.md
git commit -m "docs: update CLAUDE.md with MCP and handoff endpoints"
```
