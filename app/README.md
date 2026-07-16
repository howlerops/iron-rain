# app/ — Oculus (SwiftUI, iOS + macOS)

The native app — the product. A single universal SwiftUI target for iPhone and Mac.

- **macOS:** `MenuBarExtra` live agent status; a proper Mac citizen.
- **iOS:** session overview, live stream, steer, **lock-screen approvals** (actionable push), Live
  Activities, App Intents, Handoff.
- **Crypto:** CryptoKit (`Curve25519.KeyAgreement`, `HKDF`, `ChaChaPoly`) — the client half of the
  E2EE channel; must interop byte-for-byte with the Go daemon (locked by `../protocol` golden vectors).

> The Xcode project (`Oculus.xcodeproj`) is created in Xcode. Talks to the daemon only over the
> protocol (see `../skills/oculus-protocol`) — no FFI in v0.
