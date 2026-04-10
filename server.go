package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
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
	mux      *http.ServeMux
	useTLS   bool
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
	s.mux.HandleFunc("/mcp", s.handleMCP)
	s.mux.HandleFunc("/handoff", s.handleHandoff)

	// Protected API + WebSocket routes (require JWT)
	s.mux.Handle("/ws/term", s.auth.Middleware(http.HandlerFunc(s.terminal.WebSocketHandler())))
	s.mux.Handle("/api/claude/start", s.auth.Middleware(http.HandlerFunc(s.handleClaudeStart)))
	s.mux.Handle("/api/claude/status", s.auth.Middleware(http.HandlerFunc(s.handleClaudeStatus)))
	s.mux.Handle("/api/files", s.auth.Middleware(http.HandlerFunc(s.files.HandleList)))
	s.mux.Handle("/api/files/read", s.auth.Middleware(http.HandlerFunc(s.files.HandleRead)))

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

func (s *Server) handleAuthScan(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if token == "" || !s.auth.ValidateToken(token) {
		http.Error(w, `{"error":"invalid or expired token"}`, http.StatusUnauthorized)
		return
	}
	deviceID := fmt.Sprintf("device-%d", time.Now().UnixNano())
	jwt, err := s.auth.IssueJWT(deviceID)
	if err != nil {
		http.Error(w, `{"error":"failed to issue token"}`, http.StatusInternalServerError)
		return
	}
	// SameSite=Lax: QR scan opens from camera app (cross-site navigation)
	s.setAuthCookie(w, jwt)
	http.Redirect(w, r, "/", http.StatusFound)
}

func (s *Server) handleHandoff(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if token == "" || !s.auth.ValidateHandoffToken(token) {
		log.Printf("Handoff token invalid or expired from %s", r.RemoteAddr)
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
	if mode == "" {
		mode = "choose"
	}

	deviceID := fmt.Sprintf("handoff-%d", time.Now().UnixNano())
	jwt, err := s.auth.IssueJWT(deviceID)
	if err != nil {
		http.Error(w, `{"error":"failed to issue token"}`, http.StatusInternalServerError)
		return
	}
	s.setAuthCookie(w, jwt)

	redirect := fmt.Sprintf("/?dir=%s&mode=%s", dir, mode)
	http.Redirect(w, r, redirect, http.StatusFound)
}

// setAuthCookie clears any stale cookie variants and sets a fresh JWT cookie.
func (s *Server) setAuthCookie(w http.ResponseWriter, jwt string) {
	// Expire old cookie (may have been set with different SameSite value)
	http.SetCookie(w, &http.Cookie{
		Name: "claude-remote-auth", Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: s.useTLS,
	})
	// Set fresh cookie
	http.SetCookie(w, &http.Cookie{
		Name:     "claude-remote-auth",
		Value:    jwt,
		Path:     "/",
		MaxAge:   90 * 24 * 3600,
		HttpOnly: true,
		Secure:   s.useTLS,
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) handleClaudeStart(w http.ResponseWriter, r *http.Request) {
	log.Printf("handleClaudeStart called: method=%s", r.Method)
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Dir    string `json:"dir"`
		Resume bool   `json:"resume"`
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
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, `{"error":"failed to start: %s"}`, startErr.Error())
		return
	}
	log.Printf("Claude started successfully in %s", resolved)
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"status":"started","dir":"%s"}`, resolved)
}

func (s *Server) handleClaudeStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	s.terminal.mu.Lock()
	running := s.terminal.running
	s.terminal.mu.Unlock()
	fmt.Fprintf(w, `{"running":%v}`, running)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"status":"ok","version":"0.1.0"}`)
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
			log.Printf("TLS: using cert %s", crtPath)
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

	tlsCfg, tlsErr := s.loadTLSConfig()
	if tlsErr != nil {
		log.Printf("WARNING: No TLS certs found, running HTTP only: %v", tlsErr)
		s.useTLS = false
	} else {
		srv.TLSConfig = tlsCfg
		s.useTLS = true
	}

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

	// When TLS is active, start a separate HTTP-only listener on localhost
	// for MCP connections (Claude Code connects locally, doesn't need TLS)
	var mcpSrv *http.Server
	if s.useTLS {
		mcpMux := http.NewServeMux()
		mcpMux.HandleFunc("/mcp", s.handleMCP)
		mcpMux.HandleFunc("/health", s.handleHealth)
		mcpPort := s.config.Port + 1 // 8823
		mcpAddr := fmt.Sprintf("127.0.0.1:%d", mcpPort)
		mcpSrv = &http.Server{Addr: mcpAddr, Handler: mcpMux}
		go func() {
			log.Printf("MCP HTTP listener on %s (localhost only)", mcpAddr)
			if err := mcpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Printf("MCP listener error: %v", err)
			}
		}()
	}

	<-stop
	log.Println("Shutting down...")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s.terminal.Stop()
	if mcpSrv != nil {
		mcpSrv.Shutdown(ctx)
	}
	return srv.Shutdown(ctx)
}

func execDir() string {
	exe, err := os.Executable()
	if err != nil {
		return "."
	}
	return filepath.Dir(exe)
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
	}
	if err := json.Unmarshal(out, &status); err == nil && status.Self.DNSName != "" {
		dns := status.Self.DNSName
		if len(dns) > 0 && dns[len(dns)-1] == '.' {
			dns = dns[:len(dns)-1]
		}
		return dns
	}
	ipOut, err := exec.Command("tailscale", "ip", "-4").Output()
	if err != nil {
		return "localhost"
	}
	return strings.TrimSpace(string(ipOut))
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
