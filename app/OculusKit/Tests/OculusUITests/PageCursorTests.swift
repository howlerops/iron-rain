import XCTest
@testable import OculusUI
@testable import OculusKit

/// "Show earlier messages" must ask for the page that actually continues the transcript.
///
/// `daemonEventsRendered` is the cursor the client sends as `loaded`, and the daemon computes
/// `end := len(all) - loaded` against its replayable RING. Counting a frame the ring never held
/// inflates that cursor, so the page comes back starting further into the past than where the
/// client's transcript ends — a hole — and once the count exceeds the ring's length, `end` goes to
/// zero and the live window is skipped entirely.
///
/// session.status and session.facts were both being counted. Both are sent with broadcastTransient
/// precisely so they stay OUT of the ring (turn.go and surface.go each say so), and both carry a
/// session_id, so both matched the counting predicate. publishSessionState fires per tool call, so a
/// single busy turn overcounts by dozens.
@MainActor
final class PageCursorTests: XCTestCase {

    private func apply(_ m: Model, _ raw: Data) {
        guard let env = try? Protocol.envelope(raw) else { return XCTFail("bad frame") }
        m.applyEvent(env, raw: raw)
    }

    /// Only frames the daemon actually appends to its ring may advance the cursor.
    func testTransientFramesDoNotAdvanceThePageCursor() {
        let m = Model()
        m.sessionID = "s1"

        let before = m.daemonEventsRendered

        // Transient: delivered to subscribers, never appended to the ring.
        apply(m, Data(#"{"type":"session.status","payload":{"session_id":"s1","status":"running","detail":"Read"}}"#.utf8))
        apply(m, Data(#"{"type":"session.facts","payload":{"session_id":"s1"}}"#.utf8))
        apply(m, Data(#"{"type":"turn.state","payload":{"session_id":"s1","state":"running"}}"#.utf8))

        XCTAssertEqual(m.daemonEventsRendered, before,
                       "a frame the daemon never put in its ring advanced the paging cursor. "
                       + "\"Show earlier messages\" will skip past the messages in between, and once "
                       + "the cursor passes the ring's length it skips the live window altogether.")
    }

    /// And a real, ringed frame must still advance it — otherwise paging would re-fetch what the
    /// user is already looking at.
    func testRingedFramesStillAdvanceThePageCursor() {
        let m = Model()
        m.sessionID = "s1"
        let before = m.daemonEventsRendered
        apply(m, Data(#"{"type":"session.message","payload":{"session_id":"s1","role":"assistant","text":"hello"}}"#.utf8))
        XCTAssertEqual(m.daemonEventsRendered, before + 1,
                       "a replayable frame did not advance the cursor — paging would hand back "
                       + "messages that are already on screen")
    }

    /// A frame for a DIFFERENT session must not move this session's cursor either.
    func testAnotherSessionsFrameDoesNotAdvanceThisCursor() {
        let m = Model()
        m.sessionID = "s1"
        let before = m.daemonEventsRendered
        apply(m, Data(#"{"type":"session.message","payload":{"session_id":"other","role":"assistant","text":"x"}}"#.utf8))
        XCTAssertEqual(m.daemonEventsRendered, before)
    }
}
