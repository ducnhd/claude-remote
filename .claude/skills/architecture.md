---
name: architecture
description: System architecture — Go server with Cloudflare tunnel, pty-wrapped Claude, chat UI with hidden xterm.js, VNC screen control, MCP handoff, Vietnamese IME support
---

# Architecture

## How It Works

Claude Remote is a single Go binary (`package main`) that:

1. **Supervises cloudflared** (`tunnel.go`) — quick tunnel by default, named tunnel or `off` (Tailscale/LAN) via config
2. **Serves a folder picker UI** — user browses Desktop/Downloads/Documents, selects working directory
3. **Spawns Claude Code CLI** in a pty in the chosen directory, with `--settings {"tui":"default"}` and `childEnv()`
4. **Pipes pty I/O over WebSocket** (`/ws/term`) to the phone
5. **Renders output via hidden xterm.js** — ANSI processed offscreen, colored HTML extracted into a native-scrolling `<pre>`
6. **Accepts input via native `<textarea>`** — Vietnamese IME, sends text + `\r`
7. **Bridges macOS Screen Sharing** (`/ws/vnc`) for full-desktop control from `screen.html`
8. **Acts as an MCP server** on loopback `:8823` so Claude Code can mint handoff QRs
9. **Authenticates via QR** — pairing/handoff token → 90-day rolling JWT cookie

## Listeners

| Listener | Bind | Carries |
|----------|------|---------|
| Public `:8822` | `127.0.0.1` with tunnel, `0.0.0.0` + TLS in mode `off` | static, `/auth/scan`, `/handoff`, `/api/*`, `/ws/*`, `/health` |
| Loopback `:8823` | always `127.0.0.1`, HTTP | `/mcp`, `/local/pair`, `/local/status`, `/health` |

Behind a tunnel every request comes from `127.0.0.1`, so loopback is not proof of a local caller — the loopback mux also rejects requests with proxy headers (`viaProxy`).

## Components

### Terminal (terminal.go)
- `creack/pty`; `StartInDir(dir)` / `StartWithResume(dir)` (adds `--continue` after `tm.args`)
- Single session; `gen` counter stops a dead session's goroutines from tearing down a restart
- `RingBuffer` (64KB) replayed on join; `outMu` makes append + broadcast + replay exclusive
- Per-client write mutex + 10s write deadline (gorilla panics on concurrent writes)
- Ping every 30s, drop clients silent for 90s (Cloudflare drops idle sockets)
- Resize via `{"type":"resize","rows":N,"cols":N}`
- `childEnv()` — fills `TERM`, `COLORTERM`, `LANG`, `HOME`, widens `PATH`. launchd gives none of these; without them the TUI ignores keystrokes

### VNC + clipboard (vnc.go)
- `/ws/vnc` — same-origin check, dials `127.0.0.1:5900`, raw byte pipe (noVNC speaks RFB). ARD login happens in the browser. 502 with a hint if Screen Sharing is off
- `/api/clipboard` — POST `application/json` only (forces CORS preflight), ≤64KB, runs `pbcopy` with `childEnv()` (no `LANG` → mangled UTF-8). Exists because Screen Sharing drops Unicode keysyms

### Server (server.go)
- stdlib `ServeMux`; `protect()` = JWT middleware with rolling refresh past 1/3 of lifetime
- `accessLog()` logs every public request; `statusRecorder.Hijack` keeps WS upgrades working
- `noStore()` sets `Cache-Control: no-cache` on static (mobile Safari cache)
- Static from `<binary dir>/static`, fallback `cwd/static`
- `/local/pair` returns 503 while the tunnel is not `up`
- TLS (mode off): searches `~/.claude-remote`, `~/Desktop`, `/var/run/tailscale`, `~/.local/share/tailscale/certs`; skips expired certs, warns 14 days before expiry

### Web UI (static/)
- `index.html` + `app.js`: folder picker → handoff mode selector → chat UI; 🖥 Screen link in header
- Hidden xterm.js, cell-by-cell `renderLine()`, 256 colors, 80ms debounce, logical line joining
- Quick actions: Enter / y / n / Esc / Ctrl+C
- `screen.html`: self-contained (inline CSS), noVNC 1.5.0 ESM from jsDelivr, key bar (⌨ live typing, Fit, sticky ⌘, Esc, Tab, Enter, ⌫, arrows), Paste / Paste ↵ via `/api/clipboard` + ⌘V

### File browser (files.go, paths.go)
- `GET /api/files`, `GET /api/files/read` (≤1MB), read-only
- Allowlist with component-wise `pathWithin()` (never string prefix), `~` expansion, symlink resolution, reject `..`
- Blocks `.ssh`, `.env*`, `.claude-remote`, `.gnupg`, `.aws`, and other credential dirs

### MCP (mcp.go)
- Streamable HTTP JSON-RPC; tools `handoff {dir, mode}` and `status`
- `dir` must be in `allowed_dirs`; mode `attach|continue|choose`; token TTL 15 min

### Other
- `tunnel.go` — spawns cloudflared, scrapes `*.trycloudflare.com`, restarts with backoff
- `ratelimit.go` — per-IP token bucket on `/auth/scan` and `/handoff`; `CF-Connecting-IP` trusted only from loopback
- `doctor.go` — `claude-remote doctor` checks, reads `/local/status`
- `config.go` — `~/.claude-remote/config.json`: `port`, `allowed_dirs`, `claude_path` (absolute), `tunnel {mode, bin, hostname, token}`
