import XCTest
import CryptoKit
@testable import OculusKit

/// Proves the Swift/CryptoKit channel reproduces the Go daemon's golden vectors,
/// locking cross-language interop for the whole architecture.
final class CryptoVectorsTests: XCTestCase {
    struct Vectors: Codable {
        let client_priv, daemon_priv, client_pub, daemon_pub: String
        let c2d_key, d2c_key, seal_plaintext, seal_counter0_frame: String
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

        // Sealing the fixed plaintext at counter 0 must reproduce the exact Go frame.
        let sealer = Sealer(key: keys.c2d)
        let plaintext = try XCTUnwrap(Data(hexString: v.seal_plaintext))
        let frame = try sealer.seal(plaintext)
        XCTAssertEqual(frame.hexString, v.seal_counter0_frame, "sealed frame must match Go byte-for-byte")

        // And the round-trip opens (daemon opens client->daemon traffic on c2d).
        let opener = Opener(key: keys.c2d)
        XCTAssertEqual(try opener.open(frame), plaintext)
    }
}
