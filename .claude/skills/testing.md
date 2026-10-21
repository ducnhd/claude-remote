---
name: testing
description: Testing strategy — unit tests for auth/config/files/paths/tunnel/ratelimit, VNC + clipboard, MCP, integration tests for server+WebSocket+pty, table-driven patterns
---

# Testing Strategy

## Principles

- Test each component in isolation before integration
- Table-driven tests for input validation (paths, tokens, JWTs)
- Integration tests spawn real processes (`echo`, `cat` — not `claude`)
- No mocks for filesystem — use `t.TempDir()`
- Terminal tests pre-start the terminal via `StartInDir("")` before WebSocket connection

## Test Files

| File | Tests |
|------|-------|
| `auth_test.go` | JWT sign/verify, setup token TTL + reuse, handoff token one-time, expiry |
| `config_test.go` | Config load/save, defaults (tunnel mode quick), JSON persistence |
| `files_test.go` | Path validation, allowlist, dotfile blocking, traversal, symlink resolution |
| `paths_test.go` | `pathWithin` component matching, missing allowed roots never match |
| `ratelimit_test.go` | Token bucket, `CF-Connecting-IP` trusted only from loopback, `isSecureRequest`, `viaProxy` |
| `tunnel_test.go` | cloudflared args, URL scraping, disabled = no-op, named needs token, `BindHost` |
| `terminal_test.go` | RingBuffer + replay, pty spawn/echo, `--continue` args, WebSocket I/O, concurrent writes, restart keeps new session, resize control, `childEnv` (TERM/LANG/PATH) |
| `vnc_test.go` | VNC byte pipe, foreign Origin rejected, Screen Sharing off → 502, clipboard keeps Vietnamese, form post rejected |
| `mcp_test.go` | initialize/tools/list/call, handoff escaping + allowlist, rejects non-local, tunnel-forwarded, and public mux |
| `server_test.go` | Health, `/auth/scan` + `/handoff` (valid/invalid, escaping, mode check, rate limit), protected routes, start/resume + sibling-prefix rejection, Secure cookie behind tunnel, rolling cookie refresh, `/local/pair` rejects forwarded requests |
| `integration_test.go` | Full auth flow (setup→scan→JWT), WebSocket terminal, file browser API |

## Key Test Patterns

### Terminal tests require pre-start
```go
// WebSocket no longer auto-starts terminal — must call StartInDir first
tm := NewTerminalManager("cat", nil)
tm.StartInDir("")  // pre-start before WebSocket connect
defer tm.Stop()
```

### File browser symlink resolution
```go
// t.TempDir() returns /var/folders/... which resolves to /private/var/folders/...
// AllowedDirs must also be resolved
resolvedDir, _ := filepath.EvalSymlinks(dir)
fb := NewFileBrowser([]string{resolvedDir})
```

### Server tests create full Config
```go
cfg := &Config{Port: 0, AllowedDirs: []string{dir}, ClaudePath: "echo", DataDir: dir}
cfg.Save()
auth := NewAuth(cfg.SecretPath())
auth.GenerateSecret()
server := NewServer(cfg, auth)
```

### Swap package-level seams instead of mocking
```go
// vnc.go exposes vars for tests: point them at a fake listener / script
old := vncAddr
vncAddr = ln.Addr().String()           // local TCP listener standing in for :5900
t.Cleanup(func() { vncAddr = old })
pbcopyPath = fakeScript                // writes stdin to a temp file
```

### Tunnel-facing security
Requests from cloudflared arrive on loopback, so tests for `/mcp` and `/local/*` must also send proxy headers (`X-Forwarded-For`, `CF-Connecting-IP`) and expect rejection — a loopback-only check is not enough.

## Running Tests

```bash
go test ./... -v -count=1              # All tests
go test -run TestJWT -v                # Single test
go test -race ./... -v -count=1        # Race detector
```

## What NOT to Test

- Tailscale / Cloudflare connectivity (external dependency; tunnel tests cover args + URL parsing only)
- Real macOS Screen Sharing or pbcopy (swapped via `vncAddr` / `pbcopyPath`)
- xterm.js rendering (browser-side, hidden anyway)
- launchd plist loading (OS-level)
- QR code visual output (library responsibility)
- Vietnamese IME behavior (browser-specific)
