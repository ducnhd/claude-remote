---
name: testing
description: Testing strategy — unit tests for auth/config/files, integration tests for server+WebSocket+pty, table-driven patterns
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
| `auth_test.go` | JWT sign/verify, token generation, single-use, expiry |
| `config_test.go` | Config load/save, defaults, JSON persistence |
| `files_test.go` | Path validation, allowlist, dotfile blocking, traversal, symlink resolution |
| `terminal_test.go` | RingBuffer, pty spawn+echo, WebSocket I/O |
| `server_test.go` | Route registration, health endpoint, auth middleware |
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

## Running Tests

```bash
go test ./... -v -count=1              # All tests
go test -run TestJWT -v                # Single test
go test -race ./... -v -count=1        # Race detector
```

## What NOT to Test

- Tailscale connectivity (external dependency)
- xterm.js rendering (browser-side, hidden anyway)
- launchd plist loading (OS-level)
- QR code visual output (library responsibility)
- Vietnamese IME behavior (browser-specific)
