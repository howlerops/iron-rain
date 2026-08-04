import XCTest
@testable import OculusUI
@testable import OculusKit

/// Tests for "where is this actually running" — on both ends of the wire.
///
/// The app races a direct address against every relay and keeps the first socket to finish the
/// handshake, and it lists sessions whose agents may be running on another machine entirely. Both
/// facts used to be invisible: the race discarded the winning URL, and a remote session's host lived
/// only in its default name, which a rename overwrites.
@MainActor
final class ExecutionLocationTests: XCTestCase {

    func testLoopbackRouteIsNamedAsThisMac() {
        let url = URL(string: "ws://127.0.0.1:8765/ws")!
        XCTAssertEqual(Model.routeLabel(url, direct: "ws://127.0.0.1:8765/ws"), "this Mac")
    }

    func testDirectAddressOnTheLocalNetworkIsNamedLAN() {
        let url = URL(string: "ws://192.168.1.40:8765/ws")!
        XCTAssertEqual(Model.routeLabel(url, direct: "ws://192.168.1.40:8765/ws"), "LAN")
    }

    /// The relay route carries registration params the paired address never has, so it is NOT the
    /// direct URL — and the user needs to know, because every keystroke is round-tripping through it.
    func testRelayRouteIsNamedRelay() {
        let url = URL(string: "wss://relay.example.workers.dev/?sid=abc&role=client")!
        XCTAssertEqual(Model.routeLabel(url, direct: "ws://192.168.1.40:8765/ws"), "relay")
    }

    /// A session with no paired direct address at all (relay-only pairing) must not be mislabelled
    /// as local just because the comparison had nothing to compare against.
    func testRelayIsStillRelayWithNoDirectAddress() {
        let url = URL(string: "wss://relay.example.workers.dev/?sid=abc&role=client")!
        XCTAssertEqual(Model.routeLabel(url, direct: ""), "relay")
    }

    /// A remote session's execution host arrives as its own field, so it survives the rename that
    /// wipes the "remote: build-box" default name.
    func testSessionDecodesExecutionLocation() throws {
        let json = Data("""
        {"id":"s1","provider":"cli","status":"running","name":"nightly deploy",
         "exec_kind":"ssh","exec_host":"build-box"}
        """.utf8)
        let s = try JSONDecoder().decode(Session.self, from: json)
        XCTAssertEqual(s.execKind, "ssh")
        XCTAssertEqual(s.execHost, "build-box")
        XCTAssertEqual(s.name, "nightly deploy", "the user-set name and the host are independent")
    }

    /// A daemon predating the field sends neither key. Absent has to decode as local, not as a
    /// third "unknown" state the sidebar would have to render some hedge for.
    func testSessionWithoutExecutionFieldsIsLocal() throws {
        let json = Data(#"{"id":"s1","provider":"opencode","status":"idle"}"#.utf8)
        let s = try JSONDecoder().decode(Session.self, from: json)
        XCTAssertNil(s.execKind)
        XCTAssertNil(s.execHost)
    }
}
