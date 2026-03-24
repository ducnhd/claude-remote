---
name: architecture
description: System architecture — Go server wrapping Claude Code CLI via pty, exposed over WebSocket with file browser, Tailscale networking, QR+JWT auth
---

# Architecture

## How It Works

Claude Remote is a Go HTTP server that:

1. **Spawns Claude Code CLI** in a pseudo-terminal (pty)
2. **Pipes pty I/O over WebSocket** to a phone browser running xterm.js
3. **Serves a file browser API** for navigating Mac filesystem (read-only)
4. **Authenticates via QR code** — one-time scan sets a JWT cookie

## Components

### Terminal (terminal.go)
- Uses `creack/pty` to spawn `claude` in a pseudo-terminal
- Single session model: one claude process at a time
- New WebSocket connections attach to existing session
- 10k line output ring buffer for reconnection
- Handles terminal resize (phone rotation)
- When claude exits → session cleaned up → next connection spawns fresh

### File Browser (files.go)
- `GET /api/files?path=<dir>` — list directory
- `GET /api/files/read?path=<file>` — read text file
- Allowlist-based: only configured directories accessible
- Blocks sensitive paths: `.ssh`, `.env*`, `.claude-remote/`, `.gnupg`, `.aws`
- Path traversal protection: reject `..`
- Symlinks resolved and checked against allowlist
- Read-only: no write, delete, or upload

### Auth (auth.go)
- `claude-remote setup` generates 256-bit random secret + one-time 64-char token
- QR code contains `https://<tailscale-host>:8822/auth/scan?token=<token>`
- Token is single-use — invalidated after first scan
- Server issues JWT cookie (httpOnly, secure, 90-day expiry)
- JWT payload: `{device_id, issued_at, expires_at}`
- `claude-remote revoke` regenerates secret → all JWTs invalid

### Server (server.go)
- stdlib `net/http.ServeMux` — no framework
- JWT middleware on all routes except `/auth/*`
- TLS via Tailscale cert (`tailscale cert <hostname>`)
- Routes: `/auth/*`, `/ws/term`, `/api/files`, `/api/files/read`, `/*` (static)

### Config (config.go)
- Stored in `~/.claude-remote/`
- `config.json`: port, allowed dirs, claude binary path
- `secret.key`: JWT signing secret
- `sessions.json`: active device tracking

## Network Model

```
Phone ──Tailscale WireGuard──► Mac:8822 (TLS)
```

- Tailscale provides encrypted mesh VPN
- MagicDNS gives stable hostname (e.g., `macbook.tail-net.ts.net`)
- `tailscale cert` provides TLS cert for HTTPS
- No port forwarding, no public exposure
