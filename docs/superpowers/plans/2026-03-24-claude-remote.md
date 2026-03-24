# Claude Remote Implementation Plan

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a Go server that lets you control Claude Code CLI from a phone browser via WebSocket, with a file browser and QR code auth.

**Architecture:** Single Go binary with HTTP server. Spawns `claude` in a pty, pipes I/O over WebSocket to xterm.js in browser. File browser API for navigating Mac filesystem (read-only). JWT auth via one-time QR scan. Auto-starts via launchd. Tailscale for internet access.

**Tech Stack:** Go 1.22+, gorilla/websocket, creack/pty, golang-jwt/jwt/v5, skip2/go-qrcode, vanilla HTML/JS, xterm.js (CDN)

---

## File Structure

| File | Responsibility |
|------|---------------|
| `config.go` | Config struct, load/save `~/.claude-remote/`, defaults |
| `config_test.go` | Config load/save, defaults, missing dir creation |
| `auth.go` | Secret generation, QR code, one-time token, JWT sign/verify, middleware |
| `auth_test.go` | Token generation, JWT round-trip, expiry, revocation, single-use |
| `files.go` | File browser API: list dir, read file, allowlist, path security |
| `files_test.go` | Path validation, allowlist, dotfile blocking, traversal, symlinks |
| `terminal.go` | Pty session manager, WebSocket handler, output ring buffer, resize |
| `terminal_test.go` | Spawn echo via pty, WebSocket integration, reconnect buffer |
| `server.go` | HTTP server, route registration, TLS loading, graceful shutdown |
| `main.go` | CLI entry point: setup, serve, revoke, install, uninstall, status |
| `static/index.html` | SPA: terminal tab (xterm.js) + file browser tab |
| `static/app.js` | WebSocket management, file browser logic, tab switching |
| `static/style.css` | Mobile-first responsive styles |
| `launchd/com.claude-remote.plist` | macOS launchd auto-start config |

---

## Task 0: Project Init

- [ ] **Step 1: Initialize Go module and add dependencies**

```bash
cd /Users/datducnguyenhuu/Desktop/claude-remote
go mod init claude-remote
go get github.com/golang-jwt/jwt/v5
go get github.com/skip2/go-qrcode
go get github.com/gorilla/websocket
go get github.com/creack/pty
go mod tidy
```

Note: `go.mod` already exists from project setup. This step ensures all dependencies are fetched upfront.

- [ ] **Step 2: Commit**

```bash
git add go.mod go.sum
git commit -m "chore: add all Go dependencies"
```

---

## Task 1: Config

**Files:**
- Create: `config.go`
- Create: `config_test.go`

- [ ] **Step 1: Write config test**

```go
// config_test.go
package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Port != 8822 {
		t.Errorf("want port 8822, got %d", cfg.Port)
	}
	home, _ := os.UserHomeDir()
	if len(cfg.AllowedDirs) != 1 || cfg.AllowedDirs[0] != home {
		t.Errorf("want AllowedDirs=[%s], got %v", home, cfg.AllowedDirs)
	}
	if cfg.ClaudePath != "claude" {
		t.Errorf("want claude, got %s", cfg.ClaudePath)
	}
}

func TestConfigSaveLoad(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{
		Port:        9999,
		AllowedDirs: []string{"/tmp"},
		ClaudePath:  "/usr/local/bin/claude",
		DataDir:     dir,
	}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Port != 9999 {
		t.Errorf("want 9999, got %d", loaded.Port)
	}
	if loaded.ClaudePath != "/usr/local/bin/claude" {
		t.Errorf("want /usr/local/bin/claude, got %s", loaded.ClaudePath)
	}
}

func TestConfigCreatesDirIfMissing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "subdir")
	cfg := &Config{Port: 8822, DataDir: dir}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		t.Error("expected dir to be created")
	}
}
```

- [ ] **Step 2: Run test — expect FAIL**

Run: `cd /Users/datducnguyenhuu/Desktop/claude-remote && go test -run TestDefault -v`
Expected: FAIL — `DefaultConfig` not defined

- [ ] **Step 3: Implement config.go**

```go
// config.go
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

type Config struct {
	Port        int      `json:"port"`
	AllowedDirs []string `json:"allowed_dirs"`
	ClaudePath  string   `json:"claude_path"`
	DataDir     string   `json:"-"`
}

func DefaultConfig() *Config {
	home, _ := os.UserHomeDir()
	return &Config{
		Port:        8822,
		AllowedDirs: []string{home},
		ClaudePath:  "claude",
		DataDir:     filepath.Join(home, ".claude-remote"),
	}
}

func LoadConfig(dataDir string) (*Config, error) {
	path := filepath.Join(dataDir, "config.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			cfg := DefaultConfig()
			cfg.DataDir = dataDir
			return cfg, nil
		}
		return nil, err
	}
	cfg := DefaultConfig()
	cfg.DataDir = dataDir
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) Save() error {
	if err := os.MkdirAll(c.DataDir, 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(c.DataDir, "config.json"), data, 0600)
}

func (c *Config) SecretPath() string {
	return filepath.Join(c.DataDir, "secret.key")
}

func (c *Config) SessionsPath() string {
	return filepath.Join(c.DataDir, "sessions.json")
}
```

- [ ] **Step 4: Run tests — expect PASS**

Run: `cd /Users/datducnguyenhuu/Desktop/claude-remote && go test -run TestConfig -v && go test -run TestDefault -v`
Expected: ALL PASS

- [ ] **Step 5: Commit**

```bash
cd /Users/datducnguyenhuu/Desktop/claude-remote
git add config.go config_test.go
git commit -m "feat: config loading/saving with defaults"
```

---

## Task 2: Auth — JWT + QR + Token

**Files:**
- Create: `auth.go`
- Create: `auth_test.go`

**Dependencies:** `golang-jwt/jwt/v5`, `skip2/go-qrcode` (installed in Task 0)

- [ ] **Step 1: Write auth tests**

```go
// auth_test.go
package main

import (
	"os"
	"testing"
	"time"
)

func TestGenerateSecret(t *testing.T) {
	dir := t.TempDir()
	a := &Auth{secretPath: dir + "/secret.key"}
	if err := a.GenerateSecret(); err != nil {
		t.Fatal(err)
	}
	if len(a.secret) != 32 {
		t.Errorf("want 32 bytes, got %d", len(a.secret))
	}
	// file should exist with 0600
	info, err := os.Stat(a.secretPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("want 0600, got %o", info.Mode().Perm())
	}
}

func TestLoadSecret(t *testing.T) {
	dir := t.TempDir()
	a := &Auth{secretPath: dir + "/secret.key"}
	a.GenerateSecret()
	original := make([]byte, len(a.secret))
	copy(original, a.secret)

	a2 := &Auth{secretPath: dir + "/secret.key"}
	if err := a2.LoadSecret(); err != nil {
		t.Fatal(err)
	}
	if string(a2.secret) != string(original) {
		t.Error("loaded secret doesn't match")
	}
}

func TestGenerateToken(t *testing.T) {
	a := &Auth{}
	token := a.GenerateToken()
	if len(token) != 64 {
		t.Errorf("want 64 chars, got %d", len(token))
	}
	if a.pendingToken != token {
		t.Error("pending token not set")
	}
}

func TestTokenSingleUse(t *testing.T) {
	a := &Auth{}
	token := a.GenerateToken()
	if !a.ValidateToken(token) {
		t.Error("first use should succeed")
	}
	if a.ValidateToken(token) {
		t.Error("second use should fail")
	}
}

func TestJWTSignVerify(t *testing.T) {
	dir := t.TempDir()
	a := &Auth{secretPath: dir + "/secret.key"}
	a.GenerateSecret()

	tokenStr, err := a.IssueJWT("device-1")
	if err != nil {
		t.Fatal(err)
	}
	deviceID, err := a.VerifyJWT(tokenStr)
	if err != nil {
		t.Fatal(err)
	}
	if deviceID != "device-1" {
		t.Errorf("want device-1, got %s", deviceID)
	}
}

func TestJWTExpired(t *testing.T) {
	dir := t.TempDir()
	a := &Auth{secretPath: dir + "/secret.key", jwtExpiry: -1 * time.Hour}
	a.GenerateSecret()

	tokenStr, _ := a.IssueJWT("device-1")
	_, err := a.VerifyJWT(tokenStr)
	if err == nil {
		t.Error("expired JWT should fail")
	}
}

func TestJWTWrongSecret(t *testing.T) {
	dir := t.TempDir()
	a := &Auth{secretPath: dir + "/secret.key"}
	a.GenerateSecret()
	tokenStr, _ := a.IssueJWT("device-1")

	// Regenerate secret (simulates revoke)
	a.GenerateSecret()
	_, err := a.VerifyJWT(tokenStr)
	if err == nil {
		t.Error("JWT signed with old secret should fail")
	}
}
```

- [ ] **Step 3: Run tests — expect FAIL**

Run: `cd /Users/datducnguyenhuu/Desktop/claude-remote && go test -run TestGenerate -v`
Expected: FAIL — `Auth` struct not defined

- [ ] **Step 4: Implement auth.go**

```go
// auth.go
package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/skip2/go-qrcode"
)

type Auth struct {
	secret       []byte
	secretPath   string
	pendingToken string
	jwtExpiry    time.Duration
	mu           sync.Mutex
}

func NewAuth(secretPath string) *Auth {
	return &Auth{
		secretPath: secretPath,
		jwtExpiry:  90 * 24 * time.Hour,
	}
}

func (a *Auth) GenerateSecret() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return fmt.Errorf("generate secret: %w", err)
	}
	a.secret = b
	return os.WriteFile(a.secretPath, b, 0600)
}

func (a *Auth) LoadSecret() error {
	data, err := os.ReadFile(a.secretPath)
	if err != nil {
		return fmt.Errorf("load secret: %w", err)
	}
	a.secret = data
	return nil
}

func (a *Auth) GenerateToken() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	b := make([]byte, 32)
	rand.Read(b)
	a.pendingToken = hex.EncodeToString(b)
	return a.pendingToken
}

func (a *Auth) ValidateToken(token string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.pendingToken == "" || token != a.pendingToken {
		return false
	}
	a.pendingToken = "" // single-use
	return true
}

func (a *Auth) IssueJWT(deviceID string) (string, error) {
	claims := jwt.MapClaims{
		"device_id": deviceID,
		"iat":       time.Now().Unix(),
		"exp":       time.Now().Add(a.jwtExpiry).Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(a.secret)
}

func (a *Auth) VerifyJWT(tokenStr string) (string, error) {
	token, err := jwt.Parse(tokenStr, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", t.Header["alg"])
		}
		return a.secret, nil
	})
	if err != nil {
		return "", fmt.Errorf("verify jwt: %w", err)
	}
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok || !token.Valid {
		return "", fmt.Errorf("invalid token")
	}
	deviceID, _ := claims["device_id"].(string)
	return deviceID, nil
}

func (a *Auth) PrintQR(url string) {
	q, err := qrcode.New(url, qrcode.Medium)
	if err != nil {
		fmt.Fprintf(os.Stderr, "QR error: %v\n", err)
		return
	}
	fmt.Println(q.ToSmallString(false))
	fmt.Printf("\nScan this QR code or open:\n%s\n", url)
}

func (a *Auth) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie("claude-remote-auth")
		if err != nil {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		if _, err := a.VerifyJWT(cookie.Value); err != nil {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
```

- [ ] **Step 5: Run all auth tests — expect PASS**

Run: `cd /Users/datducnguyenhuu/Desktop/claude-remote && go test -run "TestGenerate|TestLoad|TestToken|TestJWT" -v`
Expected: ALL PASS

- [ ] **Step 6: Commit**

```bash
cd /Users/datducnguyenhuu/Desktop/claude-remote
git add auth.go auth_test.go go.mod go.sum
git commit -m "feat: auth — JWT sign/verify, QR code, one-time token"
```

---

## Task 3: File Browser

**Files:**
- Create: `files.go`
- Create: `files_test.go`

- [ ] **Step 1: Write file browser tests**

```go
// files_test.go
package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestValidatePath(t *testing.T) {
	fb := &FileBrowser{allowedDirs: []string{"/Users/test"}}
	tests := []struct {
		name    string
		path    string
		wantErr bool
	}{
		{"valid", "/Users/test/project", false},
		{"valid nested", "/Users/test/a/b/c", false},
		{"outside allowlist", "/etc/passwd", true},
		{"path traversal", "/Users/test/../etc/passwd", true},
		{"dotfile ssh", "/Users/test/.ssh/id_rsa", true},
		{"dotfile env", "/Users/test/.env", true},
		{"dotfile env.local", "/Users/test/.env.local", true},
		{"claude-remote dir", "/Users/test/.claude-remote/secret.key", true},
		{"gnupg", "/Users/test/.gnupg/key", true},
		{"aws", "/Users/test/.aws/credentials", true},
		{"empty path", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := fb.ValidatePath(tt.path)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidatePath(%q) error = %v, wantErr %v", tt.path, err, tt.wantErr)
			}
		})
	}
}

func TestListDir(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hi"), 0644)
	os.Mkdir(filepath.Join(dir, "subdir"), 0755)

	fb := &FileBrowser{allowedDirs: []string{dir}}
	entries, err := fb.ListDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Errorf("want 2 entries, got %d", len(entries))
	}
	foundFile, foundDir := false, false
	for _, e := range entries {
		if e.Name == "hello.txt" && e.Type == "file" {
			foundFile = true
		}
		if e.Name == "subdir" && e.Type == "dir" {
			foundDir = true
		}
	}
	if !foundFile || !foundDir {
		t.Error("missing expected entries")
	}
}

func TestListDirHidesDotfiles(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".env"), []byte("SECRET=x"), 0644)
	os.WriteFile(filepath.Join(dir, "readme.md"), []byte("hi"), 0644)
	os.Mkdir(filepath.Join(dir, ".ssh"), 0700)

	fb := &FileBrowser{allowedDirs: []string{dir}}
	entries, err := fb.ListDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name == ".env" || e.Name == ".ssh" {
			t.Errorf("sensitive dotfile %q should be hidden", e.Name)
		}
	}
	if len(entries) != 1 {
		t.Errorf("want 1 entry (readme.md), got %d", len(entries))
	}
}

func TestHandleFilesAPI(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "test.txt"), []byte("content"), 0644)

	fb := &FileBrowser{allowedDirs: []string{dir}}
	req := httptest.NewRequest("GET", "/api/files?path="+dir, nil)
	w := httptest.NewRecorder()
	fb.HandleList(w, req)

	if w.Code != 200 {
		t.Errorf("want 200, got %d", w.Code)
	}
	var resp DirResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.Path != dir {
		t.Errorf("want path %s, got %s", dir, resp.Path)
	}
}

func TestHandleReadFile(t *testing.T) {
	dir := t.TempDir()
	fpath := filepath.Join(dir, "test.txt")
	os.WriteFile(fpath, []byte("hello world"), 0644)

	fb := &FileBrowser{allowedDirs: []string{dir}}
	req := httptest.NewRequest("GET", "/api/files/read?path="+fpath, nil)
	w := httptest.NewRecorder()
	fb.HandleRead(w, req)

	if w.Code != 200 {
		t.Errorf("want 200, got %d", w.Code)
	}
	var resp FileContentResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.Content != "hello world" {
		t.Errorf("want 'hello world', got %q", resp.Content)
	}
}
```

- [ ] **Step 2: Run tests — expect FAIL**

Run: `cd /Users/datducnguyenhuu/Desktop/claude-remote && go test -run TestValidatePath -v`
Expected: FAIL — `FileBrowser` not defined

- [ ] **Step 3: Implement files.go**

```go
// files.go
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var blockedPaths = []string{
	".ssh", ".env", ".claude-remote", ".gnupg", ".aws",
	".config/gcloud", ".docker", ".kube",
}

type FileBrowser struct {
	allowedDirs []string
}

type FileEntry struct {
	Name     string    `json:"name"`
	Type     string    `json:"type"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
}

type DirResponse struct {
	Path    string      `json:"path"`
	Parent  string      `json:"parent"`
	Entries []FileEntry `json:"entries"`
}

type FileContentResponse struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Size    int64  `json:"size"`
}

func NewFileBrowser(allowedDirs []string) *FileBrowser {
	return &FileBrowser{allowedDirs: allowedDirs}
}

func (fb *FileBrowser) ValidatePath(path string) error {
	if path == "" {
		return fmt.Errorf("empty path")
	}
	if strings.Contains(path, "..") {
		return fmt.Errorf("path traversal not allowed")
	}
	cleaned := filepath.Clean(path)

	// Resolve symlinks
	resolved, err := filepath.EvalSymlinks(cleaned)
	if err != nil {
		resolved = cleaned // path may not exist yet for validation
	}

	// Check blocked paths
	for _, blocked := range blockedPaths {
		if containsComponent(resolved, blocked) {
			return fmt.Errorf("access to %s is blocked", blocked)
		}
	}

	// Check allowlist
	for _, dir := range fb.allowedDirs {
		if strings.HasPrefix(resolved, dir) {
			return nil
		}
	}
	return fmt.Errorf("path outside allowed directories")
}

func containsComponent(path, component string) bool {
	parts := strings.Split(path, string(filepath.Separator))
	for _, part := range parts {
		if part == component {
			return true
		}
		// Match .env* pattern
		if component == ".env" && strings.HasPrefix(part, ".env") {
			return true
		}
	}
	return false
}

func isBlockedEntry(name string) bool {
	for _, blocked := range blockedPaths {
		if name == blocked {
			return true
		}
		if blocked == ".env" && strings.HasPrefix(name, ".env") {
			return true
		}
	}
	return false
}

func (fb *FileBrowser) ListDir(path string) ([]FileEntry, error) {
	if err := fb.ValidatePath(path); err != nil {
		return nil, err
	}
	dirEntries, err := os.ReadDir(path)
	if err != nil {
		return nil, fmt.Errorf("read dir: %w", err)
	}
	var entries []FileEntry
	for _, de := range dirEntries {
		if isBlockedEntry(de.Name()) {
			continue
		}
		info, err := de.Info()
		if err != nil {
			continue
		}
		typ := "file"
		if de.IsDir() {
			typ = "dir"
		}
		entries = append(entries, FileEntry{
			Name:     de.Name(),
			Type:     typ,
			Size:     info.Size(),
			Modified: info.ModTime(),
		})
	}
	return entries, nil
}

func (fb *FileBrowser) HandleList(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		path = fb.allowedDirs[0]
	}
	entries, err := fb.ListDir(path)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusForbidden)
		return
	}
	resp := DirResponse{
		Path:    path,
		Parent:  filepath.Dir(path),
		Entries: entries,
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (fb *FileBrowser) HandleRead(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if err := fb.ValidatePath(path); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusForbidden)
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		http.Error(w, `{"error":"file not found"}`, http.StatusNotFound)
		return
	}
	if info.IsDir() {
		http.Error(w, `{"error":"cannot read directory"}`, http.StatusBadRequest)
		return
	}
	// Limit read to 1MB
	if info.Size() > 1<<20 {
		http.Error(w, `{"error":"file too large (max 1MB)"}`, http.StatusBadRequest)
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		http.Error(w, `{"error":"read failed"}`, http.StatusInternalServerError)
		return
	}
	resp := FileContentResponse{
		Path:    path,
		Content: string(data),
		Size:    info.Size(),
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
```

- [ ] **Step 4: Run all file browser tests — expect PASS**

Run: `cd /Users/datducnguyenhuu/Desktop/claude-remote && go test -run "TestValidate|TestList|TestHandle" -v`
Expected: ALL PASS

- [ ] **Step 5: Commit**

```bash
cd /Users/datducnguyenhuu/Desktop/claude-remote
git add files.go files_test.go
git commit -m "feat: file browser with allowlist and path security"
```

---

## Task 4: Terminal — Pty + WebSocket

**Files:**
- Create: `terminal.go`
- Create: `terminal_test.go`

**Dependencies:** `gorilla/websocket`, `creack/pty` (installed in Task 0)

- [ ] **Step 1: Write terminal tests**

```go
// terminal_test.go
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
	if err := tm.Start(); err != nil {
		t.Fatal(err)
	}
	defer tm.Stop()

	// Wait for process to complete and read output
	time.Sleep(500 * time.Millisecond)
	output := string(tm.buffer.Bytes())
	if !strings.Contains(output, "hello from pty") {
		t.Errorf("want output containing 'hello from pty', got %q", output)
	}
}

func TestTerminalWebSocket(t *testing.T) {
	tm := NewTerminalManager("cat", nil) // cat echoes input
	handler := tm.WebSocketHandler()

	server := httptest.NewServer(http.HandlerFunc(handler))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws/term"
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()

	// Send data
	ws.WriteMessage(websocket.BinaryMessage, []byte("test input\n"))
	time.Sleep(300 * time.Millisecond)

	// Read response (cat echoes back)
	ws.SetReadDeadline(time.Now().Add(2 * time.Second))
	_, msg, err := ws.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(msg), "test input") {
		t.Errorf("want echo of 'test input', got %q", string(msg))
	}
}
```

- [ ] **Step 3: Run tests — expect FAIL**

Run: `cd /Users/datducnguyenhuu/Desktop/claude-remote && go test -run TestRingBuffer -v`
Expected: FAIL — `RingBuffer` not defined

- [ ] **Step 4: Implement terminal.go**

```go
// terminal.go
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

// RingBuffer holds the last N bytes for reconnection replay.
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

type TerminalManager struct {
	cmd      string
	args     []string
	ptmx     *os.File
	process  *exec.Cmd
	buffer   *RingBuffer
	clients  map[*websocket.Conn]bool
	mu       sync.Mutex
	running  bool
}

func NewTerminalManager(cmd string, args []string) *TerminalManager {
	return &TerminalManager{
		cmd:     cmd,
		args:    args,
		buffer:  NewRingBuffer(64 * 1024), // 64KB replay buffer
		clients: make(map[*websocket.Conn]bool),
	}
}

func (tm *TerminalManager) Start() error {
	tm.mu.Lock()
	defer tm.mu.Unlock()
	if tm.running {
		return nil
	}

	c := exec.Command(tm.cmd, tm.args...)
	c.Env = os.Environ()
	ptmx, err := pty.Start(c)
	if err != nil {
		return fmt.Errorf("start pty: %w", err)
	}
	tm.ptmx = ptmx
	tm.process = c
	tm.running = true

	// Read pty output → buffer + broadcast to clients
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

	// Wait for process exit
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

		// Start session if not running
		if !tm.running {
			if err := tm.Start(); err != nil {
				conn.WriteMessage(websocket.TextMessage, []byte("Error: "+err.Error()))
				return
			}
		}

		// Register client
		tm.mu.Lock()
		tm.clients[conn] = true
		tm.mu.Unlock()

		// Replay buffer
		if buf := tm.buffer.Bytes(); len(buf) > 0 {
			conn.WriteMessage(websocket.BinaryMessage, buf)
		}

		// Read from WebSocket → write to pty
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
				// Handle resize: {"type":"resize","rows":24,"cols":80}
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
	// Simple JSON parse for resize
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
```

- [ ] **Step 5: Run all terminal tests — expect PASS**

Run: `cd /Users/datducnguyenhuu/Desktop/claude-remote && go test -run "TestRing|TestTerminal" -v -timeout 10s`
Expected: ALL PASS

- [ ] **Step 6: Commit**

```bash
cd /Users/datducnguyenhuu/Desktop/claude-remote
git add terminal.go terminal_test.go go.mod go.sum
git commit -m "feat: terminal — pty + WebSocket + ring buffer"
```

---

## Task 5: HTTP Server + Routes

**Files:**
- Create: `server.go`

- [ ] **Step 1: Implement server.go**

```go
// server.go
package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

type Server struct {
	config   *Config
	auth     *Auth
	terminal *TerminalManager
	files    *FileBrowser
	mux      *http.ServeMux
}

func NewServer(cfg *Config, auth *Auth) *Server {
	return &Server{
		config:   cfg,
		auth:     auth,
		terminal: NewTerminalManager(cfg.ClaudePath, nil),
		files:    NewFileBrowser(cfg.AllowedDirs),
		mux:      http.NewServeMux(),
	}
}

func (s *Server) registerRoutes() {
	// Public routes (no auth)
	s.mux.HandleFunc("/auth/scan", s.handleAuthScan)
	s.mux.HandleFunc("/health", s.handleHealth)

	// Protected routes (JWT required)
	protected := http.NewServeMux()
	protected.HandleFunc("/ws/term", s.terminal.WebSocketHandler())
	protected.HandleFunc("/api/files", s.files.HandleList)
	protected.HandleFunc("/api/files/read", s.files.HandleRead)

	// Static files
	staticDir := filepath.Join(execDir(), "static")
	if _, err := os.Stat(staticDir); err == nil {
		protected.Handle("/", http.FileServer(http.Dir(staticDir)))
	}

	s.mux.Handle("/", s.auth.Middleware(protected))
}

func (s *Server) handleAuthScan(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if token == "" || !s.auth.ValidateToken(token) {
		http.Error(w, `{"error":"invalid or expired token"}`, http.StatusUnauthorized)
		return
	}
	// Issue JWT
	deviceID := fmt.Sprintf("device-%d", time.Now().UnixNano())
	jwt, err := s.auth.IssueJWT(deviceID)
	if err != nil {
		http.Error(w, `{"error":"failed to issue token"}`, http.StatusInternalServerError)
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     "claude-remote-auth",
		Value:    jwt,
		Path:     "/",
		MaxAge:   90 * 24 * 3600,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
	})
	http.Redirect(w, r, "/", http.StatusFound)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"status":"ok","version":"0.1.0"}`)
}

func (s *Server) loadTLSConfig() (*tls.Config, error) {
	// Look for Tailscale certs
	certDirs := []string{
		"/var/run/tailscale",
		filepath.Join(os.Getenv("HOME"), ".local/share/tailscale/certs"),
	}
	for _, dir := range certDirs {
		certFile := filepath.Join(dir, "*.crt")
		matches, _ := filepath.Glob(certFile)
		if len(matches) > 0 {
			baseName := matches[0][:len(matches[0])-4] // strip .crt
			cert, err := tls.LoadX509KeyPair(baseName+".crt", baseName+".key")
			if err != nil {
				continue
			}
			return &tls.Config{Certificates: []tls.Certificate{cert}}, nil
		}
	}
	return nil, fmt.Errorf("no TLS certificates found — run: sudo tailscale cert <hostname>.ts.net")
}

func (s *Server) Run() error {
	s.registerRoutes()
	addr := fmt.Sprintf("0.0.0.0:%d", s.config.Port)

	srv := &http.Server{
		Addr:    addr,
		Handler: s.mux,
	}

	// Try TLS, fall back to HTTP
	tlsCfg, tlsErr := s.loadTLSConfig()
	if tlsErr != nil {
		log.Printf("WARNING: No TLS certs found, running HTTP only: %v", tlsErr)
		log.Printf("For HTTPS: sudo tailscale cert <hostname>.ts.net")
	} else {
		srv.TLSConfig = tlsCfg
	}

	// Graceful shutdown
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		log.Printf("Claude Remote listening on %s", addr)
		var err error
		if tlsCfg != nil {
			err = srv.ListenAndServeTLS("", "")
		} else {
			err = srv.ListenAndServe()
		}
		if err != nil && err != http.ErrServerClosed {
			log.Fatalf("server error: %v", err)
		}
	}()

	<-stop
	log.Println("Shutting down...")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s.terminal.Stop()
	return srv.Shutdown(ctx)
}

func execDir() string {
	exe, err := os.Executable()
	if err != nil {
		return "."
	}
	return filepath.Dir(exe)
}
```

- [ ] **Step 2: Create minimal main.go for build check**

```go
// main.go (placeholder — replaced in Task 6)
package main

func main() {}
```

- [ ] **Step 3: Verify build**

Run: `cd /Users/datducnguyenhuu/Desktop/claude-remote && go build -o /dev/null .`
Expected: PASS

- [ ] **Step 4: Write server tests**

```go
// server_test.go
package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthEndpoint(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{Port: 0, AllowedDirs: []string{dir}, ClaudePath: "echo", DataDir: dir}
	auth := NewAuth(cfg.SecretPath())
	auth.GenerateSecret()
	s := NewServer(cfg, auth)
	s.registerRoutes()

	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Errorf("want 200, got %d", w.Code)
	}
}

func TestAuthScanValidToken(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{Port: 0, AllowedDirs: []string{dir}, ClaudePath: "echo", DataDir: dir}
	auth := NewAuth(cfg.SecretPath())
	auth.GenerateSecret()
	token := auth.GenerateToken()
	s := NewServer(cfg, auth)
	s.registerRoutes()

	req := httptest.NewRequest("GET", "/auth/scan?token="+token, nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)
	if w.Code != 302 {
		t.Errorf("want 302, got %d", w.Code)
	}
	found := false
	for _, c := range w.Result().Cookies() {
		if c.Name == "claude-remote-auth" {
			found = true
		}
	}
	if !found {
		t.Error("expected auth cookie in response")
	}
}

func TestAuthScanInvalidToken(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{Port: 0, AllowedDirs: []string{dir}, ClaudePath: "echo", DataDir: dir}
	auth := NewAuth(cfg.SecretPath())
	auth.GenerateSecret()
	s := NewServer(cfg, auth)
	s.registerRoutes()

	req := httptest.NewRequest("GET", "/auth/scan?token=bogus", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)
	if w.Code != 401 {
		t.Errorf("want 401, got %d", w.Code)
	}
}

func TestProtectedRouteWithoutAuth(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{Port: 0, AllowedDirs: []string{dir}, ClaudePath: "echo", DataDir: dir}
	auth := NewAuth(cfg.SecretPath())
	auth.GenerateSecret()
	s := NewServer(cfg, auth)
	s.registerRoutes()

	req := httptest.NewRequest("GET", "/api/files?path="+dir, nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)
	if w.Code != 401 {
		t.Errorf("want 401, got %d", w.Code)
	}
}
```

- [ ] **Step 5: Run server tests — expect PASS**

Run: `cd /Users/datducnguyenhuu/Desktop/claude-remote && go test -run "TestHealth|TestAuthScan|TestProtected" -v`
Expected: ALL PASS

- [ ] **Step 6: Commit**

```bash
cd /Users/datducnguyenhuu/Desktop/claude-remote
git add server.go server_test.go main.go
git commit -m "feat: HTTP server with routes, TLS, tests"
```

---

## Task 6: CLI Entry Point

**Files:**
- Modify: `main.go`

- [ ] **Step 1: Implement full main.go**

```go
// main.go
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"
)

const version = "0.1.0"

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}
	switch os.Args[1] {
	case "setup":
		cmdSetup()
	case "serve":
		cmdServe()
	case "revoke":
		cmdRevoke()
	case "install":
		cmdInstall()
	case "uninstall":
		cmdUninstall()
	case "status":
		cmdStatus()
	case "version":
		fmt.Printf("claude-remote %s\n", version)
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println(`Usage: claude-remote <command>

Commands:
  setup      Generate secret + QR code for first-time auth
  serve      Start server (foreground)
  revoke     Regenerate secret, invalidate all sessions
  install    Install launchd plist + load
  uninstall  Unload + remove launchd plist
  status     Show running state
  version    Print version`)
}

func getConfig() *Config {
	home, _ := os.UserHomeDir()
	dataDir := filepath.Join(home, ".claude-remote")
	cfg, err := LoadConfig(dataDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error loading config: %v\n", err)
		os.Exit(1)
	}
	return cfg
}

func cmdSetup() {
	cfg := getConfig()
	cfg.Save()

	auth := NewAuth(cfg.SecretPath())
	if err := auth.GenerateSecret(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	token := auth.GenerateToken()

	// Detect Tailscale hostname
	hostname := detectTailscaleHost()
	url := fmt.Sprintf("https://%s:%d/auth/scan?token=%s", hostname, cfg.Port, token)

	fmt.Println("Claude Remote Setup")
	fmt.Println("===================")
	auth.PrintQR(url)
	fmt.Println("\nScan this QR code with your phone to connect.")
	fmt.Println("The server must be running: claude-remote serve")

	// Save the pending token for the server to pick up
	os.WriteFile(filepath.Join(cfg.DataDir, ".pending-token"), []byte(token), 0600)
}

func cmdServe() {
	cfg := getConfig()
	auth := NewAuth(cfg.SecretPath())
	if err := auth.LoadSecret(); err != nil {
		fmt.Fprintf(os.Stderr, "No secret found. Run 'claude-remote setup' first.\n")
		os.Exit(1)
	}

	// Load pending token if exists
	tokenPath := filepath.Join(cfg.DataDir, ".pending-token")
	if data, err := os.ReadFile(tokenPath); err == nil {
		auth.mu.Lock()
		auth.pendingToken = string(data)
		auth.mu.Unlock()
		os.Remove(tokenPath)
	}

	server := NewServer(cfg, auth)
	if err := server.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Server error: %v\n", err)
		os.Exit(1)
	}
}

func cmdRevoke() {
	cfg := getConfig()
	auth := NewAuth(cfg.SecretPath())
	if err := auth.GenerateSecret(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	// Clear sessions
	os.Remove(cfg.SessionsPath())
	fmt.Println("All sessions revoked. New secret generated.")
	fmt.Println("Run 'claude-remote setup' to generate a new QR code.")
}

func cmdInstall() {
	home, _ := os.UserHomeDir()
	plistDir := filepath.Join(home, "Library", "LaunchAgents")
	os.MkdirAll(plistDir, 0755)
	plistPath := filepath.Join(plistDir, "com.claude-remote.plist")

	// Find binary path
	binPath, err := os.Executable()
	if err != nil {
		binPath = "/usr/local/bin/claude-remote"
	}

	tmpl := template.Must(template.New("plist").Parse(plistTemplate))
	f, err := os.Create(plistPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	defer f.Close()

	tmpl.Execute(f, map[string]string{
		"BinPath": binPath,
		"Home":    home,
	})

	exec.Command("launchctl", "load", plistPath).Run()
	fmt.Printf("Installed and loaded: %s\n", plistPath)
}

func cmdUninstall() {
	home, _ := os.UserHomeDir()
	plistPath := filepath.Join(home, "Library", "LaunchAgents", "com.claude-remote.plist")
	exec.Command("launchctl", "unload", plistPath).Run()
	os.Remove(plistPath)
	fmt.Println("Uninstalled.")
}

func cmdStatus() {
	cfg := getConfig()
	fmt.Printf("Claude Remote v%s\n", version)
	fmt.Printf("Config dir: %s\n", cfg.DataDir)
	fmt.Printf("Port: %d\n", cfg.Port)

	if _, err := os.Stat(cfg.SecretPath()); err == nil {
		fmt.Println("Secret: configured")
	} else {
		fmt.Println("Secret: not configured (run setup)")
	}

	hostname := detectTailscaleHost()
	fmt.Printf("Tailscale: %s\n", hostname)
}

func detectTailscaleHost() string {
	out, err := exec.Command("tailscale", "status", "--json").Output()
	if err != nil {
		return "localhost"
	}
	var status struct {
		Self struct {
			DNSName string `json:"DNSName"`
		} `json:"Self"`
		MagicDNSSuffix string `json:"MagicDNSSuffix"`
	}
	if err := json.Unmarshal(out, &status); err == nil && status.Self.DNSName != "" {
		// DNSName ends with "." — strip it
		dns := status.Self.DNSName
		if len(dns) > 0 && dns[len(dns)-1] == '.' {
			dns = dns[:len(dns)-1]
		}
		return dns
	}
	// Fallback to IP
	ipOut, err := exec.Command("tailscale", "ip", "-4").Output()
	if err != nil {
		return "localhost"
	}
	ip := strings.TrimSpace(string(ipOut))
	return ip
}

const plistTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>com.claude-remote</string>
  <key>ProgramArguments</key>
  <array>
    <string>{{.BinPath}}</string>
    <string>serve</string>
  </array>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
  <key>StandardOutPath</key>
  <string>{{.Home}}/.claude-remote/server.log</string>
  <key>StandardErrorPath</key>
  <string>{{.Home}}/.claude-remote/server.log</string>
  <key>EnvironmentVariables</key>
  <dict>
    <key>PATH</key>
    <string>/usr/local/bin:/opt/homebrew/bin:/usr/bin:/bin</string>
  </dict>
</dict>
</plist>
`
```

- [ ] **Step 2: Verify build**

Run: `cd /Users/datducnguyenhuu/Desktop/claude-remote && go build -o claude-remote . && ./claude-remote version`
Expected: `claude-remote 0.1.0`

- [ ] **Step 3: Commit**

```bash
cd /Users/datducnguyenhuu/Desktop/claude-remote
git add main.go
git commit -m "feat: CLI entry point — setup, serve, revoke, install, status"
```

---

## Task 7: Web UI

**Files:**
- Create: `static/index.html`
- Create: `static/app.js`
- Create: `static/style.css`

- [ ] **Step 1: Create index.html**

```html
<!-- static/index.html -->
<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0, maximum-scale=1.0, user-scalable=no">
  <title>Claude Remote</title>
  <link rel="stylesheet" href="/style.css">
  <link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/@xterm/xterm@5.5.0/css/xterm.min.css">
</head>
<body>
  <header id="header">
    <span id="status-dot" class="dot disconnected"></span>
    <span>Claude Remote</span>
    <span id="status-text">Disconnected</span>
  </header>

  <main>
    <div id="tab-terminal" class="tab-content active">
      <div id="terminal-container"></div>
    </div>
    <div id="tab-files" class="tab-content">
      <div id="breadcrumb"></div>
      <div id="file-list"></div>
      <div id="file-modal" class="modal hidden">
        <div class="modal-header">
          <span id="modal-filename"></span>
          <button id="modal-close">&times;</button>
          <button id="open-in-claude">Open in Claude</button>
        </div>
        <pre id="file-content"></pre>
      </div>
    </div>
  </main>

  <nav id="tabs">
    <button class="tab active" data-tab="tab-terminal">Terminal</button>
    <button class="tab" data-tab="tab-files">Files</button>
  </nav>

  <script src="https://cdn.jsdelivr.net/npm/@xterm/xterm@5.5.0/lib/xterm.min.js"></script>
  <script src="https://cdn.jsdelivr.net/npm/@xterm/addon-fit@0.10.0/lib/addon-fit.min.js"></script>
  <script src="https://cdn.jsdelivr.net/npm/@xterm/addon-web-links@0.11.0/lib/addon-web-links.min.js"></script>
  <script src="/app.js"></script>
</body>
</html>
```

- [ ] **Step 2: Create style.css**

```css
/* static/style.css */
* { margin: 0; padding: 0; box-sizing: border-box; }
html, body { height: 100%; font-family: -apple-system, system-ui, sans-serif; background: #1a1a2e; color: #eee; }

body { display: flex; flex-direction: column; }
header { display: flex; align-items: center; gap: 8px; padding: 8px 12px; background: #16213e; font-size: 14px; }
main { flex: 1; overflow: hidden; position: relative; }
nav#tabs { display: flex; background: #16213e; border-top: 1px solid #333; }

.dot { width: 8px; height: 8px; border-radius: 50%; }
.dot.connected { background: #4ade80; }
.dot.disconnected { background: #f87171; }

.tab { flex: 1; padding: 12px; border: none; background: none; color: #888; font-size: 14px; cursor: pointer; }
.tab.active { color: #fff; border-top: 2px solid #60a5fa; }

.tab-content { display: none; height: 100%; overflow: auto; }
.tab-content.active { display: block; }

#terminal-container { height: 100%; padding: 4px; }

/* File browser */
#breadcrumb { padding: 8px 12px; background: #16213e; font-size: 13px; overflow-x: auto; white-space: nowrap; }
#breadcrumb span { cursor: pointer; color: #60a5fa; }
#breadcrumb span:last-child { color: #fff; }
#breadcrumb span::after { content: " / "; color: #666; }
#breadcrumb span:last-child::after { content: ""; }

#file-list { padding: 4px 0; }
.file-entry { display: flex; align-items: center; padding: 10px 12px; border-bottom: 1px solid #222; cursor: pointer; }
.file-entry:active { background: #16213e; }
.file-icon { width: 24px; margin-right: 10px; font-size: 18px; }
.file-info { flex: 1; }
.file-name { font-size: 14px; }
.file-meta { font-size: 11px; color: #888; }

/* Modal */
.modal { position: fixed; top: 0; left: 0; right: 0; bottom: 48px; background: #1a1a2e; z-index: 10; overflow: auto; }
.modal.hidden { display: none; }
.modal-header { display: flex; align-items: center; gap: 8px; padding: 8px 12px; background: #16213e; position: sticky; top: 0; }
.modal-header span { flex: 1; font-size: 13px; }
.modal-header button { padding: 6px 12px; border: 1px solid #444; background: none; color: #eee; border-radius: 4px; cursor: pointer; }
#open-in-claude { background: #60a5fa; border-color: #60a5fa; color: #000; }
#file-content { padding: 12px; font-size: 13px; line-height: 1.5; white-space: pre-wrap; word-break: break-all; }
```

- [ ] **Step 3: Create app.js**

```javascript
// static/app.js
(function() {
  'use strict';

  // --- State ---
  let ws = null;
  let term = null;
  let fitAddon = null;
  let currentPath = '';
  let reconnectTimer = null;

  // --- Tab Switching ---
  document.querySelectorAll('.tab').forEach(btn => {
    btn.addEventListener('click', () => {
      document.querySelectorAll('.tab').forEach(b => b.classList.remove('active'));
      document.querySelectorAll('.tab-content').forEach(c => c.classList.remove('active'));
      btn.classList.add('active');
      document.getElementById(btn.dataset.tab).classList.add('active');
      if (btn.dataset.tab === 'tab-terminal' && fitAddon) fitAddon.fit();
    });
  });

  // --- Terminal ---
  function initTerminal() {
    term = new Terminal({
      fontSize: 14,
      fontFamily: 'Menlo, Monaco, monospace',
      theme: { background: '#1a1a2e' },
      cursorBlink: true,
      allowProposedApi: true,
    });
    fitAddon = new FitAddon.FitAddon();
    term.loadAddon(fitAddon);
    term.loadAddon(new WebLinksAddon.WebLinksAddon());
    term.open(document.getElementById('terminal-container'));
    fitAddon.fit();

    term.onData(data => {
      if (ws && ws.readyState === WebSocket.OPEN) {
        ws.send(new TextEncoder().encode(data));
      }
    });

    window.addEventListener('resize', () => fitAddon.fit());
    term.onResize(({ rows, cols }) => {
      if (ws && ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({ type: 'resize', rows, cols }));
      }
    });
  }

  function connectWS() {
    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
    ws = new WebSocket(`${proto}//${location.host}/ws/term`);
    ws.binaryType = 'arraybuffer';

    ws.onopen = () => {
      setStatus(true);
      if (fitAddon) {
        fitAddon.fit();
        const dims = term._core._renderService.dimensions;
        ws.send(JSON.stringify({ type: 'resize', rows: term.rows, cols: term.cols }));
      }
    };

    ws.onmessage = (evt) => {
      const data = typeof evt.data === 'string' ? evt.data : new TextDecoder().decode(evt.data);
      term.write(data);
    };

    ws.onclose = () => {
      setStatus(false);
      reconnectTimer = setTimeout(connectWS, 3000);
    };

    ws.onerror = () => ws.close();
  }

  function setStatus(connected) {
    document.getElementById('status-dot').className = 'dot ' + (connected ? 'connected' : 'disconnected');
    document.getElementById('status-text').textContent = connected ? 'Connected' : 'Reconnecting...';
  }

  // --- File Browser ---
  async function loadDir(path) {
    try {
      const resp = await fetch(`/api/files?path=${encodeURIComponent(path)}`);
      if (resp.status === 401) { location.reload(); return; }
      const data = await resp.json();
      if (data.error) { alert(data.error); return; }
      currentPath = data.path;
      renderBreadcrumb(data.path);
      renderFiles(data.entries, data.path);
    } catch (e) {
      console.error('loadDir error:', e);
    }
  }

  function renderBreadcrumb(path) {
    const el = document.getElementById('breadcrumb');
    const parts = path.split('/').filter(Boolean);
    el.innerHTML = '';
    let accumulated = '/';
    parts.forEach((part, i) => {
      const span = document.createElement('span');
      span.textContent = part;
      accumulated += part + '/';
      const p = accumulated;
      span.addEventListener('click', () => loadDir(p));
      el.appendChild(span);
    });
  }

  function renderFiles(entries, parentPath) {
    const el = document.getElementById('file-list');
    el.innerHTML = '';
    entries.sort((a, b) => {
      if (a.type !== b.type) return a.type === 'dir' ? -1 : 1;
      return a.name.localeCompare(b.name);
    });
    entries.forEach(entry => {
      const div = document.createElement('div');
      div.className = 'file-entry';
      const fullPath = parentPath.replace(/\/$/, '') + '/' + entry.name;
      div.innerHTML = `
        <span class="file-icon">${entry.type === 'dir' ? '📁' : '📄'}</span>
        <div class="file-info">
          <div class="file-name">${entry.name}</div>
          <div class="file-meta">${entry.type === 'file' ? formatSize(entry.size) : ''}</div>
        </div>`;
      div.addEventListener('click', () => {
        if (entry.type === 'dir') loadDir(fullPath);
        else openFile(fullPath, entry.name);
      });
      el.appendChild(div);
    });
  }

  async function openFile(path, name) {
    try {
      const resp = await fetch(`/api/files/read?path=${encodeURIComponent(path)}`);
      const data = await resp.json();
      if (data.error) { alert(data.error); return; }
      document.getElementById('modal-filename').textContent = name;
      document.getElementById('file-content').textContent = data.content;
      document.getElementById('file-modal').classList.remove('hidden');

      document.getElementById('open-in-claude').onclick = () => {
        // Switch to terminal and type the file path
        document.querySelector('[data-tab="tab-terminal"]').click();
        if (ws && ws.readyState === WebSocket.OPEN) {
          ws.send(new TextEncoder().encode(path + '\n'));
        }
        document.getElementById('file-modal').classList.add('hidden');
      };
    } catch (e) {
      console.error('openFile error:', e);
    }
  }

  document.getElementById('modal-close').addEventListener('click', () => {
    document.getElementById('file-modal').classList.add('hidden');
  });

  function formatSize(bytes) {
    if (bytes < 1024) return bytes + ' B';
    if (bytes < 1048576) return (bytes / 1024).toFixed(1) + ' KB';
    return (bytes / 1048576).toFixed(1) + ' MB';
  }

  // --- Init ---
  initTerminal();
  connectWS();
  // Load home directory for file browser
  loadDir('');
})();
```

- [ ] **Step 4: Verify build with static files**

Run: `cd /Users/datducnguyenhuu/Desktop/claude-remote && go build -o claude-remote . && ls static/`
Expected: Build succeeds, static/ contains index.html, app.js, style.css

- [ ] **Step 5: Commit**

```bash
cd /Users/datducnguyenhuu/Desktop/claude-remote
git add static/
git commit -m "feat: web UI — terminal + file browser, mobile-first"
```

---

## Task 8: Launchd Plist

**Files:**
- Create: `launchd/com.claude-remote.plist`

- [ ] **Step 1: Create plist template (for reference, actual is generated by `install` command)**

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>com.claude-remote</string>
  <key>ProgramArguments</key>
  <array>
    <string>/usr/local/bin/claude-remote</string>
    <string>serve</string>
  </array>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
  <key>StandardOutPath</key>
  <string>/Users/datducnguyenhuu/.claude-remote/server.log</string>
  <key>StandardErrorPath</key>
  <string>/Users/datducnguyenhuu/.claude-remote/server.log</string>
  <key>EnvironmentVariables</key>
  <dict>
    <key>PATH</key>
    <string>/usr/local/bin:/opt/homebrew/bin:/usr/bin:/bin</string>
  </dict>
</dict>
</plist>
```

- [ ] **Step 2: Commit**

```bash
cd /Users/datducnguyenhuu/Desktop/claude-remote
git add launchd/
git commit -m "feat: launchd plist template for auto-start"
```

---

## Task 9: Integration Test — Full Flow

**Files:**
- Create: `integration_test.go`

- [ ] **Step 1: Write integration test**

```go
// integration_test.go
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
	resp, _ := http.Get(ts.URL + "/api/files?path=" + dir)
	if resp.StatusCode != 401 {
		t.Errorf("want 401 without auth, got %d", resp.StatusCode)
	}

	// 2. Scan QR token → get JWT cookie
	client := &http.Client{CheckRedirect: func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	resp, _ = client.Get(ts.URL + "/auth/scan?token=" + token)
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
	resp, _ = http.DefaultClient.Do(req)
	if resp.StatusCode != 200 {
		t.Errorf("want 200 with auth, got %d", resp.StatusCode)
	}

	// 4. Token is single-use
	resp, _ = client.Get(ts.URL + "/auth/scan?token=" + token)
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

	// Use unprotected handler directly for test
	ts := httptest.NewServer(http.HandlerFunc(server.terminal.WebSocketHandler()))
	defer ts.Close()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws/term"
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()

	// Write to terminal
	ws.WriteMessage(websocket.BinaryMessage, []byte("hello\n"))
	time.Sleep(300 * time.Millisecond)

	// Read echo back
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
	resp, _ := http.DefaultClient.Do(req)
	if resp.StatusCode != 200 {
		t.Fatalf("want 200, got %d", resp.StatusCode)
	}
	var dirResp DirResponse
	json.NewDecoder(resp.Body).Decode(&dirResp)
	if len(dirResp.Entries) != 2 {
		t.Errorf("want 2 entries, got %d", len(dirResp.Entries))
	}

	// Read file
	req, _ = http.NewRequest("GET", ts.URL+"/api/files/read?path="+dir+"/readme.md", nil)
	req.AddCookie(&http.Cookie{Name: "claude-remote-auth", Value: jwtStr})
	resp, _ = http.DefaultClient.Do(req)
	var fileResp FileContentResponse
	json.NewDecoder(resp.Body).Decode(&fileResp)
	if fileResp.Content != "# Hello" {
		t.Errorf("want '# Hello', got %q", fileResp.Content)
	}
}
```

- [ ] **Step 2: Run integration tests**

Run: `cd /Users/datducnguyenhuu/Desktop/claude-remote && go test -run "TestFull|TestWebSocket|TestFileBrowser" -v -timeout 30s`
Expected: ALL PASS

- [ ] **Step 3: Run all tests**

Run: `cd /Users/datducnguyenhuu/Desktop/claude-remote && go test ./... -v -count=1 -timeout 30s`
Expected: ALL PASS

- [ ] **Step 4: Run race detector**

Run: `cd /Users/datducnguyenhuu/Desktop/claude-remote && go test -race ./... -timeout 30s`
Expected: No race conditions

- [ ] **Step 5: Commit**

```bash
cd /Users/datducnguyenhuu/Desktop/claude-remote
git add integration_test.go
git commit -m "test: integration tests — full auth flow, WebSocket terminal, file browser"
```

---

## Deferred (v0.2)

- `sessions.json` tracking — record active device IDs on auth scan, display in `status` command
- TLS cert auto-discovery improvements — search more Tailscale cert paths
- `status` command — check if server process is actually running (PID file or port probe)
- Log rotation for `server.log`

---

## Task 10: Final Build + Manual Smoke Test

- [ ] **Step 1: Clean build**

Run: `cd /Users/datducnguyenhuu/Desktop/claude-remote && go build -o claude-remote .`
Expected: Binary created

- [ ] **Step 2: Test CLI commands**

```bash
cd /Users/datducnguyenhuu/Desktop/claude-remote
./claude-remote version
./claude-remote status
```

Expected: Version prints, status shows config info

- [ ] **Step 3: Final commit**

```bash
cd /Users/datducnguyenhuu/Desktop/claude-remote
git add -A
git commit -m "chore: final build verification"
```
