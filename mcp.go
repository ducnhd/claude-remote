package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/skip2/go-qrcode"
)

type jsonrpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      interface{}     `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type jsonrpcResponse struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      interface{} `json:"id"`
	Result  interface{} `json:"result,omitempty"`
	Error   interface{} `json:"error,omitempty"`
}

type mcpContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type mcpToolResult struct {
	Content []mcpContent `json:"content"`
}

func (s *Server) handleMCP(w http.ResponseWriter, r *http.Request) {
	// Localhost-only: also rejects tunnel-forwarded requests, which would
	// otherwise look like loopback traffic.
	if !s.localOnly(w, r) {
		return
	}

	// POST only
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	var req jsonrpcRequest
	if err := json.Unmarshal(body, &req); err != nil {
		s.mcpError(w, nil, -32700, "parse error")
		return
	}

	switch req.Method {
	case "initialize":
		s.mcpInitialize(w, &req)
	case "notifications/initialized":
		w.WriteHeader(http.StatusNoContent)
	case "tools/list":
		s.mcpToolsList(w, &req)
	case "tools/call":
		s.mcpToolsCall(w, &req)
	default:
		s.mcpError(w, req.ID, -32601, "method not found")
	}
}

func (s *Server) mcpInitialize(w http.ResponseWriter, req *jsonrpcRequest) {
	result := map[string]interface{}{
		"protocolVersion": "2024-11-05",
		"capabilities": map[string]interface{}{
			"tools": map[string]interface{}{},
		},
		"serverInfo": map[string]interface{}{
			"name":    "claude-remote",
			"version": version,
		},
	}
	s.mcpReply(w, req.ID, result)
}

func (s *Server) mcpToolsList(w http.ResponseWriter, req *jsonrpcRequest) {
	tools := []map[string]interface{}{
		{
			"name":        "handoff",
			"description": "Generate a handoff URL and QR code for connecting from a mobile device",
			"inputSchema": map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"dir": map[string]interface{}{
						"type":        "string",
						"description": "Working directory for the session",
					},
					"mode": map[string]interface{}{
						"type":        "string",
						"description": "Handoff mode: choose (ask on phone), attach (join running session), or continue (resume last session)",
						"enum":        []string{"choose", "attach", "continue"},
						"default":     "choose",
					},
				},
				"required": []string{"dir"},
			},
		},
		{
			"name":        "status",
			"description": "Get the current status of the claude-remote server and terminal session",
			"inputSchema": map[string]interface{}{
				"type":       "object",
				"properties": map[string]interface{}{},
			},
		},
	}
	s.mcpReply(w, req.ID, map[string]interface{}{"tools": tools})
}

func (s *Server) mcpToolsCall(w http.ResponseWriter, req *jsonrpcRequest) {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		s.mcpError(w, req.ID, -32602, "invalid params")
		return
	}

	switch params.Name {
	case "handoff":
		s.mcpToolHandoff(w, req.ID, params.Arguments)
	case "status":
		s.mcpToolStatus(w, req.ID)
	default:
		s.mcpError(w, req.ID, -32602, fmt.Sprintf("unknown tool: %s", params.Name))
	}
}

func (s *Server) mcpToolHandoff(w http.ResponseWriter, id interface{}, args json.RawMessage) {
	var a struct {
		Dir  string `json:"dir"`
		Mode string `json:"mode"`
	}
	if args != nil {
		if err := json.Unmarshal(args, &a); err != nil {
			s.mcpError(w, id, -32602, "invalid arguments")
			return
		}
	}
	switch a.Mode {
	case "attach", "continue", "choose":
	case "":
		a.Mode = "choose"
	default:
		s.mcpError(w, id, -32602, fmt.Sprintf("invalid mode %q (want attach, continue or choose)", a.Mode))
		return
	}

	home, _ := os.UserHomeDir()
	dir := expandHome(strings.TrimSpace(a.Dir))
	if dir == "" {
		dir = home
	}
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	if !withinAllowed(dir, s.config.AllowedDirs) {
		s.mcpError(w, id, -32602, fmt.Sprintf("directory not allowed: %s", dir))
		return
	}

	token := s.auth.GenerateHandoffToken()
	if token == "" {
		s.mcpError(w, id, -32603, "failed to generate handoff token")
		return
	}

	// PublicBase resolves to the live tunnel URL when one is up, and falls
	// back to Tailscale/LAN otherwise.
	base := s.PublicBase()
	if state, _, lastErr := s.tunnel.Status(); s.config.Tunnel.Enabled() && state != "up" {
		msg := "tunnel is not up (state: " + state + ")"
		if lastErr != "" {
			msg += ": " + lastErr
		}
		s.mcpError(w, id, -32603, msg+" — run 'claude-remote doctor' on the Mac")
		return
	}

	// Query values must be escaped: paths contain spaces and "&".
	query := neturl.Values{"token": {token}, "dir": {dir}, "mode": {a.Mode}}
	url := base + "/handoff?" + query.Encode()

	qr, err := qrcode.New(url, qrcode.Medium)
	var qrStr string
	if err == nil {
		qrStr = qr.ToSmallString(false)
	}

	var sb strings.Builder
	if qrStr != "" {
		sb.WriteString(qrStr)
		sb.WriteString("\n")
	}
	sb.WriteString(url)
	fmt.Fprintf(&sb, "\n\nDirectory: %s\nMode: %s\nToken expires in %s.", dir, a.Mode, handoffTokenTTL)

	result := mcpToolResult{
		Content: []mcpContent{{Type: "text", Text: sb.String()}},
	}
	s.mcpReply(w, id, result)
}

func (s *Server) mcpToolStatus(w http.ResponseWriter, id interface{}) {
	s.terminal.mu.Lock()
	running := s.terminal.running
	dir := s.terminal.dir
	numClients := len(s.terminal.clients)
	s.terminal.mu.Unlock()
	if dir == "" {
		dir = "(none)"
	}

	tunnelState, tunnelURL, tunnelErr := s.tunnel.Status()
	text := fmt.Sprintf("claude-remote v%s\nSession running: %v\nDirectory: %s\nConnected clients: %d\nTunnel: %s (%s)\nPhone URL: %s",
		version, running, dir, numClients, s.config.Tunnel.Mode, tunnelState, s.PublicBase())
	if tunnelURL == "" && tunnelErr != "" {
		text += "\nTunnel error: " + tunnelErr
	}

	result := mcpToolResult{
		Content: []mcpContent{{Type: "text", Text: text}},
	}
	s.mcpReply(w, id, result)
}

func (s *Server) mcpReply(w http.ResponseWriter, id interface{}, result interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(jsonrpcResponse{
		JSONRPC: "2.0",
		ID:      id,
		Result:  result,
	})
}

func (s *Server) mcpError(w http.ResponseWriter, id interface{}, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(jsonrpcResponse{
		JSONRPC: "2.0",
		ID:      id,
		Error: map[string]interface{}{
			"code":    code,
			"message": msg,
		},
	})
}
