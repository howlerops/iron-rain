# protocol/ — the Oculus wire contract

The single source of truth for how the **app** and **daemon** talk. Both sides mirror this; parity is
locked by golden JSON test vectors.

- Transport: **WebSocket** (direct/LAN, or via the relay which forwards only ciphertext).
- Payload: **typed JSON envelope** `{ "id"?: string, "type": string, "payload": {...} }`.
- Encryption: end-to-end (X25519 → HKDF-SHA256 → ChaCha20-Poly1305). See `../skills/oculus-crypto`
  (added with the crypto code).

See `../skills/oculus-protocol` for the message set and how to add a message type.

## Layout (as it lands)
```
protocol/
  schema/        # message definitions (source of truth)
  vectors/       # golden JSON test vectors (Go + Swift both validate against these)
  README.md
```

When you change a message: update `schema/` + `vectors/`, then the Go structs (`daemon/protocol/`)
and Swift structs (`app/…/Protocol/`) in the **same** change.
