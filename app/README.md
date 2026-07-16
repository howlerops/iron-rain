# app/ — Oculus (SwiftUI, iOS + macOS)

The native app — the product. A single universal SwiftUI target for iPhone and Mac.

- **macOS:** `MenuBarExtra` live agent status; a proper Mac citizen.
- **iOS:** session overview, live stream, steer, **lock-screen approvals** (actionable push), Live
  Activities, App Intents, Handoff.
- **Crypto:** CryptoKit (`Curve25519.KeyAgreement`, `HKDF`, `ChaChaPoly`) — the client half of the
  E2EE channel; must interop byte-for-byte with the Go daemon (locked by `../protocol` golden vectors).

## Structure
- `OculusKit/` — SwiftPM package: `OculusKit` (crypto + protocol + client) and `OculusUI`
  (shared `ContentView`/`Model`). `OculusUI` is the entire v0 surface, used by every app target.
- `Oculus/Sources/` — the universal `@main` App.
- `project.yml` — xcodegen spec → `Oculus.xcodeproj` (`Oculus-iOS` + `Oculus-macOS` targets). The
  generated project is gitignored; run `xcodegen generate` after checkout.
- `OculusApp/` — a no-Xcode macOS dev harness (`swift run`).

## Build
```sh
cd app && xcodegen generate
xcodebuild -project Oculus.xcodeproj -scheme Oculus-iOS -destination 'generic/platform=iOS Simulator' build
```
Status: the iOS target builds for the simulator SDK and launches. See `../skills/oculus-app`.

> Talks to the daemon only over the protocol (see `../skills/oculus-protocol`) — no FFI in v0.
> The Live Activities / Handoff / App Intents / `MenuBarExtra` items above are the next (device-gated) steps.
