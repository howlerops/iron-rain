import XCTest
@testable import OculusKit

/// Hermetic regression tests for the protocol coding fast-path fixes:
///  - shared, reused JSONEncoder/JSONDecoder instances
///  - single-pass envelope parsing (parse once, dispatch on type, decode payload)
final class ProtocolCodingTests: XCTestCase {
    /// The coders are cached and the same instance is handed out every call, so the
    /// hot streaming path does not allocate a fresh coder per message.
    func testCodersAreSharedInstances() {
        XCTAssertTrue(ProtocolCoding.encoder() === ProtocolCoding.encoder())
        XCTAssertTrue(ProtocolCoding.decoder() === ProtocolCoding.decoder())
        XCTAssertTrue(ProtocolCoding.encoder() === ProtocolCoding.sharedEncoder)
        XCTAssertTrue(ProtocolCoding.decoder() === ProtocolCoding.sharedDecoder)
    }

    /// A round-trip through the shared coders must preserve the snake_case wire keys.
    func testEncodeDecodeRoundTripUsesSharedCoders() throws {
        let data = try Protocol.encode(id: "c1", type: MessageType.outputDelta,
                                       payload: OutputDelta(sessionID: "s1", text: "hello"))
        let json = try XCTUnwrap(String(data: data, encoding: .utf8))
        XCTAssertTrue(json.contains("\"session_id\""), "expected snake_case key, got: \(json)")

        let env = try Protocol.envelope(data)
        XCTAssertEqual(env.type, MessageType.outputDelta)
        XCTAssertEqual(env.id, "c1")
        let delta = try env.payload(as: OutputDelta.self)
        XCTAssertEqual(delta.sessionID, "s1")
        XCTAssertEqual(delta.text, "hello")
    }

    /// Single-pass parse: `envelope` exposes id/type for dispatch and decodes the
    /// payload without a second parse of the raw bytes.
    func testEnvelopeSinglePassDispatchAndPayload() throws {
        let bytes = try Protocol.encode(id: "a7", type: MessageType.approvalRequest,
                                        payload: ApprovalRequest(approvalID: "ap1", sessionID: "s2",
                                                                 tool: "bash", detail: "rm -rf"))
        let env = try Protocol.envelope(bytes)
        XCTAssertEqual(env.id, "a7")
        XCTAssertEqual(env.type, MessageType.approvalRequest)
        XCTAssertTrue(env.hasPayload)

        let ar = try env.payload(as: ApprovalRequest.self)
        XCTAssertEqual(ar, ApprovalRequest(approvalID: "ap1", sessionID: "s2", tool: "bash", detail: "rm -rf"))
    }

    /// An envelope with no payload reports `hasPayload == false` and throws when a
    /// payload is requested, matching the raw-`payload(_:as:)` behavior.
    func testEnvelopeWithoutPayload() throws {
        let bytes = try Protocol.encode(id: "c1", type: MessageType.ok)
        let env = try Protocol.envelope(bytes)
        XCTAssertEqual(env.type, MessageType.ok)
        XCTAssertFalse(env.hasPayload)
        XCTAssertThrowsError(try env.payload(as: OutputDelta.self))
    }

    /// The single-pass `envelope` decodes to the same value as the legacy
    /// `header` + `payload` two-parse path.
    func testEnvelopeMatchesHeaderPlusPayload() throws {
        let bytes = try Protocol.encode(id: "s9", type: MessageType.sessionStatus,
                                        payload: SessionStatus(sessionID: "s9", status: SessionStatusValue.idle, detail: nil))
        let header = try Protocol.header(bytes)
        let viaPayload = try Protocol.payload(bytes, as: SessionStatus.self)
        let env = try Protocol.envelope(bytes)
        let viaEnvelope = try env.payload(as: SessionStatus.self)

        XCTAssertEqual(header.id, env.id)
        XCTAssertEqual(header.type, env.type)
        XCTAssertEqual(viaPayload.sessionID, viaEnvelope.sessionID)
        XCTAssertEqual(viaPayload.status, viaEnvelope.status)
    }

    func testInvalidEnvelopeThrows() {
        let garbage = Data("not json".utf8)
        XCTAssertThrowsError(try Protocol.envelope(garbage))
    }
}
