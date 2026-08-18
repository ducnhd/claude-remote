package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

// quickURLPattern matches the URL cloudflared prints for a quick tunnel.
var quickURLPattern = regexp.MustCompile(`https://[a-z0-9-]+\.trycloudflare\.com`)

// TunnelManager supervises a cloudflared process and tracks the public URL.
type TunnelManager struct {
	cfg      Tunnel
	localURL string

	mu      sync.RWMutex
	url     string
	state   string // "off", "starting", "up", "retrying", "failed"
	lastErr string
	ready   chan struct{}

	cancel context.CancelFunc
	done   chan struct{}
}

func NewTunnelManager(cfg Tunnel, port int) *TunnelManager {
	return &TunnelManager{
		cfg:      cfg,
		localURL: fmt.Sprintf("http://127.0.0.1:%d", port),
		state:    "off",
		ready:    make(chan struct{}),
	}
}

// binName returns the cloudflared binary to run.
func (tn *TunnelManager) binName() string {
	if tn.cfg.Bin != "" {
		return tn.cfg.Bin
	}
	return "cloudflared"
}

// URL returns the current public URL, or "" if the tunnel is not up.
func (tn *TunnelManager) URL() string {
	tn.mu.RLock()
	defer tn.mu.RUnlock()
	return tn.url
}

// Status returns the tunnel state and the last error seen.
func (tn *TunnelManager) Status() (state, url, lastErr string) {
	tn.mu.RLock()
	defer tn.mu.RUnlock()
	return tn.state, tn.url, tn.lastErr
}

// WaitReady blocks until the public URL is known or the timeout elapses.
func (tn *TunnelManager) WaitReady(timeout time.Duration) string {
	if !tn.cfg.Enabled() {
		return ""
	}
	select {
	case <-tn.ready:
	case <-time.After(timeout):
	}
	return tn.URL()
}

func (tn *TunnelManager) setURL(url string) {
	tn.mu.Lock()
	changed := tn.url != url
	tn.url = url
	tn.state = "up"
	tn.lastErr = ""
	select {
	case <-tn.ready: // already closed
	default:
		close(tn.ready)
	}
	tn.mu.Unlock()
	if changed {
		log.Printf("Tunnel URL: %s", url)
	}
}

func (tn *TunnelManager) setState(state, errMsg string) {
	tn.mu.Lock()
	tn.state = state
	if errMsg != "" {
		tn.lastErr = errMsg
	}
	if state != "up" {
		tn.url = ""
	}
	tn.mu.Unlock()
}

// Start launches the supervision loop. It returns immediately; use
// WaitReady to block until the public URL is known.
func (tn *TunnelManager) Start() error {
	if !tn.cfg.Enabled() {
		return nil
	}
	if tn.cfg.Mode == TunnelNamed && tn.cfg.Token == "" {
		return fmt.Errorf("tunnel mode \"named\" requires a token in config.json")
	}
	if _, err := exec.LookPath(tn.binName()); err != nil {
		return fmt.Errorf("cloudflared not found — install it with: brew install cloudflared")
	}

	ctx, cancel := context.WithCancel(context.Background())
	tn.cancel = cancel
	tn.done = make(chan struct{})
	tn.setState("starting", "")

	go func() {
		defer close(tn.done)
		backoff := time.Second
		for ctx.Err() == nil {
			start := time.Now()
			err := tn.runOnce(ctx)
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				log.Printf("cloudflared exited: %v", err)
				tn.setState("retrying", err.Error())
			} else {
				tn.setState("retrying", "cloudflared exited unexpectedly")
			}
			// A tunnel that survived a while is healthy; reset the backoff.
			if time.Since(start) > time.Minute {
				backoff = time.Second
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
		}
	}()
	return nil
}

// runOnce runs cloudflared until it exits.
func (tn *TunnelManager) runOnce(ctx context.Context) error {
	args := tn.args()
	cmd := exec.CommandContext(ctx, tn.binName(), args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start cloudflared: %w", err)
	}
	log.Printf("cloudflared started: %s %s", tn.binName(), strings.Join(args, " "))

	// cloudflared logs to stderr; scan both streams for the quick-tunnel URL.
	var wg sync.WaitGroup
	for _, r := range []io.Reader{stdout, stderr} {
		wg.Add(1)
		go func(r io.Reader) {
			defer wg.Done()
			tn.scan(r)
		}(r)
	}

	// A named tunnel never prints a URL — its hostname is known upfront.
	if tn.cfg.Mode == TunnelNamed && tn.cfg.Hostname != "" {
		tn.setURL("https://" + strings.TrimPrefix(tn.cfg.Hostname, "https://"))
	}

	wg.Wait()
	return cmd.Wait()
}

func (tn *TunnelManager) args() []string {
	if tn.cfg.Mode == TunnelNamed {
		return []string{"tunnel", "--no-autoupdate", "run", "--token", tn.cfg.Token}
	}
	return []string{"tunnel", "--no-autoupdate", "--url", tn.localURL}
}

// scan watches cloudflared output for the public URL and error hints.
func (tn *TunnelManager) scan(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 512*1024)
	for sc.Scan() {
		line := sc.Text()
		if m := quickURLPattern.FindString(line); m != "" {
			tn.setURL(m)
			continue
		}
		if strings.Contains(line, "ERR ") || strings.Contains(line, "failed to") {
			tn.setState(tn.currentState(), strings.TrimSpace(line))
		}
	}
}

func (tn *TunnelManager) currentState() string {
	tn.mu.RLock()
	defer tn.mu.RUnlock()
	return tn.state
}

// Stop terminates cloudflared and waits for the supervisor to finish.
func (tn *TunnelManager) Stop() {
	if tn.cancel == nil {
		return
	}
	tn.cancel()
	select {
	case <-tn.done:
	case <-time.After(5 * time.Second):
		log.Printf("cloudflared did not stop in time")
	}
	tn.setState("off", "")
}
