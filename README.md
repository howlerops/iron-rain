# Oculus

A **native Apple (macOS + iOS) Agent Development Environment** for coding agents. Launch, monitor,
steer, and **approve** [claude-code](https://claude.com/claude-code) and
[opencode](https://opencode.ai) sessions — from your Mac or your phone, from anywhere. *Close the
laptop, keep shipping.*

> Status: early. Building the v0 "remotes" MVP (drive + approve a Mac session from your iPhone).

## Architecture
- **`app/`** — SwiftUI universal app (iOS + macOS). The product.
- **`daemon/`** — Go 1.26 daemon that drives the agents and exposes an E2E-encrypted WebSocket protocol.
- **`relay/`** — stateless ciphertext forwarder for remote access (hosted + self-host).
- **`protocol/`** — the shared wire contract + parity test vectors.

End-to-end encrypted (X25519 / ChaCha20-Poly1305); the relay only forwards ciphertext. Approvals are
first-class — a tool call needing sign-off can be approved/denied from the iPhone lock screen.

## Working on Oculus
Any agent can pick up work here: read [`AGENTS.md`](AGENTS.md) and the portable skills in
[`skills/`](skills/). Every component ships with code + AGENTS docs + a skill.

Design docs: [`docs/plan-native-ade.md`](docs/plan-native-ade.md).

## License
MIT.
