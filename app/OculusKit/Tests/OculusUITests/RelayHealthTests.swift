import XCTest
@testable import OculusUI
@testable import OculusKit

/// Relay health exists to separate two failures the app used to render identically.
///
/// A daemon that could not register on its relay was silent from here: the phone failed to reach it
/// and said "daemon not running", which sends someone to restart a process that is running fine.
/// These assert the daemon's report survives the wire and that the derived states a human acts on
/// are actually distinguishable.
@MainActor
final class RelayHealthTests: XCTestCase {

    private func feed(_ m: Model, _ json: String) {
        let raw = Data(json.utf8)
        guard let env = try? Protocol.envelope(raw) else {
            return XCTFail("relay.health did not parse; every assertion below would be vacuous")
        }
        m.applyEvent(env, raw: raw)
    }

    func testTheDaemonsReportReachesTheModel() {
        let m = Model()
        feed(m, """
        {"type":"relay.health","payload":{"relays":[
          {"url":"wss://relay.ironrain.app/ws","connected":true,"last_ok_at":1750000000},
          {"url":"wss://other.example/ws","connected":false,"failures":4,
           "detail":"connect: connection refused"}]}}
        """)

        XCTAssertEqual(m.relays.count, 2)
        XCTAssertTrue(m.relays[0].connected)
        XCTAssertEqual(m.relays[0].host, "relay.ironrain.app",
                       "the host is what a person reads; the full ws URL is long and its scheme "
                       + "and path never vary")
        XCTAssertFalse(m.relays[1].connected)
        XCTAssertEqual(m.relays[1].detail, "connect: connection refused",
                       "the reason did not survive the wire, so the app can only say that remote "
                       + "access is down and not why")
        XCTAssertEqual(m.relays[1].failures, 4)
    }

    /// last_ok_at is snake_case on the wire. A silent decode miss here leaves every relay looking
    /// like it has never worked, which is a different diagnosis from the true one.
    func testLastSuccessDecodesAndSeparatesNeverFromDropped() {
        let m = Model()
        feed(m, """
        {"type":"relay.health","payload":{"relays":[
          {"url":"wss://never/ws","connected":false,"detail":"no such host"},
          {"url":"wss://dropped/ws","connected":false,"last_ok_at":1750000000,"detail":"EOF"}]}}
        """)

        XCTAssertTrue(m.relays[0].neverConnected,
                      "a relay that has never registered must be distinguishable from one that "
                      + "dropped — they want different responses")
        XCTAssertFalse(m.relays[1].neverConnected,
                       "last_ok_at did not decode (it is snake_case on the wire), so a relay that "
                       + "worked until moments ago reads as one that never worked at all")
        XCTAssertEqual(m.relays[1].lastOkAt, 1_750_000_000)
    }

    /// The daemon sends this on connect as well as on change, so a device that arrives while a
    /// relay is already down still learns about it.
    func testAnEmptyReportClearsPreviousState() {
        let m = Model()
        feed(m, """
        {"type":"relay.health","payload":{"relays":[{"url":"wss://a/ws","connected":false}]}}
        """)
        XCTAssertEqual(m.relays.count, 1)
        feed(m, #"{"type":"relay.health","payload":{"relays":[]}}"#)
        XCTAssertTrue(m.relays.isEmpty,
                      "a daemon reconfigured to LAN-only still shows its old relays as down, which "
                      + "reports a problem that no longer exists")
    }

    /// A malformed payload must not wipe what the app already knows.
    func testAMalformedReportIsIgnored() {
        let m = Model()
        feed(m, """
        {"type":"relay.health","payload":{"relays":[{"url":"wss://a/ws","connected":true}]}}
        """)
        feed(m, #"{"type":"relay.health","payload":{"relays":"not an array"}}"#)
        XCTAssertEqual(m.relays.count, 1, "a malformed frame discarded known-good state")
        XCTAssertTrue(m.relays[0].connected)
    }
}

// MARK: - the sentence a user actually reads

extension RelayHealthTests {

    private func model(_ relays: [RelayState]) -> Model {
        let m = Model()
        m.relays = relays
        return m
    }

    /// LAN-only is a choice (`--relay ""`), not a fault. Warning about it would train people to
    /// ignore the warning, which costs the one case that matters.
    func testNoRelaysConfiguredSaysNothing() {
        XCTAssertNil(model([]).remoteAccessWarning)
    }

    func testAHealthyRelaySaysNothing() {
        let m = model([RelayState(url: "wss://relay.ironrain.app/ws", connected: true, lastOkAt: 1)])
        XCTAssertNil(m.remoteAccessWarning,
                     "a working relay must be silent; a banner that is always present is chrome")
    }

    /// The state this exists for: the daemon is reachable on the LAN and nothing can reach it from
    /// outside. It has no other symptom until someone leaves the building, at which point it looks
    /// like the daemon is down rather than the relay.
    func testEveryRelayDownWarns() {
        let m = model([
            RelayState(url: "wss://relay.ironrain.app/ws", connected: false, lastOkAt: 1_750_000_000,
                       detail: "EOF")
        ])
        let w = m.remoteAccessWarning
        XCTAssertNotNil(w, "remote access is down and the app says nothing")
        XCTAssertTrue(w!.contains("relay.ironrain.app"), "the warning does not name the host: \(w!)")
        XCTAssertTrue(w!.contains("EOF"), "the reason is dropped, so there is nothing to act on")
    }

    /// "Never came up" is usually configuration; "dropped" is usually the network. Sending someone
    /// to check the wrong one wastes the trip, so the two read differently.
    func testNeverConnectedReadsDifferentlyFromDropped() {
        let never = model([RelayState(url: "wss://a.example/ws", connected: false)])
        let dropped = model([
            RelayState(url: "wss://a.example/ws", connected: false, lastOkAt: 1_750_000_000)
        ])
        XCTAssertNotEqual(never.remoteAccessWarning, dropped.remoteAccessWarning,
                          "a relay that never came up and one that dropped produce the same "
                          + "sentence, so the reader cannot tell configuration from network")
        XCTAssertTrue(never.remoteAccessWarning!.lowercased().contains("never"))
    }

    /// One relay up is working remote access. Warning because a SECOND is down would report a
    /// problem the user does not have.
    func testOneHealthyRelayIsEnoughToStaySilent() {
        let m = model([
            RelayState(url: "wss://up.example/ws", connected: true, lastOkAt: 1),
            RelayState(url: "wss://down.example/ws", connected: false, detail: "refused"),
        ])
        XCTAssertNil(m.remoteAccessWarning,
                     "remote access works through the healthy relay; this warns about a problem "
                     + "that does not exist")
    }

    /// The banner is mounted, not merely defined.
    func testTheBannerIsActuallyRendered() throws {
        let here = URL(fileURLWithPath: #filePath)
        let root = here.deletingLastPathComponent().deletingLastPathComponent()
            .deletingLastPathComponent()
        let src = try String(contentsOf: root.appendingPathComponent("Sources/OculusUI/ChatView.swift"),
                             encoding: .utf8)
        XCTAssertTrue(src.contains("private var relayBanner"), "the banner view is gone")
        XCTAssertTrue(src.contains("\n            relayBanner\n"),
                      "relayBanner is defined but never placed in the view tree — the derivation "
                      + "runs and nobody sees it, which is the half-delivered state this closes")
    }
}
