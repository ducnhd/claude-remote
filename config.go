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
	DataDir     string   `json:"-"`
}

func DefaultConfig() *Config {
	home, _ := os.UserHomeDir()
	return &Config{
		Port:        8822,
		AllowedDirs: []string{home},
		ClaudePath:  "claude",
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
