package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var blockedPaths = []string{
	".ssh", ".env", ".claude-remote", ".gnupg", ".aws",
	".config/gcloud", ".docker", ".kube",
}

type FileBrowser struct {
	allowedDirs []string
}

type FileEntry struct {
	Name     string    `json:"name"`
	Type     string    `json:"type"`
	Size     int64     `json:"size"`
	Modified time.Time `json:"modified"`
}

type DirResponse struct {
	Path    string      `json:"path"`
	Parent  string      `json:"parent"`
	Entries []FileEntry `json:"entries"`
}

type FileContentResponse struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Size    int64  `json:"size"`
}

func NewFileBrowser(allowedDirs []string) *FileBrowser {
	return &FileBrowser{allowedDirs: allowedDirs}
}

func (fb *FileBrowser) ValidatePath(path string) error {
	if path == "" {
		return fmt.Errorf("empty path")
	}
	if strings.Contains(path, "..") {
		return fmt.Errorf("path traversal not allowed")
	}
	cleaned := filepath.Clean(path)
	resolved, err := filepath.EvalSymlinks(cleaned)
	if err != nil {
		resolved = cleaned
	}
	for _, blocked := range blockedPaths {
		if containsComponent(resolved, blocked) {
			return fmt.Errorf("access to %s is blocked", blocked)
		}
	}
	for _, dir := range fb.allowedDirs {
		resolvedDir, err := filepath.EvalSymlinks(dir)
		if err != nil {
			resolvedDir = filepath.Clean(dir)
		}
		if strings.HasPrefix(resolved, resolvedDir) {
			return nil
		}
	}
	return fmt.Errorf("path outside allowed directories")
}

func containsComponent(path, component string) bool {
	parts := strings.Split(path, string(filepath.Separator))
	for _, part := range parts {
		if part == component {
			return true
		}
		if component == ".env" && strings.HasPrefix(part, ".env") {
			return true
		}
	}
	return false
}

func isBlockedEntry(name string) bool {
	for _, blocked := range blockedPaths {
		if name == blocked {
			return true
		}
		if blocked == ".env" && strings.HasPrefix(name, ".env") {
			return true
		}
	}
	return false
}

func (fb *FileBrowser) ListDir(path string) ([]FileEntry, error) {
	if err := fb.ValidatePath(path); err != nil {
		return nil, err
	}
	dirEntries, err := os.ReadDir(path)
	if err != nil {
		return nil, fmt.Errorf("read dir: %w", err)
	}
	var entries []FileEntry
	for _, de := range dirEntries {
		if isBlockedEntry(de.Name()) {
			continue
		}
		info, err := de.Info()
		if err != nil {
			continue
		}
		typ := "file"
		if de.IsDir() {
			typ = "dir"
		}
		entries = append(entries, FileEntry{
			Name:     de.Name(),
			Type:     typ,
			Size:     info.Size(),
			Modified: info.ModTime(),
		})
	}
	return entries, nil
}

func (fb *FileBrowser) HandleList(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		path = fb.allowedDirs[0]
	}
	// Expand ~ to home directory
	if strings.HasPrefix(path, "~/") || path == "~" {
		home, _ := os.UserHomeDir()
		if path == "~" {
			path = home
		} else {
			path = filepath.Join(home, path[2:])
		}
	}
	entries, err := fb.ListDir(path)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusForbidden)
		return
	}
	resp := DirResponse{
		Path:    path,
		Parent:  filepath.Dir(path),
		Entries: entries,
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (fb *FileBrowser) HandleRead(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if err := fb.ValidatePath(path); err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusForbidden)
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		http.Error(w, `{"error":"file not found"}`, http.StatusNotFound)
		return
	}
	if info.IsDir() {
		http.Error(w, `{"error":"cannot read directory"}`, http.StatusBadRequest)
		return
	}
	if info.Size() > 1<<20 {
		http.Error(w, `{"error":"file too large (max 1MB)"}`, http.StatusBadRequest)
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		http.Error(w, `{"error":"read failed"}`, http.StatusInternalServerError)
		return
	}
	resp := FileContentResponse{
		Path:    path,
		Content: string(data),
		Size:    info.Size(),
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
