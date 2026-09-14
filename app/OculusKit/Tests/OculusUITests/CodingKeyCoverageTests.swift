import XCTest
@testable import OculusKit

/// A property the daemon sends but the client never reads is invisible, and silently so.
///
/// Declaring a property is not declaring a KEY. A struct with an explicit `CodingKeys` enum decodes
/// only the cases that enum lists — anything omitted stays at its default forever, with no error to
/// notice. That has bitten this codebase three times now: `Session.contextTokens`/`costKnown`,
/// `Invite.maxDevices`, and `MCPUpsert.headers`/`cwd`. The last two ran in the ENCODE direction,
/// which is worse than a missing value: a header-authenticated MCP server could not be configured
/// from the app at all, and every invite the app minted silently took the daemon's default device cap.
///
/// These check the specific fields that were missing, by round-tripping real wire bytes. A blanket
/// "every property has a key" sweep is not expressible in Swift without reflection over CodingKeys,
/// which the language does not offer — so the discipline these encode is: when a field is found
/// missing, add a case here as well as a key there.
final class CodingKeyCoverageTests: XCTestCase {

    /// The device cap the daemon sends on every invite. Without a key the app read nothing, so an
    /// invite minted elsewhere with room for five devices rendered identically to a single-use one.
    func testAnInvitesDeviceCapIsDecoded() throws {
        let json = Data("""
        {"id":"inv1","label":"Sam","role":"observer","expires_at":123,"redeemed":2,"max_devices":5}
        """.utf8)
        let inv = try ProtocolCoding.decoder().decode(Invite.self, from: json)
        XCTAssertEqual(inv.maxDevices, 5,
                       "the daemon sent max_devices and the client dropped it — a five-device link "
                       + "and a single-use one are indistinguishable on screen")
        XCTAssertEqual(inv.redeemed, 2)
        XCTAssertEqual(inv.expiresAt, 123)
    }

    /// An older daemon omits it entirely (`omitempty`). That must decode, not throw — absent means
    /// the daemon's own default of one device.
    func testAnOlderDaemonsInviteStillDecodes() throws {
        let json = Data(#"{"id":"inv1","role":"observer","expires_at":1,"redeemed":0}"#.utf8)
        let inv = try ProtocolCoding.decoder().decode(Invite.self, from: json)
        XCTAssertNil(inv.maxDevices)
    }

    /// The ENCODE direction, which is where this class does the most damage: a key the app has no
    /// case for cannot be sent at all, however the UI is wired.
    func testAnInviteCreateCanCarryADeviceCap() throws {
        let body = try ProtocolCoding.encoder().encode(InviteCreate(label: "Sam", role: "observer", maxDevices: 3))
        let obj = try XCTUnwrap(try JSONSerialization.jsonObject(with: body) as? [String: Any])
        XCTAssertEqual(obj["max_devices"] as? Int, 3,
                       "the app cannot set a device cap, so every link it mints takes the daemon's "
                       + "default — the cap is unreachable from the only surface most people use")
    }

    /// Headers are how a remote or hosted MCP server is normally authenticated, and cwd is what a
    /// stdio server needs to run in the right place. The daemon reads both; the app could send neither.
    func testAnMCPUpsertCanCarryHeadersAndCwd() throws {
        let body = try ProtocolCoding.encoder().encode(MCPUpsert(
            name: "remote", transport: "http", url: "https://example.com/mcp",
            headers: ["Authorization": "Bearer x"], cwd: "/tmp/work"))
        let obj = try XCTUnwrap(try JSONSerialization.jsonObject(with: body) as? [String: Any])
        XCTAssertEqual((obj["headers"] as? [String: String])?["Authorization"], "Bearer x",
                       "an HTTP MCP server authenticated by a header cannot be configured from the app")
        XCTAssertEqual(obj["cwd"] as? String, "/tmp/work")
    }

    /// And the daemon's own kinds must all be renderable. `fanout_run` and `fanout_done` are emitted
    /// and fell through to the neutral default, so "a comparison is ready" — the point of running a
    /// fan-out — looked exactly like an ordinary finished turn.
    func testEveryActivityKindTheDaemonEmitsIsDecodable() throws {
        for kind in ["finished", "needs_input", "error", "stalled", "loop_run", "loop_pr",
                     "fanout_run", "fanout_done"] {
            let json = Data("""
            {"id":"e1","kind":"\(kind)","session_id":"s","title":"t","ts":1,"needs_you":false,"read":false}
            """.utf8)
            let e = try ProtocolCoding.decoder().decode(ActivityEvent.self, from: json)
            XCTAssertEqual(e.kind, kind)
        }
    }
}
