import XCTest
@testable import OculusUI

/// Tables and blockquotes had NO case in `MarkdownBlock`, so both fell through to `.paragraph` and
/// rendered as literal `| a | b |` pipe soup and leading `>` characters. Agents emit tables
/// constantly, which made this the most visible formatting defect in returned responses.
final class MarkdownTableTests: XCTestCase {

    private func blocks(_ s: String) -> [MarkdownBlock] { MarkdownParser.parse(s) }

    func testParsesPipeTable() {
        let md = """
        | Name | Count | Status |
        | --- | ---: | :---: |
        | parser | 12 | ok |
        | render | 3 | slow |
        """
        guard case .table(let t)? = blocks(md).first else {
            return XCTFail("expected a table, got \(blocks(md))")
        }
        XCTAssertEqual(t.columns.map(\.label), ["Name", "Count", "Status"])
        // Alignment comes from the delimiter row's colons.
        XCTAssertEqual(t.columns.map { $0.align ?? "" }, ["left", "right", "center"])
        XCTAssertEqual(t.rows.count, 2)
        XCTAssertEqual(t.rows[0].map(\.displayString), ["parser", "12", "ok"])
        XCTAssertEqual(t.rows[1].map(\.displayString), ["render", "3", "slow"])
    }

    /// GFM makes the outer pipes optional.
    func testParsesTableWithoutOuterPipes() {
        guard case .table(let t)? = blocks("a | b\n--- | ---\n1 | 2").first else {
            return XCTFail("expected a table")
        }
        XCTAssertEqual(t.columns.map(\.label), ["a", "b"])
        XCTAssertEqual(t.rows[0].map(\.displayString), ["1", "2"])
    }

    /// The delimiter row is what separates a real table from prose that merely contains a pipe.
    /// Without requiring it, a shell pipeline or a regex alternation would be eaten as a table.
    func testProseWithPipesIsNotATable() {
        for prose in ["run `cat x | grep y` to check", "match /foo|bar/ here"] {
            if case .table = blocks(prose).first {
                XCTFail("treated prose as a table: \(prose)")
            }
        }
    }

    /// Ragged rows are common in model-written tables; they must not drop cells out of alignment.
    func testRaggedRowsArePaddedAndTrimmed() {
        guard case .table(let t)? = blocks("| a | b | c |\n|---|---|---|\n| 1 |\n| 1 | 2 | 3 | 4 |").first else {
            return XCTFail("expected a table")
        }
        XCTAssertEqual(t.rows[0].map(\.displayString), ["1", "", ""])
        XCTAssertEqual(t.rows[1].map(\.displayString), ["1", "2", "3"])
    }

    func testEscapedPipeStaysInsideACell() {
        guard case .table(let t)? = blocks("| expr |\n|---|\n| a \\| b |").first else {
            return XCTFail("expected a table")
        }
        XCTAssertEqual(t.rows[0].map(\.displayString), ["a | b"])
    }

    func testBlockquote() {
        guard case .quote(let lines)? = blocks("> first\n> second").first else {
            return XCTFail("expected a quote, got \(blocks("> first\n> second"))")
        }
        XCTAssertEqual(lines, ["first", "second"])
    }

    /// A table following a paragraph must still be recognised, and the paragraph must not absorb it.
    func testTableAfterParagraph() {
        let out = blocks("Here are the results:\n\n| k | v |\n|---|---|\n| a | 1 |")
        XCTAssertEqual(out.count, 2)
        if case .table = out[1] {} else { XCTFail("second block should be the table: \(out)") }
    }
}
