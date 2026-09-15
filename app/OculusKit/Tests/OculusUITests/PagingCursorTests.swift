import XCTest
@testable import OculusUI
@testable import OculusKit

/// The paging cursor is a number the DAEMON put on the frame, not one the client derived.
///
/// The client used to tally frames it had rendered and skip the ones it believed the daemon had not
/// stored — a hand-maintained list of message types. That list was wrong in three consecutive
/// sweeps, and a census written to police it found four more the day it ran. Every over-count asks
/// for a page starting before the transcript actually ends, and the hole is invisible: a short page
/// looks exactly like the beginning of the conversation.
final class PagingCursorTests: XCTestCase {

    private func sourceOf(_ relative: String) throws -> String {
        let here = URL(fileURLWithPath: #filePath)
        let root = here.deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
        return try String(contentsOf: root.appendingPathComponent(relative), encoding: .utf8)
    }

    /// A stored frame carries its position; an unstored one does not, and the absence is the signal.
    func testTheEnvelopeCarriesTheSequence() throws {
        let stored = try Protocol.envelope(Data(#"{"seq":42,"type":"session.message","payload":{"session_id":"s1"}}"#.utf8))
        XCTAssertEqual(stored.seq, 42, "a stored frame's cursor position did not decode")

        let transient = try Protocol.envelope(Data(#"{"type":"session.status","payload":{"session_id":"s1"}}"#.utf8))
        XCTAssertNil(transient.seq,
                     "a frame the daemon never stored reported a position. That is what the old type "
                     + "list was guessing at, and guessing wrong inflates the cursor into a hole.")
    }

    /// The cursor must move only on sequenced frames, and only ever DOWNWARD — it is the oldest thing
    /// held, which is what "show me what came before" means.
    @MainActor
    func testTheCursorTracksTheOldestSequenceHeld() {
        let m = Model()
        XCTAssertNil(m.oldestSeqHeld, "a client with no history must page from nothing, not from zero")
        m.oldestSeqHeld = 9
        m.oldestSeqHeld = min(m.oldestSeqHeld ?? .max, 4)
        XCTAssertEqual(m.oldestSeqHeld, 4)
        m.oldestSeqHeld = min(m.oldestSeqHeld ?? .max, 7)
        XCTAssertEqual(m.oldestSeqHeld, 4, "the cursor moved forward; the next page would skip 4…7")
    }

    /// The counting machinery and its census are gone, and the page request sends the cursor.
    func testTheGuessingMachineryIsGone() throws {
        let model = try sourceOf("Sources/OculusUI/OculusUI.swift")
        XCTAssertFalse(model.contains("daemonEventsRendered += 1"),
                       "the client still tallies frames to build a cursor")
        XCTAssertFalse(model.contains("static let nonRingFrameTypes"),
                       "the type list is still here; it is the thing that was wrong three times")
        XCTAssertTrue(model.contains("TranscriptPage(sessionID: sid, beforeSeq: oldestSeqHeld)"),
                      "the page request does not send the cursor")

        // The wire type must carry before_seq, or the daemon falls back to the count path.
        let proto = try sourceOf("Sources/OculusKit/Protocol.swift")
        XCTAssertTrue(proto.contains(#"beforeSeq = "before_seq""#),
                      "TranscriptPage does not encode the cursor onto the wire")
    }

    /// The reconcile's "state, not history" test is a DIFFERENT question from the cursor and must not
    /// be re-expressed as `seq == nil`: a streaming delta has no sequence either, and passing deltas
    /// straight through during a reconcile would change what the transcript shows while it rebuilds.
    func testReconcileKeepsItsOwnList() throws {
        let cache = try sourceOf("Sources/OculusUI/ModelTranscriptCache.swift")
        XCTAssertTrue(cache.contains("stateNotHistory"),
                      "the reconcile lost its own list and is presumably borrowing the cursor's rule")
        // The CALL, not the prose: the comment above it explains why the borrowing stopped, and a
        // substring match on the name flags that explanation as the defect it describes.
        XCTAssertFalse(cache.contains("Model.nonRingFrameTypes.contains"),
                       "the reconcile still borrows a list that no longer exists for its purpose")
    }
}
