import XCTest
@testable import OculusUI

/// Line numbers in a diff must survive content that looks like a file header.
///
/// The parser matched `--- ` and `+++ ` anywhere, but those are FILE headers that only appear before
/// a hunk begins. Inside a hunk they are ordinary content: a deleted line whose text starts with
/// "-- " arrives as "--- ", and "-- " begins every SQL comment and plenty of prose. Such a line was
/// dropped entirely, the side counters never advanced, and every subsequent line number in that hunk
/// was off — so the review UI attached a comment to the wrong line, which is worse than showing none.
final class DiffParserTests: XCTestCase {

    func testALineThatLooksLikeAHeaderIsStillContent() {
        let diff = """
        diff --git a/schema.sql b/schema.sql
        --- a/schema.sql
        +++ b/schema.sql
        @@ -1,4 +1,4 @@
         CREATE TABLE t (
        --- a legacy note
        +-- a revised note
         );
        """
        let files = DiffParser.parse(diff)
        XCTAssertEqual(files.count, 1, "expected one file")
        guard let hunk = files.first?.hunks.first else { return XCTFail("no hunk parsed") }

        let deleted = hunk.lines.filter { $0.kind == .del }
        XCTAssertEqual(deleted.count, 1,
                       "the deleted SQL comment (diff line \"--- a legacy note\") was swallowed as a file header; lines were "
                       + "\(hunk.lines.map { "\($0.kind):\($0.text)" })")

        // The trailing context line must still be numbered correctly. With the deletion dropped,
        // the old-side counter never advanced and this came out one too low.
        guard let closing = hunk.lines.last(where: { $0.kind == .context && $0.text.contains(")") }) else {
            return XCTFail("no closing context line")
        }
        XCTAssertEqual(closing.oldLine, 3, "old-side numbering drifted after the header-looking line")
        XCTAssertEqual(closing.newLine, 3, "new-side numbering drifted after the header-looking line")
    }

    /// Real file headers must still be recognised, so the path is still picked up.
    func testRealFileHeadersStillSetThePath() {
        let diff = """
        --- a/README.md
        +++ b/README.md
        @@ -1,2 +1,2 @@
        -old
        +new
        """
        let files = DiffParser.parse(diff)
        XCTAssertEqual(files.first?.path, "README.md")
    }
}
