import XCTest
@testable import OculusUI

/// Sub-agent lanes render their child transcript with the same MessageRow the parent uses, and the
/// parent applies `.equatable()` to it while the lanes did not.
///
/// Model publishes once per streamed delta, on ANY lane. Without the equality check every publish
/// rebuilt every row of every open lane — markdown parse, code-block syntax highlighting and all —
/// so a fan-out with four lanes talking at once paid that whole cost four times per token. The
/// `.equatable()` modifier is one line at each call site; what it depends on is MessageRow's `==`
/// actually returning true for two rows the lanes consider identical, which is what this pins.
final class SubAgentLaneRebuildTests: XCTestCase {

    private let palette = OculusPalette.dark

    /// Both lane call sites build a row from exactly (message, palette) and pass no closures. Two such
    /// rows for an unchanged message must compare equal, or `.equatable()` is a no-op with a cost.
    func testTwoRowsForTheSameChildMessageAreEqual() {
        let msg = ChatMessage(role: .assistant, text: "ran the migration\n\n```sql\nSELECT 1;\n```")
        XCTAssertEqual(MessageRow(message: msg, palette: palette),
                       MessageRow(message: msg, palette: palette),
                       "a lane row rebuilds on every Model publish even when its message did not change")
    }

    /// And the live half: a row whose message is still growing must NOT compare equal, or the lane
    /// would freeze mid-stream showing a partial reply.
    func testAGrowingMessageStillRebuilds() {
        let id = UUID()
        let partial = ChatMessage(id: id, role: .assistant, text: "ran the", streaming: true)
        let more = ChatMessage(id: id, role: .assistant, text: "ran the migration", streaming: true)
        XCTAssertNotEqual(MessageRow(message: partial, palette: palette),
                          MessageRow(message: more, palette: palette),
                          "the lane would stop updating as its sub-agent streams")

        // Finalizing is a change too — the row switches from streamed plain text to parsed markdown.
        let finalized = ChatMessage(id: id, role: .assistant, text: "ran the migration", streaming: false)
        XCTAssertNotEqual(MessageRow(message: more, palette: palette),
                          MessageRow(message: finalized, palette: palette),
                          "a finalized reply would keep rendering as unparsed streaming text")
    }

    /// A theme change has to reach the lanes as well.
    func testAThemeChangeRebuilds() {
        let msg = ChatMessage(role: .assistant, text: "done")
        XCTAssertNotEqual(MessageRow(message: msg, palette: OculusPalette.dark),
                          MessageRow(message: msg, palette: OculusPalette.light),
                          "switching theme would leave open sub-agent lanes in the old colours")
    }
}
