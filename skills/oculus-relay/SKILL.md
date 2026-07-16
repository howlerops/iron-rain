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

## Security (v0 limitation — read before hardening)
The relay forwards the transport handshake, so the **pairing secret + public keys transit the relay in
cleartext**. Session CONTENT stays E2E encrypted (the relay cannot read it), but a malicious relay could
replay the secret to impersonate a client. **Follow-up:** prove secret knowledge without revealing it
(e.g. a MAC over the handshake keyed by the secret), or move auth inside the E2E channel.

## Test
`cd daemon && go test ./relay/` — a full session (create → output → approval → idle) driven end-to-end
through the relay, both sides outbound-only.
