# Oculus — v0 status

Built TDD, cross-language, end-to-end tested. This tracks what's proven vs. what remains.

## E2E-tested (green)
| Layer | Where | Test |
|---|---|---|
| E2EE crypto (X25519→HKDF-SHA256→ChaCha20-Poly1305) | `daemon/crypto` | RFC 7748 KAT, agreement symmetry, AEAD round-trip/tamper/nonce, golden vectors |
| Protocol (typed JSON envelope) | `daemon/protocol` | round-trip, event-omits-id, golden vectors |
| Encrypted transport (handshake + sealed frames) | `daemon/transport` | handshake, auth rejection, wire-is-encrypted, **pairing-secret-never-in-clear** |
| opencode provider (create/stream/approve) | `daemon/agent/opencode` | E2E vs realistic stub; **live vs real opencode 1.17.19** — streaming delta + idle AND full **tool-approval round-trip** (permission.asked → allow → idle) |
| claude-code provider (stream-json + hook approvals) | `daemon/agent/claudecode` | E2E vs fake claude incl. hook approval; **live vs real claude-code 2.1.207** (streaming + idle) |
| Daemon core (dispatch + event forward + approvals) | `daemon/hub` | full-stack E2E over encrypted transport |
| WebSocket server + runnable `oculusd` | `daemon/server` | full E2E over a real WebSocket |
| Relay ("from anywhere", outbound-only) | `daemon/relay` | full session driven through the relay |
| **Push / APNs sender** (ES256 JWT, actionable approvals) | `daemon/push`, `daemon/hub` | mock-APNs test (request shape + **JWT verified**); `device.register` → approval → push over encrypted transport |
| **Session autodetection** (opencode servers + sessions, claude-code transcripts) | `daemon/discovery`, `daemon/hub` | unit + `discover.list` over encrypted transport; **live vs real opencode 1.17.19 + real claude-code** (`oculusd discover`) |
| **Swift↔Go crypto parity** | `app/OculusKit` | CryptoKit reproduces Go golden vectors byte-for-byte |
| **LIVE Swift client ↔ real Go daemon** | `app/OculusKit` LiveE2ETests | spawns the daemon, handshakes, create→output→approval→idle |
| SwiftUI macOS app | `app/OculusApp` | builds on OculusKit (`swift build`) |
| **Universal iOS + macOS app** | `app/` (xcodegen → `Oculus.xcodeproj`) | iOS builds for the simulator SDK and launches; shares `OculusUI` with macOS; surfaces autodetected sessions; **HowlerOps theme + wolf app icon/logo** |
| **macOS MenuBarExtra** | `OculusUI.MenuBarView` | live status + one-tap approve/deny, shares the window's connection; both app targets build |
| **App Intents + Handoff** | `OculusUI/Intents.swift`, app target | "Start a session" Siri shortcut (metadata extracted); NSUserActivity advertise/restore; builds + launches |
| **Live Activities scaffold** | `app/OculusWidgets` | WidgetKit extension (lock screen + Dynamic Island) embedded as `OculusWidgets.appex`; Model drives it; builds for iOS |

Run it all: `cd daemon && go test ./...` and `cd app/OculusKit && swift test`.

## Run against real opencode (manual smoke)
```sh
opencode serve --port 4096              # terminal 1
cd daemon && go run . serve --opencode http://127.0.0.1:4096 --secret test   # terminal 2
# note the printed daemon pubkey + ws URL; connect the macOS app (app/OculusApp) with them.
```
Enable claude-code too: add `--claude claude`.

Autodetect what's already running (no config): `cd daemon && go run . discover`. Lists active
`opencode serve` instances + their live sessions and recent claude-code transcripts.

## Remaining (device / credential / real-LLM gated — not automatable here)
- **Push / APNs** — ✅ **sender built + wired + tested** (`daemon/push`; `oculusd serve --apns-key …`;
  approvals push to registered devices). Genuinely device-gated remainder: a real Apple APNs key, a
  real device token (iOS `didRegisterForRemoteNotifications` → `Model.registerDevice`), the
  notification-action entitlements, and delivery to a physical device.
- **iOS app target** — ✅ **buildable + runs**, with **MenuBarExtra (macOS), App Intents + Handoff,
  and a Live Activities scaffold** all building. Still device/entitlement-gated to fully exercise:
  real Dynamic Island / Live Activity delivery, Handoff across two paired devices, push entitlement,
  and signing for a physical device / TestFlight.
- **Live validation vs real opencode + real claude-code** — ✅ **streaming validated** by opt-in
  `live_test.go` in each provider (run with `OCULUS_OPENCODE_URL` / `OCULUS_CLAUDE_BIN`). This caught +
  fixed **two real wire-shape bugs**: opencode streams `message.part.delta` (not `message.part.updated`
  with a top-level `delta`), and claude-code `stream-json` **requires `--verbose`** in `-p` mode.
  A real opencode **tool-approval turn is now validated live** (permission.asked → allow → command
  runs → idle), which caught a third bug: `POST /message` blocks until the turn yields, so the prompt
  must be fired async or the approval deadlocks. Remaining (spend-gated): the claude-code `PreToolUse`
  hook callback against a live tool-calling turn.
- **Relay hardening** — ✅ **done.** The pairing secret is now sent as a sealed frame (never in the
  clear); a passive relay can't derive the ECDH shared secret to verify/replay it. Validated by a
  `pairing-secret-not-in-clear` unit test + the live cross-language E2E. Residual (tracked): the secret
  is still a bearer credential — per-device revocation/rotation is the next step (see `skills/oculus-relay`).

## Not yet started (planned)
Multi-machine overview UX, menu-bar polish, worktrees/diff review (P2), cloud ephemeral agents (P3).
