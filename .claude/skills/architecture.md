---
name: architecture
description: System architecture — Go server with chat UI, hidden xterm.js for ANSI processing, native mobile scroll, folder picker, Vietnamese IME support
---

# Architecture

## How It Works

Claude Remote is a Go HTTP server that:

1. **Serves a folder picker UI** — user browses Desktop/Downloads/Documents, selects working directory
2. **Spawns Claude Code CLI** in a pseudo-terminal (pty) in the chosen directory
3. **Pipes pty I/O over WebSocket** to a phone browser
4. **Renders output via hidden xterm.js** — ANSI processed offscreen, colored HTML extracted, displayed in native-scrolling `<pre>`
5. **Accepts input via native `<textarea>`** — supports Vietnamese IME, sends text + `\r` to pty
6. **Authenticates via QR code** — one-time scan sets a JWT cookie (90 days)

## Components

### Terminal (terminal.go)
- Uses `creack/pty` to spawn `claude` in a pseudo-terminal
- `StartInDir(dir)` — starts Claude in user-selected directory
- Single session model: `Stop()` + `StartInDir()` for new sessions
- `RingBuffer` (64KB) for reconnection replay; cleared on new session
- WebSocket handler: broadcasts pty output to all connected clients
- Handles terminal resize via JSON control messages `{"type":"resize","rows":N,"cols":N}`

### Web UI (static/)
- **Screen 1: Folder Picker** — quick-access Desktop/Downloads/Documents buttons, browse subdirectories, "Start Claude" starts session
- **Screen 2: Chat UI** — output area + quick actions + text input
- **Hidden xterm.js** — processes ANSI escape sequences offscreen; `serializeBuffer()` extracts colored HTML per-cell (fg/bg color, bold, italic, underline, dim)
- **Native scroll** — `<pre>` in `overflow-y:auto` div with `-webkit-overflow-scrolling:touch`
- **Dynamic cols** — terminal width calculated from phone screen width (not fixed 80)
- **Vietnamese IME** — `compositionstart`/`compositionend` tracking, `isComposing` guard on send
- **Quick actions** — Enter/Accept(y)/Reject(n)/Esc/Ctrl+C buttons with `parseKey()` for HTML→real char conversion
- **Auto-reconnect** — checks `/api/claude/status` on load, reconnects to running session

### File Browser (files.go)
- `GET /api/files?path=<dir>` — list directory (supports `~/` expansion)
- `GET /api/files/read?path=<file>` — read text file (max 1MB)
- Allowlist-based: only configured directories accessible
- Blocks: `.ssh`, `.env*`, `.claude-remote/`, `.gnupg`, `.aws`, `.config/gcloud`, `.docker`, `.kube`
- Path traversal protection: reject `..`, resolve symlinks, check against allowlist
- Read-only: no write, delete, or upload

### Auth (auth.go)
- `setup` generates 256-bit secret + one-time 64-char token
- QR contains `https://<tailscale-host>:8822/auth/scan?token=<token>`
- Token single-use — consumed on first scan
- JWT cookie: httpOnly, secure (conditional on TLS), SameSite (Strict/Lax), 90 days
- Middleware on all routes except `/auth/*` and `/health`

### Server (server.go)
- stdlib `net/http.ServeMux`
- Routes: `/auth/scan`, `/health`, `/api/claude/start`, `/api/claude/status`, `/api/files`, `/api/files/read`, `/ws/term`, `/*` (static)
- TLS auto-discovery: searches `~/.claude-remote/`, `~/Desktop`, `/var/run/tailscale`, `~/.local/share/tailscale/certs` for `*.crt` + `*.key`
- Static file serving with fallback: `execDir()/static/` → `cwd/static/` (for `go run`)
- `/api/claude/start` validates directory against allowlist before spawning

### Config (config.go)
- Stored in `~/.claude-remote/`
- `config.json`: port (default 8822), allowed dirs (default $HOME), claude binary path
- `secret.key`: JWT signing secret
- `sessions.json`: active device tracking

## Network Model

```
Phone ──Tailscale WireGuard──► Mac:8822 (TLS)
```

- Tailscale encrypted mesh VPN, MagicDNS hostname
- `tailscale cert` for HTTPS
- No port forwarding, no public exposure
- Falls back to HTTP with warning if no certs found
