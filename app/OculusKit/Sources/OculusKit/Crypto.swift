import Foundation
import CryptoKit

/// The client half of the Oculus end-to-end-encrypted channel, built on CryptoKit.
/// It must interop byte-for-byte with the Go daemon (`daemon/crypto`); parity is
/// locked by `protocol/vectors/handshake.json`. Scheme:
///   X25519 ECDH (static-static) -> HKDF-SHA256 -> ChaCha20-Poly1305 (12-byte counter nonce).
public enum OculusCrypto {
    // Wire contract — must equal the Go labels exactly.
    static let hkdfSalt = Data("oculus/v0 handshake".utf8)
    static let hkdfInfoC2D = Data("oculus/v0 c2d".utf8)
    static let hkdfInfoD2C = Data("oculus/v0 d2c".utf8)

    public struct SessionKeys {
        public let c2d: SymmetricKey
        public let d2c: SymmetricKey
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
        let ikm = SymmetricKey(data: shared.withUnsafeBytes { Data($0) })
        let c2d = HKDF<SHA256>.deriveKey(inputKeyMaterial: ikm, salt: hkdfSalt, info: hkdfInfoC2D, outputByteCount: 32)
        let d2c = HKDF<SHA256>.deriveKey(inputKeyMaterial: ikm, salt: hkdfSalt, info: hkdfInfoD2C, outputByteCount: 32)
        return SessionKeys(c2d: c2d, d2c: d2c)
    }
}

/// Sealer encrypts an ordered stream with a per-message big-endian counter nonce.
/// Frame = nonce(12) || ciphertext || tag (== ChaChaPoly SealedBox.combined).
public final class Sealer {
    private let key: SymmetricKey
    private var counter: UInt64 = 0

    public init(key: SymmetricKey) { self.key = key }

    public func seal(_ plaintext: Data) throws -> Data {
        var nonce = Data(repeating: 0, count: 12)
        let be = counter.bigEndian
        withUnsafeBytes(of: be) { raw in
            for i in 0..<8 { nonce[4 + i] = raw[i] }
        }
        counter &+= 1
        let box = try ChaChaPoly.seal(plaintext, using: key, nonce: try ChaChaPoly.Nonce(data: nonce))
        return box.combined
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
    /// Lowercase hex.
    var hexString: String { map { String(format: "%02x", $0) }.joined() }

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
