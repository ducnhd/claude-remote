package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	neturl "net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

type Server struct {
	config   *Config
	auth     *Auth
	terminal *TerminalManager
	files    *FileBrowser
	tunnel   *TunnelManager
	mux      *http.ServeMux
	useTLS   bool
	// authLimiter throttles token guessing on the public endpoints.
	authLimiter *limiter
}

func NewServer(cfg *Config, auth *Auth) *Server {
	return &Server{
		config:      cfg,
		auth:        auth,
		terminal:    NewTerminalManager(cfg.ClaudePath, nil),
		files:       NewFileBrowser(cfg.AllowedDirs),
		tunnel:      NewTunnelManager(cfg.Tunnel, cfg.Port),
		mux:         http.NewServeMux(),
		authLimiter: newLimiter(10, 6*time.Second), // 10 burst, ~10/min sustained
	}
}

// PublicBase returns the base URL a phone should use to reach this server.
func (s *Server) PublicBase() string {
	if u := s.tunnel.URL(); u != "" {
		return u
	}
	proto := "http"
	if s.useTLS {
		proto = "https"
	}
	return fmt.Sprintf("%s://%s:%d", proto, detectReachableHost(), s.config.Port)
}

func (s *Server) registerRoutes() {
	// Public routes (no auth)
	s.mux.HandleFunc("/auth/scan", s.handleAuthScan)
	s.mux.HandleFunc("/health", s.handleHealth)
	s.mux.HandleFunc("/handoff", s.handleHandoff)
	// NOTE: /mcp is deliberately NOT registered here. Behind cloudflared every
	// request arrives from 127.0.0.1, so the loopback check alone would let the
	// whole internet mint handoff tokens. MCP lives on the localhost listener.

	// Protected API + WebSocket routes (require JWT)
	s.mux.Handle("/ws/term", s.protect(s.terminal.WebSocketHandler()))
	s.mux.Handle("/api/claude/start", s.protect(s.handleClaudeStart))
	s.mux.Handle("/api/claude/status", s.protect(s.handleClaudeStatus))
	s.mux.Handle("/api/files", s.protect(s.files.HandleList))
	s.mux.Handle("/api/files/read", s.protect(s.files.HandleRead))

	// Static files (public — auth checked by JS calling protected APIs)
	staticDir := filepath.Join(execDir(), "static")
	if _, err := os.Stat(staticDir); err != nil {
		if wd, wdErr := os.Getwd(); wdErr == nil {
			staticDir = filepath.Join(wd, "static")
		}
	}
	if _, err := os.Stat(staticDir); err == nil {
		log.Printf("Serving static files from %s", staticDir)
		s.mux.Handle("/", http.FileServer(http.Dir(staticDir)))
	} else {
		log.Printf("WARNING: static directory not found")
	}
}

// protect requires a valid JWT cookie and transparently extends it, so a
// phone in regular use never has to scan a QR code again.
func (s *Server) protect(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(authCookieName)
		if err != nil {
			log.Printf("AUTH DENIED %s %s — no cookie", r.Method, r.URL.Path)
			writeJSONError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		deviceID, remaining, err := s.auth.VerifyJWTRemaining(cookie.Value)
		if err != nil {
			log.Printf("AUTH DENIED %s %s — invalid JWT: %v", r.Method, r.URL.Path, err)
			writeJSONError(w, http.StatusUnauthorized, "session expired")
			return
		}
		// Rolling refresh: renew once the cookie is past a third of its life.
		if remaining < s.auth.jwtExpiry*2/3 {
			if fresh, err := s.auth.IssueJWT(deviceID); err == nil {
				s.setAuthCookie(w, r, fresh)
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleAuthScan(w http.ResponseWriter, r *http.Request) {
	if !s.authLimiter.allow(clientIP(r)) {
		log.Printf("AUTH THROTTLED /auth/scan from %s", clientIP(r))
		writeJSONError(w, http.StatusTooManyRequests, "too many attempts, try again shortly")
		return
	}
	token := r.URL.Query().Get("token")
	if token == "" || !s.auth.ValidateToken(token) {
		log.Printf("AUTH DENIED /auth/scan — invalid or expired setup token from %s", clientIP(r))
		writeJSONError(w, http.StatusUnauthorized, "invalid or expired token")
		return
	}
	deviceID := fmt.Sprintf("device-%d", time.Now().UnixNano())
	jwt, err := s.auth.IssueJWT(deviceID)
	if err != nil {
		log.Printf("issue jwt: %v", err)
		writeJSONError(w, http.StatusInternalServerError, "failed to issue token")
		return
	}
	// SameSite=Lax: QR scan opens from camera app (cross-site navigation)
	s.setAuthCookie(w, r, jwt)
	http.Redirect(w, r, "/", http.StatusFound)
}

func (s *Server) handleHandoff(w http.ResponseWriter, r *http.Request) {
	if !s.authLimiter.allow(clientIP(r)) {
		writeJSONError(w, http.StatusTooManyRequests, "too many attempts, try again shortly")
		return
	}
	token := r.URL.Query().Get("token")
	if token == "" || !s.auth.ValidateHandoffToken(token) {
		log.Printf("Handoff token invalid or expired from %s", clientIP(r))
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `<!DOCTYPE html><html><head><meta charset="UTF-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<style>body{background:#1a1a2e;color:#eee;font-family:system-ui;display:flex;align-items:center;justify-content:center;height:100vh;margin:0;text-align:center}
.box{padding:32px}h2{color:#f87171;margin-bottom:16px}p{color:#888;margin-bottom:24px}
a{color:#60a5fa;text-decoration:none;padding:12px 24px;border:1px solid #60a5fa;border-radius:8px;display:inline-block}</style>
</head><body><div class="box"><h2>Token Expired</h2><p>QR code has expired. Run the handoff command again on your computer to generate a new QR.</p>
<a href="/">Go to Home</a></div></body></html>`)
		return
	}

	dir := r.URL.Query().Get("dir")
	mode := r.URL.Query().Get("mode")
	switch mode {
	case "attach", "continue", "choose":
	default:
		mode = "choose"
	}

	deviceID := fmt.Sprintf("handoff-%d", time.Now().UnixNano())
	jwt, err := s.auth.IssueJWT(deviceID)
	if err != nil {
		log.Printf("issue jwt: %v", err)
		writeJSONError(w, http.StatusInternalServerError, "failed to issue token")
		return
	}
	s.setAuthCookie(w, r, jwt)

	// Escape: directories routinely contain spaces and "&".
	redirect := "/?" + neturl.Values{"dir": {dir}, "mode": {mode}}.Encode()
	http.Redirect(w, r, redirect, http.StatusFound)
}

// setAuthCookie clears any stale cookie variants and sets a fresh JWT cookie.
// Secure is decided per request: behind the tunnel the local listener is
// plain HTTP but the browser is on HTTPS, and a non-Secure cookie set on an
// HTTPS page is what silently broke logins before.
func (s *Server) setAuthCookie(w http.ResponseWriter, r *http.Request, jwt string) {
	secure := isSecureRequest(r) || s.useTLS
	// Expire old cookie (may have been set with different flags)
	http.SetCookie(w, &http.Cookie{
		Name: "claude-remote-auth", Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: secure,
	})
	// Set fresh cookie
	http.SetCookie(w, &http.Cookie{
		Name:     "claude-remote-auth",
		Value:    jwt,
		Path:     "/",
		MaxAge:   90 * 24 * 3600,
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) handleClaudeStart(w http.ResponseWriter, r *http.Request) {
	log.Printf("handleClaudeStart called: method=%s", r.Method)
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		Dir    string `json:"dir"`
		Resume bool   `json:"resume"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid request")
		return
	}
	// Validate directory: must exist, be a real directory, and live inside
	// one of the allowed roots (component-wise, not a raw prefix match).
	resolved, err := filepath.EvalSymlinks(expandHome(req.Dir))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid directory")
		return
	}
	if info, statErr := os.Stat(resolved); statErr != nil || !info.IsDir() {
		writeJSONError(w, http.StatusBadRequest, "not a directory")
		return
	}
	if !withinAllowed(resolved, s.config.AllowedDirs) {
		writeJSONError(w, http.StatusForbidden, "directory not allowed")
		return
	}
	// Stop existing session if running
	s.terminal.Stop()
	// Start new session in the requested directory
	log.Printf("Starting claude: dir=%s resume=%v cmd=%s", resolved, req.Resume, s.config.ClaudePath)
	var startErr error
	if req.Resume {
		startErr = s.terminal.StartWithResume(resolved)
	} else {
		startErr = s.terminal.StartInDir(resolved)
	}
	if startErr != nil {
		log.Printf("Claude start failed: %v", startErr)
		writeJSONError(w, http.StatusInternalServerError, "failed to start: "+startErr.Error())
		return
	}
	log.Printf("Claude started successfully in %s", resolved)
	writeJSON(w, http.StatusOK, map[string]string{"status": "started", "dir": resolved})
}

func (s *Server) handleClaudeStatus(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"running": s.terminal.Running()})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "version": version})
}

// writeJSON writes a JSON body with the proper content type and status.
func writeJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(payload)
}

// writeJSONError keeps error bodies valid JSON even when the message
// contains quotes or backslashes (e.g. filesystem paths).
func writeJSONError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func (s *Server) loadTLSConfig() (*tls.Config, error) {
	home := os.Getenv("HOME")
	certDirs := []string{
		s.config.DataDir,     // ~/.claude-remote/
		home + "/Desktop",    // where tailscale cert writes by default
		"/var/run/tailscale", // Linux default
		filepath.Join(home, ".local/share/tailscale/certs"), // alt Linux
	}
	for _, dir := range certDirs {
		matches, _ := filepath.Glob(filepath.Join(dir, "*.crt"))
		for _, crtPath := range matches {
			keyPath := crtPath[:len(crtPath)-4] + ".key"
			cert, err := tls.LoadX509KeyPair(crtPath, keyPath)
			if err != nil {
				continue
			}
			// Serving an expired cert makes every phone refuse the
			// connection with no useful message. Skip it loudly instead.
			if leaf, perr := x509.ParseCertificate(cert.Certificate[0]); perr == nil {
				if now := time.Now(); now.After(leaf.NotAfter) {
					log.Printf("WARNING: ignoring expired cert %s (expired %s) — renew with: sudo tailscale cert %s",
						crtPath, leaf.NotAfter.Format("2006-01-02"), leaf.Subject.CommonName)
					continue
				} else if now.Add(14 * 24 * time.Hour).After(leaf.NotAfter) {
					log.Printf("WARNING: cert %s expires %s — renew soon", crtPath, leaf.NotAfter.Format("2006-01-02"))
				}
			}
			log.Printf("TLS: using cert %s", crtPath)
			return &tls.Config{Certificates: []tls.Certificate{cert}}, nil
		}
	}
	return nil, fmt.Errorf("no valid TLS certificates found — run: sudo tailscale cert <hostname>.ts.net")
}

func (s *Server) Run() error {
	s.registerRoutes()
	addr := fmt.Sprintf("%s:%d", s.config.BindHost(), s.config.Port)

	srv := &http.Server{
		Addr:    addr,
		Handler: s.mux,
		// No write timeout: /ws/term is a long-lived WebSocket.
		ReadHeaderTimeout: 20 * time.Second,
	}

	// TLS is only meaningful without a tunnel; cloudflared terminates HTTPS.
	if s.config.Tunnel.Enabled() {
		log.Printf("Tunnel mode %q: serving plain HTTP on %s (cloudflared provides HTTPS)", s.config.Tunnel.Mode, addr)
	} else if tlsCfg, tlsErr := s.loadTLSConfig(); tlsErr != nil {
		log.Printf("WARNING: No usable TLS certs, running HTTP only: %v", tlsErr)
	} else {
		srv.TLSConfig = tlsCfg
		s.useTLS = true
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	fatal := make(chan error, 2)

	go func() {
		log.Printf("Claude Remote listening on %s", addr)
		var err error
		if s.useTLS {
			err = srv.ListenAndServeTLS("", "")
		} else {
			err = srv.ListenAndServe()
		}
		if err != nil && err != http.ErrServerClosed {
			fatal <- fmt.Errorf("main listener: %w", err)
		}
	}()

	// Localhost-only listener: MCP for Claude Code plus the local pairing
	// endpoint used by `claude-remote qr`. Never exposed by the tunnel.
	localAddr := fmt.Sprintf("127.0.0.1:%d", s.config.Port+1)
	localSrv := &http.Server{Addr: localAddr, Handler: s.localHandler(), ReadHeaderTimeout: 20 * time.Second}
	go func() {
		log.Printf("Local listener on %s (MCP + pairing, localhost only)", localAddr)
		if err := localSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fatal <- fmt.Errorf("local listener: %w", err)
		}
	}()

	if err := s.tunnel.Start(); err != nil {
		// A missing tunnel must not take the server down: LAN access and
		// `claude-remote doctor` still work, and the error is actionable.
		log.Printf("WARNING: tunnel disabled: %v", err)
	} else if s.config.Tunnel.Enabled() {
		go func() {
			if url := s.tunnel.WaitReady(60 * time.Second); url != "" {
				log.Printf("Phone URL ready: %s — run 'claude-remote qr' to pair", url)
			} else {
				log.Printf("WARNING: tunnel did not come up within 60s")
			}
		}()
	}

	var runErr error
	select {
	case <-stop:
		log.Println("Shutting down...")
	case runErr = <-fatal:
		log.Printf("Fatal: %v", runErr)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s.terminal.Stop()
	s.tunnel.Stop()
	localSrv.Shutdown(ctx)
	if err := srv.Shutdown(ctx); err != nil && runErr == nil {
		runErr = err
	}
	return runErr
}

// localHandler builds the loopback-only mux (MCP + pairing + health).
func (s *Server) localHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", s.handleMCP)
	mux.HandleFunc("/health", s.handleHealth)
	mux.HandleFunc("/local/pair", s.handleLocalPair)
	mux.HandleFunc("/local/status", s.handleLocalStatus)
	return mux
}

// handleLocalPair mints a pairing URL for the CLI. Localhost only.
func (s *Server) handleLocalPair(w http.ResponseWriter, r *http.Request) {
	if !s.localOnly(w, r) {
		return
	}
	token := s.auth.GenerateToken()
	if token == "" {
		writeJSONError(w, http.StatusInternalServerError, "failed to generate token")
		return
	}
	base := s.PublicBase()
	writeJSON(w, http.StatusOK, map[string]string{
		"url":  base + "/auth/scan?token=" + neturl.QueryEscape(token),
		"base": base,
		"ttl":  setupTokenTTL.String(),
	})
}

// handleLocalStatus reports live runtime state for `claude-remote doctor`.
func (s *Server) handleLocalStatus(w http.ResponseWriter, r *http.Request) {
	if !s.localOnly(w, r) {
		return
	}
	state, url, lastErr := s.tunnel.Status()
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"version":      version,
		"running":      s.terminal.Running(),
		"dir":          s.terminal.dir,
		"tunnel_mode":  s.config.Tunnel.Mode,
		"tunnel_state": state,
		"tunnel_url":   url,
		"tunnel_error": lastErr,
		"public_base":  s.PublicBase(),
		"tls":          s.useTLS,
	})
}

// localOnly rejects anything that is not a direct loopback request.
func (s *Server) localOnly(w http.ResponseWriter, r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return false
	}
	ip := net.ParseIP(host)
	// viaProxy: a request forwarded by cloudflared also originates from
	// 127.0.0.1, so loopback alone is not proof of a local caller.
	if ip == nil || !ip.IsLoopback() || viaProxy(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return false
	}
	return true
}

func execDir() string {
	exe, err := os.Executable()
	if err != nil {
		return "."
	}
	return filepath.Dir(exe)
}

// detectTailscaleHost returns the MagicDNS name of this machine, or ""
// when Tailscale is not installed or not running. The previous version
// returned "localhost" on failure, which silently produced QR codes
// pointing at the phone itself.
func detectTailscaleHost() string {
	out, err := exec.Command("tailscale", "status", "--json").Output()
	if err != nil {
		return ""
	}
	var status struct {
		BackendState string `json:"BackendState"`
		Self         struct {
			DNSName string `json:"DNSName"`
			Online  bool   `json:"Online"`
		} `json:"Self"`
	}
	if err := json.Unmarshal(out, &status); err != nil {
		return ""
	}
	if status.BackendState != "" && status.BackendState != "Running" {
		return ""
	}
	if dns := strings.TrimSuffix(status.Self.DNSName, "."); dns != "" {
		return dns
	}
	ipOut, err := exec.Command("tailscale", "ip", "-4").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(ipOut))
}

// detectLANIP returns this machine's primary non-loopback IPv4 address.
func detectLANIP() string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return ""
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipNet.IP.To4()
			if ip == nil || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
				continue
			}
			return ip.String()
		}
	}
	return ""
}

// detectReachableHost picks the best hostname a phone could use when no
// tunnel is running: Tailscale MagicDNS first, then the LAN address.
func detectReachableHost() string {
	if h := detectTailscaleHost(); h != "" {
		return h
	}
	if ip := detectLANIP(); ip != "" {
		return ip
	}
	return "localhost"
}

func detectProto(dataDirs ...string) string {
	for _, dir := range dataDirs {
		matches, _ := filepath.Glob(filepath.Join(dir, "*.crt"))
		if len(matches) > 0 {
			return "https"
		}
	}
	return "http"
}
