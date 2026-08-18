package main

import (
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type checkStatus int

const (
	statusOK checkStatus = iota
	statusWarn
	statusFail
)

func (c checkStatus) icon() string {
	switch c {
	case statusOK:
		return "✅"
	case statusWarn:
		return "⚠️ "
	default:
		return "❌"
	}
}

type check struct {
	name   string
	status checkStatus
	detail string
	fix    string
}

// localStatus mirrors /local/status on the loopback listener.
type localStatus struct {
	Version     string `json:"version"`
	Running     bool   `json:"running"`
	Dir         string `json:"dir"`
	TunnelMode  string `json:"tunnel_mode"`
	TunnelState string `json:"tunnel_state"`
	TunnelURL   string `json:"tunnel_url"`
	TunnelError string `json:"tunnel_error"`
	PublicBase  string `json:"public_base"`
	TLS         bool   `json:"tls"`
}

// fetchLocalStatus asks the running server about itself.
func fetchLocalStatus(cfg *Config) (*localStatus, error) {
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(fmt.Sprintf("http://127.0.0.1:%d/local/status", cfg.Port+1))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	var st localStatus
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		return nil, err
	}
	return &st, nil
}

func cmdDoctor() {
	cfg := getConfig()
	var checks []check
	add := func(c check) { checks = append(checks, c) }

	// --- Secret ---
	if info, err := os.Stat(cfg.SecretPath()); err != nil {
		add(check{"Auth secret", statusFail, "not found", "claude-remote setup"})
	} else if info.Size() < 16 {
		add(check{"Auth secret", statusFail, "too short/corrupt", "claude-remote setup"})
	} else {
		add(check{"Auth secret", statusOK, cfg.SecretPath(), ""})
	}

	// --- Claude binary ---
	claudePath := cfg.ClaudePath
	if !filepath.IsAbs(claudePath) {
		if resolved, err := exec.LookPath(claudePath); err == nil {
			add(check{"Claude CLI", statusWarn, "relative path resolves to " + resolved,
				`set "claude_path" in ~/.claude-remote/config.json to that absolute path (launchd has a minimal PATH)`})
		} else {
			add(check{"Claude CLI", statusFail, claudePath + " not found in PATH",
				`set an absolute "claude_path" in ~/.claude-remote/config.json`})
		}
	} else if info, err := os.Stat(claudePath); err != nil {
		add(check{"Claude CLI", statusFail, claudePath + " does not exist",
			`fix "claude_path" in ~/.claude-remote/config.json`})
	} else if info.Mode()&0111 == 0 {
		add(check{"Claude CLI", statusFail, claudePath + " is not executable", "chmod +x " + claudePath})
	} else {
		add(check{"Claude CLI", statusOK, claudePath, ""})
	}

	// --- Allowed dirs ---
	var missing []string
	for _, d := range cfg.AllowedDirs {
		if _, err := os.Stat(expandHome(d)); err != nil {
			missing = append(missing, d)
		}
	}
	switch {
	case len(cfg.AllowedDirs) == 0:
		add(check{"Allowed dirs", statusFail, "empty — nothing can be opened",
			`add "allowed_dirs" to ~/.claude-remote/config.json`})
	case len(missing) > 0:
		add(check{"Allowed dirs", statusWarn, "missing: " + strings.Join(missing, ", "),
			"remove them from config.json"})
	default:
		add(check{"Allowed dirs", statusOK, strings.Join(cfg.AllowedDirs, ", "), ""})
	}

	// --- Server process ---
	st, statusErr := fetchLocalStatus(cfg)
	if statusErr != nil {
		if portInUse(cfg.Port) {
			add(check{"Server", statusWarn, fmt.Sprintf("port %d is busy but /local/status did not answer (old build?)", cfg.Port),
				"make install && launchctl kickstart -k gui/$(id -u)/com.claude-remote"})
		} else {
			add(check{"Server", statusFail, "not running: " + statusErr.Error(),
				"claude-remote serve   (or: make install to run it at login)"})
		}
	} else {
		add(check{"Server", statusOK, fmt.Sprintf("v%s, session running: %v", st.Version, st.Running), ""})
	}

	// --- launchd ---
	if out, err := exec.Command("launchctl", "list", "com.claude-remote").Output(); err == nil && len(out) > 0 {
		add(check{"Auto-start", statusOK, "launchd job loaded", ""})
	} else {
		add(check{"Auto-start", statusWarn, "launchd job not loaded", "claude-remote install"})
	}

	// --- Tunnel ---
	checks = append(checks, tunnelChecks(cfg, st)...)

	// --- Stale Tailscale shim ---
	if _, err := exec.LookPath("tailscale"); err == nil {
		if err := exec.Command("tailscale", "status").Run(); err != nil {
			add(check{"Tailscale", statusWarn, "CLI present but not working (app removed?)",
				"ignore it if you use the tunnel, or reinstall Tailscale"})
		} else {
			add(check{"Tailscale", statusOK, detectTailscaleHost(), ""})
		}
	}

	// --- Certs ---
	checks = append(checks, certChecks(cfg)...)

	// --- MCP registration ---
	if out, err := exec.Command("claude", "mcp", "list").CombinedOutput(); err == nil {
		if strings.Contains(string(out), "claude-remote") {
			add(check{"MCP registration", statusOK, "registered with Claude Code", ""})
		} else {
			add(check{"MCP registration", statusWarn, "not registered",
				fmt.Sprintf("claude mcp add --transport http -s user claude-remote http://127.0.0.1:%d/mcp", cfg.Port+1)})
		}
	}

	// --- Report ---
	fmt.Printf("claude-remote doctor  (v%s)\n", version)
	fmt.Println(strings.Repeat("=", 52))
	var fails, warns int
	for _, c := range checks {
		fmt.Printf("%s %-18s %s\n", c.status.icon(), c.name, c.detail)
		if c.fix != "" && c.status != statusOK {
			fmt.Printf("   → %s\n", c.fix)
		}
		switch c.status {
		case statusFail:
			fails++
		case statusWarn:
			warns++
		}
	}
	fmt.Println(strings.Repeat("-", 52))
	if st != nil && st.PublicBase != "" {
		fmt.Printf("Phone URL: %s\n", st.PublicBase)
		fmt.Println("Pair with: claude-remote qr")
	}
	fmt.Printf("%d ok, %d warning(s), %d problem(s)\n", len(checks)-fails-warns, warns, fails)
	if fails > 0 {
		os.Exit(1)
	}
}

func tunnelChecks(cfg *Config, st *localStatus) []check {
	if !cfg.Tunnel.Enabled() {
		return []check{{"Tunnel", statusWarn, "disabled (mode: " + cfg.Tunnel.Mode + ")",
			`set "tunnel": {"mode":"quick"} in ~/.claude-remote/config.json for access outside your wifi`}}
	}
	bin := cfg.Tunnel.Bin
	if bin == "" {
		bin = "cloudflared"
	}
	out := []check{}
	if path, err := exec.LookPath(bin); err != nil {
		out = append(out, check{"cloudflared", statusFail, "not installed", "brew install cloudflared"})
		return out
	} else {
		out = append(out, check{"cloudflared", statusOK, path, ""})
	}
	if cfg.Tunnel.Mode == TunnelNamed && cfg.Tunnel.Token == "" {
		out = append(out, check{"Tunnel config", statusFail, `mode "named" without a token`,
			`add "token" to the tunnel block, or switch mode to "quick"`})
	}
	switch {
	case st == nil:
		out = append(out, check{"Tunnel", statusWarn, "cannot tell — server is not running", "claude-remote serve"})
	case st.TunnelState == "up" && st.TunnelURL != "":
		detail := st.TunnelURL
		if err := probeURL(st.TunnelURL + "/health"); err != nil {
			out = append(out, check{"Tunnel", statusWarn, detail + " (health probe failed: " + err.Error() + ")",
				"check your internet connection"})
		} else {
			out = append(out, check{"Tunnel", statusOK, detail + " (reachable)", ""})
		}
	default:
		detail := "state: " + st.TunnelState
		if st.TunnelError != "" {
			detail += " — " + st.TunnelError
		}
		out = append(out, check{"Tunnel", statusFail, detail,
			"check ~/.claude-remote/server.log, then: launchctl kickstart -k gui/$(id -u)/com.claude-remote"})
	}
	return out
}

func certChecks(cfg *Config) []check {
	matches, _ := filepath.Glob(filepath.Join(cfg.DataDir, "*.crt"))
	if len(matches) == 0 {
		return nil
	}
	var out []check
	for _, crt := range matches {
		notAfter, cn, err := certInfo(crt)
		name := "TLS cert"
		switch {
		case err != nil:
			out = append(out, check{name, statusWarn, filepath.Base(crt) + ": unreadable", "delete it if unused"})
		case time.Now().After(notAfter):
			out = append(out, check{name, statusWarn,
				fmt.Sprintf("%s expired %s (ignored)", filepath.Base(crt), notAfter.Format("2006-01-02")),
				"not needed with the tunnel — delete it, or renew: sudo tailscale cert " + cn})
		case time.Until(notAfter) < 14*24*time.Hour:
			out = append(out, check{name, statusWarn,
				fmt.Sprintf("%s expires %s", filepath.Base(crt), notAfter.Format("2006-01-02")),
				"sudo tailscale cert " + cn})
		default:
			out = append(out, check{name, statusOK,
				fmt.Sprintf("%s valid until %s", filepath.Base(crt), notAfter.Format("2006-01-02")), ""})
		}
	}
	return out
}

// certInfo returns the expiry and common name of a PEM certificate file.
func certInfo(path string) (time.Time, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return time.Time{}, "", err
	}
	for {
		var block *pem.Block
		block, data = pem.Decode(data)
		if block == nil {
			return time.Time{}, "", fmt.Errorf("no certificate in %s", path)
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		crt, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return time.Time{}, "", err
		}
		return crt.NotAfter, crt.Subject.CommonName, nil
	}
}

func portInUse(port int) bool {
	conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

func probeURL(url string) error {
	client := &http.Client{Timeout: 8 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return nil
}
