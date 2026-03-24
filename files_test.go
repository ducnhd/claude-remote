package main

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestValidatePath(t *testing.T) {
	fb := &FileBrowser{allowedDirs: []string{"/Users/test"}}
	tests := []struct {
		name    string
		path    string
		wantErr bool
	}{
		{"valid", "/Users/test/project", false},
		{"valid nested", "/Users/test/a/b/c", false},
		{"outside allowlist", "/etc/passwd", true},
		{"path traversal", "/Users/test/../etc/passwd", true},
		{"dotfile ssh", "/Users/test/.ssh/id_rsa", true},
		{"dotfile env", "/Users/test/.env", true},
		{"dotfile env.local", "/Users/test/.env.local", true},
		{"claude-remote dir", "/Users/test/.claude-remote/secret.key", true},
		{"gnupg", "/Users/test/.gnupg/key", true},
		{"aws", "/Users/test/.aws/credentials", true},
		{"empty path", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := fb.ValidatePath(tt.path)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidatePath(%q) error = %v, wantErr %v", tt.path, err, tt.wantErr)
			}
		})
	}
}

func TestListDir(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "hello.txt"), []byte("hi"), 0644)
	os.Mkdir(filepath.Join(dir, "subdir"), 0755)

	fb := &FileBrowser{allowedDirs: []string{dir}}
	entries, err := fb.ListDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Errorf("want 2 entries, got %d", len(entries))
	}
}

func TestListDirHidesDotfiles(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".env"), []byte("SECRET=x"), 0644)
	os.WriteFile(filepath.Join(dir, "readme.md"), []byte("hi"), 0644)
	os.Mkdir(filepath.Join(dir, ".ssh"), 0700)

	fb := &FileBrowser{allowedDirs: []string{dir}}
	entries, err := fb.ListDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name == ".env" || e.Name == ".ssh" {
			t.Errorf("sensitive dotfile %q should be hidden", e.Name)
		}
	}
	if len(entries) != 1 {
		t.Errorf("want 1 entry (readme.md), got %d", len(entries))
	}
}

func TestHandleFilesAPI(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "test.txt"), []byte("content"), 0644)

	fb := &FileBrowser{allowedDirs: []string{dir}}
	req := httptest.NewRequest("GET", "/api/files?path="+dir, nil)
	w := httptest.NewRecorder()
	fb.HandleList(w, req)

	if w.Code != 200 {
		t.Errorf("want 200, got %d", w.Code)
	}
	var resp DirResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.Path != dir {
		t.Errorf("want path %s, got %s", dir, resp.Path)
	}
}

func TestHandleReadFile(t *testing.T) {
	dir := t.TempDir()
	fpath := filepath.Join(dir, "test.txt")
	os.WriteFile(fpath, []byte("hello world"), 0644)

	fb := &FileBrowser{allowedDirs: []string{dir}}
	req := httptest.NewRequest("GET", "/api/files/read?path="+fpath, nil)
	w := httptest.NewRecorder()
	fb.HandleRead(w, req)

	if w.Code != 200 {
		t.Errorf("want 200, got %d", w.Code)
	}
	var resp FileContentResponse
	json.NewDecoder(w.Body).Decode(&resp)
	if resp.Content != "hello world" {
		t.Errorf("want 'hello world', got %q", resp.Content)
	}
}
