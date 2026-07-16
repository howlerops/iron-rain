---
name: oculus-transport
description: The encrypted message transport that carries the Oculus protocol (handshake + sealed frames over a MsgConn / WebSocket). Use when touching the handshake, the MsgConn abstraction, or how protocol messages are moved on the wire.
---

# Oculus transport

Carries `protocol` envelopes over an end-to-end-encrypted channel. Lives in `daemon/transport/`.

## Pieces
- **`MsgConn`** — interface for discrete byte messages (`WriteMsg`/`ReadMsg`/`Close`). A WebSocket is
  a `MsgConn`; tests use an in-memory pair. The relay operates at this layer and sees only ciphertext.
- **Handshake** — `ClientHandshake(mc, kp, daemonPub, secret)` / `ServerHandshake(mc, kp, authorize)`.
  The client announces its static X25519 pubkey in the clear (a public key — safe), both sides derive
  directional keys from static-static ECDH via `crypto.DeriveSessionKeys` (see `skills/oculus-crypto`),
  then the client proves the pairing secret by sending it as the **first sealed frame** — the secret
  never transits in the clear. `authorize(clientPub, secret)` runs on the decrypted secret. A passive
  relay can't derive the ECDH shared secret, so it can't verify/replay secret guesses.
- **`Conn`** — `Send`/`Recv` seal/open every message with ChaCha20-Poly1305. Client sends on `c2d`,
  server on `d2c`.

## Rules
- Never send app<->daemon protocol data before the handshake completes.
- `Send` is mutex-guarded (the sealer's nonce counter must not race).
- The handshake wire shape is part of the contract — the Swift client must speak it exactly: message 1
  is plaintext `{client_pub}`; message 2 is the **sealed** secret; message 3 is the sealed
  `{ok, error?}` verdict. Keep `daemon/transport` and `app/OculusKit/.../Client.swift` in lockstep.

## Test
`cd daemon && go test ./transport/` — handshake+exchange, auth rejection, wire-is-encrypted, and a
pairing-secret-not-in-clear check. Cross-language: `app/OculusKit` LiveE2ETests spawns the real daemon
and completes the sealed handshake.
