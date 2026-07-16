---
name: oculus-daemon
description: The Oculus daemon core (hub) that routes protocol messages between clients and agent sessions. Use when changing request dispatch, event forwarding, approval routing, or the daemon's session lifecycle.
---

# Oculus daemon (hub)

`daemon/hub` is the core: it holds providers + live sessions and drives one client connection.

## Model
- `hub.New()` → `Register(provider)` (by `Name()`).
- `Serve(ctx, *transport.Conn)` loops: `Recv` a protocol envelope → `dispatch`. Blocks until the
  client disconnects.
- **Sessions persist across client disconnects** (the whole point: work runs on the Mac; reconnect
  from the phone). Serve returning does NOT kill sessions.

## Dispatch (client→daemon)
- `session.create {provider,cwd,prompt}` → `provider.Create` → store → spawn `forward` goroutine →
  reply `ok` with a `Session`.
- `session.list` → `ok` `SessionList`. `session.prompt` → `Session.Prompt`. `session.stop` → `Stop`.
- `approval.respond {approval_id,decision}` → look up the owning session (recorded when the
  `ApprovalRequest` was forwarded) → `Session.Respond`.

## forward (daemon→client)
Ranges `Session.Events()`, records `approval_id → session` for any `ApprovalRequest`, encodes each
event and `conn.Send`s it. Multiple sessions share one `conn` (Send is mutex-guarded).

## E2E test
`daemon/hub/hub_test.go` drives the whole spine over the real encrypted transport (in-memory MsgConn
pair) against an opencode stub: create → output → approval → respond → idle. Run:
`cd daemon && go test ./hub/` (compile-and-run a binary if the harness backgrounds `go test`; a
persistent SSE means the stub server needs `CloseClientConnections()` in teardown).
