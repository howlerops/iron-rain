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
  Exchanges static X25519 pubkeys + a pairing secret (server authorizes) in the clear, then derives
  directional keys via `crypto.DeriveSessionKeys` (see `skills/oculus-crypto`).
- **`Conn`** — `Send`/`Recv` seal/open every message with ChaCha20-Poly1305. Client sends on `c2d`,
  server on `d2c`.

## Rules
- Never send app<->daemon protocol data before the handshake completes.
- `Send` is mutex-guarded (the sealer's nonce counter must not race).
- The handshake JSON (`client_pub`, `secret`, `daemon_pub`, `ok`/`error`) is part of the contract —
  the Swift client must speak it exactly.

## Test
`cd daemon && go test ./transport/` — handshake+exchange, auth rejection, and a wire-is-encrypted
check (plaintext must never appear in sent frames).
