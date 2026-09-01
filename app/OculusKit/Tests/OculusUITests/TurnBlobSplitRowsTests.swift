import XCTest
@testable import OculusUI
@testable import OculusKit

/// opencode emits SEVERAL assistant messages inside one turn (a short step summary before each phase;
/// see the `message.part.delta` handler, which injects a "\n\n" delta at each message boundary), and
/// `resyncLast` re-emits only the LAST one. `turnStreamedText` accumulates ALL of them, so the
/// exact-equality guard misses and the split-row duplicate survives.
@MainActor
final class TurnBlobSplitRowsTests: XCTestCase {

    private func apply(_ m: Model, _ json: String) {
        let raw = Data(json.utf8)
        guard let env = try? Protocol.envelope(raw) else { return XCTFail("bad frame: \(json)") }
        m.applyEvent(env, raw: raw)
    }


    /// prose A → tool card → prose B, all inside ONE opencode assistant message that follows an
    /// earlier step-summary message. The resync blob is A+B; row A stays on screen above it.
    func testResyncOfTheSecondMessageDoesNotDuplicateProseSplitByAToolCard() {
        let m = Model()
        m.sessionID = "s1"
        apply(m, #"{"type":"session.message","payload":{"session_id":"s1","role":"user","text":"go"}}"#)
        apply(m, #"{"type":"output.delta","payload":{"session_id":"s1","text":"Step one summary."}}"#)
        apply(m, #"{"type":"output.delta","payload":{"session_id":"s1","text":"\n\n"}}"#) // message boundary
        apply(m, #"{"type":"output.delta","payload":{"session_id":"s1","text":"Part one. "}}"#)
        apply(m, #"{"type":"session.tool","payload":{"session_id":"s1","id":"t1","name":"bash","status":"completed","output":"ok"}}"#)
        apply(m, #"{"type":"output.delta","payload":{"session_id":"s1","text":"Part two."}}"#)
        // opencode resyncLast (SSE reconnect mid-turn, or the turn engine's reconciler): the LAST
        // assistant message's text parts, concatenated.
        apply(m, #"{"type":"session.message","payload":{"session_id":"s1","role":"assistant","text":"Part one. Part two.","msg_id":"msg_2"}}"#)

        let joined = m.messages.filter { $0.role == .assistant }.map(\.text).joined()
        XCTAssertEqual(joined, "Step one summary.\n\nPart one. Part two.",
                       "the reply rendered twice: \(joined)")
    }

    /// The turn's last row is the tool card (the model ended on a tool call): the replace rule cannot
    /// fire at all, so the whole message is appended a second time.
    func testResyncOfTheSecondMessageDoesNotDuplicateWhenTheLastRowIsAToolCard() {
        let m = Model()
        m.sessionID = "s1"
        apply(m, #"{"type":"session.message","payload":{"session_id":"s1","role":"user","text":"go"}}"#)
        apply(m, #"{"type":"output.delta","payload":{"session_id":"s1","text":"Step one summary."}}"#)
        apply(m, #"{"type":"output.delta","payload":{"session_id":"s1","text":"\n\n"}}"#)
        apply(m, #"{"type":"output.delta","payload":{"session_id":"s1","text":"All done."}}"#)
        apply(m, #"{"type":"session.tool","payload":{"session_id":"s1","id":"t1","name":"bash","status":"completed","output":"ok"}}"#)
        apply(m, #"{"type":"session.message","payload":{"session_id":"s1","role":"assistant","text":"All done.","msg_id":"msg_2"}}"#)

        let texts = m.messages.filter { $0.role == .assistant }.map(\.text)
        XCTAssertEqual(texts, ["Step one summary.\n\nAll done."], "the reply rendered twice: \(texts)")
    }
}
