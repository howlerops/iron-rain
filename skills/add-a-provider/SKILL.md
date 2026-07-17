---
name: add-a-provider
description: How to add a coding-agent provider (opencode, claude-code, ...) to the Oculus daemon. Use when integrating a new agent backend or changing session/approval translation.
---

# Add a provider

A provider adapts one agent backend to the uniform `agent` interface the daemon drives. Lives in
`daemon/agent/<name>/`.

## Implement `agent.Provider` + `agent.Session`
- `Provider`: `Name()`, `Create(ctx, cwd, prompt) (Session, error)`, `List(ctx) ([]protocol.Session, error)`.
- `Session`: `ID()`, `Provider()`, `Events() <-chan agent.Event`, `Prompt`, `Respond(approvalID, decision)`,
  `Stop`, `Close`.
- Translate the backend's stream into `agent.Event{Type, Payload}` using `protocol` payloads:
  - output text → `protocol.OutputDelta`
  - a permission/tool-approval prompt → `protocol.SessionStatus{awaiting_approval}` **then**
    `protocol.ApprovalRequest{ApprovalID, SessionID, Tool, Input}`
  - completion/idle → `protocol.SessionStatus{idle|done}`
- `Respond` maps `protocol.DecisionAllow/Deny` to the backend's native decision.

## Reference: opencode (`daemon/agent/opencode`) — event shapes verified vs 1.17.19
- `POST /session` → id; `POST /session/{id}/message` → start/prompt; `GET /event` (SSE, global —
  filter by your sessionID); `POST /session/{id}/permissions/{permissionID}` `{response: once|always|reject}`
  (allow→`once`, deny→`reject`); `POST /session/{id}/abort` → stop.
- **Streaming output** = `message.part.delta` with `properties.{sessionID, field:"text", delta}` —
  NOT `message.part.updated` (that carries the full accumulated part, incl. the echoed user prompt).
  Stream deltas only, or you double-emit. Idle = `session.idle`.
- **Approval** = `permission.asked` with `properties.{id, sessionID, permission, metadata}` (older
  builds: `permission.updated` + `properties.type`; handle both). Reply: `POST
  /session/{id}/permissions/{permID}` `{response: once|always|reject}` (unchanged).
- **`POST /message` blocks server-side until the turn yields** (it parks on a permission ask). Fire it
  **async** and drive progress from SSE — a synchronous prompt deadlocks (you can't answer the very
  approval the turn is waiting on). See `opencode.session.Prompt`.

## claude-code (`daemon/agent/claudecode`) — persistent streaming via a Node sidecar
- **Not** single-shot `-p` anymore. The old `claude -p` + `PreToolUse` HTTP hook **doesn't block**
  in `-p` mode (anthropics/claude-code#36071) — tools ran unapproved. Now a **Node sidecar**
  (`sidecar/sidecar.mjs`, Claude Agent SDK) runs one persistent streaming session; the SDK's
  `canUseTool` callback genuinely blocks the tool until answered.
- The Go provider spawns the sidecar and speaks **line-delimited JSON over stdio** (protocol at the
  top of `claudecode.go`): `prompt`/`approval`/`stop` in; `session`/`text`/`thinking`/`tool`/
  `approval`/`idle`/`error` out. `New([]string{"node", sidecarPath})`; enable via `--claude-sidecar`.
- Auth: the Agent SDK needs **`ANTHROPIC_API_KEY`** (no claude.ai login for the SDK).
- Streaming deltas via the SDK's `includePartialMessages` (`stream_event` → `content_block_delta`,
  `text_delta`/`thinking_delta`); tool runs via `content_block_start`.

## Test it (two layers)
1. **Stub/fake** (fast, offline): emit the backend's real event shapes and drive create → output →
   approval → respond → idle (see `daemon/agent/opencode/opencode_test.go`). **Flush SSE headers in the
   stub** or the client's stream open blocks. Run: `cd daemon && go test ./agent/...`.
2. **Live** (opt-in, spends LLM): a guarded `live_test.go` that runs vs the real tool — this is how the
   two shape bugs above were caught. Run e.g.
   `OCULUS_OPENCODE_URL=http://127.0.0.1:PORT go test ./agent/opencode/ -run TestLive`. **Get the real
   event shapes from a raw capture, not the docs** (`curl -sN .../event` while sending a prompt).
