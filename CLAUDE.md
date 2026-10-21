# CLAUDE.md

## Project Overview

**Claude Remote** — a lightweight Go server that lets you control Claude Code CLI from your phone browser, anywhere over the internet. Also acts as an MCP server so Claude Code can trigger phone handoff.

**Core idea**: Mac runs a Go server that wraps Claude Code in a pseudo-terminal, exposes it via WebSocket, and provides a mobile-friendly chat UI. Phone reaches it through a Cloudflare Tunnel managed by the server itself (Tailscale/LAN still supported via `"tunnel": {"mode":"off"}`). Claude Code connects via MCP (localhost HTTP on port 8823) to generate handoff QR codes. Auth via one-time QR code scan, persistent JWT cookie (SameSite=Lax for cross-site QR scan compatibility). Auto-starts on Mac boot via launchd.

**Target**: Single user, single Mac. Personal tool, not a product.

## Build & Run

```bash
make build          # Build binary
make run            # Build + run locally
make test           # Run all tests
make test-race      # Run all tests with the race detector
make install        # Build + sign + install binary and static/ to ~/bin + bootout/bootstrap launchd
make restart        # Reload the launchd service without rebuilding
make logs           # tail -f ~/.claude-remote/server.log
make uninstall      # Unload launchd + remove binary
```

### CLI Commands

```bash
claude-remote setup      # Generate secret + show QR code
claude-remote serve      # Start server (foreground)
claude-remote revoke     # Regenerate secret, invalidate all sessions
claude-remote install    # Install launchd plist + load
claude-remote uninstall  # Unload + remove launchd plist
claude-remote status     # Show running state, tunnel state, phone URL
claude-remote qr         # Print a pairing QR for the running server (alias: pair; does not touch the secret)
claude-remote doctor     # Diagnose every link in the connection chain and print fixes
claude-remote version    # Print version
```

### MCP Registration (one-time)

```bash
claude mcp add --transport http -s user claude-remote http://127.0.0.1:8823/mcp
```

MCP uses port 8823 (HTTP, localhost only). The main web UI uses port 8822 (plain HTTP on loopback behind cloudflared, or HTTPS on all interfaces in `"mode":"off"`). The MCP listener always runs on `127.0.0.1:<port+1>`, with or without TLS, so MCP registration stays valid either way.

### Triggering Handoff

MCP tools are available globally in Claude Code. Just ask:
- "handoff to phone"
- "generate QR for phone"

Claude calls the `handoff` MCP tool and displays QR. In Claude Code CLI, `/handoff` skill also works.

## Architecture

```
Go binary (single process, two listeners when TLS active)
├── main.go        # CLI entry point (setup/serve/revoke/install/uninstall/status/qr/doctor)
├── paths.go       # Shared path safety: expandHome, resolvePath, pathWithin, withinAllowed
├── tunnel.go      # cloudflared supervisor: quick/named modes, URL detection, auto-restart
├── ratelimit.go   # Per-IP token bucket, clientIP/viaProxy/isSecureRequest helpers
├── doctor.go      # `doctor` checks + /local/status client
├── server.go      # HTTP/HTTPS server, routes, auth cookie helper (setAuthCookie),
│                  #   handoff endpoint, detectTailscaleHost, detectProto, dual-listener
├── mcp.go         # MCP Streamable HTTP handler (JSON-RPC), handoff + status tools
├── auth.go        # QR generation, token verify, JWT issue/validate, middleware,
│                  #   handoff tokens (15min expiry, one-time), auth logging
├── terminal.go    # WebSocket + pty: spawn claude in chosen dir or with --continue,
│                  #   pipe stdin/stdout via startIO(), ring buffer, multi-client broadcast
├── vnc.go         # /ws/vnc: byte pipe from noVNC to macOS Screen Sharing (127.0.0.1:5900),
│                  #   /api/clipboard: pbcopy so non-ASCII text can be pasted with ⌘V
├── files.go       # File browser API (read-only, allowlist, ~ expansion)
├── config.go      # Config loading/saving (~/.claude-remote/)
├── static/        # Web UI (vanilla HTML/JS/CSS, no build step)
│   ├── index.html # Three screens: folder picker → handoff selector → chat UI
│   ├── app.js     # WebSocket, hidden xterm.js, cell-by-cell rendering, 80ms debounce,
│   │              #   Vietnamese IME, handoff URL params, mode selector, auth error handling
│   ├── screen.html# Self-contained Mac screen viewer/controller (noVNC from CDN), key bar,
│   │              #   ⌘ modifier, Paste / Paste ↵ via /api/clipboard
│   └── style.css  # Mobile-first dark theme
├── .claude/
│   └── skills/
│       ├── remote.md       # /handoff skill — calls MCP handoff tool
│       ├── architecture.md # Component map + design rules
│       ├── auth-flow.md    # Pairing, handoff, cookie lifecycle
│       ├── deployment.md   # make install, signing, tunnel modes, troubleshooting
│       └── testing.md      # Test files + patterns
└── launchd/
    └── com.claude-remote.plist  # __HOME__ placeholder, templated by `make install`
```

### Web UI Flow

```
Flow A: Direct access (no URL params)
  Screen 1: Folder Picker
  ├── Quick dirs: Desktop / Downloads / Documents
  ├── Browse subdirectories (auth error shown if no valid cookie)
  └── "Start Claude" → POST /api/claude/start {dir}
  ↓
  Screen 2: Chat UI (auto-attach if session already running)

Flow B: Handoff (URL params ?dir=...&mode=...)
  /handoff?token=xxx → validate → clear old cookie → set new cookie → redirect
  ↓
  Screen 3: Handoff Mode Selector (if mode=choose)
  ├── 🔗 Attach session — check status, auto-start if needed, connect WS
  ├── 📋 Continue session — POST /api/claude/start {resume:true}, connect WS
  └── 📁 Choose another folder — go to folder picker
  ↓
  Screen 2: Chat UI

Flow C: Mac screen (🖥 Screen button in the header → /screen.html)
  noVNC → /ws/vnc → 127.0.0.1:5900 (macOS Screen Sharing, ARD login in-browser)
  Text bar → POST /api/clipboard → pbcopy → ⌘V on the Mac
```

### Data Flow

```
Phone browser (<textarea> → \r)
  ↕ WebSocket (wss:// via Cloudflare Tunnel → 127.0.0.1:8822; or Tailscale HTTPS in mode off)
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

- **Hidden xterm.js** — runs offscreen for ANSI processing; output extracted with colors into native `<pre>` for smooth mobile scrolling
- **Cell-by-cell rendering** — `renderLine()` iterates xterm cells directly (not `translateToString`) to avoid character/style misalignment; supports 256-color palette
- **Debounced sync (80ms)** — reduces flicker during Claude streaming output
- **Logical line joining** — wrapped lines joined into single logical lines for correct display at any screen width
- **Native `<textarea>` for input** — supports Vietnamese IME (Telex/VNI)
- **`\r` not `\n`** — PTY expects carriage return
- **Dynamic terminal cols** — calculated from screen width at runtime
- **Quick action buttons** — y/n/Enter/Esc buttons for mobile (impossible on touch keyboard)
- **Dual listener** — public :8822 for phone, loopback HTTP :8823 for MCP + pairing. Claude Code MCP client can't use Tailscale certs
- **Handoff tokens** — 15 min expiry (`handoffTokenTTL`), one-time, in memory. Setup tokens expire after 30 min (`setupTokenTTL`) and are compared in constant time
- **Component-wise path checks** — `pathWithin()` matches on path components, never a raw string prefix: `~/Desktop-secret` must not pass for allowed root `~/Desktop`, and an unresolvable root never matches everything
- **Session generation counter** — `TerminalManager.gen` invalidates goroutines of a stopped session so a restart is not torn down by the previous pty's exit handler
- **Per-connection write mutex** — gorilla/websocket panics on concurrent writes; both the pty broadcast and the request goroutine write, so each client carries its own mutex plus a 10s write deadline
- **Backlog replay is exclusive** — `outMu` serializes buffer append + broadcast + join-time replay, so a joining phone never sees duplicated or missing output. The browser resets its terminal on every `onopen` for the same reason
- **SameSite=Lax cookies** — QR scan opens from camera app (cross-site navigation); SameSite=Strict cookies get dropped on redirect. `setAuthCookie()` helper clears old cookie before setting new one
- **Static files public** — HTML/JS/CSS served without auth; only API endpoints and WebSocket require JWT. Prevents blank page when cookie expires
- **MCP localhost-only** — `/mcp` rejects non-loopback IPs
- **Absolute claude_path** — config.json must use absolute path (e.g., `/Users/you/.local/bin/claude`); launchd PATH may not include `~/.local/bin`
- **startIO() shared** — both StartInDir and StartWithResume use same goroutine pattern; process exit is logged
- **`childEnv()` for child processes** — launchd gives the server no `TERM`/`LANG` and a minimal `PATH`; passing `os.Environ()` straight through left the Claude TUI unable to read keystrokes and made `pbcopy` mangle non-ASCII. `childEnv()` fills `TERM=xterm-256color`, `COLORTERM`, `LANG=en_US.UTF-8` and prepends `~/.local/bin`, `~/bin`, Homebrew dirs to `PATH`
- **Inline TUI forced** — `claudeArgs` passes `--settings {"tui":"default"}` (also on `--continue`). The fullscreen TUI uses the alternate screen, which has no scrollback, so the phone could not scroll back
- **`Cache-Control: no-cache` on static** — `noStore()` wrapper; mobile Safari otherwise serves a stale `app.js` for months
- **Access log** — `accessLog()` logs every public request (`REQ ... → status`), its `statusRecorder` implements `Hijack` so WebSocket upgrades still work
- **`/local/pair` refuses while the tunnel is down** — returns 503 instead of minting a LAN-only QR
- **VNC bridge** — `/ws/vnc` only ever dials loopback `:5900`; both it and its upgrader enforce same-origin because the socket is authenticated by cookie alone. The macOS ARD login still happens end to end in noVNC
- **Clipboard paste instead of typing** — Screen Sharing drops Unicode keysyms, so Vietnamese typed key by key never arrives. `/api/clipboard` (POST, `application/json` only to force a CORS preflight, 64KB cap) runs `pbcopy` and the page sends ⌘V
- **Stable code signing** — Go signs ad hoc, so every rebuild re-triggers macOS folder / Full Disk Access prompts. `make install` signs with the `claude-remote-signing` identity (override `SIGN_IDENTITY`) when it exists
- **launchd reload = bootout + wait + bootstrap** — `launchctl load` on a loaded job keeps the old plist and `kickstart` then dies with EX_CONFIG(78); bootout is async, so wait for the label to disappear first

### Network

Default: cloudflared quick tunnel. The server spawns `cloudflared tunnel --url http://127.0.0.1:<port>`, scrapes the `*.trycloudflare.com` URL from its output, and restarts it with backoff if it dies. Cloudflare terminates HTTPS, so the Go server speaks plain HTTP and binds `127.0.0.1:8822` only; the loopback listener on `127.0.0.1:8823` carries MCP + pairing.

`"tunnel": {"mode":"named"}` runs a token-based named tunnel with a stable hostname. `"mode":"off"` restores the old behaviour: bind `0.0.0.0:8822`, use Tailscale/LAN, load TLS certs from disk (expired certs are now skipped with a warning instead of being served).

**Security consequences of tunnelling** (all handled, do not regress):
- Every request arrives from `127.0.0.1`, so a loopback check is *not* proof of a local caller. `/mcp`, `/local/pair` and `/local/status` live on the loopback mux only and additionally reject requests carrying proxy headers (`viaProxy`).
- The cookie's `Secure` flag comes from `X-Forwarded-Proto`, not from local TLS, or the browser silently drops it.
- `/auth/scan` and `/handoff` are rate limited per client IP (`CF-Connecting-IP`, trusted only from loopback).
- Cloudflare drops idle WebSockets, so the server pings every 30s and drops clients silent for 90s.

### Auth

- First time: `setup` (secret only) → `qr` asks the running server for a pairing URL → phone scans `/auth/scan` → JWT cookie (90 days, SameSite=Lax)
- Setup tokens are reusable until they expire (30 min): camera apps and link previewers prefetch the URL, and burning the token on that first fetch used to break pairing
- Cookies roll over: any request past a third of the JWT lifetime gets a fresh cookie, so a phone in regular use never re-pairs
- Handoff: MCP generates QR → phone scans `/handoff` → old cookie cleared → new JWT cookie
- Subsequent: cookie auto-authenticates
- Revoke: `revoke` → new secret → all cookies invalid → phone must re-scan QR
- `setAuthCookie(w, jwt)` — expires old cookie first, then sets new one (fixes stale cookie issue)

### Config

```
~/.claude-remote/
├── secret.key      # JWT signing secret (256-bit)
├── config.json     # port, allowed_dirs, claude_path (MUST be absolute),
│                   #   tunnel {mode: quick|named|off, bin, hostname, token}
├── sessions.json   # active device list
├── *.crt / *.key   # Tailscale TLS certificates (mode off only)
└── server.log      # stdout/stderr from launchd service
```

## API Endpoints

| Route | Method | Auth | Purpose |
|-------|--------|------|---------|
| `/auth/scan` | GET | No | QR token exchange → clear old cookie → set JWT → redirect / |
| `/health` | GET | No | Health check `{"status":"ok","version":"..."}` |
| `/mcp` | POST | Localhost | MCP Streamable HTTP (JSON-RPC) — handoff + status tools. **Loopback listener only** |
| `/handoff` | GET | Token | Handoff token exchange → clear old cookie → set JWT → redirect |
| `/local/pair` | POST | Localhost | Mint a pairing URL for `claude-remote qr` |
| `/local/status` | GET | Localhost | Runtime state for `claude-remote doctor` |
| `/api/claude/start` | POST | JWT | Start Claude `{"dir":"/path","resume":bool}` |
| `/api/claude/status` | GET | JWT | Session status `{"running":bool}` |
| `/api/files` | GET | JWT | List directory |
| `/api/files/read` | GET | JWT | Read file content |
| `/ws/term` | WS | JWT | Terminal WebSocket (binary I/O to pty) |
| `/ws/vnc` | WS | JWT + same-origin | noVNC ↔ macOS Screen Sharing (`127.0.0.1:5900`); 502 if Screen Sharing is off |
| `/api/clipboard` | POST | JWT | `{"text":"..."}` (JSON only, ≤64KB) → `pbcopy` on the Mac |
| `/*` | GET | No | Static files (HTML/JS/CSS) |

### MCP Tools

| Tool | Input | Description |
|------|-------|-------------|
| `handoff` | `{dir: string, mode?: "attach"\|"continue"\|"choose"}` | Generate QR + URL for phone handoff (token expires 15min). `dir` must be inside `allowed_dirs`; query params are URL-escaped so paths with spaces/`&` work |
| `status` | `{}` | Service status, running session info, connected client count |

## Code Conventions

- **Go 1.25+**, module `github.com/ducnhd/claude-remote` (single `package main`)
- **Short receivers**: `(s *Server)`, `(a *Auth)`, `(tm *TerminalManager)`
- **Error wrapping**: `fmt.Errorf("context: %w", err)`
- **No frameworks**: stdlib `net/http` + minimal deps
- **Frontend**: Vanilla JS, no build step. xterm.js and noVNC (`@novnc/novnc@1.5.0` ESM) from CDN. `screen.html` keeps its CSS inline so it lays out even if `style.css` fails
- **Security-first file browser**: allowlist dirs, block dotfiles, no write ops, reject `..`, symlink resolution
- **Additive changes**: new features use new routes/screens/methods; existing behavior stays untouched
- **Auth logging**: middleware logs denied requests with path + reason for debugging

## Dependencies

| Library | Purpose |
|---------|---------|
| gorilla/websocket | WebSocket server |
| creack/pty | Pseudo-terminal for Claude CLI |
| golang-jwt/jwt/v5 | JWT auth |
| skip2/go-qrcode | QR code in terminal + MCP handoff |

External: cloudflared (`brew install cloudflared`, default transport), Tailscale (only for `mode: off`), macOS Screen Sharing (only for the screen page), xterm.js + xterm-addon-fit + noVNC (CDN).

## Testing

```bash
go test ./... -v -count=1       # All tests
go test -race ./... -v -count=1 # With race detector
```

Test files: `auth_test.go`, `config_test.go`, `files_test.go`, `paths_test.go`, `ratelimit_test.go`, `terminal_test.go`, `tunnel_test.go`, `server_test.go`, `mcp_test.go`, `vnc_test.go`, `integration_test.go`.

## Deployment

Local Mac only. Auto-starts on boot via launchd.

```bash
brew install cloudflared            # default transport (quick tunnel)

# One-time: stable signing identity so macOS keeps folder/FDA permissions
# across rebuilds (openssl + security import recipe in README)

make install                        # build, sign, copy binary + static/ to ~/bin,
                                    # template plist, bootout/bootstrap launchd
claude-remote setup                 # generate secret (first time only)
claude-remote qr                    # pairing QR for the live tunnel URL
claude-remote doctor                # if anything fails

# config.json: "claude_path" MUST be absolute, e.g. /Users/yourname/.local/bin/claude

# Register MCP (one-time, user scope — persists across all projects)
claude mcp add --transport http -s user claude-remote http://127.0.0.1:8823/mcp
```

`make install` copies `static/` next to the binary; the server serves `<binary dir>/static`, so skipping that step means UI changes never reach the phone.

Tailscale (`"tunnel": {"mode":"off"}`) still needs `sudo tailscale cert <hostname>.ts.net` and the cert/key copied into `~/.claude-remote/` (chown to your user).
