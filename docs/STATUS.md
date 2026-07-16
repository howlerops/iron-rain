# Oculus — v0 status

Built TDD, cross-language, end-to-end tested. This tracks what's proven vs. what remains.

## E2E-tested (green)
| Layer | Where | Test |
|---|---|---|
| E2EE crypto (X25519→HKDF-SHA256→ChaCha20-Poly1305) | `daemon/crypto` | RFC 7748 KAT, agreement symmetry, AEAD round-trip/tamper/nonce, golden vectors |
| Protocol (typed JSON envelope) | `daemon/protocol` | round-trip, event-omits-id, golden vectors |
| Encrypted transport (handshake + sealed frames) | `daemon/transport` | handshake, auth rejection, wire-is-encrypted |
| opencode provider (create/stream/approve) | `daemon/agent/opencode` | E2E vs realistic stub incl. approval |
| claude-code provider (stream-json + hook approvals) | `daemon/agent/claudecode` | E2E vs fake claude incl. hook approval |
| Daemon core (dispatch + event forward + approvals) | `daemon/hub` | full-stack E2E over encrypted transport |
| WebSocket server + runnable `oculusd` | `daemon/server` | full E2E over a real WebSocket |
| Relay ("from anywhere", outbound-only) | `daemon/relay` | full session driven through the relay |
| **Session autodetection** (opencode servers + sessions, claude-code transcripts) | `daemon/discovery`, `daemon/hub` | unit + `discover.list` over encrypted transport; **live vs real opencode 1.17.19 + real claude-code** (`oculusd discover`) |
| **Swift↔Go crypto parity** | `app/OculusKit` | CryptoKit reproduces Go golden vectors byte-for-byte |
| **LIVE Swift client ↔ real Go daemon** | `app/OculusKit` LiveE2ETests | spawns the daemon, handshakes, create→output→approval→idle |
| SwiftUI macOS app | `app/OculusApp` | builds on OculusKit (`swift build`) |

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
- **Push / APNs** — daemon-side sender + actionable lock-screen approvals. Needs an Apple Developer
  APNs key + a real device. (Design: hosted default + self-host BYO-key.)
- **iOS app target** — the SwiftUI views are cross-platform; iOS specifics (Live Activities,
  Handoff, App Intents, the iOS app target/entitlements) need an Xcode iOS build + device/simulator.
- **Live validation vs real opencode + real claude-code** — the provider tests use faithful
  stubs/fakes. **Partially validated:** opencode 1.17.19 `GET /session` shape + the claude-code
  transcript store are confirmed live via `oculusd discover`. Still spend-gated: the *streaming*
  paths (opencode `/event` SSE deltas + permission asks; claude-code stream-json + PreToolUse hook)
  need a real LLM turn to exercise end-to-end.
- **Relay hardening** — the pairing secret currently transits the relay in cleartext (content stays
  E2E). Prove-secret-without-revealing is a tracked follow-up (see `skills/oculus-relay`).

## Not yet started (planned)
Multi-machine overview UX, menu-bar polish, worktrees/diff review (P2), cloud ephemeral agents (P3).
