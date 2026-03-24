---
name: deployment
description: Local Mac deployment — Tailscale HTTPS with cert discovery, launchd auto-start, install/uninstall
---

# Deployment

## Prerequisites

1. Go 1.25+ installed
2. Tailscale installed on Mac + phone (both on same tailnet)
3. MagicDNS enabled in Tailscale admin console
4. `claude` CLI in PATH

## Install

```bash
make install
# 1. go build -o /usr/local/bin/claude-remote .
# 2. cp launchd/com.claude-remote.plist ~/Library/LaunchAgents/
# 3. launchctl load ~/Library/LaunchAgents/com.claude-remote.plist
```

Then first-time setup:
```bash
claude-remote setup    # generates secret + shows QR code
claude-remote serve    # start server (or let launchd handle it)
```

## Tailscale HTTPS Setup

```bash
# Get Mac's Tailscale hostname
tailscale status --json | jq -r '.Self.DNSName'

# Generate TLS cert
sudo tailscale cert <hostname>.ts.net
# Creates: <hostname>.ts.net.crt + <hostname>.ts.net.key

# Cert files are owned by root — either:
# Option A: Copy to config dir
sudo cp ~/Desktop/<hostname>.ts.net.* ~/.claude-remote/
sudo chown $(whoami) ~/.claude-remote/*.key ~/.claude-remote/*.crt

# Option B: Run server with sudo (not recommended)
```

### TLS Certificate Discovery

Server searches these directories for `*.crt` + matching `*.key`:
1. `~/.claude-remote/`
2. `~/Desktop/`
3. `/var/run/tailscale/`
4. `~/.local/share/tailscale/certs/`

Falls back to HTTP with warning if no valid cert+key pair found.

## Verify

```bash
claude-remote status
# Claude Remote v0.1.0
# Config dir: ~/.claude-remote
# Port: 8822
# Secret: configured
# Tailscale: <hostname>.ts.net

curl https://<hostname>.ts.net:8822/health
# {"status":"ok","version":"0.1.0"}
```

## Uninstall

```bash
make uninstall
# 1. launchctl unload ~/Library/LaunchAgents/com.claude-remote.plist
# 2. rm ~/Library/LaunchAgents/com.claude-remote.plist
# 3. rm /usr/local/bin/claude-remote
```

## Logs

```bash
tail -f ~/.claude-remote/server.log
```

## Static Files

When running via `go run`, static files are served from `$CWD/static/`. When running the compiled binary, from `$BINARY_DIR/static/`. The server logs which directory is used: `Serving static files from <path>`.

## Troubleshooting

| Problem | Solution |
|---------|----------|
| Service not starting | `launchctl list \| grep claude-remote` |
| Port in use | Change port in `~/.claude-remote/config.json` |
| Claude not found | Set `claude_path` in config.json |
| TLS cert expired | Re-run `sudo tailscale cert <hostname>.ts.net` |
| TLS key permission denied | `sudo chown $(whoami) ~/.claude-remote/*.key` |
| QR shows http:// not https:// | Cert not in discoverable location — copy to ~/.claude-remote/ |
| Phone shows 401 unauthorized | Token consumed — re-run `setup` for fresh QR |
| Old session content showing | Server bug if RingBuffer not cleared — restart server |
