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
	s.mux.HandleFunc("/auth/scan", s.handleAuthScan)
	s.mux.HandleFunc("/health", s.handleHealth)
	s.mux.HandleFunc("/mcp", s.handleMCP)

	protected := http.NewServeMux()
	protected.HandleFunc("/ws/term", s.terminal.WebSocketHandler())
	protected.HandleFunc("/api/claude/start", s.handleClaudeStart)
	protected.HandleFunc("/api/claude/status", s.handleClaudeStatus)
	protected.HandleFunc("/api/files", s.files.HandleList)
	protected.HandleFunc("/api/files/read", s.files.HandleRead)

	staticDir := filepath.Join(execDir(), "static")
	if _, err := os.Stat(staticDir); err != nil {
		// Fallback to current working directory (for go run)
		if wd, wdErr := os.Getwd(); wdErr == nil {
			staticDir = filepath.Join(wd, "static")
		}
	}
	if _, err := os.Stat(staticDir); err == nil {
		log.Printf("Serving static files from %s", staticDir)
		protected.Handle("/", http.FileServer(http.Dir(staticDir)))
	} else {
		log.Printf("WARNING: static directory not found")
	}

	s.mux.Handle("/", s.auth.Middleware(protected))
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
	http.Redirect(w, r, "/", http.StatusFound)
}

func (s *Server) handleClaudeStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Dir string `json:"dir"`
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
	if err := s.terminal.StartInDir(resolved); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		fmt.Fprintf(w, `{"error":"failed to start: %s"}`, err.Error())
		return
	}
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
		s.config.DataDir,                                    // ~/.claude-remote/
		home + "/Desktop",                                   // where tailscale cert writes by default
		"/var/run/tailscale",                                // Linux default
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
