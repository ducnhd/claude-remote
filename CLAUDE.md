# CLAUDE.md

## Project Overview

**Claude Remote** — a lightweight Go server that lets you control Claude Code CLI from your phone browser, anywhere over the internet.

**Core idea**: Mac runs a Go server that wraps Claude Code in a pseudo-terminal, exposes it via WebSocket, and provides a mobile-friendly chat UI. Phone connects through Tailscale VPN mesh. Auth via one-time QR code scan, persistent JWT cookie. Auto-starts on Mac boot via launchd.

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
claude mcp add --transport http claude-remote http://localhost:8822/mcp
```

### Skill

| Command | Description |
|---------|-------------|
| `/remote` | Generate QR to continue session on phone |

## Architecture

```
Go binary (single process)
├── main.go        # CLI entry point (setup/serve/revoke/install/uninstall/status)
├── server.go      # HTTP server, routes, JWT middleware, /api/claude/start+status
├── auth.go        # QR generation, token verify, JWT issue/validate, middleware
├── terminal.go    # WebSocket + pty: spawn claude in chosen dir, pipe stdin/stdout, ring buffer
├── files.go       # File browser API (read-only, allowlist, ~ expansion)
├── config.go      # Config loading/saving (~/.claude-remote/)
├── static/        # Web UI (vanilla HTML/JS/CSS, no build step)
│   ├── index.html # Two screens: folder picker → chat UI
│   ├── app.js     # WebSocket, hidden xterm.js, native scroll, Vietnamese IME
│   └── style.css  # Mobile-first dark theme
└── launchd/
    └── com.claude-remote.plist
```

### Web UI Flow

```
Screen 1: Folder Picker
├── Quick dirs: Desktop / Downloads / Documents
├── Browse subdirectories
└── "Bắt đầu Claude" → POST /api/claude/start {dir}

Screen 2: Chat UI
├── Output area (native scroll, colored text from hidden xterm.js buffer)
├── Quick actions: Enter ↵ / ✓ Chấp nhận / ✗ Từ chối / Esc / Ctrl+C
├── Text input (<textarea> with Vietnamese IME support)
├── Gửi + ẩn bàn phím buttons
└── ← Back (stops session, clears output, returns to picker)
```

### Data Flow

```
Phone browser (<textarea> → \r)
  ↕ WebSocket (wss:// via Tailscale HTTPS)
Go server (terminal.go RingBuffer)
  ↕ pty (pseudo-terminal)
claude CLI (spawned in user-selected directory)
  ↕ output → hidden xterm.js → serializeBuffer() → native <pre>
```

### Key Design Decisions

- **Hidden xterm.js** — xterm.js runs offscreen to process ANSI escape sequences; output extracted with colors and rendered in native `<pre>` for smooth mobile scrolling (xterm.js touch scroll is unusable on mobile)
- **Native `<textarea>` for input** — supports Vietnamese IME (Telex/VNI); xterm.js keyboard breaks mobile IME composition
- **`\r` not `\n`** — PTY expects carriage return for Enter, not line feed
- **Dynamic terminal cols** — calculated from phone screen width at runtime, not fixed 80 cols
- **Quick action buttons** — Claude Code needs single-key responses (y/n/Enter/Esc) impossible to type on mobile touch keyboard
- **Session isolation** — RingBuffer cleared on new session start, output div cleared on screen switch
- **Folder picker first** — user chooses working directory before Claude starts; Claude spawned with `cmd.Dir` set
- **`compositionstart`/`compositionend`** — tracks Vietnamese IME state, prevents sending incomplete compositions
- **TLS auto-detect** — searches ~/.claude-remote/, ~/Desktop, /var/run/tailscale for *.crt files; QR URL protocol matches; cookie Secure flag conditional on TLS

### Network

Tailscale VPN mesh — Mac + phone on same tailnet. MagicDNS for hostname. `tailscale cert` for HTTPS. Server binds `0.0.0.0:8822`. Falls back to HTTP with warning if no TLS certs.

### Auth

- First time: `setup` → QR in terminal → phone scans → JWT cookie (90 days)
- Subsequent: cookie auto-authenticates
- Revoke: `revoke` → new secret → all cookies invalid
- Cookie: `Secure` + `SameSite=Strict` when TLS, `SameSite=Lax` when HTTP

### Config

```
~/.claude-remote/
├── secret.key      # JWT signing secret (256-bit)
├── config.json     # port, allowed directories, claude binary path
└── sessions.json   # active device list
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

## Code Conventions

- **Go 1.25+**, module name `claude-remote`
- **Short receivers**: `(s *Server)`, `(a *Auth)`, `(tm *TerminalManager)`
- **Error wrapping**: `fmt.Errorf("context: %w", err)`
- **No frameworks**: stdlib `net/http` + minimal deps
- **Frontend**: Vanilla JS, no build step. xterm.js from CDN (hidden, ANSI processing only).
- **Security-first file browser**: allowlist dirs, block dotfiles, no write ops, reject `..`, symlink resolution

## Dependencies

| Library | Purpose |
|---------|---------|
| gorilla/websocket | WebSocket server |
| creack/pty | Pseudo-terminal for Claude CLI |
| golang-jwt/jwt/v5 | JWT auth |
| skip2/go-qrcode | QR code in terminal |

External: Tailscale (installed separately), xterm.js + xterm-addon-fit (CDN).

## Testing

```bash
go test ./... -v -count=1       # All tests
go test -race ./... -v -count=1 # With race detector
```

Test files: `auth_test.go`, `config_test.go`, `files_test.go`, `terminal_test.go`, `server_test.go`, `integration_test.go`.

## Deployment

Local Mac only. `make install` copies binary + loads launchd plist. Auto-starts on boot.

```bash
# Tailscale HTTPS setup
sudo tailscale cert <hostname>.ts.net
# If key not readable, copy to config dir:
sudo cp ~/Desktop/<hostname>.ts.net.* ~/.claude-remote/
sudo chown $(whoami) ~/.claude-remote/*.key
```
