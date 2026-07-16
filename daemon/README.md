# daemon/ — oculusd (Go 1.26)

The Oculus daemon. Drives coding agents on the host and exposes the E2E-encrypted WebSocket protocol
to the apps. Single static binary; builds for macOS + Linux (runs on your Mac via launchd, or headless
on a dev box / cloud).

## Build & run
```sh
go build ./...
go test ./...
go run . --version
```

## Responsibilities (as they land)
- **Providers:** `opencode` (attach to `opencode serve`, HTTP/SSE) and `claude-code` (headless
  `stream-json` + `PreToolUse` hook). See `../skills/add-a-provider`.
- **Protocol:** serve `../protocol` over WebSocket; see `../skills/oculus-protocol`.
- **Crypto:** X25519 → HKDF-SHA256 → ChaCha20-Poly1305 channel; see `../skills/oculus-crypto`.
- **Relay:** dial the relay outbound for access from anywhere; see `../relay`.
- **Approvals:** normalize provider permission prompts to `approval.request` / `approval.response`.
