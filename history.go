package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// claudeProjectsDir is where Claude Code keeps one JSONL transcript per
// session, grouped by working directory. Swapped out in tests.
var claudeProjectsDir = func() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude", "projects")
}

var (
	nonAlnum    = regexp.MustCompile(`[^A-Za-z0-9]`)
	sessionIDRe = regexp.MustCompile(`^[0-9a-fA-F-]{36}$`)
)

// transcriptDir mirrors Claude Code's naming: every non-alphanumeric
// character of the working directory becomes '-'.
func transcriptDir(dir string) string {
	return filepath.Join(claudeProjectsDir(), nonAlnum.ReplaceAllString(dir, "-"))
}

type SessionSummary struct {
	ID          string    `json:"id"`
	Title       string    `json:"title"`
	FirstPrompt string    `json:"first_prompt"`
	Prompts     int       `json:"prompts"`
	Updated     time.Time `json:"updated"`
}

type HistoryMessage struct {
	Role string `json:"role"` // "user", "assistant" or "tool"
	Text string `json:"text"`
	Time string `json:"time,omitempty"`
}

// transcriptLine holds only the fields history needs; everything else in the
// (often multi-megabyte) lines is skipped by the decoder.
type transcriptLine struct {
	Type        string          `json:"type"`
	AITitle     string          `json:"aiTitle"`
	IsMeta      bool            `json:"isMeta"`
	IsSidechain bool            `json:"isSidechain"`
	Timestamp   string          `json:"timestamp"`
	Message     json.RawMessage `json:"message"`
}

type contentBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
	Name string `json:"name"`
}

// messageText turns one transcript line into the text a person typed or
// read. Tool results, thinking and injected reminders are dropped; tool calls
// collapse to a one-line marker so the flow of a turn stays readable.
func messageText(l transcriptLine) []HistoryMessage {
	if (l.Type != "user" && l.Type != "assistant") || l.IsMeta || l.IsSidechain || len(l.Message) == 0 {
		return nil
	}
	var m struct {
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(l.Message, &m) != nil || len(m.Content) == 0 {
		return nil
	}
	var out []HistoryMessage
	add := func(role, text string) {
		text = strings.TrimSpace(text)
		// Slash-command echoes and system reminders are not conversation.
		if text == "" || strings.HasPrefix(text, "<") {
			return
		}
		out = append(out, HistoryMessage{Role: role, Text: text, Time: l.Timestamp})
	}
	var s string
	if json.Unmarshal(m.Content, &s) == nil {
		add(l.Type, s)
		return out
	}
	var blocks []contentBlock
	if json.Unmarshal(m.Content, &blocks) != nil {
		return nil
	}
	for _, b := range blocks {
		switch b.Type {
		case "text":
			add(l.Type, b.Text)
		case "tool_use":
			out = append(out, HistoryMessage{Role: "tool", Text: b.Name, Time: l.Timestamp})
		}
	}
	return out
}

// scanTranscript streams a transcript, calling fn for every line it can
// decode. A truncated last line (a session still being written) just ends it.
func scanTranscript(path string, fn func(transcriptLine)) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	dec := json.NewDecoder(f)
	for {
		var l transcriptLine
		if dec.Decode(&l) != nil {
			return nil
		}
		fn(l)
	}
}

// summaryCache avoids re-reading unchanged transcripts on every list.
var summaryCache sync.Map // path -> cachedSummary

type cachedSummary struct {
	mod time.Time
	sum SessionSummary
}

func summarize(path string, mod time.Time) SessionSummary {
	if c, ok := summaryCache.Load(path); ok && c.(cachedSummary).mod.Equal(mod) {
		return c.(cachedSummary).sum
	}
	sum := SessionSummary{ID: strings.TrimSuffix(filepath.Base(path), ".jsonl"), Updated: mod}
	scanTranscript(path, func(l transcriptLine) {
		if l.Type == "ai-title" && l.AITitle != "" {
			sum.Title = l.AITitle // the latest title wins
		}
		if l.Type != "user" {
			return
		}
		for _, m := range messageText(l) {
			if m.Role == "user" {
				sum.Prompts++
				if sum.FirstPrompt == "" {
					sum.FirstPrompt = truncateRunes(m.Text, 200)
				}
			}
		}
	})
	summaryCache.Store(path, cachedSummary{mod: mod, sum: sum})
	return sum
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// listSessions returns the newest sessions for dir that have at least one
// real prompt (empty sessions are just Claude being opened and closed).
func listSessions(dir string, limit int) ([]SessionSummary, error) {
	entries, err := os.ReadDir(transcriptDir(dir))
	if os.IsNotExist(err) {
		return []SessionSummary{}, nil
	}
	if err != nil {
		return nil, err
	}
	type file struct {
		path string
		mod  time.Time
	}
	var files []file
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		if info, err := e.Info(); err == nil {
			files = append(files, file{filepath.Join(transcriptDir(dir), e.Name()), info.ModTime()})
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.After(files[j].mod) })

	out := []SessionSummary{}
	for _, f := range files {
		if len(out) >= limit {
			break
		}
		if s := summarize(f.path, f.mod); s.Prompts > 0 {
			out = append(out, s)
		}
	}
	return out, nil
}

// readMessages returns the last `limit` messages of one session.
func readMessages(dir, id string, limit int) ([]HistoryMessage, error) {
	path := filepath.Join(transcriptDir(dir), id+".jsonl")
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	var msgs []HistoryMessage
	scanTranscript(path, func(l transcriptLine) {
		for _, m := range messageText(l) {
			m.Text = truncateRunes(m.Text, 4000)
			msgs = append(msgs, m)
		}
	})
	if len(msgs) > limit {
		msgs = msgs[len(msgs)-limit:]
	}
	return msgs, nil
}

// resolveProjectDir validates a directory from the client: it must exist, be
// a real directory, and live inside one of the allowed roots.
func (s *Server) resolveProjectDir(raw string) (string, int, string) {
	resolved, err := filepath.EvalSymlinks(expandHome(raw))
	if err != nil {
		return "", http.StatusBadRequest, "invalid directory"
	}
	if info, statErr := os.Stat(resolved); statErr != nil || !info.IsDir() {
		return "", http.StatusBadRequest, "not a directory"
	}
	if !withinAllowed(resolved, s.config.AllowedDirs) {
		return "", http.StatusForbidden, "directory not allowed"
	}
	return resolved, 0, ""
}

// GET /api/sessions?dir=...
func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	dir, status, msg := s.resolveProjectDir(r.URL.Query().Get("dir"))
	if status != 0 {
		writeJSONError(w, status, msg)
		return
	}
	sessions, err := listSessions(dir, 30)
	if err != nil {
		writeJSONError(w, http.StatusInternalServerError, "could not read history")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"dir": dir, "sessions": sessions})
}

// GET /api/sessions/messages?dir=...&id=...&limit=...
func (s *Server) handleSessionMessages(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	dir, status, msg := s.resolveProjectDir(q.Get("dir"))
	if status != 0 {
		writeJSONError(w, status, msg)
		return
	}
	id := q.Get("id")
	if !sessionIDRe.MatchString(id) {
		writeJSONError(w, http.StatusBadRequest, "invalid session id")
		return
	}
	limit := 300
	if n, err := strconv.Atoi(q.Get("limit")); err == nil && n > 0 && n < limit {
		limit = n
	}
	msgs, err := readMessages(dir, id, limit)
	if err != nil {
		writeJSONError(w, http.StatusNotFound, "session not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id, "messages": msgs})
}
