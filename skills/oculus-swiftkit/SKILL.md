---
name: oculus-swiftkit
description: OculusKit — the Swift package (CryptoKit crypto + protocol + client) the app uses to talk to the daemon. Use when changing the Swift crypto, protocol Codable types, or the WebSocket client, and to keep them in parity with the Go daemon.
---

# OculusKit (Swift)

`app/OculusKit` is the SwiftPM package the SwiftUI app depends on. It is the Swift half of the
protocol; it MUST stay byte-for-byte compatible with the Go daemon.

## Crypto (`Sources/OculusKit/Crypto.swift`)
CryptoKit implementation of the channel — `Curve25519.KeyAgreement`, `HKDF<SHA256>` (salt
`oculus/v0 handshake`, info `oculus/v0 c2d`/`oculus/v0 d2c`), `ChaChaPoly` with a 12-byte big-endian
counter nonce. `Sealer` returns `SealedBox.combined` (= Go's `nonce||ct||tag` frame).

## Parity is enforced by the Go golden vectors
`Tests/OculusKitTests/CryptoVectorsTests.swift` loads `protocol/vectors/handshake.json` (found via
`#filePath` → 5 dirs up → `protocol/vectors`) and asserts CryptoKit reproduces every field. If you
change the scheme, regenerate the Go vectors (see `skills/oculus-crypto`) — this test then guards Swift.

## Run
`cd app/OculusKit && swift test`

## Rules
- Never change crypto/protocol here without the matching Go change + regenerated vectors.
- Salt/info/nonce/frame format must equal Go exactly (see `skills/oculus-crypto`, `skills/oculus-protocol`).
