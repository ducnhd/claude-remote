---
name: handoff
description: Generate QR code to continue current Claude session on phone via claude-remote
user-invocable: true
---

When the user invokes /handoff, follow these steps:

1. Call the MCP tool `status` from the `claude-remote` server to check if the service is running.
   - If the service is not reachable, tell the user: "claude-remote service is not running. Start it with: claude-remote serve"

2. Call the MCP tool `handoff` from the `claude-remote` server with:
   - `dir`: set to the current working directory
   - `mode`: set to "choose"

3. Display the QR code result exactly as returned (it contains ASCII art QR + URL).

4. Tell the user: "Quét QR bằng điện thoại để tiếp tục session. Token hết hạn sau 5 phút."
