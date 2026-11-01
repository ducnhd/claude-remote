//go:build windows

package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	procGetClipboardData = user32.NewProc("GetClipboardData")
	procGlobalSize       = kernel32.NewProc("GlobalSize")
)

// readClipboard returns the clipboard text, or ok=false if there is none.
func readClipboard(t *testing.T) (string, bool) {
	t.Helper()
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if r, _, err := procOpenClipboard.Call(0); r == 0 {
		t.Fatalf("OpenClipboard: %v", err)
	}
	defer procCloseClipboard.Call()
	h, _, _ := procGetClipboardData.Call(cfUnicodeText)
	if h == 0 {
		return "", false
	}
	size, _, _ := procGlobalSize.Call(h)
	p, _, _ := procGlobalLock.Call(h)
	if p == 0 || size < 2 {
		return "", false
	}
	defer procGlobalUnlock.Call(h)
	buf := make([]uint16, size/2)
	procRtlMoveMemory.Call(uintptr(unsafe.Pointer(&buf[0])), p, size)
	return windows.UTF16ToString(buf), true
}

// The real clipboard: Vietnamese must survive the UTF-16 round trip. The
// previous clipboard text is put back afterwards.
func TestClipboardKeepsVietnamese(t *testing.T) {
	if prev, ok := readClipboard(t); ok {
		t.Cleanup(func() { setClipboard(prev) })
	}

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
	if got, _ := readClipboard(t); got != want {
		t.Errorf("clipboard = %q, want %q", got, want)
	}
}

// "Path" and "PATH" are one variable on Windows; childEnv must not emit both.
func TestChildEnvSinglePathOnWindows(t *testing.T) {
	t.Setenv("Path", `C:\Windows\system32`)
	var n int
	for _, kv := range childEnv() {
		if k, _, _ := strings.Cut(kv, "="); strings.EqualFold(k, "PATH") {
			n++
		}
	}
	if n != 1 {
		t.Errorf("PATH appears %d times in the child environment", n)
	}
}

func TestPathWithinIgnoresCase(t *testing.T) {
	cases := []struct {
		path, dir string
		want      bool
	}{
		{`C:\Users\me\Desktop\a.txt`, `c:\users\ME\desktop`, true},
		{`C:\Users\me\Desktop-secret`, `C:\Users\me\Desktop`, false},
		{`D:\anything`, `D:\`, true},
		{`C:\anything`, `D:\`, false},
	}
	for _, c := range cases {
		if got := pathWithin(c.path, c.dir); got != c.want {
			t.Errorf("pathWithin(%q, %q) = %v, want %v", c.path, c.dir, got, c.want)
		}
	}
}

// npm installs claude as claude.cmd, which only runs under cmd.exe.
func TestTerminalRunsBatchFile(t *testing.T) {
	dir := t.TempDir()
	bat := filepath.Join(dir, "fake-claude.cmd")
	os.WriteFile(bat, []byte("@echo batch got %*\r\n"), 0o600)
	tm := NewTerminalManager(bat, []string{"--settings", `{"tui":"default"}`})
	if err := tm.StartInDir(dir); err != nil {
		t.Fatal(err)
	}
	defer tm.Stop()

	var out string
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if out = string(tm.buffer.Bytes()); strings.Contains(out, "tui") {
			break
		}
	}
	if !strings.Contains(out, "batch got --settings") || !strings.Contains(out, "tui") {
		t.Errorf("batch output = %q", out)
	}
}

// The task file must be well-formed XML once paths are substituted.
func TestTaskTemplateEscapesPaths(t *testing.T) {
	got := xmlEscape(`C:\Users\A&B\bin\claude-remote.exe`)
	if !strings.Contains(got, "&amp;") {
		t.Errorf("xmlEscape left & unescaped: %q", got)
	}
	if b := utf16LE("a"); !bytes.Equal(b, []byte{0xFF, 0xFE, 'a', 0}) {
		t.Errorf("utf16LE(a) = %v", b)
	}
}

// This session is unlocked while tests run, so LogonUI.exe must not show.
func TestLocalScreenStateUnlocked(t *testing.T) {
	out, ok := localScreenState()
	if !ok {
		t.Fatal("no screen state on Windows")
	}
	var st struct{ Locked, Asleep *bool }
	if err := json.Unmarshal(out, &st); err != nil || st.Locked == nil || st.Asleep == nil {
		t.Fatalf("state = %s (%v)", out, err)
	}
	if *st.Locked {
		t.Errorf("state = %s, want unlocked", out)
	}
}
