---
name: auth-flow
description: QR code + JWT authentication — one-time scan, persistent cookie, TLS-conditional security, revocation
---

# Auth Flow

## First-Time Setup

```
Mac terminal                         Phone
─────────────                        ─────
1. claude-remote setup
2. Generate secret.key (256-bit)
3. Generate one-time token (64-char)
4. Auto-detect TLS (search *.crt in
   ~/.claude-remote, ~/Desktop,
   /var/run/tailscale)
5. Display QR code in terminal
   URL: <proto>://<host>:8822/auth/scan?token=<token>
                                     6. Scan QR → browser opens URL
                                     7. Server verifies token (one-time)
                                     8. Server issues JWT cookie
                                     9. Redirect → / (folder picker)
```

## Subsequent Visits

Phone opens `https://<host>:8822` → JWT cookie sent → valid → access granted → auto-reconnect to running session if any.

## Token Rules

- 64 characters, crypto/rand
- Single-use: cleared from memory after first scan
- Persisted to `.pending-token` file so `serve` can load it after `setup`
- Only one pending token at a time

## JWT Details

- Algorithm: HS256
- Secret: `~/.claude-remote/secret.key`
- Payload: `{device_id: <device-timestamp>, iat: <unix>, exp: <unix>}`
- Expiry: 90 days
- Cookie name: `claude-remote-auth`

## Cookie Security (TLS-conditional)

| Setting | With TLS | Without TLS |
|---------|----------|-------------|
| Secure | true | false |
| SameSite | Strict | Lax |
| HttpOnly | true | true |
| MaxAge | 90 days | 90 days |

## QR URL Protocol Detection

`setup` auto-detects TLS cert availability to generate correct protocol in QR URL:
- Searches `~/.claude-remote/`, `~/Desktop`, `/var/run/tailscale` for `*.crt`
- Found → `https://`, not found → `http://`
- Displayed in setup output: `Protocol: HTTPS` or `Protocol: HTTP`

## Revocation

`claude-remote revoke`:
1. Generate new `secret.key`
2. Clear `sessions.json`
3. All existing JWTs invalid (signature mismatch)
4. Re-run `setup` + scan QR

## Middleware

All routes except `/auth/*` and `/health` require valid JWT. Invalid/missing → 401 JSON `{"error":"unauthorized"}`. Frontend detects 401 → `location.reload()`.
