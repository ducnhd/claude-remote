---
name: testing
description: Testing strategy — unit tests for auth/files, integration tests for WebSocket+pty, table-driven patterns
---

# Testing Strategy

## Principles

- Test each component in isolation before integration
- Table-driven tests for input validation (paths, tokens, JWTs)
- Integration tests spawn real processes (not claude — use `echo` or `cat`)
- No mocks for filesystem — use `t.TempDir()`

## Test Files

| File | Tests |
|------|-------|
| auth_test.go | JWT sign/verify, token generation, expiry, revocation |
| files_test.go | Path validation, allowlist, dotfile blocking, traversal |
| terminal_test.go | WebSocket + pty integration (spawn `echo`, verify output) |
| config_test.go | Config load/save, defaults |

## Auth Tests (auth_test.go)

```go
func TestGenerateToken(t *testing.T)     // 64 chars, crypto random
func TestJWTSignVerify(t *testing.T)     // round-trip sign → verify
func TestJWTExpired(t *testing.T)        // reject expired token
func TestJWTWrongSecret(t *testing.T)    // reject after revoke
func TestTokenSingleUse(t *testing.T)    // second scan fails
```

## File Browser Tests (files_test.go)

Table-driven:
```go
tests := []struct {
    name      string
    path      string
    allowed   []string
    wantErr   bool
}{
    {"valid path", "/Users/dat/Desktop", []string{"/Users/dat"}, false},
    {"outside allowlist", "/etc/passwd", []string{"/Users/dat"}, true},
    {"path traversal", "/Users/dat/../etc/passwd", []string{"/Users/dat"}, true},
    {"dotfile blocked", "/Users/dat/.ssh/id_rsa", []string{"/Users/dat"}, true},
    {"claude-remote dir", "/Users/dat/.claude-remote/secret.key", []string{"/Users/dat"}, true},
}
```

## Terminal Tests (terminal_test.go)

Integration test with real pty:
```go
func TestTerminalEcho(t *testing.T)      // spawn "echo hello", verify WebSocket receives "hello"
func TestTerminalResize(t *testing.T)    // send resize, verify pty window size
func TestTerminalReconnect(t *testing.T) // disconnect + reconnect, verify buffer replay
```

Use `cat` or `echo` instead of `claude` in tests — fast, deterministic.

## Running Tests

```bash
go test ./... -v -count=1              # All tests
go test -run TestJWT -v                # Single test
go test -run TestFileBrowser -v        # File browser tests
go test -race ./...                    # Race detector (WebSocket concurrency)
```

## What NOT to Test

- Tailscale connectivity (external dependency)
- xterm.js rendering (browser-side)
- launchd plist loading (OS-level)
- QR code visual output (library responsibility)
