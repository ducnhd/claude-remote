package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	neturl "net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

// A directory that merely shares a string prefix with an allowed root
// must be rejected by /api/claude/start.
func TestClaudeStartRejectsSiblingPrefixDir(t *testing.T) {
	root := t.TempDir()
	allowed := filepath.Join(root, "work")
	sibling := filepath.Join(root, "work-secret")
	for _, d := range []string{allowed, sibling} {
		if err := os.Mkdir(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cfg := &Config{Port: 8822, AllowedDirs: []string{allowed}, ClaudePath: "echo", DataDir: root}
	auth := NewAuth(cfg.SecretPath())
	if err := auth.GenerateSecret(); err != nil {
		t.Fatal(err)
	}
	s := NewServer(cfg, auth)
	s.registerRoutes()
	defer s.terminal.Stop()

	jwt, err := auth.IssueJWT("test")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]interface{}{"dir": sibling})
	req := httptest.NewRequest(http.MethodPost, "/api/claude/start", bytes.NewReader(body))
	req.AddCookie(&http.Cookie{Name: "claude-remote-auth", Value: jwt})
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("want 403 for sibling dir, got %d: %s", w.Code, w.Body.String())
	}
	if s.terminal.Running() {
		t.Error("terminal must not start for a disallowed directory")
	}
}

// Error bodies must stay valid JSON even when the message embeds a path.
func TestClaudeStartErrorBodyIsJSON(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{Port: 8822, AllowedDirs: []string{dir}, ClaudePath: "echo", DataDir: dir}
	auth := NewAuth(cfg.SecretPath())
	if err := auth.GenerateSecret(); err != nil {
		t.Fatal(err)
	}
	s := NewServer(cfg, auth)
	s.registerRoutes()

	jwt, err := auth.IssueJWT("test")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]interface{}{"dir": `/no/such"dir`})
	req := httptest.NewRequest(http.MethodPost, "/api/claude/start", bytes.NewReader(body))
	req.AddCookie(&http.Cookie{Name: "claude-remote-auth", Value: jwt})
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	var payload map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
		t.Fatalf("error body is not valid JSON: %v (%s)", err, w.Body.String())
	}
	if payload["error"] == "" {
		t.Errorf("want an error field, got %s", w.Body.String())
	}
}

// The handoff redirect must escape directories containing spaces and "&".
func TestHandoffRedirectEscapesDir(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{Port: 8822, AllowedDirs: []string{dir}, ClaudePath: "echo", DataDir: dir}
	auth := NewAuth(cfg.SecretPath())
	if err := auth.GenerateSecret(); err != nil {
		t.Fatal(err)
	}
	s := NewServer(cfg, auth)
	s.registerRoutes()

	token := auth.GenerateHandoffToken()
	raw := "/Users/me/My Project & Co"
	req := httptest.NewRequest(http.MethodGet,
		"/handoff?token="+token+"&dir="+neturl.QueryEscape(raw)+"&mode=attach", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusFound {
		t.Fatalf("want 302, got %d", w.Code)
	}
	loc, err := neturl.Parse(w.Header().Get("Location"))
	if err != nil {
		t.Fatalf("redirect is not parseable: %v", err)
	}
	if got := loc.Query().Get("dir"); got != raw {
		t.Errorf("dir round-trip failed: got %q want %q", got, raw)
	}
	if got := loc.Query().Get("mode"); got != "attach" {
		t.Errorf("mode = %q, want attach", got)
	}
}

// An unknown mode falls back to the picker instead of reaching the UI.
func TestHandoffRejectsUnknownMode(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{Port: 8822, AllowedDirs: []string{dir}, ClaudePath: "echo", DataDir: dir}
	auth := NewAuth(cfg.SecretPath())
	if err := auth.GenerateSecret(); err != nil {
		t.Fatal(err)
	}
	s := NewServer(cfg, auth)
	s.registerRoutes()

	token := auth.GenerateHandoffToken()
	req := httptest.NewRequest(http.MethodGet, "/handoff?token="+token+"&dir=/tmp&mode=evil", nil)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	loc, _ := neturl.Parse(w.Header().Get("Location"))
	if got := loc.Query().Get("mode"); got != "choose" {
		t.Errorf("mode = %q, want choose", got)
	}
}

// Behind the tunnel the local listener is plain HTTP while the browser is on
// HTTPS: the cookie must still be marked Secure or the browser drops it.
func TestAuthCookieSecureBehindTunnel(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{Port: 8822, AllowedDirs: []string{dir}, ClaudePath: "echo", DataDir: dir,
		Tunnel: Tunnel{Mode: TunnelQuick}}
	auth := NewAuth(cfg.SecretPath())
	if err := auth.GenerateSecret(); err != nil {
		t.Fatal(err)
	}
	s := NewServer(cfg, auth)
	s.registerRoutes()

	token := auth.GenerateToken()
	req := httptest.NewRequest(http.MethodGet, "/auth/scan?token="+token, nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	var authCookie *http.Cookie
	for _, c := range w.Result().Cookies() {
		if c.Name == authCookieName && c.Value != "" {
			authCookie = c
		}
	}
	if authCookie == nil {
		t.Fatalf("no auth cookie issued (status %d)", w.Code)
	}
	if !authCookie.Secure {
		t.Error("cookie must be Secure when the browser is on HTTPS")
	}
	if authCookie.SameSite != http.SameSiteLaxMode {
		t.Error("cookie must stay SameSite=Lax for QR scans")
	}
}

// Repeated bad tokens must be throttled: the tunnel puts this endpoint
// on the public internet.
func TestAuthScanIsRateLimited(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{Port: 8822, AllowedDirs: []string{dir}, ClaudePath: "echo", DataDir: dir}
	auth := NewAuth(cfg.SecretPath())
	if err := auth.GenerateSecret(); err != nil {
		t.Fatal(err)
	}
	s := NewServer(cfg, auth)
	s.registerRoutes()

	var throttled bool
	for i := 0; i < 30; i++ {
		req := httptest.NewRequest(http.MethodGet, "/auth/scan?token=guess", nil)
		req.RemoteAddr = "203.0.113.9:4444"
		w := httptest.NewRecorder()
		s.mux.ServeHTTP(w, req)
		if w.Code == http.StatusTooManyRequests {
			throttled = true
			break
		}
	}
	if !throttled {
		t.Error("brute-forcing /auth/scan was never throttled")
	}
}

// A session in daily use should never have to be re-paired: a cookie past
// a third of its life is silently refreshed.
func TestProtectRefreshesAgingCookie(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{Port: 8822, AllowedDirs: []string{dir}, ClaudePath: "echo", DataDir: dir}
	auth := NewAuth(cfg.SecretPath())
	if err := auth.GenerateSecret(); err != nil {
		t.Fatal(err)
	}
	s := NewServer(cfg, auth)
	s.registerRoutes()

	// Issue a token that is already deep into its lifetime.
	auth.jwtExpiry = time.Minute
	aging, err := auth.IssueJWT("phone")
	if err != nil {
		t.Fatal(err)
	}
	auth.jwtExpiry = 90 * 24 * time.Hour

	req := httptest.NewRequest(http.MethodGet, "/api/claude/status", nil)
	req.AddCookie(&http.Cookie{Name: authCookieName, Value: aging})
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
	var refreshed string
	for _, c := range w.Result().Cookies() {
		if c.Name == authCookieName && c.Value != "" {
			refreshed = c.Value
		}
	}
	if refreshed == "" {
		t.Fatal("aging cookie was not refreshed")
	}
	if _, remaining, err := auth.VerifyJWTRemaining(refreshed); err != nil || remaining < 24*time.Hour {
		t.Errorf("refreshed cookie is not long-lived: remaining=%v err=%v", remaining, err)
	}
}

// A fresh cookie must not be re-issued on every single request.
func TestProtectLeavesFreshCookieAlone(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{Port: 8822, AllowedDirs: []string{dir}, ClaudePath: "echo", DataDir: dir}
	auth := NewAuth(cfg.SecretPath())
	if err := auth.GenerateSecret(); err != nil {
		t.Fatal(err)
	}
	s := NewServer(cfg, auth)
	s.registerRoutes()

	fresh, err := auth.IssueJWT("phone")
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/claude/status", nil)
	req.AddCookie(&http.Cookie{Name: authCookieName, Value: fresh})
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, req)

	for _, c := range w.Result().Cookies() {
		if c.Name == authCookieName {
			t.Error("fresh cookie should not be rewritten on every request")
		}
	}
}

// The pairing endpoint is loopback-only and must reject tunnel traffic.
func TestLocalPairRejectsForwardedRequest(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{Port: 8822, AllowedDirs: []string{dir}, ClaudePath: "echo", DataDir: dir}
	auth := NewAuth(cfg.SecretPath())
	if err := auth.GenerateSecret(); err != nil {
		t.Fatal(err)
	}
	s := NewServer(cfg, auth)

	req := httptest.NewRequest(http.MethodPost, "/local/pair", nil)
	req.RemoteAddr = "127.0.0.1:9999"
	req.Header.Set("CF-Connecting-IP", "203.0.113.9")
	w := httptest.NewRecorder()
	s.localHandler().ServeHTTP(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("want 403 for forwarded pairing request, got %d", w.Code)
	}

	// A genuine local call works.
	req2 := httptest.NewRequest(http.MethodPost, "/local/pair", nil)
	req2.RemoteAddr = "127.0.0.1:9999"
	w2 := httptest.NewRecorder()
	s.localHandler().ServeHTTP(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("want 200 for local pairing request, got %d", w2.Code)
	}
	var out map[string]string
	json.Unmarshal(w2.Body.Bytes(), &out)
	if !strings.Contains(out["url"], "/auth/scan?token=") {
		t.Errorf("pair response missing pairing URL: %s", w2.Body.String())
	}
}
