---
name: claude-remote
description: Hand the current Claude Code session off to the user's phone with claude-remote (QR code), pair a phone, or check/fix the claude-remote service. Use whenever the user wants to continue, view or control this work from their phone or mobile — "handoff to phone", "continue on my phone", "QR for phone", "send this to my phone", "chuyển sang điện thoại", "tiếp tục trên điện thoại", "mở trên điện thoại", "quét QR", "handoff", "claude-remote", "/handoff".
user-invocable: true
---

# claude-remote: continue on the phone

claude-remote runs on this machine and exposes Claude Code to the phone browser. Its MCP server (`claude-remote`, `http://127.0.0.1:8823/mcp`) has two tools: `status` and `handoff`.

## Handoff (default when the user mentions the phone)

1. Call the MCP tool `status` from the `claude-remote` server.
   - Tool missing → MCP not registered. Tell the user to run:
     `claude mcp add --transport http -s user claude-remote http://127.0.0.1:8823/mcp`
   - Tool fails / not reachable → the service is down. Start it:
     Windows: `claude-remote install` (or `~\bin\claude-remote.exe install`); macOS: `make restart` or `claude-remote serve`. Then `claude-remote doctor`.
2. Call the MCP tool `handoff` with:
   - `dir`: the current working directory, absolute.
   - `mode`: `"choose"`, unless the user asked to attach to the running terminal (`"attach"`) or continue the conversation (`"continue"`).
   - Error "directory not allowed" → the folder is outside `allowed_dirs` in `~/.claude-remote/config.json`. Say so and offer to add its parent folder (the server must restart afterwards: `claude-remote install` on Windows, `make restart` on macOS).
3. Show the returned QR code exactly as returned, inside a code block so the ASCII art stays aligned, plus the URL.
4. Tell the user: scan it with the phone camera; the link works once and expires in 15 minutes.

## Other requests

- Pair a new phone without handing off a session: run `claude-remote qr` (Windows: `~\bin\claude-remote.exe qr`) and show its output.
- "It doesn't work" / phone cannot connect: run `claude-remote doctor` and follow the fixes it prints.
