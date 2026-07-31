import XCTest
@testable import OculusUI

/// Regression tests for the "wall of text" render: reasoning-step titles arriving with no separator
/// between them and the prose that follows.
final class MarkdownJamTests: XCTestCase {

    /// The exact shape from the report: a bold title fused to the sentence after it.
    func testBoldTitleFusedToProseIsSeparated() {
        let raw = "**Testing directory listing with bash ls**The filesystem tool batch is aborting."
        let fixed = MarkdownParser.separateJammedBold(raw)
        XCTAssertTrue(fixed.contains("**Testing directory listing with bash ls**\nThe filesystem"),
                      "a closing bold run followed by a new sentence must gain a break, got: \(fixed)")
    }

    /// Two adjacent titles collide into `****`, which is never meaningful markdown.
    func testAdjacentBoldRunsAreSplit() {
        let raw = "**Delegating tasks with corrected paths****Inspecting Jira projects for UNIT**"
        let fixed = MarkdownParser.separateJammedBold(raw)
        XCTAssertFalse(fixed.contains("****"), "an empty bold must not survive: \(fixed)")
        let blocks = MarkdownParser.parse(raw)
        XCTAssertEqual(blocks.count, 2, "two jammed titles should render as two blocks, got \(blocks.count)")
    }

    /// Legitimate inline bold must be left alone — this is the risk of the repair.
    func testInlineBoldIsUntouched() {
        for raw in [
            "This is **important**, and then some more prose.",
            "The **config** file lives there.",
            "**Note**: check the logs.",
            "Use **bold**s sparingly.",
            "no bold here at all",
        ] {
            XCTAssertEqual(MarkdownParser.separateJammedBold(raw), raw,
                           "legitimate inline bold must not be rewritten: \(raw)")
        }
    }

    /// Text that already has proper breaks must round-trip unchanged.
    func testWellFormedMarkdownIsUnchanged() {
        let raw = """
        **A heading**

        Some prose here.

        **Another heading**

        More prose.
        """
        XCTAssertEqual(MarkdownParser.separateJammedBold(raw), raw)
    }

    /// End to end: the jammed turn gains real breaks rather than rendering as one wall.
    ///
    /// Asserts the user-visible property (a break exists between each title and its prose), not the
    /// block count — a single newline inside a paragraph is already rendered as a HARD break by
    /// ChatMarkdownView, so counting blocks would test an implementation detail and miss the point.
    func testJammedTurnGainsBreaks() {
        let raw = "**Confirming repository path resolutions**Reviewing the package.**Planning global project authentication**Deciding scope."
        let blocks = MarkdownParser.parse(raw)
        let paragraphText = blocks.compactMap { block -> String? in
            if case .paragraph(let t) = block { return t }
            return nil
        }.joined(separator: "\n")
        XCTAssertTrue(paragraphText.contains("resolutions**\nReviewing"),
                      "the first title must be broken from its prose, got: \(paragraphText)")
        XCTAssertTrue(paragraphText.contains("authentication**\nDeciding"),
                      "the second title must be broken from its prose, got: \(paragraphText)")
        XCTAssertFalse(paragraphText.contains("****"), "no empty bold may survive")
    }
}
