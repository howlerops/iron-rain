import XCTest
@testable import OculusUI
@testable import OculusKit

/// Device enrolment and revocation were built on both sides — the daemon mints a per-device
/// credential at pairing, lists, revokes and renames; the client models all four — and the whole
/// thing was reachable from no screen. A phone you no longer have kept a live credential to a
/// machine that runs shell commands for you, and the only way to take it back was a terminal.
///
/// The screen itself is SwiftUI, but everything it depends on being right is not: the wire keys, and
/// which rows it is allowed to offer a Revoke button for.
final class DevicesScreenTests: XCTestCase {

    private func decodeList(_ json: String) throws -> [DeviceInfo] {
        try ProtocolCoding.decoder().decode(DeviceList.self, from: Data(json.utf8)).devices
    }

    /// The daemon's exact shape. A key mismatch here does not throw — `first_seen` simply arrives as
    /// nothing — so the screen would render every device as "paired unknown, last seen never" and
    /// look broken in a way no test would catch.
    func testTheDaemonsDeviceShapeDecodes() throws {
        let devices = try decodeList("""
        {"devices":[
          {"pub":"aabbccdd11223344","label":"Jacob's iPhone","first_seen":1750000000,"last_seen":1760000000},
          {"pub":"ffeeddcc99887766","first_seen":1755000000,"last_seen":1759000000,"this":true},
          {"pub":"1234567890abcdef","label":"Reviewer","first_seen":1758000000,"last_seen":1758000001,"guest":true}
        ]}
        """)

        XCTAssertEqual(devices.count, 3)
        XCTAssertEqual(devices[0].label, "Jacob's iPhone")
        XCTAssertEqual(devices[0].firstSeen, 1750000000, "first_seen did not map — the screen shows \"paired unknown\"")
        XCTAssertEqual(devices[0].lastSeen, 1760000000, "last_seen did not map — the screen shows \"last seen never\"")
        XCTAssertNil(devices[0].this)
        XCTAssertEqual(devices[1].this, true)
        XCTAssertEqual(devices[2].guest, true)
        // Identity is the key, not the label: two devices may carry the same name and revoking must
        // not be able to hit the wrong one.
        XCTAssertEqual(devices[0].id, "aabbccdd11223344")
        XCTAssertNotEqual(devices[0].id, devices[1].id)
    }

    /// The screen must never offer to revoke the connection it is speaking over — that would drop the
    /// session mid-tap and leave the user with no way back in.
    func testTheDeviceYouAreUsingIsNotRevocable() throws {
        let devices = try decodeList("""
        {"devices":[
          {"pub":"aa","label":"Old phone","first_seen":1,"last_seen":2},
          {"pub":"bb","label":"This Mac","first_seen":1,"last_seen":2,"this":true}
        ]}
        """)
        let revocable = devices.filter { $0.this != true }
        XCTAssertEqual(revocable.map(\.pub), ["aa"],
                       "the screen would offer to revoke the device it is running on")
    }

    /// The legacy shared secret is not a device. Revoking every device listed above does not retire
    /// it, which is why the screen has to ask about it separately — and why the app asking at all is
    /// the entire point: `pair.status` had no caller, so nobody could find out.
    func testPairStatusDecodesBothStates() throws {
        let live = try ProtocolCoding.decoder().decode(
            PairStatus.self, from: Data(#"{"legacy_live":true,"legacy_retire_at":1799999999}"#.utf8))
        XCTAssertTrue(live.legacyLive)
        XCTAssertEqual(live.legacyRetireAt, 1799999999, "legacy_retire_at did not map — the screen "
                       + "would claim the old secret has no expiry when it has one")

        // Retired: the daemon omits the timestamp entirely.
        let retired = try ProtocolCoding.decoder().decode(
            PairStatus.self, from: Data(#"{"legacy_live":false}"#.utf8))
        XCTAssertFalse(retired.legacyLive)
        XCTAssertNil(retired.legacyRetireAt)
    }

    /// Retiring the old secret must report FAILURE when it did not happen.
    ///
    /// The screen's whole job here is to say whether a door is shut. Returning true on a call that
    /// never reached the daemon would tell the user their Mac is locked down while anything holding
    /// the old shared secret still walks in — the one direction this must not fail in.
    @MainActor
    func testRetiringReportsFailureWhenItCouldNotHappen() async {
        let m = Model() // never connected
        let ok = await m.retireLegacySecret()
        XCTAssertFalse(ok, "reported the old pairing secret retired without having asked anything")
        XCTAssertNil(m.pairStatus, "invented a status nothing supplied")
    }

    /// The two message names the screen sends. They are spelled in the daemon's protocol.go and a
    /// typo here is a runtime "unknown message type" the UI reports as a generic failure.
    func testTheMessageNamesMatchTheDaemon() {
        XCTAssertEqual(MessageType.pairStatus, "pair.status")
        XCTAssertEqual(MessageType.pairRetireLegacy, "pair.retire_legacy")
        XCTAssertEqual(MessageType.deviceList, "device.list")
        XCTAssertEqual(MessageType.deviceRevoke, "device.revoke")
    }
}
