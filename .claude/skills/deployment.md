---
name: deployment
description: Local Mac deployment — launchd auto-start, Tailscale setup, install/uninstall commands
---

# Deployment

## Prerequisites

1. Go 1.22+ installed
2. Tailscale installed on Mac + phone
3. MagicDNS enabled in Tailscale admin
4. `claude` CLI in PATH

## Install

```bash
make install
# Equivalent to:
# 1. go build -o /usr/local/bin/claude-remote .
# 2. claude-remote setup    (if first time — generates secret + QR)
# 3. cp launchd/com.claude-remote.plist ~/Library/LaunchAgents/
# 4. launchctl load ~/Library/LaunchAgents/com.claude-remote.plist
```

## Tailscale HTTPS Setup

```bash
# Get your Mac's Tailscale hostname
tailscale status | grep $(hostname)

# Generate TLS cert (stored in /var/run/tailscale/ or similar)
sudo tailscale cert <hostname>.ts.net

# Config will reference these cert files
```

## Verify

```bash
claude-remote status     # Check service is running
curl https://<hostname>.ts.net:8822/health  # Health check
```

## Uninstall

```bash
make uninstall
# Equivalent to:
# 1. launchctl unload ~/Library/LaunchAgents/com.claude-remote.plist
# 2. rm ~/Library/LaunchAgents/com.claude-remote.plist
# 3. rm /usr/local/bin/claude-remote
```

## Logs

```bash
tail -f ~/.claude-remote/server.log
```

## Troubleshooting

- **Service not starting**: Check `launchctl list | grep claude-remote`
- **Port in use**: Change port in `~/.claude-remote/config.json`
- **Claude not found**: Set `claude_path` in config.json
- **TLS cert expired**: Re-run `sudo tailscale cert <hostname>.ts.net`
