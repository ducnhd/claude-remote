# MCP Handoff — Continue Claude Sessions on Phone

**Date**: 2026-03-31
**Status**: Approved

## Problem

User works in Claude Code (terminal/VS Code/Cursor) on their Mac. When they need to leave, there's no way to continue the same conversation on their phone. They must start a new session and lose context.

## Solution

Add MCP server capability to claude-remote so Claude Code can call a `handoff` tool that generates a QR code. User scans QR with phone, phone opens claude-remote web UI and either attaches to the running session or starts a new session with `claude --continue` to preserve conversation history.

## Constraints

- All existing functionality (folder picker, direct web UI access, WebSocket chat) must remain unchanged
- New features only activate via new routes/endpoints and URL params
- claude-remote remains a single Go binary, single service, single port (8822)

## Architecture

```
claude-remote (launchd service, port 8822)
├── Web UI layer (EXISTING — unchanged)
│   ├── /ws/term          ← phone WebSocket
│   ├── /api/claude/start ← start session
│   ├── /api/claude/status← check session
│   ├── /api/files        ← folder browser
│   └── /*                ← static files (index.html, app.js, style.css)
│
├── MCP layer (NEW)
│   └── /mcp              ← MCP Streamable HTTP (JSON-RPC)
│       ├── tool: handoff  → generates QR URL for phone handoff
│       └── tool: status   → current session info
│
├── Handoff layer (NEW)
│   └── /handoff          ← QR token exchange + redirect with params
│
└── Auth layer (EXISTING — extended)
    ├── /auth/scan        ← existing QR token exchange
    ├── /handoff          ← new handoff token exchange
    └── JWT middleware     ← protects web UI routes (unchanged)
```

## MCP Protocol

MCP Streamable HTTP at `/mcp`. Handles JSON-RPC request/response only (no streaming needed).

Security: `/mcp` is NOT behind JWT middleware. Instead, it only accepts connections from localhost (127.0.0.1 / ::1). This is safe because Claude Code runs on the same machine.

### Messages handled

```
initialize        → server capabilities (tools support)
notifications/initialized → no-op acknowledgment
tools/list        → returns [handoff, status]
tools/call        → executes tool, returns result
```

### Tool: `handoff`

**Input:**
- `dir` (string, required): Working directory path
- `mode` (string, optional): "attach" | "continue" | "choose" (default: "choose")

**Behavior:**
1. Detect Tailscale hostname (or fallback to localhost)
2. Detect TLS availability (choose https/http)
3. Generate one-time handoff token (expires 5 minutes)
4. Build URL: `{proto}://{hostname}:8822/handoff?token={token}&dir={dir}&mode={mode}`
5. Render QR as ASCII art using skip2/go-qrcode (already a dependency)
6. Return text content: QR art + clickable URL

**Output:**
```json
{
  "content": [{
    "type": "text",
    "text": "█████████████\n█████████████\n...\n\nScan QR or open:\nhttps://mac.tail123.ts.net:8822/handoff?token=abc&dir=/path&mode=choose\n\nToken expires in 5 minutes."
  }]
}
```

### Tool: `status`

**Input:** none

**Output:**
```json
{
  "content": [{
    "type": "text",
    "text": "claude-remote v0.1.0\nSession: running\nDir: /Users/user/project\nClients: 1 connected"
  }]
}
```

## Handoff Flow

```
1. User types /remote in Claude Code
2. Skill instructs Claude to call `handoff` MCP tool with current dir
3. Claude Code → POST http://localhost:8822/mcp (JSON-RPC tools/call)
4. Server generates token + QR, returns to Claude
5. Claude displays QR in terminal
6. User scans QR with phone camera
7. Phone opens: https://hostname:8822/handoff?token=xxx&dir=/path&mode=choose
8. Server validates token (one-time, < 5min old)
9. Server sets JWT cookie on phone (if not already authed)
10. Server redirects to: /?dir=/path&mode=choose
11. Web UI detects URL params:
    - If mode=choose → show mode selector screen
    - If mode=attach → skip to chat, connect WebSocket to running session
    - If mode=continue → start `claude --continue` in dir, then chat
12. User picks mode (if choose) → session starts on phone
```

## File Changes

### New file: `mcp.go`

MCP Streamable HTTP handler. Contains:
- `handleMCP(w, r)` — main handler, parses JSON-RPC, routes to method handlers
- `mcpInitialize()` — returns server info + capabilities
- `mcpToolsList()` — returns tool definitions
- `mcpToolsCall(name, args)` — dispatches to tool implementations
- `toolHandoff(dir, mode)` — generates token, QR, URL
- `toolStatus()` — returns service/session info
- Localhost-only check middleware

### Modified: `server.go`

- Add route: `s.mux.HandleFunc("/mcp", s.handleMCP)` (before protected mux, no JWT)
- Add route: `s.mux.HandleFunc("/handoff", s.handleHandoff)` (no JWT — token-based auth)
- Add `handleHandoff(w, r)`: validate handoff token → set JWT cookie → redirect with params
- Expose `detectTailscaleHost()` and TLS detection to `mcp.go` (move from main.go or make Server methods)

### Modified: `terminal.go`

- Add field `dir string` to `TerminalManager` — set in `StartInDir`, returned by status
- Add method `StartWithResume(dir string) error` — spawns `claude --continue` in dir
- `StartInDir` unchanged — still used by existing `/api/claude/start`

### Modified: `auth.go`

- Add `GenerateHandoffToken() string` — creates short-lived token (5 min expiry)
- Add `ValidateHandoffToken(token string) bool` — validates + consumes token
- Internal: handoff tokens stored in memory map with expiry timestamps

### Modified: `main.go`

- Move `detectTailscaleHost()` to `server.go` as `(s *Server)` method (or standalone in a shared file)
- No other changes to CLI commands

### Modified: `static/app.js`

- Add URL param detection at init: `new URLSearchParams(location.search)`
- If `dir` + `mode` params present → call `showHandoffScreen(dir, mode)` instead of normal init
- `showHandoffScreen()`:
  - If mode=attach AND session running → go straight to chat screen, connect WS
  - If mode=continue → POST `/api/claude/start` with `{dir, resume: true}` → chat screen
  - If mode=choose → show new mode selector screen
- New function `showModeSelector(dir)` — renders the 3 buttons (attach/continue/folder picker)
- Existing `initPicker()` and auto-attach logic unchanged

### Modified: `static/index.html`

- Add new screen div `#screen-handoff` with mode selector UI
- Existing screens `#screen-picker` and `#screen-chat` unchanged

### Modified: `server.go` — `handleClaudeStart`

- Accept optional `resume` field in POST body: `{"dir": "/path", "resume": true}`
- If `resume: true` → call `tm.StartWithResume(dir)` instead of `tm.StartInDir(dir)`
- If `resume: false` or missing → existing behavior unchanged

### New file: `skills/remote.md` (Claude Code skill)

```markdown
When user types /remote:
1. Call the `status` MCP tool from claude-remote to check service
2. Call the `handoff` MCP tool with dir set to current working directory
3. Display the QR code result to the user
```

## Installation

After building, user registers MCP once:

```bash
# Build + install service (existing)
make install

# Register MCP with Claude Code (new, one-time)
claude mcp add --transport http claude-remote http://localhost:8822/mcp
```

The skill file goes into the project's `.claude/skills/` or user's global skills directory.

## Testing

- `mcp_test.go`: JSON-RPC parsing, tool dispatch, localhost check, handoff token generation
- Manual: `/remote` in Claude Code → QR appears → scan → phone connects
- Existing tests unchanged: `auth_test.go`, `terminal_test.go`, `server_test.go`, etc.
