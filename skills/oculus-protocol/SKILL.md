---
name: oculus-protocol
description: The Oculus app<->daemon wire protocol (WebSocket + typed JSON envelope). Use when adding or changing a message type, an event, or the approval flow.
---

# Oculus protocol

App (SwiftUI/CryptoKit) and daemon (Go) talk over **WebSocket**, carrying a **typed JSON envelope**.
The channel is end-to-end encrypted (see `skills/oculus-crypto`); the relay only forwards ciphertext.

## Envelope
```json
{ "id": "optional-request-id", "type": "message.type", "payload": { ... } }
```
- **Requests** carry `id`; the matching **response** echoes the same `id`.
- **Events** (server→client, no `id`): `output.delta`, `session.status`, `approval.request`, …

## Core message types (v0)
| type | dir | purpose |
|---|---|---|
| `session.list` / `session.get` | app→daemon (req/resp) | overview + detail |
| `session.prompt` | app→daemon | send a follow-up |
| `output.delta` | daemon→app (event) | streamed agent output |
| `session.status` | daemon→app (event) | running / idle / awaiting_approval / done |
| `approval.request` | daemon→app (event) | a tool call needs sign-off (`ApprovalRequest`) |
| `approval.response` | app→daemon | `allow` \| `deny` (+ optional reason) |

## Where it lives / how to change it
- Schema + **golden JSON test vectors**: `protocol/`.
- Go structs: `daemon/protocol/` (mirror the schema).
- Swift `Codable`: `app/…/Protocol/` (mirror the schema).
- **When you add/change a message:** update `protocol/` vectors, the Go structs, AND the Swift structs
  in the same change; run `cd daemon && go test ./protocol/...` (golden vectors must pass on both sides).

## Approval flow (normalized across providers)
- opencode emits `permission.updated` → daemon maps to `approval.request`; app replies `approval.response`
  → daemon calls `POST /session/{id}/permissions/{permissionID}` (`once|always|reject`).
- claude-code `PreToolUse` hook blocks → daemon maps to `approval.request`; the hook's decision is the
  app's `approval.response`.
See `skills/add-a-provider` for wiring a new provider into this flow.
