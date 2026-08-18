// config.go
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

type Config struct {
	Port        int      `json:"port"`
	AllowedDirs []string `json:"allowed_dirs"`
	ClaudePath  string   `json:"claude_path"`
	Tunnel      Tunnel   `json:"tunnel"`
	DataDir     string   `json:"-"`
}

// Tunnel describes how the phone reaches this Mac.
//
//	mode "quick"  — cloudflared quick tunnel, fresh *.trycloudflare.com URL per run
//	mode "named"  — cloudflared named tunnel with a stable hostname (needs token)
//	mode "off"    — no tunnel; reach the server over LAN / an existing VPN
type Tunnel struct {
	Mode     string `json:"mode"`
	Bin      string `json:"bin,omitempty"`
	Hostname string `json:"hostname,omitempty"`
	Token    string `json:"token,omitempty"`
}

const (
	TunnelQuick = "quick"
	TunnelNamed = "named"
	TunnelOff   = "off"
)

// Enabled reports whether a cloudflared process should be managed.
func (t Tunnel) Enabled() bool {
	return t.Mode == TunnelQuick || t.Mode == TunnelNamed
}

// BindHost returns the interface the web listener should bind to.
// With a tunnel, cloudflared connects locally, so the server never needs
// to be exposed on the LAN.
func (c *Config) BindHost() string {
	if c.Tunnel.Enabled() {
		return "127.0.0.1"
	}
	return "0.0.0.0"
}

func DefaultConfig() *Config {
	home, _ := os.UserHomeDir()
	return &Config{
		Port:        8822,
		AllowedDirs: []string{home},
		ClaudePath:  "claude",
		Tunnel:      Tunnel{Mode: TunnelQuick},
		DataDir:     filepath.Join(home, ".claude-remote"),
	}
}

func LoadConfig(dataDir string) (*Config, error) {
	path := filepath.Join(dataDir, "config.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			cfg := DefaultConfig()
			cfg.DataDir = dataDir
			return cfg, nil
		}
		return nil, err
	}
	cfg := DefaultConfig()
	cfg.DataDir = dataDir
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, err
	}
	// Configs written before tunnel support have no tunnel block.
	if cfg.Tunnel.Mode == "" {
		cfg.Tunnel.Mode = TunnelQuick
	}
	return cfg, nil
}

func (c *Config) Save() error {
	if err := os.MkdirAll(c.DataDir, 0700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(c.DataDir, "config.json"), data, 0600)
}

func (c *Config) SecretPath() string {
	return filepath.Join(c.DataDir, "secret.key")
}

func (c *Config) SessionsPath() string {
	return filepath.Join(c.DataDir, "sessions.json")
}
