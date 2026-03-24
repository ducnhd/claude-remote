// config_test.go
package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Port != 8822 {
		t.Errorf("want port 8822, got %d", cfg.Port)
	}
	home, _ := os.UserHomeDir()
	if len(cfg.AllowedDirs) != 1 || cfg.AllowedDirs[0] != home {
		t.Errorf("want AllowedDirs=[%s], got %v", home, cfg.AllowedDirs)
	}
	if cfg.ClaudePath != "claude" {
		t.Errorf("want claude, got %s", cfg.ClaudePath)
	}
}

func TestConfigSaveLoad(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{
		Port:        9999,
		AllowedDirs: []string{"/tmp"},
		ClaudePath:  "/usr/local/bin/claude",
		DataDir:     dir,
	}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Port != 9999 {
		t.Errorf("want 9999, got %d", loaded.Port)
	}
	if loaded.ClaudePath != "/usr/local/bin/claude" {
		t.Errorf("want /usr/local/bin/claude, got %s", loaded.ClaudePath)
	}
}

func TestConfigCreatesDirIfMissing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "subdir")
	cfg := &Config{Port: 8822, DataDir: dir}
	if err := cfg.Save(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		t.Error("expected dir to be created")
	}
}
