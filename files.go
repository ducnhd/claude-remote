package main

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// maxReadSize caps the file-read endpoint response.
const maxReadSize = 1 << 20

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

// ValidatePath returns the resolved absolute path if it is allowed.
func (fb *FileBrowser) ValidatePath(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("empty path")
	}
	if hasDotDot(path) {
		return "", fmt.Errorf("path traversal not allowed")
	}
	resolved := resolvePath(path)
	if !filepath.IsAbs(resolved) {
		return "", fmt.Errorf("path must be absolute")
	}
	for _, blocked := range blockedPaths {
		if containsComponent(resolved, blocked) {
			return "", fmt.Errorf("access to %s is blocked", blocked)
		}
	}
	if !withinAllowed(resolved, fb.allowedDirs) {
		return "", fmt.Errorf("path outside allowed directories")
	}
	return resolved, nil
}

// hasDotDot reports whether any path component is "..", without rejecting
// legitimate names such as "notes..txt".
func hasDotDot(path string) bool {
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if part == ".." {
			return true
		}
	}
	return false
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
	resolved, err := fb.ValidatePath(path)
	if err != nil {
		return nil, err
	}
	dirEntries, err := os.ReadDir(resolved)
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
		if len(fb.allowedDirs) == 0 {
			writeJSONError(w, http.StatusForbidden, "no allowed directories configured")
			return
		}
		path = fb.allowedDirs[0]
	}
	path = expandHome(path)
	entries, err := fb.ListDir(path)
	if err != nil {
		writeJSONError(w, http.StatusForbidden, err.Error())
		return
	}
	resolved := resolvePath(path)
	// Keep the parent inside the allowed roots so the UI cannot walk out.
	parent := filepath.Dir(resolved)
	if !withinAllowed(parent, fb.allowedDirs) {
		parent = resolved
	}
	resp := DirResponse{
		Path:    resolved,
		Parent:  parent,
		Entries: entries,
	}
	writeJSON(w, http.StatusOK, resp)
}

func (fb *FileBrowser) HandleRead(w http.ResponseWriter, r *http.Request) {
	resolved, err := fb.ValidatePath(r.URL.Query().Get("path"))
	if err != nil {
		writeJSONError(w, http.StatusForbidden, err.Error())
		return
	}
	info, err := os.Stat(resolved)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, "file not found")
		return
	}
	if info.IsDir() {
		writeJSONError(w, http.StatusBadRequest, "cannot read directory")
		return
	}
	if !info.Mode().IsRegular() {
		writeJSONError(w, http.StatusBadRequest, "not a regular file")
		return
	}
	if info.Size() > maxReadSize {
		writeJSONError(w, http.StatusBadRequest, "file too large (max 1MB)")
		return
	}
	data, err := os.ReadFile(resolved)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "read failed")
		return
	}
	resp := FileContentResponse{
		Path:    resolved,
		Content: string(data),
		Size:    info.Size(),
	}
	writeJSON(w, http.StatusOK, resp)
}
