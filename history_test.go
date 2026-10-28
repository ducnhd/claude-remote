package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const sampleTranscript = `{"type":"mode","mode":"normal","sessionId":"x"}
{"type":"user","message":{"role":"user","content":"Đổi nguồn dewi01 sang avtub"},"timestamp":"2026-10-27T01:00:00Z"}
{"type":"user","isMeta":true,"message":{"role":"user","content":"<command-name>/model</command-name>"}}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"thinking","thinking":"hmm"},{"type":"text","text":"Đang xem cấu hình."},{"type":"tool_use","name":"Bash","input":{}}]}}
{"type":"user","message":{"role":"user","content":[{"type":"tool_result","content":"ok"}]}}
{"type":"assistant","isSidechain":true,"message":{"role":"assistant","content":[{"type":"text","text":"subagent noise"}]}}
{"type":"ai-title","aiTitle":"Old title"}
{"type":"user","message":{"role":"user","content":[{"type":"text","text":"Chạy test luôn"}]}}
{"type":"ai-title","aiTitle":"Đổi nguồn dewi01"}
{"type":"assistant","message":{"role":"assistant","content":[{"type":"text","text":"Xong."}]}}
{"type":"user","message":{"role":"user","content":"truncated`

func withTranscripts(t *testing.T) (projectDir string) {
	t.Helper()
	root := t.TempDir()
	old := claudeProjectsDir
	claudeProjectsDir = func() string { return root }
	t.Cleanup(func() { claudeProjectsDir = old })
	projectDir = "/Users/me/Desktop/tevi_auto-post"
	if err := os.MkdirAll(transcriptDir(projectDir), 0o755); err != nil {
		t.Fatal(err)
	}
	return projectDir
}

func writeTranscript(t *testing.T, dir, id, body string, mod time.Time) {
	t.Helper()
	p := filepath.Join(transcriptDir(dir), id+".jsonl")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	os.Chtimes(p, mod, mod)
}

func TestTranscriptDirNaming(t *testing.T) {
	withTranscripts(t)
	got := filepath.Base(transcriptDir("/Users/me/Desktop/tevi_auto-post"))
	if got != "-Users-me-Desktop-tevi-auto-post" {
		t.Errorf("transcriptDir = %q", got)
	}
}

func TestListSessions(t *testing.T) {
	dir := withTranscripts(t)
	now := time.Now()
	older := "11111111-1111-1111-1111-111111111111"
	newer := "22222222-2222-2222-2222-222222222222"
	empty := "33333333-3333-3333-3333-333333333333"
	writeTranscript(t, dir, older, sampleTranscript, now.Add(-time.Hour))
	writeTranscript(t, dir, newer, `{"type":"user","message":{"content":"second"}}`, now)
	writeTranscript(t, dir, empty, `{"type":"mode","mode":"normal"}`, now.Add(time.Minute))

	got, err := listSessions(dir, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d sessions, want 2 (empty one skipped): %+v", len(got), got)
	}
	if got[0].ID != newer || got[1].ID != older {
		t.Errorf("order = %s, %s; want newest first", got[0].ID, got[1].ID)
	}
	s := got[1]
	if s.Title != "Đổi nguồn dewi01" {
		t.Errorf("title = %q, want the latest ai-title", s.Title)
	}
	if s.FirstPrompt != "Đổi nguồn dewi01 sang avtub" || s.Prompts != 2 {
		t.Errorf("first prompt = %q, prompts = %d", s.FirstPrompt, s.Prompts)
	}
}

func TestListSessionsNoHistory(t *testing.T) {
	withTranscripts(t)
	got, err := listSessions("/never/used", 10)
	if err != nil || len(got) != 0 {
		t.Errorf("got %v, %v; want empty list", got, err)
	}
}

func TestReadMessages(t *testing.T) {
	dir := withTranscripts(t)
	id := "11111111-1111-1111-1111-111111111111"
	writeTranscript(t, dir, id, sampleTranscript, time.Now())

	msgs, err := readMessages(dir, id, 100)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, m := range msgs {
		got = append(got, m.Role+":"+m.Text)
	}
	want := []string{
		"user:Đổi nguồn dewi01 sang avtub",
		"assistant:Đang xem cấu hình.",
		"tool:Bash",
		"user:Chạy test luôn",
		"assistant:Xong.",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("messages =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	last, _ := readMessages(dir, id, 2)
	if len(last) != 2 || last[1].Text != "Xong." {
		t.Errorf("limit should keep the newest messages, got %+v", last)
	}
}
