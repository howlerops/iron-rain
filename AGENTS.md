# Oculus — agent instructions (AGENTS.md)

> This is the **universal** instruction file for any coding agent working on Oculus
> (claude-code, opencode, codex, pi, cursor, …). `CLAUDE.md` just includes this file.
> Pair it with the skills in [`skills/`](skills/) — see **Working practice** below.

## What Oculus is
A **native Apple (macOS + iOS) Agent Development Environment (ADE)** — an IDE *for coding agents*, not
a code editor. You launch, monitor, steer, and **approve** coding agents. The Mac runs the work; the
iPhone is a first-class remote control. MVP: drive **claude-code** and **opencode** sessions from
anywhere, including approving tool calls from the lock screen.

Full design: [`docs/plan-native-ade.md`](docs/plan-native-ade.md).

## Architecture (locked)
```
 SwiftUI app (iOS + macOS) ── WS + typed JSON, E2E encrypted ──▶ Go daemon ──┬─ opencode (opencode serve: HTTP/SSE)
   (CryptoKit)                  direct/LAN, else via relay                    └─ claude-code (headless stream-json + PreToolUse hook)
```
- **`app/`** — SwiftUI universal app (the product). CryptoKit for the client half of E2EE.
- **`daemon/`** — **Go 1.26**, single static binary, runs anywhere (Mac launchd, Linux, cloud). Drives
  the providers, exposes the protocol over WebSocket, dials the relay outbound.
- **`relay/`** — stateless ciphertext forwarder (sees only encrypted frames + routing IDs). Hosted +
  self-host, installer-driven.
- **`protocol/`** — the shared contract: typed JSON envelope `{id?, type, payload}` + golden test
  vectors that lock Go↔Swift parity.

### Locked technical decisions (do not re-litigate without a doc update)
- Daemon **Go 1.26**; app **SwiftUI**; **decoupled** (no FFI) — app talks to daemon only over the protocol.
- E2EE: **X25519 ECDH → HKDF-SHA256 → ChaCha20-Poly1305** (CryptoKit-friendly; not Noise).
- Approvals are **first-class in v0**: opencode `POST /session/{id}/permissions/{permissionID}`
  (`once|always|reject`) and claude-code `PreToolUse` hook both normalize to one
  `ApprovalRequest`/`ApprovalResponse(allow|deny, reason?)`.
- Push: hosted APNs default + self-host BYO-key. License **MIT**.
- **Session autodetection** is first-class: the daemon discovers running `opencode serve` instances
  (+ their live sessions) and recent claude-code transcripts on the host, exposed via `discover.list`.
  See `daemon/discovery` + `skills/oculus-discovery`; try `oculusd discover`.

## Build & run (as components land)
- Daemon: `cd daemon && go build ./... && go test ./...` (Go 1.26).
- Protocol parity: `cd daemon && go test ./protocol/...` runs the golden-vector tests.
- App: `cd app && xcodegen generate` → `Oculus.xcodeproj` (universal iOS + macOS, both share
  `OculusUI.ContentView`). Open in Xcode, or build headless:
  `xcodebuild -project app/Oculus.xcodeproj -scheme Oculus-iOS -destination 'generic/platform=iOS Simulator' build`.
  Quick macOS dev harness without Xcode: `cd app/OculusApp && swift run`.
- Relay: see `relay/README.md` (self-host) — hosted default configured by the installer.

## Working practice — **definition of done for every component**
Every component ships with **three** things, always:
1. **Code** (+ tests where it's logic/crypto/protocol).
2. A section in the nearest **`AGENTS.md`** (root or package-level) describing how to use/extend it.
3. A portable **`SKILL.md`** skill in [`skills/`](skills/) so *any* agent can pick up the work.

Skills use the Agent Skills (`SKILL.md` + frontmatter) format so they're portable across agents;
`scripts/sync-skills.sh` (or `npx skills`) fans them into each agent's native location. When you add or
change a subsystem, **write/update its skill in the same change** — that's what keeps Oculus easy to
work on long-term.

## Conventions
- Small, reviewable changes. Match surrounding style. No secrets in the repo (env/keychain only).
- The **protocol is the contract**: change `protocol/` + its golden vectors, and update both the Go and
  Swift sides in the same PR.
- Keep the daemon platform-agnostic (it must build for Linux too — no macOS-only assumptions in core logic).
