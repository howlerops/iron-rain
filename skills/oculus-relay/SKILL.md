---
name: oculus-relay
description: The stateless relay that lets the app reach the daemon from anywhere (both dial outbound, bridged by server_id). Use when changing relay routing, pairing, or self-host deployment.
---

# Oculus relay

`daemon/relay` — a stateless WebSocket forwarder. The daemon ("host") and app ("client") both dial the
relay **outbound**; the relay bridges them by `server_id` and copies opaque messages. No inbound ports.

## Protocol
1. Both sides connect and send a registration message `{"role":"host"|"client","server_id":"..."}`.
2. A host registers and waits; a client with the same `server_id` is bridged to it.
3. The relay then copies every subsequent WS message host↔client — it's the transport for the normal
   encrypted handshake + protocol (see `skills/oculus-transport`).

## Wiring
- Daemon: `relay.ServeHost(ctx, relayURL, serverID, srv.ServeConn)` (loop to accept clients sequentially).
- Client: `relay.DialClient(ctx, relayURL, serverID)` → a `transport.MsgConn` ready for `ClientHandshake`.
- Shared WS↔MsgConn adapter: `daemon/wsmsg`.

## Security
The relay forwards the transport handshake but sees **only ciphertext + public keys**. The pairing
secret is **never sent in the clear**: the client announces its public key, derives the channel from
static-static ECDH, then sends the secret as the first *sealed* frame (see `daemon/transport` +
[[oculus-transport]]). A passive relay can't compute the ECDH shared secret (no private key), so it
can't verify or replay secret guesses. Session content is E2E encrypted throughout.

Residual (tracked): the pairing secret is a bearer credential — anyone who *learns* it (out of band)
can pair. Per-device revocation + rotation and binding the client's static key at pair time are the
next hardening steps.

## Test
`cd daemon && go test ./relay/` — a full session (create → output → approval → idle) driven end-to-end
through the relay, both sides outbound-only.
