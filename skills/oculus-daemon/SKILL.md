---
name: oculus-daemon
description: The Oculus daemon core (hub) that routes protocol messages between clients and agent sessions. Use when changing request dispatch, event forwarding, approval routing, or the daemon's session lifecycle.
---

# Oculus daemon (hub)

`daemon/hub` is the core: it holds providers + live sessions and drives one client connection.

## Providers auto-detect (`autodetect.go`)
`oculusd serve` (no provider flags) enables every provider present on the host — `enableProviders`
registers: a running/installed **opencode** (reuse a discovered `opencode serve`, else start one if
the binary exists), the **claude-code** sidecar if `claude`+`node`+`sidecar.mjs`(+`node_modules`) are
found (candidates: `$OCULUS_CLAUDE_SIDECAR`, cwd, exe-dir, `~/.oculus/claude-sidecar`), and **pi** if
on PATH. `--opencode/--claude-sidecar/--pi` only OVERRIDE a specific provider's detection. No provider
found → warn + serve anyway.

**claude-code auto-setup:** the sidecar's `sidecar.mjs`+`package.json` are `go:embed`ded into the
daemon (`agent/claudecode/embed.go`); when claude+node are present but no installed sidecar is found,
`detectOrSetupClaudeSidecar` materializes them into `~/.oculus/claude-sidecar` and runs `npm install`
(prefers npm, falls back to bun). `--claude-setup=ask|auto|off` (default **ask** — prompts on a TTY,
skips on non-interactive stdin; `auto` installs silently; `off` never installs). Verified live: a fresh
HOME auto-installs 102 pkgs and registers claude-code.

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
- `session.list` → `ok` `SessionList` of primary sessions only; child/sub-agent sessions (`parent_id`
  set) stay inline under their parent transcript and must not appear as top-level rows.
  `session.prompt`/`session.stop` → `managed(id).sess.*`.
- `approval.respond {approval_id,decision}` → look up the owning `managedSession` (recorded in
  `run()`), `Respond`, then **broadcast `approval.resolved`** so the card clears on every client.

## Role gates
Role checks live in `hub/roles.go` and must be enforced in daemon dispatch, not the client. Observers
are watch-only. Steerers may drive live session actions (`session.prompt`, `session.create`,
`session.stop`, `run.test`, `fs.write`, worktree actions). Owners may do everything, including
approval responses and persisted daemon/admin configuration (`device.*`, accounts, agents, projects,
integrations, telemetry, MCP upsert/delete/enable/import/exclusive/check). New mutating request
handlers must call `requireCapability` before side effects and must return a `bad <type>` error when
`env.Unmarshal` fails; do not continue with zero-valued payload structs.

## broadcast + subscribers
`managedSession.broadcast(raw)` records to the transcript and sends to all subs (snapshot under lock,
send outside). `Serve` adds the conn to `clients` (global broadcasts like `approval.resolved`) and, on
disconnect, `unsubscribe`s it from every session. **Sessions persist** across client disconnects; they
end only when the provider's `Events()` channel closes (`run` → `removeSession`).

## Restart persistence
`hub/persist.go` stores the session's working metadata as JSON (`persistedMeta`): cwd, project,
worktree/workspace state, selected roots, parent/subtask linkage, provider URL, model, and mode.
Restores must read that meta before attaching so resumed agents run in the original project and keep
their safety mode/model instead of starting cold or in the wrong directory. Server-backed providers
whose sessions live outside the daemon should expose their exact endpoint (`BaseURL() string`, as
opencode does) so newly created sessions persist the server that owns the conversation.

## E2E test
`daemon/hub/hub_test.go` drives the whole spine over the real encrypted transport (in-memory MsgConn
pair) against an opencode stub: create → output → approval → respond → idle.
`daemon/hub/multiclient_test.go` proves the shared-session model: two clients, one session — B
`subscribe`s and gets the approval via transcript replay; A answers → **both** get `approval.resolved`.
Run:
`cd daemon && go test ./hub/` (compile-and-run a binary if the harness backgrounds `go test`; a
persistent SSE means the stub server needs `CloseClientConnections()` in teardown).

## Connection and tool-card diagnostics
- A client can disconnect after WebSocket accept but before the encrypted handshake finishes (app
  reconnects, update restarts, sleep/wake). Log closed-reader/closed-network errors as client
  disconnects, not scary handshake failures; authorization and malformed handshakes must still log as
  handshake failures.
- opencode `message.part.updated` tool payloads vary by version. Preserve rich tool cards by reading
  both nested `state.status/title/output/error` and top-level `status/title/output/error` plus
  structured `input`/`args`/`metadata` command/path/query fields. A bash card showing only `bash`
  usually means this projection lost the command summary.
