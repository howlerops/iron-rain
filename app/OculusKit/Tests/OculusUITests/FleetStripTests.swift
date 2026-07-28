import XCTest
@testable import OculusUI
import OculusKit

/// Integration-style test for the in-session Fleet strip's selection logic: it must surface the
/// OTHER running / needs-you / errored sessions (never the active one), and stay empty when there's
/// nothing else worth watching. Sessions are decoded from the exact JSON the daemon emits, so this
/// also exercises the Codable mapping end to end.
final class FleetStripTests: XCTestCase {
    private func session(_ id: String, status: String) -> Session {
        let json = """
        {"id":"\(id)","provider":"opencode","status":"\(status)"}
        """.data(using: .utf8)!
        return try! JSONDecoder().decode(Session.self, from: json)
    }

    func testSurfacesOtherRunningAndNeedsYou() {
        let sessions = [
            session("a", status: SessionStatusValue.running),
            session("b", status: SessionStatusValue.awaitingApproval),
            session("c", status: SessionStatusValue.idle),      // idle → not surfaced
            session("active", status: SessionStatusValue.running),
        ]
        let others = FleetStrip.others(sessions: sessions, activeID: "active", errored: [])
        XCTAssertEqual(Set(others.map { $0.id }), ["a", "b"], "running + awaiting surface; idle + active do not")
    }

    func testExcludesActiveEvenIfRunning() {
        let sessions = [session("active", status: SessionStatusValue.running)]
        XCTAssertTrue(FleetStrip.others(sessions: sessions, activeID: "active", errored: []).isEmpty,
                      "the active session is never in its own fleet strip")
    }

    func testErroredSessionSurfacesViaErrorMap() {
        let sessions = [session("x", status: SessionStatusValue.idle)]
        // idle status, but the model flagged it errored (no-response / send failed) → must surface.
        let others = FleetStrip.others(sessions: sessions, activeID: "active", errored: ["x"])
        XCTAssertEqual(others.map { $0.id }, ["x"])
    }

    func testEmptyWhenNothingElseActive() {
        let sessions = [
            session("active", status: SessionStatusValue.running),
            session("done", status: SessionStatusValue.done),
            session("stopped", status: SessionStatusValue.stopped),
        ]
        XCTAssertTrue(FleetStrip.others(sessions: sessions, activeID: "active", errored: []).isEmpty,
                      "a single-agent user sees no strip")
    }
}
