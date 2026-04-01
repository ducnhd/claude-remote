# Claude Remote

Control Claude Code CLI from your phone browser, anywhere. Continue sessions seamlessly between Mac and phone.

## What It Does

Your Mac runs a Go server that wraps Claude Code in a pseudo-terminal. Your phone connects through Tailscale VPN and interacts via a mobile-optimized chat UI. Type `/remote` in Claude Code to get a QR code — scan it to continue your session on phone.

## Features

- **Session handoff** — type `/remote` in Claude Code, scan QR, continue on phone
- **Two handoff modes** — attach to running terminal (like tmux) or continue with full conversation history
- **MCP integration** — claude-remote runs as both web server and MCP server
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
# Install binary + launchd auto-start
make install

# Setup Tailscale HTTPS
sudo tailscale cert $(tailscale status --json | jq -r '.Self.DNSName' | sed 's/\.$//')
sudo cp ~/Desktop/*.crt ~/Desktop/*.key ~/.claude-remote/
sudo chown $(whoami) ~/.claude-remote/*.key

# Generate QR for first-time phone auth
claude-remote setup

# Register MCP with Claude Code (one-time)
claude mcp add --transport http -s user claude-remote http://127.0.0.1:8823/mcp
```

## Session Handoff

The killer feature: continue Claude Code sessions on your phone.

```
Mac (Claude Code terminal)        Phone (browser)
┌──────────────────────┐
│ > working on feature │
│ > /remote            │
│                      │
│ ████████████████     │    Scan QR
│ ████████████████     │ ──────────────►  ┌──────────────────┐
│ ████████████████     │                  │ Tiếp tục từ      │
│                      │                  │ máy tính          │
│ Scan to continue     │                  │                   │
│ on phone             │                  │ [🔗 Attach]       │
└──────────────────────┘                  │ [📋 Continue]     │
                                          │ [📁 New folder]   │
                                          └──────────────────┘
```

**Attach** — connect to the same running terminal. See live output, type into the same session. Like tmux attach.

**Continue** — start a new Claude session with `--continue` flag. Gets full conversation history from the previous session in the same directory.

### How it works

1. Type `/remote` in Claude Code (uses the MCP `handoff` tool)
2. Claude generates a QR code with a one-time token (expires 5 min)
3. Scan with phone camera
4. Phone opens claude-remote web UI, auto-authenticates via token
5. Choose mode: attach or continue
6. Start working on phone

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

Two listeners:
- **Port 8822** (HTTPS, all interfaces) — web UI for phone
- **Port 8823** (HTTP, localhost only) — MCP endpoint for Claude Code

## Commands

| Command | Description |
|---------|-------------|
| `setup` | Generate secret + QR code for first-time auth |
| `serve` | Start server (foreground) |
| `revoke` | Regenerate secret, invalidate all sessions |
| `install` | Install launchd plist + load |
| `uninstall` | Unload + remove launchd plist |
| `status` | Show running state |

### Claude Code Skill

| Command | Description |
|---------|-------------|
| `/remote` | Generate QR to continue session on phone |

## Tech Stack

- **Go** — single binary, no runtime deps
- **Tailscale** — secure networking
- **MCP** — Streamable HTTP (JSON-RPC) for Claude Code integration
- **xterm.js** — ANSI processing (hidden)
- **Vanilla JS** — no framework, no build step
