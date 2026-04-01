# CLAUDE.md

## Project Overview

**Claude Remote** — a lightweight Go server that lets you control Claude Code CLI from your phone browser, anywhere over the internet. Also acts as an MCP server so Claude Code can trigger phone handoff via `/handoff`.

**Core idea**: Mac runs a Go server that wraps Claude Code in a pseudo-terminal, exposes it via WebSocket, and provides a mobile-friendly chat UI. Phone connects through Tailscale VPN mesh. Claude Code connects via MCP (localhost HTTP) to generate handoff QR codes. Auth via one-time QR code scan, persistent JWT cookie. Auto-starts on Mac boot via launchd.

**Target**: Single user, single Mac. Personal tool, not a product.

## Build & Run

```bash
make build          # Build binary
make run            # Build + run locally
make test           # Run all tests
make install        # Build + install to /usr/local/bin + load launchd
make uninstall      # Unload launchd + remove binary
```

### CLI Commands

```bash
claude-remote setup      # Generate secret + show QR code
claude-remote serve      # Start server (foreground)
claude-remote revoke     # Regenerate secret, invalidate all sessions
claude-remote install    # Install launchd plist + load
claude-remote uninstall  # Unload + remove launchd plist
claude-remote status     # Show running state, port, Tailscale hostname
```

### MCP Registration (one-time)

```bash
claude mcp add --transport http -s user claude-remote http://127.0.0.1:8823/mcp
```

Note: MCP uses port 8823 (HTTP, localhost only). The main web UI uses port 8822 (HTTPS via Tailscale). When TLS is active, the server runs two listeners — HTTPS on 8822 for phone access, HTTP on 8823 for local MCP connections.

### Skill

| Command | Description |
|---------|-------------|
| `/handoff` | Generate QR to continue session on phone |

## Architecture

```
Go binary (single process, two listeners when TLS active)
├── main.go        # CLI entry point (setup/serve/revoke/install/uninstall/status)
├── server.go      # HTTP/HTTPS server, routes, JWT middleware, handoff endpoint,
│                  #   detectTailscaleHost, detectProto, dual-listener setup
├── mcp.go         # MCP Streamable HTTP handler (JSON-RPC), handoff + status tools
├── auth.go        # QR generation, token verify, JWT issue/validate, middleware,
│                  #   handoff tokens (short-lived, one-time, 5min expiry)
├── terminal.go    # WebSocket + pty: spawn claude in chosen dir or with --continue,
│                  #   pipe stdin/stdout, ring buffer, multi-client broadcast
├── files.go       # File browser API (read-only, allowlist, ~ expansion)
├── config.go      # Config loading/saving (~/.claude-remote/)
├── static/        # Web UI (vanilla HTML/JS/CSS, no build step)
│   ├── index.html # Three screens: folder picker → handoff selector → chat UI
│   ├── app.js     # WebSocket, hidden xterm.js, native scroll, Vietnamese IME,
│   │              #   handoff URL param detection, mode selector logic
│   └── style.css  # Mobile-first dark theme
├── .claude/
│   └── skills/
│       └── remote.md  # /handoff skill — calls MCP handoff tool
└── launchd/
    └── com.claude-remote.plist
```

### Web UI Flow

```
Flow A: Direct access (no URL params) — EXISTING
  Screen 1: Folder Picker
  ├── Quick dirs: Desktop / Downloads / Documents
  ├── Browse subdirectories
  └── "Bắt đầu Claude" → POST /api/claude/start {dir}
  ↓
  Screen 2: Chat UI (auto-attach if session already running)

Flow B: Handoff (URL params ?dir=...&mode=...) — NEW
  /handoff?token=xxx → validate → set cookie → redirect /?dir=...&mode=...
  ↓
  Screen 3: Handoff Mode Selector (if mode=choose)
  ├── 🔗 Attach session — connect to running PTY
  ├── 📋 Continue session — start claude --continue
  └── 📁 Chọn thư mục khác — go to folder picker
  ↓
  Screen 2: Chat UI
```

### Data Flow

```
Phone browser (<textarea> → \r)
  ↕ WebSocket (wss:// via Tailscale HTTPS, port 8822)
Go server (terminal.go RingBuffer)
  ↕ pty (pseudo-terminal)
claude CLI (spawned in user-selected directory)
  ↕ output → hidden xterm.js → renderLine() → logical lines → native <pre>

Claude Code (MCP client)
  ↕ JSON-RPC over HTTP (port 8823, localhost only)
Go server (mcp.go)
  → tools: handoff (QR generation), status (session info)
```

### Key Design Decisions

- **Hidden xterm.js** — xterm.js runs offscreen to process ANSI escape sequences; output extracted with colors and rendered in native `<pre>` for smooth mobile scrolling (xterm.js touch scroll is unusable on mobile)
- **Cell-by-cell rendering** — `renderLine()` iterates xterm cells directly instead of using `translateToString` to avoid character/style misalignment; supports 256-color palette
- **Debounced sync (80ms)** — output syncs at 80ms intervals instead of every animation frame to reduce flicker during streaming
- **Logical line joining** — wrapped lines (soft wraps from terminal width) are joined into single logical lines for correct display at any screen width
- **Native `<textarea>` for input** — supports Vietnamese IME (Telex/VNI); xterm.js keyboard breaks mobile IME composition
- **`\r` not `\n`** — PTY expects carriage return for Enter, not line feed
- **Dynamic terminal cols** — calculated from phone screen width at runtime, not fixed 80 cols
- **Quick action buttons** — Claude Code needs single-key responses (y/n/Enter/Esc) impossible to type on mobile touch keyboard
- **Session isolation** — RingBuffer cleared on new session start, output div cleared on screen switch
- **Folder picker first** — user chooses working directory before Claude starts; Claude spawned with `cmd.Dir` set
- **Dual listener** — HTTPS on 8822 (Tailscale cert, all interfaces) for phone; HTTP on 8823 (localhost only) for MCP. Necessary because Claude Code MCP client can't use Tailscale certs
- **Handoff tokens** — short-lived (5 min), one-time tokens for QR scan auth. Separate from setup tokens (which are also one-time but stored differently)
- **MCP localhost-only** — `/mcp` endpoint rejects non-loopback IPs (no JWT needed for local connections)
- **TLS auto-detect** — searches ~/.claude-remote/, ~/Desktop, /var/run/tailscale for *.crt files; QR URL protocol matches; cookie Secure flag conditional on TLS

### Network

Tailscale VPN mesh — Mac + phone on same tailnet. MagicDNS for hostname. `tailscale cert` for HTTPS. Server binds `0.0.0.0:8822` (HTTPS) and `127.0.0.1:8823` (HTTP MCP). Falls back to single HTTP listener with warning if no TLS certs.

### Auth

- First time: `setup` → QR in terminal → phone scans → JWT cookie (90 days)
- Handoff: `/handoff` → MCP generates QR with handoff token → phone scans → JWT cookie
- Subsequent: cookie auto-authenticates
- Revoke: `revoke` → new secret → all cookies invalid
- Cookie: `Secure` + `SameSite=Strict` when TLS, `SameSite=Lax` when HTTP
- Handoff tokens: 5 min expiry, single-use, stored in memory

### Config

```
~/.claude-remote/
├── secret.key      # JWT signing secret (256-bit)
├── config.json     # port, allowed directories, claude binary path
├── sessions.json   # active device list
├── *.crt / *.key   # Tailscale TLS certificates
└── server.log      # stdout/stderr from launchd service
```

## API Endpoints

| Route | Method | Auth | Purpose |
|-------|--------|------|---------|
| `/auth/scan` | GET | No | QR token exchange → set JWT cookie → redirect / |
| `/health` | GET | No | Health check `{"status":"ok"}` |
| `/mcp` | POST | Localhost | MCP Streamable HTTP (JSON-RPC) — handoff + status tools |
| `/handoff` | GET | Token | Handoff token exchange → set JWT cookie → redirect with params |
| `/api/claude/start` | POST | Yes | Start Claude in directory `{"dir":"/path","resume":bool}` |
| `/api/claude/status` | GET | Yes | Check if Claude session running `{"running":bool}` |
| `/api/files` | GET | Yes | List directory (supports `~/` expansion) |
| `/api/files/read` | GET | Yes | Read file content (max 1MB) |
| `/ws/term` | WS | Yes | Terminal WebSocket (binary I/O to pty) |
| `/*` | GET | Yes | Static files (index.html, app.js, style.css) |

### MCP Tools

| Tool | Input | Description |
|------|-------|-------------|
| `handoff` | `{dir: string, mode?: "attach"\|"continue"\|"choose"}` | Generate QR code + URL for phone handoff |
| `status` | `{}` | Service status, running session info, connected client count |

## Code Conventions

- **Go 1.25+**, module name `claude-remote`
- **Short receivers**: `(s *Server)`, `(a *Auth)`, `(tm *TerminalManager)`
- **Error wrapping**: `fmt.Errorf("context: %w", err)`
- **No frameworks**: stdlib `net/http` + minimal deps
- **Frontend**: Vanilla JS, no build step. xterm.js from CDN (hidden, ANSI processing only).
- **Security-first file browser**: allowlist dirs, block dotfiles, no write ops, reject `..`, symlink resolution
- **Additive changes**: new features use new routes/screens/methods; existing behavior stays untouched

## Dependencies

| Library | Purpose |
|---------|---------|
| gorilla/websocket | WebSocket server |
| creack/pty | Pseudo-terminal for Claude CLI |
| golang-jwt/jwt/v5 | JWT auth |
| skip2/go-qrcode | QR code in terminal + MCP handoff |

External: Tailscale (installed separately), xterm.js + xterm-addon-fit (CDN).

## Testing

```bash
go test ./... -v -count=1       # All tests
go test -race ./... -v -count=1 # With race detector
```

Test files: `auth_test.go`, `config_test.go`, `files_test.go`, `terminal_test.go`, `server_test.go`, `mcp_test.go`, `integration_test.go`.

## Deployment

Local Mac only. `make install` copies binary + loads launchd plist. Auto-starts on boot.

```bash
# Tailscale HTTPS setup
sudo tailscale cert <hostname>.ts.net
sudo cp ~/Desktop/<hostname>.ts.net.* ~/.claude-remote/
sudo chown $(whoami) ~/.claude-remote/*.key

# Register MCP (one-time, user scope — persists across projects)
claude mcp add --transport http -s user claude-remote http://127.0.0.1:8823/mcp
```
