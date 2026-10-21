---
name: auth-flow
description: QR pairing + handoff tokens + rolling JWT cookie — token TTLs, SameSite=Lax, Secure from X-Forwarded-Proto, rate limits, revocation
---

# Auth Flow

## First-Time Pairing

```
Mac                                       Phone
───                                       ─────
1. claude-remote setup   → secret.key (256-bit)
2. claude-remote serve   (or launchd)
3. claude-remote qr      → POST 127.0.0.1:8823/local/pair
   server mints setup token (30 min),
   returns <tunnel URL>/auth/scan?token=…
   (503 while the tunnel is not up)
                                          4. Scan QR → GET /auth/scan?token=…
                                          5. Token checked (constant time)
                                          6. Old cookie cleared, JWT cookie set
                                          7. Redirect → /
```

Setup tokens are **reusable until expiry**: camera apps and link previewers prefetch the URL, and burning the token on that first fetch broke pairing.

## Handoff

1. Claude Code calls MCP `handoff {dir, mode}` on `127.0.0.1:8823/mcp`
2. Server mints a one-time handoff token (15 min, `handoffTokenTTL`, in memory)
3. Phone scans `/handoff?token=…&dir=…&mode=…` → old cookie cleared → new JWT → redirect with params
4. `mode=choose` shows Attach / Continue / Choose folder

## JWT Cookie

| Property | Value |
|----------|-------|
| Name | `claude-remote-auth` |
| Algorithm | HS256, secret `~/.claude-remote/secret.key` |
| Claims | `device_id`, `iat`, `exp` |
| Lifetime | 90 days, **rolling**: any request past 1/3 of the lifetime gets a fresh cookie |
| HttpOnly | true |
| SameSite | **Lax** always — the QR opens from the camera app (cross-site); Strict drops the cookie on redirect |
| Secure | from `X-Forwarded-Proto` / TLS (`isSecureRequest`) — behind cloudflared the local hop is plain HTTP |

`setAuthCookie(w, jwt)` expires the old cookie first, then sets the new one (stale cookies with an old signature caused 401 loops).

## Protection

- Public, no auth: static files, `/health`, `/auth/scan`, `/handoff`
- JWT (`protect()`): `/api/*`, `/ws/term`, `/ws/vnc`
- `/ws/vnc` also requires same-origin; `/api/clipboard` requires `Content-Type: application/json`
- Loopback mux only, and rejects proxy headers: `/mcp`, `/local/pair`, `/local/status`
- Rate limited per client IP: `/auth/scan`, `/handoff`
- Denied requests are logged with path + reason; frontend shows an auth error on 401

## Revocation

`claude-remote revoke` → new `secret.key`, `sessions.json` removed → every existing JWT fails signature check → re-pair with `claude-remote qr`.
