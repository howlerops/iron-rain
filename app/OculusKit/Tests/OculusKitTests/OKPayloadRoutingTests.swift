import XCTest
@testable import OculusKit

/// Locks the invariant the OculusUI `ok`-frame router relies on: every OK payload type
/// carries a distinct top-level discriminator key, so a frame can be routed to a single
/// typed decode (via that key) instead of being re-parsed for every candidate type. If a
/// new payload type ever collides on one of these keys, or the wire key changes, this
/// fails loudly so the router in OculusUI can be updated in lockstep.
final class OKPayloadRoutingTests: XCTestCase {
    /// The top-level keys the daemon actually puts on the wire for a given OK payload —
    /// the exact set the router inspects via a single JSON parse.
    private func payloadKeys<T: Encodable>(_ payload: T) throws -> Set<String> {
        let data = try Protocol.encode(id: "1", type: MessageType.ok, payload: payload)
        let obj = try XCTUnwrap(JSONSerialization.jsonObject(with: data) as? [String: Any])
        let p = try XCTUnwrap(obj["payload"] as? [String: Any])
        return Set(p.keys)
    }

    func testEachOKTypeCarriesItsDiscriminatorKey() throws {
        XCTAssertTrue(try payloadKeys(DiscoverList(items: [])).contains("items"))
        XCTAssertTrue(try payloadKeys(ProjectList(projects: [])).contains("projects"))
        XCTAssertTrue(try payloadKeys(SessionList(sessions: [])).contains("sessions"))
        XCTAssertTrue(try payloadKeys(IntegrationStatus(connected: [])).contains("connected"))
        XCTAssertTrue(try payloadKeys(IssueList(issues: [])).contains("issues"))
        XCTAssertTrue(try payloadKeys(IntegrationOAuth(provider: "linear", url: "https://x")).contains("url"))
        XCTAssertTrue(try payloadKeys(IntegrationOAuth(provider: "linear", url: "https://x")).contains("provider"))
        XCTAssertTrue(try payloadKeys(WorktreeDiff(sessionID: "s", diff: "d")).contains("diff"))
        XCTAssertTrue(try payloadKeys(WorktreeConflicts(sessionID: "s", files: [])).contains("files"))
        XCTAssertTrue(try payloadKeys(WorktreePRResult(sessionID: "s", branch: "b", pushed: true, url: nil)).contains("pushed"))
    }

    /// A frame of one type must decode as that type and NOT as any sibling — this is what
    /// lets the router pick a single branch by key and decode exactly once.
    func testDiscriminatorRoutesToExactlyOneType() throws {
        let data = try Protocol.encode(id: "1", type: MessageType.ok, payload: SessionList(sessions: []))
        XCTAssertNotNil(try? Protocol.payload(data, as: SessionList.self))
        XCTAssertNil(try? Protocol.payload(data, as: DiscoverList.self))
        XCTAssertNil(try? Protocol.payload(data, as: ProjectList.self))
        XCTAssertNil(try? Protocol.payload(data, as: IssueList.self))
        XCTAssertNil(try? Protocol.payload(data, as: IntegrationStatus.self))
    }

    /// The keys used as router discriminators are mutually exclusive across the collection
    /// payload types — no two share their routing key.
    func testCollectionDiscriminatorKeysAreMutuallyExclusive() throws {
        let discover = try payloadKeys(DiscoverList(items: []))
        let projects = try payloadKeys(ProjectList(projects: []))
        let sessions = try payloadKeys(SessionList(sessions: []))
        let status = try payloadKeys(IntegrationStatus(connected: []))
        let issues = try payloadKeys(IssueList(issues: []))
        XCTAssertFalse(discover.contains("projects"))
        XCTAssertFalse(projects.contains("items"))
        XCTAssertFalse(sessions.contains("issues"))
        XCTAssertFalse(status.contains("sessions"))
        XCTAssertFalse(issues.contains("connected"))
    }
}
