import XCTest
@testable import OculusUI
@testable import OculusKit

/// "Show earlier messages" must ask for the page that actually continues the transcript.
///
/// This file used to assert the COUNTING contract: the client tallied frames it had rendered, sent
/// the total as `loaded`, and the daemon computed `end := len(all) - loaded` against its ring. That
/// only works if the client's tally agrees exactly with the daemon's ring, and it never reliably
/// did — the client had to guess, by message type, which frames the daemon stored. The guess was
/// wrong in three consecutive sweeps, and every over-count asks for a page starting before the
/// transcript ends, leaving a hole nothing can see.
///
/// The frames now carry their own position, so the tests below assert the same PROPERTIES against a
/// cursor that cannot disagree with the daemon: transient frames must not move it, real ones must,
/// and another session's must not.
@MainActor
final class PageCursorTests: XCTestCase {

    private func apply(_ m: Model, _ raw: Data) {
        guard let env = try? Protocol.envelope(raw) else { return XCTFail("bad frame") }
        m.applyEvent(env, raw: raw)
    }

    /// Transient frames carry no sequence — the daemon never stored them — so they cannot move the
    /// cursor however they are delivered. This is the property the type list was approximating.
    func testTransientFramesDoNotMoveTheCursor() {
        let m = Model()
        m.sessionID = "s1"
        m.oldestSeqHeld = 100

        apply(m, Data(#"{"type":"session.status","payload":{"session_id":"s1","status":"running","detail":"Read"}}"#.utf8))
        apply(m, Data(#"{"type":"session.facts","payload":{"session_id":"s1"}}"#.utf8))
        apply(m, Data(#"{"type":"turn.state","payload":{"session_id":"s1","state":"running"}}"#.utf8))
        apply(m, Data(#"{"type":"session.heartbeat","payload":{"session_id":"s1","state":"working"}}"#.utf8))
        apply(m, Data(#"{"type":"output.delta","payload":{"session_id":"s1","text":"streaming"}}"#.utf8))

        XCTAssertEqual(m.oldestSeqHeld, 100,
                       "a frame the daemon never stored moved the paging cursor. It has no position "
                       + "in the transcript, so the number it moved the cursor to means nothing — "
                       + "which is exactly how the old type-list guess produced holes.")
    }

    /// A stored frame carries its position and must set the cursor to the OLDEST one held.
    func testStoredFramesMoveTheCursorToTheOldestHeld() {
        let m = Model()
        m.sessionID = "s1"
        apply(m, Data(#"{"seq":40,"type":"session.message","payload":{"session_id":"s1","role":"assistant","text":"later"}}"#.utf8))
        XCTAssertEqual(m.oldestSeqHeld, 40)

        apply(m, Data(#"{"seq":12,"type":"session.message","payload":{"session_id":"s1","role":"assistant","text":"earlier"}}"#.utf8))
        XCTAssertEqual(m.oldestSeqHeld, 12, "a page of older frames must move the cursor back, or the "
                       + "next page repeats what was just fetched")

        apply(m, Data(#"{"seq":55,"type":"session.message","payload":{"session_id":"s1","role":"assistant","text":"newest"}}"#.utf8))
        XCTAssertEqual(m.oldestSeqHeld, 12, "a newer frame moved the cursor FORWARD; the next page "
                       + "would start at 55 and skip everything between 12 and 55")
    }

    /// A frame for a DIFFERENT session must not move this session's cursor.
    func testAnotherSessionsFrameDoesNotMoveThisCursor() {
        let m = Model()
        m.sessionID = "s1"
        m.oldestSeqHeld = 30
        apply(m, Data(#"{"seq":2,"type":"session.message","payload":{"session_id":"other","role":"assistant","text":"x"}}"#.utf8))
        XCTAssertEqual(m.oldestSeqHeld, 30,
                       "another session's sequence became this session's cursor — the two counters are "
                       + "unrelated, so the next page would be computed from a foreign position")
    }
}
