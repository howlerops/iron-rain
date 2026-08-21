import XCTest
@testable import OculusUI

/// Block-level gaps found by auditing our renderer against Claude Code's.
final class MarkdownBlockTests: XCTestCase {

    private func blocks(_ s: String) -> [MarkdownBlock] { MarkdownParser.parse(s) }

    // MARK: setext headings

    /// "Title" over "---" is an H2. It was being consumed by the thematic-break rule, which demoted
    /// the heading to body text AND drew a spurious divider beneath it.
    func testSetextUnderlineBecomesHeading() {
        let out = blocks("Overview\n---\nBody text here.")
        guard case .heading(let lvl, let t) = out[0] else { return XCTFail("expected heading: \(out)") }
        XCTAssertEqual(lvl, 2)
        XCTAssertEqual(t, "Overview")
        if case .rule = out[1] { XCTFail("underline should not also emit a rule: \(out)") }
    }

    func testSetextEqualsIsH1AndAnyRunLength() {
        guard case .heading(let lvl, let t)? = blocks("Title\n=====\nBody.").first else {
            return XCTFail("expected heading")
        }
        XCTAssertEqual(lvl, 1)
        XCTAssertEqual(t, "Title")
        // A longer dash run used to render literally as part of the paragraph.
        guard case .heading(_, let t2)? = blocks("Overview\n--------\nBody.").first else {
            return XCTFail("expected heading for long underline")
        }
        XCTAssertEqual(t2, "Overview")
    }

    /// After a blank line the same characters are a thematic break, not an underline. This is the
    /// CommonMark disambiguator and the reason the check is gated on an open paragraph.
    func testDashesAfterBlankLineStayARule() {
        let out = blocks("Some prose.\n\n---\n\nMore prose.")
        XCTAssertEqual(out.count, 3)
        if case .rule = out[1] {} else { XCTFail("expected a rule: \(out)") }
    }

    // MARK: thematic breaks

    func testThematicBreakVariants() {
        for s in ["---", "----", "***", "___", "_____", "* * *", "- - -"] {
            let out = blocks("Para.\n\n\(s)\n\nAfter.")
            guard out.count == 3, case .rule = out[1] else {
                return XCTFail("\(s) did not produce a rule: \(out)")
            }
        }
    }

    // MARK: task lists

    /// Rendered as literal "[ ] foo" before — agents emit checklists constantly.
    func testTaskListCheckboxes() {
        guard case .bullet(let items)? = blocks("- [ ] todo\n- [x] done\n- plain").first else {
            return XCTFail("expected bullets")
        }
        XCTAssertEqual(items.map(\.text), ["todo", "done", "plain"])
        XCTAssertEqual(items.map(\.checked), [false, true, nil])
    }

    // MARK: nesting

    func testNestedBulletsCarryDepth() {
        guard case .bullet(let items)? = blocks("- parent\n  - child\n    - deep").first else {
            return XCTFail("expected bullets")
        }
        XCTAssertEqual(items.map(\.depth), [0, 1, 2])
        XCTAssertEqual(items.map(\.text), ["parent", "child", "deep"])
    }

    func testFourSpaceIndentAlsoNests() {
        guard case .bullet(let items)? = blocks("- parent\n    - child").first else {
            return XCTFail("expected bullets")
        }
        XCTAssertEqual(items.map(\.depth), [0, 2])
    }

    // MARK: lazy continuation

    /// A wrapped bullet used to become a detached full-width paragraph between two list items.
    func testLazyContinuationJoinsTheItem() {
        let out = blocks("- a bullet whose text wraps\n  onto the next line\n- second bullet")
        XCTAssertEqual(out.count, 1, "continuation should not split the list: \(out)")
        guard case .bullet(let items) = out[0] else { return XCTFail("expected bullets") }
        XCTAssertEqual(items[0].text, "a bullet whose text wraps onto the next line")
        XCTAssertEqual(items[1].text, "second bullet")
    }

    /// A fence directly after a list must still open a code block rather than being glued on.
    func testContinuationStopsAtABlockStart() {
        let out = blocks("- item\n```swift\nlet x = 1\n```")
        guard case .bullet = out[0] else { return XCTFail("expected bullets first: \(out)") }
        guard case .code = out[1] else { return XCTFail("expected a code block: \(out)") }
    }

    // MARK: code fences

    func testUnterminatedFenceStillRendersAsCode() {
        let out = blocks("Here:\n```swift\nlet x = 1\n## not a heading")
        guard case .code(let lang, let body) = out[1] else { return XCTFail("expected code: \(out)") }
        XCTAssertEqual(lang, "swift")
        XCTAssertTrue(body.contains("## not a heading"), "the rest of the doc belongs to the fence")
    }

    /// Text ending exactly at the opener produced an empty bordered box.
    func testEmptyFenceProducesNoBlock() {
        let out = blocks("Here:\n```swift")
        XCTAssertFalse(out.contains { if case .code = $0 { return true }; return false },
                       "an empty fence should not emit a code block: \(out)")
    }

    // MARK: separateJammedBold scope

    /// A glob's `**` used to invert bold parity for the rest of the document, so the next real
    /// `**Title**` had a newline injected into the middle of it.
    func testGlobDoesNotPoisonBoldParity() {
        let out = MarkdownParser.separateJammedBold("Use `**/*.swift` to match.")
        XCTAssertEqual(out, "Use `**/*.swift` to match.", "a glob must pass through untouched")
    }

    /// Fenced code must be reproduced exactly — this injected a newline into a Python signature.
    func testFencedCodeIsNotRewritten() {
        let src = "```python\ndef f(**Kwargs):\n    return 1\n```"
        guard case .code(_, let body)? = blocks(src).first else { return XCTFail("expected code") }
        XCTAssertEqual(body, "def f(**Kwargs):\n    return 1")
    }

    /// The behaviour the repair exists for must still work.
    func testJammedBoldTitleStillSeparated() {
        let out = MarkdownParser.separateJammedBold("**Summary**Everything works.")
        XCTAssertTrue(out.contains("**Summary**\n"), "expected a break after the title, got \(out)")
    }

    // MARK: tables

    /// The row loop accepted any following line containing a pipe, so prose after a table was
    /// swallowed into the grid and disappeared.
    func testProseAfterTableIsNotEatenAsARow() {
        let out = blocks("| a | b |\n|---|---|\n| 1 | 2 |\nRun `cat foo | grep bar` to check.")
        guard case .table(let t) = out[0] else { return XCTFail("expected a table: \(out)") }
        XCTAssertEqual(t.rows.count, 1, "only the real row belongs to the table")
        XCTAssertEqual(out.count, 2, "the prose must survive as its own block: \(out)")
    }
}
