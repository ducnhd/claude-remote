---
name: auth-flow
description: QR code + JWT authentication — one-time scan, persistent cookie, revocation mechanism
---

# Auth Flow

## First-Time Setup

```
Mac terminal                         Phone
─────────────                        ─────
1. claude-remote setup
2. Generate secret.key (256-bit)
3. Generate one-time token (64-char)
4. Display QR code in terminal
   URL: https://<host>:8822/auth/scan?token=<token>
                                     5. Scan QR → browser opens URL
                                     6. Server verifies token (one-time)
                                     7. Server issues JWT cookie
                                        (httpOnly, secure, 90 days)
                                     8. Redirect → main page
```

## Subsequent Visits

Phone opens `https://<host>:8822` → JWT cookie sent automatically → valid → access granted.

## Token Rules

- 64 characters, crypto/rand
- Single-use: deleted from memory after first successful scan
- No expiry (consumed immediately or never)
- Only one pending token at a time — `setup` replaces any existing token

## JWT Details

- Algorithm: HS256
- Secret: `~/.claude-remote/secret.key`
- Payload: `{device_id: <uuid>, iat: <unix>, exp: <unix>}`
- Cookie: `claude-remote-auth`, httpOnly, secure, SameSite=Strict, 90-day maxAge
- Validation: check signature + expiry on every request

## Revocation

`claude-remote revoke`:
1. Generate new `secret.key`
2. Clear `sessions.json`
3. All existing JWTs become invalid (signature mismatch)
4. User must re-run `setup` + scan QR again

## Middleware

All routes except `/auth/*` require valid JWT. Invalid/missing → 401 JSON response. Frontend detects 401 → shows "Scan QR to connect" message.
