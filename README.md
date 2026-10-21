# Claude Remote

Control Claude Code CLI from your phone browser, anywhere. Continue sessions seamlessly between Mac and phone.

## What It Does

Your Mac runs a Go server that wraps Claude Code in a pseudo-terminal. Your phone reaches it over a Cloudflare Tunnel (no VPN, no port forwarding, works on mobile data) and interacts via a mobile-optimized chat UI. Ask Claude to "handoff to phone" — scan QR to continue your session.

## Features

- **Session handoff** — ask Claude "handoff to phone", scan QR, continue on phone
- **Two handoff modes** — attach to running terminal (like tmux) or continue with full conversation history
- **MCP integration** — claude-remote runs as both web server and MCP server for Claude Code
- **Folder picker** — browse Desktop/Downloads/Documents, choose working directory
- **Chat UI** — native text input with IME support, colored terminal output with smooth native scrolling
- **Quick actions** — Enter, Accept (y), Reject (n), Esc, Ctrl+C buttons for Claude's prompts
- **Mac screen control** — view and drive the whole Mac desktop from the phone (noVNC over macOS Screen Sharing), with a paste bar that handles Vietnamese and other non-ASCII text
- **QR auth** — scan once, JWT cookie lasts 90 days and renews itself while you keep using it
- **Cloudflare Tunnel** — HTTPS from anywhere with nothing installed on the phone; Tailscale/LAN still supported
- **Self-diagnosing** — `claude-remote doctor` checks every link in the chain and prints the exact fix
- **Auto-start** — launchd keeps server running on Mac boot; the tunnel restarts itself if it drops

## Quick Start

```bash
# Prerequisites: Go 1.25+, cloudflared (brew install cloudflared)

make build
./claude-remote setup    # generates the auth secret
./claude-remote serve    # starts the server + tunnel
```

In another terminal:

```bash
./claude-remote qr       # QR code with the live tunnel URL
```

Scan it with your phone → pick a folder → start Claude. If anything fails:

```bash
./claude-remote doctor
```

### Connection modes

Set the `tunnel` block in `~/.claude-remote/config.json`:

| Mode | URL | Setup | Notes |
|------|-----|-------|-------|
| `quick` (default) | random `*.trycloudflare.com` | none | URL changes whenever cloudflared restarts, so re-pair with `claude-remote qr` |
| `named` | your own stable hostname | Cloudflare account + tunnel token | URL never changes, so the phone stays paired |
| `off` | `https://<host>:8822` | Tailscale or LAN | Requires a valid TLS cert; server binds `0.0.0.0` |

```jsonc
// named tunnel example
"tunnel": { "mode": "named", "hostname": "claude.example.com", "token": "eyJ..." }
```

With a tunnel enabled the server binds `127.0.0.1` only — cloudflared is the sole way in, and `/mcp` plus the pairing endpoints stay on the loopback listener where the tunnel cannot reach them.

## Install as Service

```bash
brew install cloudflared   # tunnel client
make install               # build, sign, copy binary + static/ to ~/bin, load launchd

# Set the absolute path to the claude binary (launchd's PATH is minimal):
#   ~/.claude-remote/config.json → "claude_path": "/Users/yourname/.local/bin/claude"

claude-remote setup        # first time only: generate the auth secret
claude-remote qr           # pair the phone

# Register MCP with Claude Code (one-time, user scope)
claude mcp add --transport http -s user claude-remote http://127.0.0.1:8823/mcp
```

`make install` templates `launchd/com.claude-remote.plist` with your home directory and reloads the service with `launchctl bootout` + `bootstrap` (plain `load` keeps the old plist running). Other targets:

| Target | What it does |
|--------|--------------|
| `make restart` | Reload the service without rebuilding |
| `make logs` | `tail -f ~/.claude-remote/server.log` |
| `make uninstall` | Unload the service, remove plist and binary |

### Important: launchd environment

launchd starts the server with no `TERM`/`LANG` and a minimal `PATH`. The server fills those in for the Claude process itself (`TERM=xterm-256color`, `LANG=en_US.UTF-8`, `~/.local/bin`, `~/bin` and Homebrew dirs prepended to `PATH`), but `claude_path` in `config.json` must still be absolute.

### Stop macOS re-asking for permissions

Go signs binaries ad hoc, so each rebuild looks like a brand-new app and macOS asks again for Desktop/Documents (or Full Disk Access). `make install` signs with a `claude-remote-signing` identity when one exists. Create it once:

```bash
cat > /tmp/cs.cnf <<'EOF'
[req]
distinguished_name = dn
x509_extensions = ext
prompt = no
[dn]
CN = claude-remote-signing
[ext]
basicConstraints = critical,CA:false
keyUsage = critical,digitalSignature
extendedKeyUsage = critical,codeSigning
EOF
openssl req -x509 -newkey rsa:2048 -nodes -days 3650 -config /tmp/cs.cnf -keyout /tmp/cs.key -out /tmp/cs.pem
openssl pkcs12 -export -legacy -inkey /tmp/cs.key -in /tmp/cs.pem -name claude-remote-signing -out /tmp/cs.p12 -passout pass:tmp
security import /tmp/cs.p12 -k ~/Library/Keychains/login.keychain-db -P tmp -T /usr/bin/codesign
rm /tmp/cs.cnf /tmp/cs.key /tmp/cs.pem /tmp/cs.p12
```

The certificate does not need to be trusted. Grant the permissions one last time after the first signed install.

## Session Handoff

The killer feature: continue Claude Code sessions on your phone.

```
Mac (Claude Code)                  Phone (browser)
┌──────────────────────┐
│ > working on feature │
│ > "handoff to phone" │
│                      │
│ ████████████████     │   Scan QR
│ ████████████████     │ ────────────►  ┌──────────────────┐
│ ████████████████     │                │ Continue from     │
│                      │                │ computer          │
│ Scan to continue     │                │                   │
│ on phone             │                │ [🔗 Attach]       │
└──────────────────────┘                │ [📋 Continue]     │
                                        │ [📁 New folder]   │
                                        └──────────────────┘
```

**Attach** — connect to the same running terminal. See live output, type into the same session. Like tmux attach.

**Continue** — start a new Claude session with `--continue` flag. Gets full conversation history from the previous session in the same directory.

### How it works

1. Ask Claude to "handoff to phone" (Claude calls the MCP `handoff` tool)
2. Claude generates a QR code with a one-time token (expires 15 min)
3. Scan with phone camera
4. Phone opens claude-remote web UI, auto-authenticates via token
5. Choose mode: attach or continue
6. Start working on phone

### Triggering handoff

The MCP `handoff` tool is registered globally. In any Claude Code session (CLI, VS Code, Cursor), just ask:
- "handoff to phone"
- "generate QR for phone"
- "transfer session to phone"

Claude will call the `handoff` MCP tool and display the QR code.

In **Claude Code CLI**, the `/handoff` skill also works (requires the skill file in `.claude/skills/` or `~/.claude/skills/`).

## Mac Screen Control

The 🖥 **Screen** button in the header opens `/screen.html`: a full view of the Mac desktop you can tap, drag and type into.

1. On the Mac: **System Settings → General → Sharing → Screen Sharing** → on.
2. On the phone: tap 🖥 Screen and log in with your macOS username and password (the login runs inside the browser; the server only pipes bytes to `127.0.0.1:5900`).

The key bar has ⌨ (live key-by-key typing), ⤢ Fit, a sticky ⌘ modifier, Esc, Tab, Enter, Backspace and arrows. Screen Sharing drops non-ASCII keystrokes, so for Vietnamese or any accented text use the text bar at the bottom: **Paste** puts the text on the Mac clipboard and presses ⌘V, **Paste ↵** also presses Enter.

## Tailscale Setup (optional, `"tunnel": {"mode":"off"}`)

Only needed if you turn the Cloudflare tunnel off. Tailscale creates a private VPN mesh so your phone can reach your Mac from anywhere — no port forwarding, no public IP needed.

### 1. Install Tailscale

- **Mac**: Download from [tailscale.com/download](https://tailscale.com/download) or `brew install tailscale`
- **Phone (iOS)**: App Store → search "Tailscale"
- **Phone (Android)**: Play Store → search "Tailscale"

Sign in with the same account on both devices.

### 2. Enable MagicDNS

Go to [Tailscale admin console](https://login.tailscale.com/admin/dns) → enable MagicDNS. This gives your Mac a stable hostname like `macbook-pro.tail1234.ts.net`.

### 3. Generate HTTPS Certificate

```bash
# Get your Mac's Tailscale hostname
tailscale status

# Generate TLS cert (requires sudo)
sudo tailscale cert <your-hostname>.ts.net

# Copy to config dir:
mkdir -p ~/.claude-remote
sudo cp ~/Desktop/<your-hostname>.ts.net.crt ~/.claude-remote/
sudo cp ~/Desktop/<your-hostname>.ts.net.key ~/.claude-remote/
sudo chown $(whoami) ~/.claude-remote/*.key ~/.claude-remote/*.crt
```

### 4. Verify Connection

On your phone (connected to Tailscale):
- Open Safari/Chrome → `https://<your-hostname>.ts.net:8822/health`
- Should show `{"status":"ok","version":"0.1.0"}`

## How It Works

```
Phone (chat UI / screen page)
  ↕ HTTPS + WebSocket via Cloudflare Tunnel (or Tailscale HTTPS :8822 in mode off)
cloudflared → 127.0.0.1:8822
Mac (Go server)
  ├─ pseudo-terminal → Claude Code CLI
  └─ /ws/vnc → 127.0.0.1:5900 (macOS Screen Sharing)

Claude Code (MCP client)
  ↕ JSON-RPC over HTTP (:8823, localhost only)
Mac (Go server /mcp endpoint)
  → generates QR + handoff token
```

Two listeners:
- **Port 8822** — web UI for the phone; static files public, API/WS protected by JWT. Plain HTTP on `127.0.0.1` behind the tunnel, HTTPS on all interfaces in mode `off`
- **Port 8823** (HTTP, 127.0.0.1 only) — MCP endpoint for Claude Code, plus the pairing/status endpoints used by `qr` and `doctor`

## Commands

| Command | Description |
|---------|-------------|
| `setup` | Generate the auth secret (first time) |
| `serve` | Start server + tunnel (foreground) |
| `qr` | Print a pairing QR for the running server (alias `pair`) |
| `doctor` | Check every link in the connection chain and print fixes |
| `status` | Show running state, tunnel state, phone URL |
| `revoke` | Regenerate secret, invalidate all sessions |
| `install` | Install launchd plist + load |
| `uninstall` | Unload + remove launchd plist |
| `version` | Print version |

## Troubleshooting

### Phone shows "Not authenticated" or API returns 401
Old cookie with invalid signature (or `revoke` was run). Scan a fresh QR from `claude-remote qr`; if that still fails, clear cookies for the site in the phone browser.

### Quick tunnel URL stopped working
Quick tunnels get a new `*.trycloudflare.com` URL every time cloudflared restarts. Run `claude-remote qr` again, or switch to a `named` tunnel for a stable hostname. `qr` refuses (503) while the tunnel is still coming up — wait a few seconds and retry.

### "Waiting for Claude to start..." forever
Claude process failed to start. Check:
1. `~/.claude-remote/config.json` — `claude_path` must be absolute (e.g., `/Users/you/.local/bin/claude`)
2. `~/.claude-remote/server.log` — look for "Claude start failed" or "claude process exited"
3. `claude auth status` — verify Claude Code is authenticated

### WebSocket disconnects / red dot
1. Run `claude-remote doctor`
2. Check server logs: `make logs` — every request is logged as `REQ <method> <path> → <status>`
3. Mode `off` only: check Tailscale on both devices (`tailscale status`) and `https://<hostname>.ts.net:8822/health`

### Can't scroll back through Claude's output
The server starts Claude with `--settings {"tui":"default"}` because the fullscreen TUI has no scrollback. If you start Claude yourself with `"tui": "fullscreen"`, attaching from the phone won't scroll.

### Screen page says "Screen Sharing is off"
Enable **System Settings → General → Sharing → Screen Sharing** on the Mac.

### macOS keeps asking for Desktop/Documents access
The binary is ad-hoc signed. Create the `claude-remote-signing` identity (see *Stop macOS re-asking for permissions*), `make install`, and grant access once more.

### MCP "Failed to connect"
MCP uses HTTP on port 8823 (localhost only). Verify:
```bash
curl http://127.0.0.1:8823/health
# Should return {"status":"ok"}
```

## Tech Stack

- **Go** — single binary, no runtime deps
- **cloudflared** — Cloudflare Tunnel (default); **Tailscale** optional
- **noVNC** — Mac screen in the browser, over macOS Screen Sharing
- **MCP** — Streamable HTTP (JSON-RPC) for Claude Code integration
- **xterm.js** — ANSI processing (hidden, cell-by-cell rendering)
- **Vanilla JS** — no framework, no build step

## Support

If you find this useful, consider buying me a coffee:

[![PayPal](https://img.shields.io/badge/PayPal-Donate-blue?logo=paypal)](https://paypal.me/ducnhd)

## License

[MIT](LICENSE)
