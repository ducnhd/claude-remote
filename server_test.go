package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
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
	if !strings.Contains(loc, "dir=") {
		t.Error("redirect should contain dir param")
	}
	if !strings.Contains(loc, "mode=choose") {
		t.Error("redirect should contain mode param")
	}
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
