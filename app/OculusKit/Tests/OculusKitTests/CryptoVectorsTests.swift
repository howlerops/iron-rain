import XCTest
import CryptoKit
@testable import OculusKit

/// Proves the Swift/CryptoKit channel reproduces the Go daemon's golden vectors,
/// locking cross-language interop for the whole architecture.
final class CryptoVectorsTests: XCTestCase {
    struct Vectors: Codable {
        let client_priv, daemon_priv, client_pub, daemon_pub: String
        let c2d_key, d2c_key, seal_plaintext, open_frame: String
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
}
