---
name: deployment
description: Local Mac deployment — make install (sign, static copy, launchd bootout/bootstrap), cloudflared tunnel modes, Screen Sharing, troubleshooting
---

# Deployment

## Prerequisites

1. Go 1.25+
2. `brew install cloudflared` (default transport). Tailscale only for `"tunnel": {"mode":"off"}`
3. `claude` CLI installed; its **absolute** path in `config.json` → `claude_path`
4. Optional: macOS Screen Sharing on (System Settings → General → Sharing) for the 🖥 Screen page
5. Optional but recommended: `claude-remote-signing` code-signing identity (recipe in README) so macOS stops re-asking for folder / Full Disk Access after each rebuild

## Install / Update

```bash
make install
```

What it does:
1. `go build`, copy to `~/bin/claude-remote`
2. `codesign -s claude-remote-signing -i com.claude-remote` if the identity exists (override with `SIGN_IDENTITY=…`), else warns
3. Copy `static/` → `~/bin/static/` (server serves `<binary dir>/static`; skip this and UI changes never land)
4. Template `launchd/com.claude-remote.plist` (`__HOME__` → `$HOME`) into `~/Library/LaunchAgents/`
5. `launchctl bootout` → wait until the label is gone → `bootstrap` (retry once) → verify running

Never use `launchctl load`/`kickstart` for reinstalls: `load` keeps the old plist and `kickstart` then exits EX_CONFIG(78).

First time only:
```bash
claude-remote setup     # secret
claude-remote qr        # pairing QR (needs the running server + live tunnel)
claude mcp add --transport http -s user claude-remote http://127.0.0.1:8823/mcp
```

## Other targets

| Command | Purpose |
|---------|---------|
| `make restart` | bootout + bootstrap without rebuilding |
| `make logs` | `tail -f ~/.claude-remote/server.log` |
| `make uninstall` | unload, remove plist + binary |
| `claude-remote status` | running state, tunnel state, phone URL |
| `claude-remote doctor` | checks every link, prints fixes |

## Tunnel modes (`config.json` → `tunnel`)

| Mode | Notes |
|------|-------|
| `quick` (default) | random `*.trycloudflare.com`; changes on every cloudflared restart → re-run `qr` |
| `named` | `hostname` + `token`; stable URL |
| `off` | binds `0.0.0.0:8822`, needs TLS certs (`sudo tailscale cert <host>.ts.net`, copy `.crt`/`.key` to `~/.claude-remote/`, chown) |

## Verify

```bash
claude-remote doctor
curl http://127.0.0.1:8823/health     # {"status":"ok","version":"0.1.0"}
grep 'REQ ' ~/.claude-remote/server.log | tail   # did the phone reach us?
```

## Troubleshooting

| Problem | Solution |
|---------|----------|
| Service not starting | `make logs`; `launchctl list \| grep claude-remote` |
| Phone types but Claude ignores keys | Missing `TERM`/`LANG` in child env — `childEnv()` must be used for every spawned process |
| Phone can't scroll back | Claude must run with `--settings {"tui":"default"}` (fullscreen TUI = alternate screen, no scrollback) |
| UI change not visible on phone | Run `make install` (copies `static/`); static is `no-cache` so a reload suffices |
| `qr` fails with 503 | Tunnel still starting; retry or `doctor` |
| Old quick-tunnel URL dead | cloudflared restarted → `claude-remote qr` |
| Screen page: "Screen Sharing is off" | Enable Screen Sharing on the Mac |
| Vietnamese text lost on screen page | Use the Paste bar, not live typing (Screen Sharing drops Unicode keysyms) |
| macOS re-asks folder permissions | Create the signing identity, `make install`, grant once more |
| Claude not found | `claude_path` must be absolute |
| TLS cert expired (mode off) | Server skips it with a warning; re-run `sudo tailscale cert` |
| Phone 401 | Re-pair with `claude-remote qr` |
