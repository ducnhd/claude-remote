# CLAUDE.md

## Project Overview

**Claude Remote** — a lightweight Go server that lets you control Claude Code CLI from your phone browser, anywhere over the internet.

**Core idea**: Mac runs a Go server that wraps Claude Code in a pseudo-terminal, exposes it via WebSocket, and provides a file browser. Phone connects through Tailscale VPN mesh. Auth via one-time QR code scan, persistent JWT cookie. Auto-starts on Mac boot via launchd.

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
claude-remote status     # Show running state, port, connected devices
```

## Architecture

```
Go binary (single process)
├── main.go        # CLI entry point (setup/serve/revoke/install/uninstall/status)
├── server.go      # HTTP server, routes, JWT middleware
├── auth.go        # QR generation, token verify, JWT issue/validate
├── terminal.go    # WebSocket + pty: spawn claude, pipe stdin/stdout
├── files.go       # File browser API (read-only, allowlist)
├── tls.go         # Tailscale cert loading
├── config.go      # Config loading/saving (~/.claude-remote/)
├── static/        # Web UI (vanilla HTML/JS/CSS, no build step)
│   ├── index.html # SPA: terminal tab + file browser tab
│   ├── app.js     # WebSocket, xterm.js, file browser logic
│   └── style.css  # Mobile-first styles
└── launchd/
    └── com.claude-remote.plist
```

### Data Flow

```
Phone browser (xterm.js)
  ↕ WebSocket (wss:// via Tailscale HTTPS)
Go server
  ↕ pty (pseudo-terminal)
claude CLI
```

### Network

Tailscale VPN mesh — Mac + phone on same tailnet. MagicDNS + `tailscale cert` for HTTPS. Server binds `0.0.0.0:8822` with TLS.

### Auth

- First time: `claude-remote setup` → QR code in terminal → phone scans → JWT cookie (90 days)
- Subsequent: cookie auto-authenticates
- Revoke: `claude-remote revoke` → new secret → all cookies invalid

### Config

```
~/.claude-remote/
├── secret.key      # JWT signing secret (256-bit)
├── config.json     # port, allowed directories, claude binary path
└── sessions.json   # active device list
```

## Code Conventions

- **Go 1.22+**, module name `claude-remote`
- **Short receivers**: `(s *Server)`, `(a *Auth)`, `(t *Terminal)`
- **Error wrapping**: `fmt.Errorf("context: %w", err)`
- **No frameworks**: stdlib `net/http` + minimal deps
- **Frontend**: Vanilla JS, no build step. xterm.js from CDN.
- **Security-first file browser**: allowlist dirs, block dotfiles, no write ops, reject `..`

## Dependencies

| Library | Purpose |
|---------|---------|
| gorilla/websocket | WebSocket server |
| creack/pty | Pseudo-terminal for claude CLI |
| golang-jwt/jwt/v5 | JWT auth |
| skip2/go-qrcode | QR code in terminal |

External: Tailscale (installed separately).

## Testing

```bash
go test ./... -v -count=1
```

- Unit tests for auth (JWT sign/verify, token generation)
- Unit tests for file browser (path validation, allowlist, dotfile blocking)
- Integration test for WebSocket + pty (spawn echo command, verify output)
- Table-driven test patterns

## Deployment

Local Mac only. `make install` copies binary to `/usr/local/bin/claude-remote` and loads launchd plist. Service auto-starts on boot, auto-restarts on crash.
