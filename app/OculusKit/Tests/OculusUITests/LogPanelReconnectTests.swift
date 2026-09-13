import XCTest
@testable import OculusUI
@testable import OculusKit

/// The daemon's log subscription is per-CONNECTION. The client's belief in it was per-panel.
///
/// `openLogPanel` guards on a `logSubscribed` flag so it can be called idempotently, and nothing
/// cleared that flag when the socket dropped. So a panel that was open across a reconnect kept
/// displaying the previous connection's lines and never received another one — silently, with no
/// error and no empty state, because the lines already on screen look exactly like a quiet daemon.
///
/// The reconnect is not an edge case here. It is the main case: the reason to have the log panel
/// open at all is that the daemon is misbehaving, and a daemon that restarts or drops the socket is
/// the single most likely thing to be diagnosing.
@MainActor
final class LogPanelReconnectTests: XCTestCase {

    private func offlineModel() -> Model {
        let m = Model()
        m.wsURL = "ws://127.0.0.1:9/ws" // discard port: nothing will ever answer
        m.daemonPubHex = String(repeating: "0", count: 64)
        m.secret = "unit"
        return m
    }

    func testALostConnectionForgetsTheLogSubscription() {
        let m = offlineModel()
        m.showLogPanel = true
        m.logSubscribed = true // the panel is streaming
        m.connected = true

        m.dropConnection("Reconnecting…")

        XCTAssertFalse(m.logSubscribed,
                       "the client still believes the daemon is streaming it logs over a socket that "
                       + "is gone; openLogPanel will refuse to re-subscribe and the panel is stranded")
        XCTAssertTrue(m.showLogPanel, "the panel itself must stay open — the user did not close it")
        m.disconnect()
    }

    /// And with the flag cleared, reopening actually re-issues the subscription rather than
    /// short-circuiting on its idempotence guard.
    func testReopeningAfterADropReSubscribes() {
        let m = offlineModel()
        m.showLogPanel = true
        m.logSubscribed = true
        m.connected = true
        m.dropConnection("Reconnecting…")

        m.openLogPanel()
        XCTAssertTrue(m.logSubscribed,
                      "openLogPanel took its idempotence path and sent nothing — this is the exact "
                      + "shape of the bug, one layer up")
        m.disconnect()
    }
}
