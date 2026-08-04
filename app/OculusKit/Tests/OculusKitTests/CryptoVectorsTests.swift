import XCTest
import CryptoKit
@testable import OculusKit

/// Proves the Swift/CryptoKit channel reproduces the Go daemon's golden vectors,
/// locking cross-language interop for the whole architecture.
final class CryptoVectorsTests: XCTestCase {
    struct Vectors: Codable {
        let client_priv, daemon_priv, client_pub, daemon_pub: String
        let c2d_key, d2c_key, seal_plaintext, open_frame: String
        // v1: the challenge-bound schedule and the sequence framing.
        let challenge, transcript, v1_c2d_key, v1_d2c_key: String
        let v1_framed_plaintext, v1_open_frame: String
    }

    func loadVectors() throws -> Vectors {
        // .../app/OculusKit/Tests/OculusKitTests/CryptoVectorsTests.swift -> repo root
        var url = URL(fileURLWithPath: #filePath)
        for _ in 0 ..< 5 { url.deleteLastPathComponent() }
        url.appendPathComponent("protocol/vectors/handshake.json")
        let data = try Data(contentsOf: url)
        return try JSONDecoder().decode(Vectors.self, from: data)
    }

    func testReproducesGoldenVectors() throws {
        let v = try loadVectors()

        let clientPriv = try XCTUnwrap(Data(hexString: v.client_priv))
        let daemonPriv = try XCTUnwrap(Data(hexString: v.daemon_priv))
        let daemonPub = try XCTUnwrap(Data(hexString: v.daemon_pub))

        // Public keys (also RFC 7748 §6.1 known-answers).
        XCTAssertEqual(try OculusCrypto.publicKey(fromPrivate: clientPriv).hexString, v.client_pub)
        XCTAssertEqual(try OculusCrypto.publicKey(fromPrivate: daemonPriv).hexString, v.daemon_pub)

        // Directional session keys (client side: local=client priv, remote=daemon pub).
        let keys = try OculusCrypto.deriveSessionKeys(localPrivate: clientPriv, remotePublic: daemonPub)
        XCTAssertEqual(keys.c2d.rawData.hexString, v.c2d_key, "c2d key must match Go")
        XCTAssertEqual(keys.d2c.rawData.hexString, v.d2c_key, "d2c key must match Go")

        // Production nonces are random, so parity is pinned on OPEN: CryptoKit must
        // decrypt the Go-generated KAT frame back to the plaintext under c2d.
        let plaintext = try XCTUnwrap(Data(hexString: v.seal_plaintext))
        let goFrame = try XCTUnwrap(Data(hexString: v.open_frame))
        let opener = Opener(key: keys.c2d)
        XCTAssertEqual(try opener.open(goFrame), plaintext, "must open the Go KAT frame")

        // And a locally-sealed frame (random nonce) round-trips through our own Opener.
        let sealer = Sealer(key: keys.c2d)
        XCTAssertEqual(try opener.open(try sealer.seal(plaintext)), plaintext)
    }

    /// The v1 (challenge-bound) schedule and its sequence framing, against the same Go-generated
    /// vectors. This is the parity that matters for replay protection: if CryptoKit and Go
    /// disagreed about the transcript by so much as a byte, every v1 connection would fail to
    /// decrypt — and the temptation would be to "fix" it by falling back to v0.
    func testReproducesV1GoldenVectors() throws {
        let v = try loadVectors()

        let clientPriv = try XCTUnwrap(Data(hexString: v.client_priv))
        let clientPub = try XCTUnwrap(Data(hexString: v.client_pub))
        let daemonPub = try XCTUnwrap(Data(hexString: v.daemon_pub))
        let challenge = try XCTUnwrap(Data(hexString: v.challenge))

        let transcript = try OculusCrypto.handshakeTranscript(clientPublic: clientPub, daemonPublic: daemonPub, challenge: challenge)
        XCTAssertEqual(transcript.hexString, v.transcript, "transcript must match Go")

        let keys = try OculusCrypto.deriveSessionKeysV1(localPrivate: clientPriv, daemonPublic: daemonPub, challenge: challenge)
        XCTAssertEqual(keys.c2d.rawData.hexString, v.v1_c2d_key, "v1 c2d key must match Go")
        XCTAssertEqual(keys.d2c.rawData.hexString, v.v1_d2c_key, "v1 d2c key must match Go")

        // A v1 key must never coincide with the v0 key for the same pair — if it did, a
        // recorded v0 frame would open on a v1 channel and the fix would be cosmetic.
        XCTAssertNotEqual(keys.c2d.rawData.hexString, v.c2d_key)
        XCTAssertNotEqual(keys.d2c.rawData.hexString, v.d2c_key)

        // The sealed payload is seq(8, big-endian) || plaintext, byte for byte.
        let plaintext = try XCTUnwrap(Data(hexString: v.seal_plaintext))
        let framed = try XCTUnwrap(Data(hexString: v.v1_framed_plaintext))
        XCTAssertEqual(SequenceFraming.frame(plaintext, seq: 0).hexString, framed.hexString)
        let (seq, unframed) = try SequenceFraming.unframe(framed)
        XCTAssertEqual(seq, 0)
        XCTAssertEqual(unframed, plaintext)

        // And CryptoKit must open the Go-generated v1 KAT frame to exactly that payload.
        let goFrame = try XCTUnwrap(Data(hexString: v.v1_open_frame))
        XCTAssertEqual(try Opener(key: keys.c2d).open(goFrame), framed, "must open the Go v1 KAT frame")
    }

    /// A different challenge must produce different keys. This is the whole mechanism: it is
    /// what makes a stream recorded from one connection undecryptable against the next.
    func testChallengeChangesTheKeys() throws {
        let v = try loadVectors()
        let clientPriv = try XCTUnwrap(Data(hexString: v.client_priv))
        let daemonPub = try XCTUnwrap(Data(hexString: v.daemon_pub))
        var other = try XCTUnwrap(Data(hexString: v.challenge))
        other[0] ^= 0x01

        let a = try OculusCrypto.deriveSessionKeysV1(localPrivate: clientPriv, daemonPublic: daemonPub, challenge: try XCTUnwrap(Data(hexString: v.challenge)))
        let b = try OculusCrypto.deriveSessionKeysV1(localPrivate: clientPriv, daemonPublic: daemonPub, challenge: other)
        XCTAssertNotEqual(a.c2d.rawData, b.c2d.rawData)
        XCTAssertNotEqual(a.d2c.rawData, b.d2c.rawData)

        // A frame sealed under one challenge must not open under another.
        let sealed = try Sealer(key: a.c2d).seal(Data("replay me".utf8))
        XCTAssertThrowsError(try Opener(key: b.c2d).open(sealed))
    }

    /// The transcript pins every input at 32 bytes; a short field would let two different
    /// handshakes hash to the same transcript.
    func testTranscriptRejectsMalformedInputs() throws {
        let ok = Data(repeating: 0xAB, count: 32)
        XCTAssertThrowsError(try OculusCrypto.handshakeTranscript(clientPublic: Data(repeating: 1, count: 31), daemonPublic: ok, challenge: ok))
        XCTAssertThrowsError(try OculusCrypto.handshakeTranscript(clientPublic: ok, daemonPublic: ok, challenge: Data(repeating: 2, count: 16)))
    }
}
