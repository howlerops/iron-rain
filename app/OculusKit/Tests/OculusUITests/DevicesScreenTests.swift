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
    /// Asserted against the SCREEN's source, not a predicate written here.
    ///
    /// This used to build its own `devices.filter { $0.this != true }` and assert that the filter
    /// filtered. That predicate exists nowhere in the app — the real gate is an independent
    /// `if d.this != true` wrapping the Revoke button in DevicesView — so deleting the wrapper left
    /// the test green while the screen started offering to revoke the connection it is speaking over.
    func testTheDeviceYouAreUsingIsNotRevocable() throws {
        let src = try sourceOf("Sources/OculusUI/DevicesView.swift")
        // The ROW's Revoke button specifically — identified by the action it performs. There is a
        // second `Button("Revoke", …)` in the confirmation dialog, which is a different control and
        // is correctly unguarded (you only reach it having already chosen a device).
        guard let revokeAt = src.range(of: "confirmRevoke = d }") else {
            XCTFail("the row's Revoke button is gone — this test can no longer tell whether it is guarded")
            return
        }
        let before = src[..<revokeAt.lowerBound].suffix(400)
        XCTAssertTrue(before.contains("d.this != true"),
                      "the Revoke button is not guarded by `d.this != true`.\n\n"
                      + "The screen would offer to revoke the credential it is connected THROUGH, "
                      + "which drops the session mid-tap and leaves the user locked out of their own "
                      + "Mac with no way back in.")
    }

    /// Locates a source file from the test's own path, so this works wherever the package is checked
    /// out. The Go side does the same thing in capability_census_test.go.
    private func sourceOf(_ relative: String) throws -> String {
        let here = URL(fileURLWithPath: #filePath) // …/Tests/OculusUITests/DevicesScreenTests.swift
        let packageRoot = here.deletingLastPathComponent().deletingLastPathComponent()
            .deletingLastPathComponent()
        return try String(contentsOf: packageRoot.appendingPathComponent(relative), encoding: .utf8)
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
    /// Asks the PRODUCTION rule what a given daemon answer means.
    ///
    /// The old version called retireLegacySecret() on a never-connected Model, which returns false at
    /// its `guard client != nil` before reaching any decision — so it asserted that a function which
    /// did nothing returned false, and inverting the real rule three statements later left it green.
    @MainActor
    func testWhatCountsAsTheOldSecretBeingRetired() {
        XCTAssertTrue(Model.legacySecretIsRetired(PairStatus(legacyLive: false)),
                      "the daemon said the secret is gone and we did not believe it")
        XCTAssertFalse(Model.legacySecretIsRetired(PairStatus(legacyLive: true)),
                       "reported the old pairing secret retired while the daemon says it still works "
                       + "— the user is told their Mac is locked down while anything holding that "
                       + "secret still walks in")
        XCTAssertFalse(Model.legacySecretIsRetired(nil),
                       "no answer is not success: a reply that never arrived must not read as a "
                       + "retirement")
    }

    /// And the disconnected path still has to refuse, which is the half the old test did cover.
    @MainActor
    func testRetiringWithNoConnectionReportsFailure() async {
        let m = Model() // never connected
        let ok = await m.retireLegacySecret()
        XCTAssertFalse(ok, "reported the old pairing secret retired without having asked anything")
    }

    /// The two message names the screen sends. They are spelled in the daemon's protocol.go and a
    /// typo here is a runtime "unknown message type" the UI reports as a generic failure.
    /// Read from the DAEMON's protocol.go, which is the authority this test's comment always named.
    ///
    /// It used to compare `MessageType.pairStatus` against the literal "pair.status" — two literals
    /// in the same language, in the same repo, three lines apart. Changing the daemon's constant left
    /// it green while every call from this screen became a runtime "unknown message type".
    func testTheMessageNamesMatchTheDaemon() throws {
        let go = try sourceOf("../../daemon/protocol/protocol.go")
        let pairs: [(String, String)] = [
            ("TypePairStatus", MessageType.pairStatus),
            ("TypePairRetireLegacy", MessageType.pairRetireLegacy),
            ("TypeDeviceList", MessageType.deviceList),
            ("TypeDeviceRevoke", MessageType.deviceRevoke),
        ]
        for (goConst, swiftValue) in pairs {
            guard let declared = Self.goStringConst(goConst, in: go) else {
                XCTFail("\(goConst) is not declared in the daemon's protocol.go")
                continue
            }
            XCTAssertEqual(swiftValue, declared,
                           "\(goConst) is \"\(declared)\" in the daemon but \"\(swiftValue)\" here — "
                           + "every call this screen makes becomes an unknown message type, which the "
                           + "UI reports as a generic failure")
        }
    }

    /// Extracts `Name = "value"` from Go source.
    private static func goStringConst(_ name: String, in source: String) -> String? {
        for line in source.split(separator: "\n") {
            let t = line.trimmingCharacters(in: .whitespaces)
            guard t.hasPrefix(name), let eq = t.firstIndex(of: "=") else { continue }
            let rest = t[t.index(after: eq)...]
            guard let open = rest.firstIndex(of: "\""),
                  let close = rest[rest.index(after: open)...].firstIndex(of: "\"") else { continue }
            return String(rest[rest.index(after: open)..<close])
        }
        return nil
    }
}
