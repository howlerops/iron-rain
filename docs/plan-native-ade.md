# Plan — Oculus: Native Apple Agent Development Environment (ADE)

**Status: LOCKED (2026-07-16). Building.** Supersedes the OpenChamber-fork / Oculus-rebrand direction
(archived at `reference/oculus`).

## Vision
A **native Apple application for Mac + iPhone** that is an **Agent Development Environment** — an
IDE *for agents*, not a code editor. You launch, steer, review, and **approve** coding agents; the Mac
is where work runs, the iPhone is a first-class control surface. *Close the laptop, keep shipping.*

## MVP v0 — "remotes" for claude-code + opencode
Start a **claude-code** or **opencode** session on your Mac; **monitor, steer, and approve it from your
iPhone (and back), from anywhere.** Delivers:
- **Session overview** — every running agent, live status (running / awaiting input / **awaiting
  approval** / done).
- **Live stream + steer** — real-time output (no polling), send follow-ups.
- **Human-in-the-loop approvals (v0, first-class)** — a tool call needing sign-off pushes to the phone;
  approve/deny from the lock screen.
- **From anywhere** — E2EE relay (outbound-only), push for status + approvals.
- Two providers only (claude-code + opencode). Depth over breadth.

Non-goals for v0: cloud/ephemeral agents (Mode-2, later), Android, web, voice, provider catalog.

---

## Locked stack

| Layer | Decision |
|---|---|
| **App** | **SwiftUI universal** (iOS + macOS; `MenuBarExtra`, ActivityKit, App Intents, Handoff, APNs) + **CryptoKit**. Talks to the daemon **only over the protocol** (decoupled — no FFI in v0). |
| **Daemon** | **Go 1.26** — single static binary, cross-compiles anywhere. `tokio`-equivalent stdlib + `nhooyr/coder websocket`, `golang.org/x/crypto`, `os/exec`. Runs as a launchd agent on Mac (app-supervised); same binary runs headless on Linux/dev-box/cloud. |
| **Relay** | Stateless **ciphertext forwarder** (sees only encrypted frames + routing IDs). **Hosted default + self-host**, installer-driven. Impl language irrelevant (small Worker/Fly service). |
| **Protocol** | WebSocket + **typed JSON envelope** `{id?, type, payload}`: request/response + server→client events (output deltas, status, `ApprovalRequest`). Go structs ↔ Swift `Codable`, locked by **golden-JSON parity tests**. |
| **Crypto / pairing** | **X25519 ECDH → HKDF-SHA256 → ChaCha20-Poly1305** channel (CryptoKit-friendly; not Noise). Pairing = TOFU on the daemon's static pubkey (QR / LAN / login) + per-device client key + daemon pairing secret. |
| **Providers** | `Provider` interface. **opencode:** attach to `opencode serve` (HTTP + SSE). **claude-code:** headless `stream-json` + `PreToolUse` hook → daemon callback. Installer detects + configures both. |
| **Approvals** | First-class in v0. opencode permission `ask` + claude-code `PreToolUse` normalize to one `ApprovalRequest`/`ApprovalResponse(allow|deny, reason?)` → actionable lock-screen push. |
| **Push** | **Hosted APNs sender by default + self-host BYO-APNs-key** (installer-driven, mirrors the relay). |
| **License / repo** | **MIT**, monorepo re-initializing **`howlerops/oculus`**. |

## Architecture (decoupled, E2E encrypted)
```
 iPhone (SwiftUI + CryptoKit) ─┐
 Mac (SwiftUI menu-bar +       ├─ WS + typed JSON, E2E encrypted ─┐
   Live Activities)            │   · direct / LAN when reachable  │
 (CLI later)                 ──┘   · else via relay (ciphertext)  │
                                                                  ▼
                                   Go daemon (launchd on Mac; run-anywhere)
                                     ├─ opencode  (opencode serve: HTTP/SSE)
                                     └─ claude-code (headless stream-json + PreToolUse hook)
```
The app↔daemon channel is end-to-end encrypted; the relay only forwards ciphertext by serverID.

## Providers + approvals — VERIFY in the P0 spike (make-or-break for "approvals in v0")
- **claude-code:** `PreToolUse` hook must be able to **block and return allow/deny** to the CLI. Verify.
- **opencode:** must expose the permission **`ask` + programmatic allow/deny over the server API**
  (not just the TUI). Verify.
If either can't intercept programmatically, we adjust the approval UX for that provider before building
the protocol on the assumption.

## Cross-agent skills & docs (working practice — applies to every component)
Make Oculus easy to work on long-term with *any* agent (claude-code, opencode, codex, pi, cursor):
- **`AGENTS.md`** (root + per-package): universal instructions every agent reads. `CLAUDE.md` = include of `AGENTS.md`.
- **`skills/`**: source-of-truth skills in the portable **SKILL.md** format (Agent Skills spec); a
  `scripts/sync-skills` (or `npx skills`) fans them into each agent's native location.
- **Definition of done for every component = code + `AGENTS.md` section + a `SKILL.md`.** First skills:
  `oculus-protocol`, `oculus-crypto` (Go↔Swift parity), `add-a-provider`, `oculus-relay`,
  `authoring-oculus-skills` (meta).

## Repo layout (`howlerops/oculus`, MIT)
```
AGENTS.md  CLAUDE.md  LICENSE  README.md
app/        # Xcode SwiftUI universal (iOS + macOS)
daemon/     # Go 1.26 module
relay/      # stateless ciphertext forwarder + self-host docs
protocol/   # schema + golden JSON test vectors (shared contract)
skills/     # portable SKILL.md skills
scripts/    # sync-skills, dev helpers
docs/        # design docs (this plan, etc.)
```

## Phasing
- **P0 — Spike (go/no-go):** Go daemon ↔ Swift/CryptoKit **handshake + stream one live session + one
  approval round-trip**, and **verify claude-code + opencode approval interception**. Proves crypto
  interop + the approval model before we build on them.
- **P1 — MVP:** iPhone app (overview · stream · steer · **approve/deny** · push) + Mac menu-bar
  companion, over the relay, for claude-code + opencode.
- **P2 — ADE depth:** git worktrees, diff review, parallel sessions, Handoff, Live Activities, context/
  harness tooling.
- **P3 — Own cloud / Mode-2:** ephemeral cloud agents as the forward differentiator (see
  `plan-cloud-agents.md`).

## Inspiration (what we borrowed)
- **HumanLayer/CodeLayer** — ADE framing + HITL approvals + daemon/client/CLI shape (Apache-2, Go `hld`).
- **Paseo** — daemon+protocol+E2EE-relay+push proven for claude-code+opencode; `import`/handoff; pairing (AGPL — not used, only studied). Clone at `reference/paseo`.
- **OpenChamber** — outbound-only relay + APNs push + the multi-machine overview we shipped (MIT). At `reference/openchamber-upstream`.
```
```
