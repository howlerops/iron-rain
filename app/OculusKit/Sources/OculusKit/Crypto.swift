import Foundation
import CryptoKit

/// The client half of the Oculus end-to-end-encrypted channel, built on CryptoKit.
/// It must interop byte-for-byte with the Go daemon (`daemon/crypto`); parity is
/// locked by `protocol/vectors/handshake.json`. Scheme:
///   X25519 ECDH (static-static) -> HKDF-SHA256 -> ChaCha20-Poly1305 (12-byte random nonce).
public enum OculusCrypto {
    // Wire contract — must equal the Go labels exactly.
    static let hkdfSalt = Data("oculus/v0 handshake".utf8)
    static let hkdfInfoC2D = Data("oculus/v0 c2d".utf8)
    static let hkdfInfoD2C = Data("oculus/v0 d2c".utf8)

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
}

/// Sealer encrypts messages with a fresh random 12-byte nonce per message.
/// Frame = nonce(12) || ciphertext || tag (== ChaChaPoly SealedBox.combined).
///
/// The channel key is static per pairing, so a counter reset to 0 each session would
/// reuse (key, nonce) across sessions — catastrophic for ChaCha20-Poly1305. A random
/// nonce per message avoids that and needs no shared mutable state, so it is safe under
/// concurrent seals (no counter to race).
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
