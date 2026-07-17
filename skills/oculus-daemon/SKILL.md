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

## Single-session-broadcast model (`hub/session.go`)
The daemon is the **fan-out point**: one `managedSession` per agent session, shared by every
subscribed client. One `run()` goroutine reads the provider stream ONCE, records each event to a
`transcript`, and **broadcasts** to all subscribers. `subscribe(conn)` replays the transcript so a
late joiner is caught up, then live events flow. This is the only model that works across providers
(opencode fans out for you, but claude-code/pi are single stdio pipes where the daemon MUST be sole
owner — see the pressure-test verdict). `sessions`/`approvals` are `map → *managedSession`.

## Dispatch (client→daemon)
- `session.create {provider,cwd,prompt}` → `provider.Create` → `addSession` → `subscribe(creator)` →
  `go managedSession.run()` → reply `ok` with a `Session`.
- `session.attach {provider,session_id,url}` → **if the daemon already owns it, just `subscribe`**
  (no duplicate provider subscription); else `Attacher.Attach` → `addSession` → subscribe → run.
- `session.subscribe {session_id}` → `subscribe` to an already-owned session (replays transcript).
- `session.list` → `ok` `SessionList`. `session.prompt`/`session.stop` → `managed(id).sess.*`.
- `approval.respond {approval_id,decision}` → look up the owning `managedSession` (recorded in
  `run()`), `Respond`, then **broadcast `approval.resolved`** so the card clears on every client.

## broadcast + subscribers
`managedSession.broadcast(raw)` records to the transcript and sends to all subs (snapshot under lock,
send outside). `Serve` adds the conn to `clients` (global broadcasts like `approval.resolved`) and, on
disconnect, `unsubscribe`s it from every session. **Sessions persist** across client disconnects; they
end only when the provider's `Events()` channel closes (`run` → `removeSession`).

## E2E test
`daemon/hub/hub_test.go` drives the whole spine over the real encrypted transport (in-memory MsgConn
pair) against an opencode stub: create → output → approval → respond → idle.
`daemon/hub/multiclient_test.go` proves the shared-session model: two clients, one session — B
`subscribe`s and gets the approval via transcript replay; A answers → **both** get `approval.resolved`.
Run:
`cd daemon && go test ./hub/` (compile-and-run a binary if the harness backgrounds `go test`; a
persistent SSE means the stub server needs `CloseClientConnections()` in teardown).
