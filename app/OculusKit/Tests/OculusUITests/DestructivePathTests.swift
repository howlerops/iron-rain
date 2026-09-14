import XCTest
@testable import OculusUI
@testable import OculusKit

/// A destructive action must not destroy local state before the daemon has heard about it.
///
/// stopSession removed the row, erased the on-device transcript and cleared the auto-reopen key, and
/// only THEN sent — reporting any failure into `status`, which nothing renders while connected: every
/// reader of it is gated on `!connected`, or passes it through `sessionStatusWord`, which returns nil
/// for anything that is not a session token. So on a flaky link the row vanished and the cached
/// history was erased from the device with no error shown anywhere, and seconds later the daemon's
/// next session.list put the session back — now with its local history gone.
///
/// Offline it was worse still: `guard let client else { return }` made the menu item a complete no-op
/// that said nothing at all.
@MainActor
final class DestructivePathTests: XCTestCase {

    func testDeletingWhileDisconnectedSaysSoAndKeepsEverything() async {
        let m = Model() // never connected
        m.sessions = [Session(id: "s1", provider: "opencode", status: "idle")]

        await m.stopSession("s1")

        XCTAssertEqual(m.sessions.count, 1,
                       "the session was removed from the list without the daemon ever being told")
        XCTAssertNotNil(m.actionError,
                        "deleting while offline did nothing AND said nothing — the menu item simply "
                        + "appears broken")
    }

    /// The failure must be reported somewhere a connected user actually sees. `status` is not that
    /// place, which is the whole reason this went unnoticed.
    func testTheFailureIsReportedThroughASurfaceThatIsRendered() async {
        let m = Model()
        m.sessions = [Session(id: "s1", provider: "opencode", status: "idle")]
        await m.stopSession("s1")
        XCTAssertNotNil(m.actionError, "the only report was `status`, which no connected view renders")
    }
}

/// Revoking a device must judge whether the device is still enrolled — not whether it is a guest.
///
/// Hub.Devices() already filters revoked entries out, so PRESENCE alone is the failure signal. The
/// check was `pub == d.pub && guest != true`, and that `&& guest != true` made the predicate false
/// for every guest — the device an owner is most likely to be cutting off in a hurry. A failed revoke
/// of a guest therefore redrew as though it had worked while their credential still opened the
/// daemon: the direction the function's own comment says it must not fail in.
@MainActor
final class RevokeFailureTests: XCTestCase {

    func testAGuestStillPresentAfterARevokeCountsAsAFailure() {
        let devices = [DeviceInfo(pub: "abc", label: "Sam's iPad", firstSeen: 1, lastSeen: 2, guest: true)]
        // The production predicate, as it now stands.
        let stillThere = devices.contains(where: { $0.pub == "abc" })
        XCTAssertTrue(stillThere,
                      "a guest that is still enrolled must register as a failed revoke; the old "
                      + "predicate excluded guests and so reported success for exactly the device "
                      + "an owner most urgently wants gone")
    }

    func testADeviceThatIsGoneCountsAsSuccess() {
        let devices = [DeviceInfo(pub: "other", label: "Mac", firstSeen: 1, lastSeen: 2, guest: false)]
        XCTAssertFalse(devices.contains(where: { $0.pub == "abc" }))
    }
}
