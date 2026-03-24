# Claude Remote — Design Spec

**Date:** 2026-03-24
**Status:** Approved
**Scope:** Personal tool — single user, single Mac

## Purpose

Control Claude Code from a phone browser over the internet. Mac runs a Go server that spawns Claude CLI in a pseudo-terminal and exposes it via WebSocket. A file browser lets you navigate the Mac filesystem. Auth via QR code scan (one-time), persistent JWT cookie for subsequent visits. Auto-starts on Mac boot via launchd. Internet access via Tailscale VPN mesh.

## Architecture

```
Mac (launchd auto-start)
┌──────────────────────────────┐
│  claude-remote (Go binary)   │
│  ├── /auth/qr    → QR code  │
│  ├── /auth/scan  → verify   │
│  ├── /ws/term    → WebSocket │
│  │    └── pty → claude CLI   │
│  ├── /api/files  → browse   │
│  └── static/     → web UI   │
│       ├── terminal (xterm.js)│
│       └── file browser       │
└──────────────────────────────┘
        │ Tailscale (100.x.x.x)
        ▼
┌──────────────────┐
│  Phone browser   │
│  https://100.x.x.x:8822
└──────────────────┘
```

- 1 Go binary, ~1000 lines total
- 1 port (8822), Tailscale provides encrypted transport
- launchd plist for auto-start + auto-restart on crash
- Web UI: static HTML/JS/CSS, no build step

## Auth Flow

### First time (QR scan)

1. Run `claude-remote setup` on Mac terminal
2. Generates random 64-char token, displays QR code in terminal
3. QR contains: `https://100.x.x.x:8822/auth/scan?token=<64-char>`
4. Phone scans QR → opens URL → server verifies token (one-time use)
5. Server returns JWT cookie (httpOnly, secure, 90-day expiry)
6. Redirect to main page

### Subsequent visits

Phone opens `https://100.x.x.x:8822` → JWT cookie valid → straight in.
JWT expired → shows "Scan QR again" message.

### Security details

- One-time token: invalidated after scan, prevents replay
- JWT payload: `{device_id, issued_at, expires_at}`
- Signed with random secret generated at setup, stored in `~/.claude-remote/secret.key`
- Revoke all sessions: `claude-remote revoke` → regenerates secret → all JWTs invalid
- No username/password

### Config directory

```
~/.claude-remote/
├── secret.key      # JWT signing secret (256-bit random)
├── config.json     # port, allowed directories
└── sessions.json   # active device list (informational)
```

## Terminal (Claude Code Remote)

### WebSocket protocol

```
Phone (xterm.js) ── WebSocket ── Go server ── pty ── claude CLI
  keypress → binary frame    → ptmx.Write()
  render   ← binary frame    ← ptmx.Read()
```

### Behavior

- Spawns `claude` CLI in a pseudo-terminal (creack/pty)
- 1 session at a time — new connection attaches to existing session
- If no session exists → spawns new `claude` process
- Permission prompts appear inline — user taps Y/N on phone
- Resize: phone sends terminal dimensions on orientation change → server resizes pty
- Reconnect: WebSocket drops → auto-reconnect → reattach (10k line output buffer)
- Session ends when Claude exits → next connection spawns fresh session

## File Browser

### API

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/api/files?path=<dir>` | GET | List directory entries |
| `/api/files/read?path=<file>` | GET | Read text file content |

### Response format

```json
{
  "path": "/Users/dat/Desktop",
  "parent": "/Users/dat",
  "entries": [
    {"name": "project", "type": "dir", "size": 0, "modified": "2026-03-24T10:00:00Z"},
    {"name": "notes.txt", "type": "file", "size": 1234, "modified": "2026-03-23T15:00:00Z"}
  ]
}
```

### Security

- Allowlist: only browse within configured directories (default: `$HOME`)
- Block sensitive paths: `.ssh`, `.env*`, `.claude-remote/`, `.gnupg`, `.aws`, `.config/gcloud`
- Read-only — no write, delete, or upload
- Path traversal protection: reject any path containing `..`
- Symlink: resolve and check against allowlist

## Web UI

Single HTML page with 2 bottom tabs, optimized for mobile.

```
┌─────────────────────────────┐
│ 🟢 Claude Remote    [status]│
├─────────────────────────────┤
│                             │
│   (tab content area)        │
│                             │
├─────────────────────────────┤
│  [ Terminal ]  [ Files ]    │
└─────────────────────────────┘
```

### Terminal tab
- Full-screen xterm.js, mobile-optimized (touch scroll, soft keyboard)
- Claude output with full ANSI color support
- Permission prompts inline — tap to respond

### Files tab
- List view: icon + name + size + date
- Tap folder → navigate in, breadcrumb for back
- Tap file → modal with content + syntax highlighting (highlight.js or plain)
- "Open in Claude" button → switches to terminal tab, sends file path as context

### Tech
- Vanilla HTML/JS/CSS — no framework, no build step
- xterm.js + xterm-addon-fit + xterm-addon-web-links (from CDN)
- Responsive: works on phone portrait/landscape

## CLI Commands

```bash
claude-remote setup      # Generate secret + QR code for first-time auth
claude-remote serve      # Start server (foreground, used by launchd)
claude-remote revoke     # Regenerate secret, invalidate all sessions
claude-remote install    # Install launchd plist + load
claude-remote uninstall  # Unload + remove launchd plist
claude-remote status     # Show running state, port, connected devices
```

## Auto-start (launchd)

```xml
<!-- ~/Library/LaunchAgents/com.claude-remote.plist -->
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>com.claude-remote</string>
  <key>ProgramArguments</key>
  <array>
    <string>/usr/local/bin/claude-remote</string>
    <string>serve</string>
  </array>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
  <key>StandardOutPath</key>
  <string>/Users/datducnguyenhuu/.claude-remote/server.log</string>
  <key>StandardErrorPath</key>
  <string>/Users/datducnguyenhuu/.claude-remote/server.log</string>
  <key>EnvironmentVariables</key>
  <dict>
    <key>PATH</key>
    <string>/usr/local/bin:/opt/homebrew/bin:/usr/bin:/bin</string>
  </dict>
</dict>
</plist>
```

- Mac boot → service starts automatically
- Crash → auto-restart (KeepAlive)
- `claude-remote install` copies plist + `launchctl load`
- `claude-remote uninstall` does `launchctl unload` + removes plist

## Dependencies

| Library | Purpose |
|---------|---------|
| gorilla/websocket | WebSocket server |
| creack/pty | Pseudo-terminal for Claude CLI |
| golang-jwt/jwt/v5 | JWT generation + verification |
| skip2/go-qrcode | QR code generation in terminal |

External: Tailscale (installed separately, free tier).

## Network Setup

1. Install Tailscale on Mac + phone
2. Enable MagicDNS in Tailscale admin console
3. Run `tailscale cert <hostname>.ts.net` on Mac → generates TLS cert
4. Server uses TLS cert for HTTPS (required for secure cookies)
5. QR code uses MagicDNS name: `https://<hostname>.ts.net:8822/auth/scan?token=...`
6. Server binds to `0.0.0.0:8822` with TLS
7. Tailscale WireGuard encrypts transport; TLS cert enables browser HTTPS + secure cookies

## Project Structure

```
claude-remote/
├── main.go              # CLI entry point (setup/serve/revoke/install/uninstall/status)
├── server.go            # HTTP server, routes, middleware
├── auth.go              # QR generation, token verification, JWT
├── terminal.go          # WebSocket + pty management
├── files.go             # File browser API
├── tls.go               # Tailscale cert loading
├── config.go            # Config loading/saving
├── static/
│   ├── index.html       # Single page app (terminal + file browser)
│   ├── app.js           # Tab switching, WebSocket, file browser logic
│   └── style.css        # Mobile-first styles
├── launchd/
│   └── com.claude-remote.plist
├── go.mod
├── go.sum
├── Makefile
└── docs/
    └── 2026-03-24-claude-remote-design.md
```

## Non-goals

- Multi-user support
- File editing/upload/delete
- Built-in encryption (Tailscale handles transport)
- Mobile native app (web-only)
- Windows/Linux support (macOS launchd only)
