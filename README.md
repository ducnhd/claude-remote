# Claude Remote

Control Claude Code CLI from your phone browser, anywhere.

## What It Does

Your Mac runs a Go server that wraps Claude Code in a pseudo-terminal. Your phone connects through Tailscale VPN and interacts via a mobile-optimized chat UI. Authenticate once by scanning a QR code — then just open the URL anytime.

## Features

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

# Generate QR
claude-remote setup
```

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
# Example: sudo tailscale cert macbook-pro.tail1234.ts.net

# Cert files appear on ~/Desktop (macOS default)
# Copy to config dir so the server can read them:
mkdir -p ~/.claude-remote
sudo cp ~/Desktop/<your-hostname>.ts.net.crt ~/.claude-remote/
sudo cp ~/Desktop/<your-hostname>.ts.net.key ~/.claude-remote/
sudo chown $(whoami) ~/.claude-remote/*.key ~/.claude-remote/*.crt
```

### 4. Verify Connection

On your phone (connected to Tailscale):
- Open Safari/Chrome → `https://<your-hostname>.ts.net:8822/health`
- Should show `{"status":"ok","version":"0.1.0"}`

If you see a certificate error, the cert wasn't loaded — check `claude-remote status` and server logs at `~/.claude-remote/server.log`.

## How It Works

```
Phone (chat UI)
  ↕ WebSocket over Tailscale HTTPS
Mac (Go server)
  ↕ pseudo-terminal
Claude Code CLI
```

The web UI uses a hidden xterm.js instance to process ANSI escape sequences, then extracts colored text into a native `<pre>` element for smooth mobile scrolling. Input goes through a native `<textarea>` that supports Vietnamese IME composition.

## Commands

| Command | Description |
|---------|-------------|
| `setup` | Generate secret + QR code for first-time auth |
| `serve` | Start server (foreground) |
| `revoke` | Regenerate secret, invalidate all sessions |
| `install` | Install launchd plist + load |
| `uninstall` | Unload + remove launchd plist |
| `status` | Show running state |

## Tech Stack

- **Go** — single binary, no runtime deps
- **Tailscale** — secure networking
- **xterm.js** — ANSI processing (hidden)
- **Vanilla JS** — no framework, no build step
