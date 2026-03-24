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

	hostname := detectTailscaleHost()

	// Detect if TLS certs are available to choose protocol
	proto := "http"
	home, _ := os.UserHomeDir()
	for _, dir := range []string{cfg.DataDir, home + "/Desktop", "/var/run/tailscale"} {
		matches, _ := filepath.Glob(filepath.Join(dir, "*.crt"))
		if len(matches) > 0 {
			proto = "https"
			break
		}
	}
	url := fmt.Sprintf("%s://%s:%d/auth/scan?token=%s", proto, hostname, cfg.Port, token)

	fmt.Println("Claude Remote Setup")
	fmt.Println("===================")
	fmt.Printf("Protocol: %s\n", strings.ToUpper(proto))
	auth.PrintQR(url)
	fmt.Println("\nScan this QR code with your phone to connect.")
	fmt.Println("The server must be running: claude-remote serve")

	os.WriteFile(filepath.Join(cfg.DataDir, ".pending-token"), []byte(token), 0600)
}

func cmdServe() {
	cfg := getConfig()
	auth := NewAuth(cfg.SecretPath())
	if err := auth.LoadSecret(); err != nil {
		fmt.Fprintf(os.Stderr, "No secret found. Run 'claude-remote setup' first.\n")
		os.Exit(1)
	}

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
	os.Remove(cfg.SessionsPath())
	fmt.Println("All sessions revoked. New secret generated.")
	fmt.Println("Run 'claude-remote setup' to generate a new QR code.")
}

func cmdInstall() {
	home, _ := os.UserHomeDir()
	plistDir := filepath.Join(home, "Library", "LaunchAgents")
	os.MkdirAll(plistDir, 0755)
	plistPath := filepath.Join(plistDir, "com.claude-remote.plist")

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
