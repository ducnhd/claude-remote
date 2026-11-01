package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	neturl "net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
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
	case "qr", "pair":
		cmdQR()
	case "doctor":
		cmdDoctor()
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
  install    Run the server at login (launchd on macOS, Task Scheduler on Windows)
  uninstall  Stop the server and remove it from login
  status     Show running state
  qr         Print a pairing QR code for the running server
  doctor     Diagnose connection problems and print fixes
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
	if err := cfg.Save(); err != nil {
		fmt.Fprintf(os.Stderr, "Error saving config: %v\n", err)
		os.Exit(1)
	}

	auth := NewAuth(cfg.SecretPath())
	if err := auth.GenerateSecret(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	token := auth.GenerateToken()
	if token == "" {
		fmt.Fprintln(os.Stderr, "Error: failed to generate setup token")
		os.Exit(1)
	}

	fmt.Println("Claude Remote Setup")
	fmt.Println("===================")
	fmt.Printf("Secret generated: %s\n\n", cfg.SecretPath())

	if cfg.Tunnel.Enabled() {
		// The pairing URL depends on the live tunnel, which only exists
		// while the server runs — so `qr` prints it, not `setup`.
		fmt.Println("Next steps:")
		fmt.Println("  1. claude-remote install   # run the server at login (or: claude-remote serve)")
		fmt.Println("  2. claude-remote qr        # show the QR code to scan with your phone")
		fmt.Println("\nIf anything goes wrong: claude-remote doctor")
	} else {
		home, _ := os.UserHomeDir()
		proto := detectProto(cfg.DataDir, home+"/Desktop", "/var/run/tailscale")
		host := detectReachableHost()
		url := fmt.Sprintf("%s://%s:%d/auth/scan?token=%s", proto, host, cfg.Port, neturl.QueryEscape(token))
		fmt.Printf("Protocol: %s   Host: %s\n", strings.ToUpper(proto), host)
		auth.PrintQR(url)
		fmt.Printf("\nScan with your phone (valid %s). The server must be running: claude-remote serve\n", setupTokenTTL)
		if host == "localhost" {
			fmt.Println("\nWARNING: no LAN/VPN address found — this URL will not work from a phone.")
			fmt.Println("Enable the tunnel instead: set \"tunnel\": {\"mode\":\"quick\"} in config.json")
		}
	}

	if err := os.WriteFile(filepath.Join(cfg.DataDir, ".pending-token"), []byte(token), 0600); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: could not persist setup token: %v\n", err)
	}
}

func cmdServe() {
	// launchd redirects output to server.log itself; Task Scheduler cannot,
	// so the Windows task passes --log.
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	logPath := fs.String("log", "", "append all output to this file")
	fs.Parse(os.Args[2:])
	if *logPath != "" {
		f, err := os.OpenFile(*logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error opening log: %v\n", err)
			os.Exit(1)
		}
		log.SetOutput(f)
		os.Stdout, os.Stderr = f, f
	}
	killChildrenOnExit()

	cfg := getConfig()
	auth := NewAuth(cfg.SecretPath())
	if err := auth.LoadSecret(); err != nil {
		fmt.Fprintf(os.Stderr, "No secret found. Run 'claude-remote setup' first.\n")
		os.Exit(1)
	}

	tokenPath := filepath.Join(cfg.DataDir, ".pending-token")
	if data, err := os.ReadFile(tokenPath); err == nil {
		if token := strings.TrimSpace(string(data)); token != "" {
			auth.setPendingToken(token)
		}
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
	os.Remove(cfg.SessionsPath())
	fmt.Println("All sessions revoked. New secret generated.")
	fmt.Println("Run 'claude-remote setup' to generate a new QR code.")
}

// cmdQR asks the running server for a fresh pairing URL and prints its QR.
// Unlike `setup` this never touches the secret, so existing phones stay
// logged in.
func cmdQR() {
	cfg := getConfig()
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Post(fmt.Sprintf("http://127.0.0.1:%d/local/pair", cfg.Port+1), "application/json", nil)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Cannot reach the server on 127.0.0.1:%d — is it running?\n", cfg.Port+1)
		fmt.Fprintln(os.Stderr, "Start it with: claude-remote serve   (then retry)")
		fmt.Fprintln(os.Stderr, "Diagnose with: claude-remote doctor")
		os.Exit(1)
	}
	defer resp.Body.Close()
	var out struct {
		URL  string `json:"url"`
		Base string `json:"base"`
		TTL  string `json:"ttl"`
		Err  string `json:"error"`
	}
	json.NewDecoder(resp.Body).Decode(&out)
	if out.URL == "" {
		fmt.Fprintf(os.Stderr, "Pairing failed: %s\n", strings.TrimSpace(out.Err))
		if resp.StatusCode == http.StatusServiceUnavailable {
			fmt.Fprintln(os.Stderr, "The tunnel needs a few seconds after a restart — retry, or run: claude-remote doctor")
		}
		os.Exit(1)
	}
	if strings.Contains(out.Base, "localhost") {
		fmt.Fprintln(os.Stderr, "WARNING: no tunnel or LAN address detected — this QR only works on this Mac.")
		fmt.Fprintln(os.Stderr, "Run 'claude-remote doctor' to see why.")
	}

	auth := NewAuth(cfg.SecretPath())
	fmt.Println("Scan to connect your phone")
	fmt.Println("==========================")
	auth.PrintQR(out.URL)
	fmt.Printf("\nValid for %s. Re-run 'claude-remote qr' any time.\n", out.TTL)
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

	st, err := fetchLocalStatus(cfg)
	if err != nil {
		fmt.Println("Server: not running")
		fmt.Println("\nRun 'claude-remote doctor' for a full diagnosis.")
		return
	}
	fmt.Printf("Server: running (session active: %v)\n", st.Running)
	fmt.Printf("Tunnel: %s (%s)\n", st.TunnelMode, st.TunnelState)
	if st.TunnelError != "" {
		fmt.Printf("Tunnel error: %s\n", st.TunnelError)
	}
	fmt.Printf("Phone URL: %s\n", st.PublicBase)
	fmt.Println("\nPair a phone with: claude-remote qr")
}
