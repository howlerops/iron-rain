import XCTest
@testable import OculusUI
@testable import OculusKit

/// A turn split across rows by a tool card or a generative-UI block, replayed.
///
/// The daemon rings a synthetic end-of-turn message holding the WHOLE turn's text concatenated. On
/// screen that text is not one row — a tool card or an iron:ui block seals the row mid-turn and the
/// next delta opens a fresh one — so neither the dedupReplay scan nor the streaming-replace rule
/// recognised it, and the turn re-rendered under the rows it restates. The client now keeps its own
/// copy of the daemon's per-turn accumulation (Model.turnStreamedText) and matches against that.
@MainActor
final class SplitTurnReplayTests: XCTestCase {

    private func delta(_ text: String, session: String = "s1") -> Data {
        Data(#"{"type":"output.delta","payload":{"session_id":"\#(session)","text":"\#(text)"}}"#.utf8)
    }
    private func msg(_ text: String, session: String = "s1") -> Data {
        Data(#"{"type":"session.message","payload":{"session_id":"\#(session)","role":"assistant","text":"\#(text)"}}"#.utf8)
    }
    private func tool(_ id: String, session: String = "s1") -> Data {
        Data(#"{"type":"session.tool","payload":{"session_id":"\#(session)","id":"\#(id)","name":"bash","title":"ls","output":"a.txt","status":"completed"}}"#.utf8)
    }
    private func uiCard(_ id: String, session: String = "s1") -> Data {
        Data(#"{"type":"ui.component","payload":{"session_id":"\#(session)","id":"\#(id)","component":"callout","schema_v":1,"status":"ready","fallback_text":"note"}}"#.utf8)
    }

    private func apply(_ m: Model, _ frames: [Data]) {
        for raw in frames {
            if let env = try? Protocol.envelope(raw) { m.applyEvent(env, raw: raw) }
        }
    }

    private func dump(_ m: Model) -> String {
        m.messages.map { "\($0.role):\($0.text)" }.joined(separator: " | ")
    }

    /// FRESH OPEN (no cache): the daemon replays deltas + its synthetic end-of-turn message.
    /// A mid-turn tool card splits the prose into two rows; the synthetic message is the
    /// CONCATENATION of both, so neither dedupReplay nor the streaming-replace rule recognises it.
    func testReplayOfTurnSplitByToolCard() {
        let m = Model()
        m.sessionID = "s1"
        m.dedupReplay = true
        apply(m, [delta("Hello world."), tool("t1"), delta("Done."), msg("Hello world.Done.")])
        let prose = m.messages.filter { $0.role == .assistant }.map(\.text)
        XCTAssertFalse(prose.contains { $0.contains("Hello world.") && $0 != "Hello world." },
                       "the whole-turn concatenation re-rendered prose already on screen: \(dump(m))")
    }

    /// Same turn, but ending with a generative-UI card (flushUI runs immediately before
    /// finalizeTurnTranscript daemon-side), so the last row is sealed and the message APPENDS.
    func testReplayOfTurnSplitByToolCardEndingInUICard() {
        let m = Model()
        m.sessionID = "s1"
        m.dedupReplay = true
        apply(m, [delta("Hello world."), tool("t1"), delta("Done."), uiCard("u1"), msg("Hello world.Done.")])
        let prose = m.messages.filter { $0.role == .assistant }.map(\.text)
        XCTAssertFalse(prose.contains { $0.contains("Done.") && $0 != "Done." },
                       "the whole-turn concatenation was appended below the rows it restates: \(dump(m))")
    }

    /// CACHE SPLICE path, same split turn: the new transcriptSplicing guard compares whole rows.
    func testSpliceOfTurnSplitByToolCard() {
        let m = Model()
        m.daemonPubHex = "pub"
        let frames = [delta("Hello world."), tool("t1"), delta("Done.")]
        m.transcriptHydrated["s1"] = frames
        m.sessionID = "s1"
        XCTAssertTrue(m.paintFromCache("s1"))
        m.transcriptReplayBuffer = frames + [msg("Hello world.Done.")]
        m.finishReconcile()
        let prose = m.messages.filter { $0.role == .assistant }.map(\.text)
        XCTAssertFalse(prose.contains { $0.contains("Done.") && $0 != "Done." },
                       "spliced synthetic message restated the split turn: \(dump(m))")
    }

    /// CONTROL: an unsplit turn — must stay clean (this is the case the existing guards cover).
    func testReplayOfUnsplitTurn() {
        let m = Model()
        m.sessionID = "s1"
        m.dedupReplay = true
        apply(m, [delta("Hello world."), msg("Hello world.")])
        XCTAssertEqual(m.messages.filter { $0.role == .assistant }.count, 1, dump(m))
    }
}
