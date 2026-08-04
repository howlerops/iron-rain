import Foundation
import CryptoKit

public enum OculusCryptoError: Error {
    /// A transcript input was not the exact length the wire format fixes it at.
    case badHandshakeInput(String)
    /// A v1 payload was too short to contain its sequence number.
    case shortSequencedPayload
}

/// The client half of the Oculus end-to-end-encrypted channel, built on CryptoKit.
/// It must interop byte-for-byte with the Go daemon (`daemon/crypto`); parity is
/// locked by `protocol/vectors/handshake.json`. Scheme:
///   X25519 ECDH (static-static) -> HKDF-SHA256 -> ChaCha20-Poly1305 (12-byte random nonce).
///
/// There are two key schedules, and the difference is the point:
///   - `deriveSessionKeys` (v0, shipped) uses static-static ECDH alone, so the key is the
///     same for every connection this pairing ever makes — which is what made recorded
///     sessions replayable into the live daemon (`docs/security-interception-review.md` §4.3).
///   - `deriveSessionKeysV1` mixes the daemon's per-connection challenge into the
///     derivation over a transcript that also binds both public keys, so nothing recorded
///     under one connection opens under the next.
/// v0 is kept only until every daemon in the wild speaks v1; `OculusClient` negotiates.
public enum OculusCrypto {
    // Wire contract — must equal the Go labels exactly.
    static let hkdfSalt = Data("oculus/v0 handshake".utf8)
    static let hkdfInfoC2D = Data("oculus/v0 c2d".utf8)
    static let hkdfInfoD2C = Data("oculus/v0 d2c".utf8)

    // v1 labels are distinct from v0's on purpose: a v1 key can never coincide with the v0
    // key for the same pair, so a frame from one schedule can never open on the other —
    // the version is part of what the key commits to, not just a field in a JSON object.
    static let hkdfSaltV1 = Data("oculus/v1 handshake".utf8)
    static let hkdfInfoV1C2D = Data("oculus/v1 c2d".utf8)
    static let hkdfInfoV1D2C = Data("oculus/v1 d2c".utf8)
    static let transcriptLabelV1 = Data("oculus/v1 transcript".utf8)

    /// Length of the daemon's per-connection handshake challenge (`crypto.ChallengeSize`).
    public static let challengeSize = 32

    public struct SessionKeys {
        public let c2d: SymmetricKey
        public let d2c: SymmetricKey
    }

    /// Generates a fresh random X25519 private key (32 raw bytes).
    public static func generatePrivateKey() -> Data {
        Curve25519.KeyAgreement.PrivateKey().rawRepresentation
    }

    /// The 32-byte X25519 public key for a raw private key.
    public static func publicKey(fromPrivate priv: Data) throws -> Data {
        try Curve25519.KeyAgreement.PrivateKey(rawRepresentation: priv).publicKey.rawRepresentation
    }

    /// Derives the two directional session keys from the local private key and the
    /// remote public key. Both endpoints derive identical keys.
    public static func deriveSessionKeys(localPrivate: Data, remotePublic: Data) throws -> SessionKeys {
        let priv = try Curve25519.KeyAgreement.PrivateKey(rawRepresentation: localPrivate)
        let pub = try Curve25519.KeyAgreement.PublicKey(rawRepresentation: remotePublic)
        let shared = try priv.sharedSecretFromKeyAgreement(with: pub)
        // Copy the raw shared secret only long enough to seed HKDF, then wipe the
        // exposed buffer (SymmetricKey keeps its own protected copy).
        var raw = shared.withUnsafeBytes { Data($0) }
        let ikm = SymmetricKey(data: raw)
        raw.resetBytes(in: 0 ..< raw.count)
        let c2d = HKDF<SHA256>.deriveKey(inputKeyMaterial: ikm, salt: hkdfSalt, info: hkdfInfoC2D, outputByteCount: 32)
        let d2c = HKDF<SHA256>.deriveKey(inputKeyMaterial: ikm, salt: hkdfSalt, info: hkdfInfoD2C, outputByteCount: 32)
        return SessionKeys(c2d: c2d, d2c: d2c)
    }

    /// Hashes the public inputs of one v1 handshake — must match `crypto.HandshakeTranscript`:
    ///   SHA256("oculus/v1 transcript" || clientPub || daemonPub || challenge)
    ///
    /// All three inputs are exactly 32 bytes, so the concatenation is unambiguous; anything
    /// else is rejected rather than hashed short, because a variable-length field here would
    /// let two different handshakes produce the same transcript.
    public static func handshakeTranscript(clientPublic: Data, daemonPublic: Data, challenge: Data) throws -> Data {
        guard clientPublic.count == 32, daemonPublic.count == 32 else {
            throw OculusCryptoError.badHandshakeInput("transcript public keys must be 32 bytes")
        }
        guard challenge.count == challengeSize else {
            throw OculusCryptoError.badHandshakeInput("transcript challenge must be \(challengeSize) bytes")
        }
        var h = SHA256()
        h.update(data: transcriptLabelV1)
        h.update(data: clientPublic)
        h.update(data: daemonPublic)
        h.update(data: challenge)
        return Data(h.finalize())
    }

    /// `deriveSessionKeys` with the handshake transcript mixed in: same X25519 ECDH, then
    /// HKDF-SHA256 with the v1 labels and the transcript appended to the info string.
    /// Mirrors `crypto.DeriveSessionKeysV1`.
    ///
    /// The Go function takes the two public keys by role because the daemon sees local and
    /// remote swapped; here the local key is always the client's, so the client public key is
    /// derived from `localPrivate` rather than passed in — one fewer input a caller can get
    /// backwards, and the transcript still comes out identical on both sides.
    public static func deriveSessionKeysV1(localPrivate: Data, daemonPublic: Data, challenge: Data) throws -> SessionKeys {
        let priv = try Curve25519.KeyAgreement.PrivateKey(rawRepresentation: localPrivate)
        let transcript = try handshakeTranscript(
            clientPublic: priv.publicKey.rawRepresentation,
            daemonPublic: daemonPublic,
            challenge: challenge
        )
        let pub = try Curve25519.KeyAgreement.PublicKey(rawRepresentation: daemonPublic)
        let shared = try priv.sharedSecretFromKeyAgreement(with: pub)
        var raw = shared.withUnsafeBytes { Data($0) }
        let ikm = SymmetricKey(data: raw)
        raw.resetBytes(in: 0 ..< raw.count)
        let c2d = HKDF<SHA256>.deriveKey(inputKeyMaterial: ikm, salt: hkdfSaltV1, info: hkdfInfoV1C2D + transcript, outputByteCount: 32)
        let d2c = HKDF<SHA256>.deriveKey(inputKeyMaterial: ikm, salt: hkdfSaltV1, info: hkdfInfoV1D2C + transcript, outputByteCount: 32)
        return SessionKeys(c2d: c2d, d2c: d2c)
    }
}

/// The v1 sequence prefix: an 8-byte big-endian counter placed in front of every payload
/// *before* sealing, and checked for strict increase after opening.
///
/// It sits inside the ciphertext, so it is authenticated by the AEAD tag and invisible to
/// the relay — a wire-visible counter would have handed the relay a per-connection message
/// count for free. It is what stops an active attacker from duplicating or reordering
/// frames inside a live session; the challenge-bound keys are what stop replay across
/// sessions. Mirrors `seqLen` and `Conn.openLocked` in `daemon/transport`.
public enum SequenceFraming {
    public static let width = 8

    // Both directions are written byte by byte rather than through a memory copy: `framed`
    // can be a slice with no alignment guarantee, and reading a UInt64 out of an unaligned
    // address is undefined behaviour. Eight iterations cost nothing and the byte order is
    // then big-endian by construction, not by platform.
    public static func frame(_ payload: Data, seq: UInt64) -> Data {
        var out = Data(capacity: width + payload.count)
        for shift in stride(from: (width - 1) * 8, through: 0, by: -8) {
            out.append(UInt8(truncatingIfNeeded: seq >> UInt64(shift)))
        }
        out.append(payload)
        return out
    }

    public static func unframe(_ framed: Data) throws -> (seq: UInt64, payload: Data) {
        guard framed.count >= width else { throw OculusCryptoError.shortSequencedPayload }
        var seq: UInt64 = 0
        for byte in framed.prefix(width) { seq = (seq << 8) | UInt64(byte) }
        return (seq, Data(framed.dropFirst(width)))
    }
}

/// Sealer encrypts messages with a fresh random 12-byte nonce per message.
/// Frame = nonce(12) || ciphertext || tag (== ChaChaPoly SealedBox.combined).
///
/// NONCES ARE RANDOM DELIBERATELY, and the reason changed with v1 — read this before
/// "fixing" it to a counter (`daemon/crypto/crypto.go` carries the same note):
///
/// Under v0 the channel key is static per pairing, so a counter reset to 0 each session
/// would reuse (key, nonce) across sessions — catastrophic for ChaCha20-Poly1305. Under
/// v1 the key commits to a fresh daemon challenge, so a per-connection counter *would*
/// now be safe. We still use random nonces because it keeps AEAD safety independent of
/// challenge freshness: a future bug that reuses a challenge stays a replay bug (which the
/// sequence check rejects) instead of becoming plaintext recovery. Random nonces also need
/// no shared mutable state, so they are safe under concurrent seals (no counter to race).
public struct Sealer {
    private let key: SymmetricKey

    public init(key: SymmetricKey) { self.key = key }

    public func seal(_ plaintext: Data) throws -> Data {
        // Passing no nonce makes CryptoKit generate a fresh random one; .combined is
        // nonce(12) || ciphertext || tag.
        try ChaChaPoly.seal(plaintext, using: key).combined
    }
}

/// Opener decrypts frames produced by a matching Sealer.
public final class Opener {
    private let key: SymmetricKey
    public init(key: SymmetricKey) { self.key = key }

    public func open(_ frame: Data) throws -> Data {
        try ChaChaPoly.open(try ChaChaPoly.SealedBox(combined: frame), using: key)
    }
}

public extension SymmetricKey {
    /// Raw key bytes.
    var rawData: Data { withUnsafeBytes { Data($0) } }
}

public extension Data {
    /// Lowercase hex (table lookup — avoids String(format:) per byte).
    var hexString: String {
        let digits = Array("0123456789abcdef".utf8)
        var out = [UInt8]()
        out.reserveCapacity(count * 2)
        for b in self {
            out.append(digits[Int(b >> 4)])
            out.append(digits[Int(b & 0x0f)])
        }
        return String(decoding: out, as: UTF8.self)
    }

    /// Parses a lowercase/uppercase hex string.
    init?(hexString: String) {
        let chars = Array(hexString)
        guard chars.count % 2 == 0 else { return nil }
        var bytes = [UInt8]()
        bytes.reserveCapacity(chars.count / 2)
        var i = 0
        while i < chars.count {
            guard let b = UInt8(String(chars[i ... i + 1]), radix: 16) else { return nil }
            bytes.append(b)
            i += 2
        }
        self.init(bytes)
    }
}
