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
	s.mux.HandleFunc("/auth/scan", s.handleAuthScan)
	s.mux.HandleFunc("/health", s.handleHealth)

	protected := http.NewServeMux()
	protected.HandleFunc("/ws/term", s.terminal.WebSocketHandler())
	protected.HandleFunc("/api/files", s.files.HandleList)
	protected.HandleFunc("/api/files/read", s.files.HandleRead)

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
	certDirs := []string{
		"/var/run/tailscale",
		filepath.Join(os.Getenv("HOME"), ".local/share/tailscale/certs"),
	}
	for _, dir := range certDirs {
		certFile := filepath.Join(dir, "*.crt")
		matches, _ := filepath.Glob(certFile)
		if len(matches) > 0 {
			baseName := matches[0][:len(matches[0])-4]
			cert, err := tls.LoadX509KeyPair(baseName+".crt", baseName+".key")
			if err != nil {
				continue
			}
			return &tls.Config{Certificates: []tls.Certificate{cert}}, nil
		}
	}
	return nil, fmt.Errorf("no TLS certificates found")
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
	} else {
		srv.TLSConfig = tlsCfg
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
