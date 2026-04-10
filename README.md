# Claude Remote

Control Claude Code CLI from your phone browser, anywhere. Continue sessions seamlessly between Mac and phone.

## What It Does

Your Mac runs a Go server that wraps Claude Code in a pseudo-terminal. Your phone connects through Tailscale VPN and interacts via a mobile-optimized chat UI. Ask Claude to "handoff to phone" — scan QR to continue your session.

## Features

- **Session handoff** — ask Claude "handoff sang điện thoại", scan QR, continue on phone
- **Two handoff modes** — attach to running terminal (like tmux) or continue with full conversation history
- **MCP integration** — claude-remote runs as both web server and MCP server for Claude Code
- **Folder picker** — browse Desktop/Downloads/Documents, choose working directory
- **Chat UI** — native text input with Vietnamese IME support, colored terminal output with smooth native scrolling
- **Quick actions** — Enter, Accept (y), Reject (n), Esc, Ctrl+C buttons for Claude's prompts
- **QR auth** — scan once, JWT cookie lasts 90 days
- **Tailscale HTTPS** — encrypted VPN mesh, no port forwarding needed
- **Auto-start** — launchd keeps server running on Mac boot

## Quick Start

```bash
# Prerequisites: Go 1.25+, Tailscale on Mac + phone

# Build
make build

# First-time setup
./claude-remote setup    # generates QR code
./claude-remote serve    # start server

# Scan QR with phone → open URL → pick folder → start Claude
```

## Install as Service

```bash
# Build binary
make build

# Copy to ~/bin (or /usr/local/bin with sudo)
mkdir -p ~/bin
cp claude-remote ~/bin/
cp -r static ~/bin/static

# Setup Tailscale HTTPS
sudo tailscale cert $(tailscale status --json | jq -r '.Self.DNSName' | sed 's/\.$//')
mkdir -p ~/.claude-remote
sudo cp ~/Desktop/*.crt ~/Desktop/*.key ~/.claude-remote/
sudo chown $(whoami) ~/.claude-remote/*.key ~/.claude-remote/*.crt

# Install launchd service (auto-start on boot)
# Edit the plist to point to your binary path, then:
mkdir -p ~/Library/LaunchAgents
cp launchd/com.claude-remote.plist ~/Library/LaunchAgents/
launchctl load ~/Library/LaunchAgents/com.claude-remote.plist

# Set absolute path to claude binary in config
cat ~/.claude-remote/config.json
# Ensure "claude_path" points to the full path, e.g.:
# "claude_path": "/Users/yourname/.local/bin/claude"

# Generate QR for first-time phone auth
claude-remote setup

# Register MCP with Claude Code (one-time, user scope)
claude mcp add --transport http -s user claude-remote http://127.0.0.1:8823/mcp
```

### Important: launchd PATH

The launchd plist must include the directory containing the `claude` binary in its PATH. Example for `~/.local/bin/claude`:

```xml
<key>EnvironmentVariables</key>
<dict>
  <key>PATH</key>
  <string>/Users/yourname/.local/bin:/Users/yourname/bin:/usr/local/bin:/opt/homebrew/bin:/usr/bin:/bin</string>
  <key>HOME</key>
  <string>/Users/yourname</string>
</dict>
```

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
│ ████████████████     │                │ Tiếp tục từ      │
│                      │                │ máy tính          │
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
- "handoff sang điện thoại"
- "tạo QR cho điện thoại"
- "chuyển session sang phone"

Claude will call the `handoff` MCP tool and display the QR code.

In **Claude Code CLI**, the `/handoff` skill also works (requires the skill file in `.claude/skills/` or `~/.claude/skills/`).

## Tailscale Setup

Tailscale creates a private VPN mesh so your phone can reach your Mac from anywhere — no port forwarding, no public IP needed.

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
Phone (chat UI)
  ↕ WebSocket over Tailscale HTTPS (:8822)
Mac (Go server)
  ↕ pseudo-terminal
Claude Code CLI

Claude Code (MCP client)
  ↕ JSON-RPC over HTTP (:8823, localhost only)
Mac (Go server /mcp endpoint)
  → generates QR + handoff token
```

Two listeners when TLS is active:
- **Port 8822** (HTTPS, all interfaces) — web UI for phone, static files public, API/WS protected by JWT
- **Port 8823** (HTTP, 127.0.0.1 only) — MCP endpoint for Claude Code

Without TLS certs, falls back to single HTTP listener on 8822.

## Commands

| Command | Description |
|---------|-------------|
| `setup` | Generate secret + QR code for first-time auth |
| `serve` | Start server (foreground) |
| `revoke` | Regenerate secret, invalidate all sessions |
| `install` | Install launchd plist + load |
| `uninstall` | Unload + remove launchd plist |
| `status` | Show running state |

## Troubleshooting

### Phone shows "Chưa xác thực" or API returns 401
Old cookie with invalid signature. Clear cookies for the site in phone browser settings, then scan a new QR code.

### "Waiting for Claude to start..." forever
Claude process failed to start. Check:
1. `~/.claude-remote/config.json` — `claude_path` must be absolute (e.g., `/Users/you/.local/bin/claude`)
2. `~/.claude-remote/server.log` — look for "Claude start failed" or "claude process exited"
3. `claude auth status` — verify Claude Code is authenticated

### WebSocket disconnects / red dot
1. Check Tailscale is connected on both devices: `tailscale status`
2. Verify health: `https://<hostname>.ts.net:8822/health`
3. Check server logs: `tail -f ~/.claude-remote/server.log`

### MCP "Failed to connect"
MCP uses HTTP on port 8823 (localhost only). Verify:
```bash
curl http://127.0.0.1:8823/health
# Should return {"status":"ok"}
```

## Tech Stack

- **Go** — single binary, no runtime deps
- **Tailscale** — secure networking
- **MCP** — Streamable HTTP (JSON-RPC) for Claude Code integration
- **xterm.js** — ANSI processing (hidden, cell-by-cell rendering)
- **Vanilla JS** — no framework, no build step

## Support

If you find this useful, consider buying me a coffee:

[![PayPal](https://img.shields.io/badge/PayPal-Donate-blue?logo=paypal)](https://paypal.me/ducnhd)

## License

[MIT](LICENSE)
