---
name: oculus-crypto
description: The Oculus end-to-end-encrypted channel (X25519 -> HKDF-SHA256 -> ChaCha20-Poly1305). Use when touching the handshake, key derivation, the AEAD frame format, or the Go<->Swift crypto parity vectors.
---

# Oculus crypto channel

End-to-end encryption between the app and daemon. The relay only ever sees ciphertext.

## Scheme (locked — changing any label breaks interop)
- **Key agreement:** X25519 ECDH, **static-static** for a paired channel (daemon static key ↔ client
  device key). *No forward secrecy yet — an ephemeral handshake is a tracked follow-up.*
- **Key derivation:** HKDF-SHA256 → two directional 32-byte keys.
  - salt = `oculus/v0 handshake`
  - info = `oculus/v0 c2d` (client→daemon) and `oculus/v0 d2c` (daemon→client)
- **AEAD:** ChaCha20-Poly1305. Nonce = 12 bytes = `0x00*4 || big-endian uint64 counter`.
  Frame on the wire = `nonce (12) || ciphertext+tag`. Each `Seal` advances the counter.

Client seals with `c2d` / opens with `d2c`; daemon seals with `d2c` / opens with `c2d`.

## Go implementation
`daemon/crypto/` — `GenerateKeyPair`, `KeyPairFromPrivate`, `DeriveSessionKeys`, `Sealer`/`Opener`,
and raw `X25519` (for the RFC KAT). Uses stdlib `crypto/ecdh` + `golang.org/x/crypto/{hkdf,curve25519,chacha20poly1305}`.

## Swift side (CryptoKit) — must match exactly
Use `Curve25519.KeyAgreement`, `HKDF<SHA256>` (same salt/info bytes), `ChaChaPoly` (same nonce
construction). It is correct **iff** it reproduces every field in the golden vectors below.

## Golden vectors = the cross-language contract
`protocol/vectors/handshake.json` pins fixed inputs (RFC 7748 §6.1 keypairs) → derived keys → a
counter-0 sealed frame. Both Go and Swift validate against it.
- **Regenerate (only when the scheme intentionally changes):**
  `cd daemon && OCULUS_UPDATE_VECTORS=1 go test ./crypto/ -run TestHandshakeGoldenVectors`
- **Verify:** `cd daemon && go test ./crypto/` (RFC KAT + agreement symmetry + round-trip + tamper +
  nonce-advance + golden-vector assert).

## Rules
- Never change salt/info/nonce/frame format without regenerating vectors **and** updating the Swift
  side in the same change.
- Keep the raw X25519 RFC 7748 KAT test — it's the canary for standards drift that would break CryptoKit interop.
