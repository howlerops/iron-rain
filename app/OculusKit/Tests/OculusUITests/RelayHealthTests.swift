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
