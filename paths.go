package main

import (
	"os"
	"path/filepath"
	"strings"
)

// expandHome expands a leading "~" to the user's home directory.
func expandHome(path string) string {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if path == "~" {
		return home
	}
	return filepath.Join(home, path[2:])
}

// resolvePath cleans a path and resolves symlinks when possible.
// Falls back to the cleaned path if the target does not exist yet.
func resolvePath(path string) string {
	cleaned := filepath.Clean(expandHome(path))
	if resolved, err := filepath.EvalSymlinks(cleaned); err == nil {
		return resolved
	}
	return cleaned
}

// pathWithin reports whether path is dir itself or lives underneath it.
// Both arguments must already be cleaned/resolved. A plain strings.HasPrefix
// is not enough: "/Users/me/Desktop-secret" must not match "/Users/me/Desktop",
// and an empty dir must never match everything.
func pathWithin(path, dir string) bool {
	if path == "" || dir == "" {
		return false
	}
	path = filepath.Clean(path)
	dir = filepath.Clean(dir)
	if path == dir {
		return true
	}
	if dir == string(filepath.Separator) {
		return strings.HasPrefix(path, dir)
	}
	return strings.HasPrefix(path, dir+string(filepath.Separator))
}

// withinAllowed reports whether path is inside any of the allowed dirs.
func withinAllowed(path string, allowedDirs []string) bool {
	for _, d := range allowedDirs {
		if pathWithin(path, resolvePath(d)) {
			return true
		}
	}
	return false
}
