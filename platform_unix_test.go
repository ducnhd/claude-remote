//go:build !windows

package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestClipboardKeepsVietnamese(t *testing.T) {
	out := filepath.Join(t.TempDir(), "clip.txt")
	fake := filepath.Join(t.TempDir(), "pbcopy")
	os.WriteFile(fake, []byte("#!/bin/sh\ncat > "+out+"\n"), 0o755)
	old := pbcopyPath
	pbcopyPath = fake
	t.Cleanup(func() { pbcopyPath = old })

	s := &Server{}
	want := "Đổi nguồn sang avtub — tiếng Việt có dấu ệ ữ"
	body, _ := json.Marshal(map[string]string{"text": want})
	req := httptest.NewRequest(http.MethodPost, "/api/clipboard", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.handleClipboard(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	got, _ := os.ReadFile(out)
	if string(got) != want {
		t.Errorf("clipboard = %q, want %q", got, want)
	}
}
