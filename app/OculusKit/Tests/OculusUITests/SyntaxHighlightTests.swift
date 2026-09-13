import XCTest
@testable import OculusUI

/// The tokenizer is reached from a SwiftUI view body, so it runs on scroll, on theme change, and on
/// any @Published touch. It used to build all nine of its NSRegularExpressions on EVERY call —
/// compiling a regex costs orders of magnitude more than matching with one, so that was most of the
/// cost of drawing a code block, paid again on every frame that touched it.
///
/// These assert the behaviour is unchanged (the real risk in the swap) and that repeated calls are
/// cheap enough that compilation is clearly not happening per call.
final class SyntaxHighlightTests: XCTestCase {

    private let sample = """
    // a comment
    func greet(_ name: String) -> Int {
        let msg = "hello \\(name)"   // trailing
        let n = 42
        return n
    }
    """

    func testTokensAreUnchangedByPrecompilation() {
        let toks = SyntaxHighlighter.tokens(sample, language: .swift)
        XCTAssertFalse(toks.isEmpty, "the sample must produce tokens at all")

        // The specific kinds the sample is built to exercise.
        let kinds = Set(toks.map { String(describing: $0.1) })
        for expected in ["comment", "string", "number", "keyword"] {
            XCTAssertTrue(kinds.contains(expected), "expected a \(expected) token, got \(kinds.sorted())")
        }
        // Deterministic: the same input must tokenize identically every time.
        let again = SyntaxHighlighter.tokens(sample, language: .swift)
        XCTAssertEqual(toks.count, again.count)
        for (a, b) in zip(toks, again) {
            XCTAssertEqual(a.0, b.0)
            XCTAssertEqual(String(describing: a.1), String(describing: b.1))
        }
    }

    func testJSONStillTokenizes() {
        let toks = SyntaxHighlighter.tokens(#"{"a": 1, "ok": true}"#, language: .json)
        let kinds = Set(toks.map { String(describing: $0.1) })
        XCTAssertTrue(kinds.contains("string"))
        XCTAssertTrue(kinds.contains("number"))
        XCTAssertTrue(kinds.contains("keyword"))
    }

    /// A scroll re-renders many rows: 200 tokenizations stands in for that. With the regexes
    /// compiled per call this was the dominant cost of a code-heavy transcript.
    func testRepeatedTokenizationIsCheap() {
        let started = Date()
        for _ in 0..<200 {
            _ = SyntaxHighlighter.tokens(sample, language: .swift)
        }
        let elapsed = Date().timeIntervalSince(started)
        XCTAssertLessThan(elapsed, 1.0,
                          "200 tokenizations took \(elapsed)s — that is regex compilation, not matching")
    }
}
