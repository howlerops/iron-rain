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

## Reference: opencode (`daemon/agent/opencode`)
- `POST /session` → id; `POST /session/{id}/message` → start/prompt; `GET /event` (SSE, global —
  filter by your sessionID); `POST /session/{id}/permissions/{permissionID}` `{response: once|always|reject}`
  (allow→`once`, deny→`reject`); `POST /session/{id}/abort` → stop.

## Next: claude-code (`daemon/agent/claudecode`)
- Headless `stream-json`; a `PreToolUse` hook that blocks and calls back to the daemon becomes the
  `ApprovalRequest`; the hook's returned decision is the `Respond`.

## Test it
Write a stub server that emits the backend's real event shapes and drive create → output → approval →
respond → idle (see `daemon/agent/opencode/opencode_test.go`). **Flush SSE headers in the stub** or the
client's stream open blocks. Run: `cd daemon && go test ./agent/...`.
