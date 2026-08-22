import XCTest
@testable import OculusUI

/// The parser scans assistant text for standalone generative-UI component objects. It ran on every
/// body evaluation — finalize, theme change, chat-font change — and was O(braces × length), because
/// it recomputed "am I inside a code fence?" from the start of the document for every candidate.
final class AssistantContentParserTests: XCTestCase {

    private func parse(_ s: String) -> [AssistantContentSegment] {
        AssistantContentParser.parse(s, sessionID: "s", messageID: "m")
    }

    /// The behaviour that must survive the optimization: braces inside a fence are code, not
    /// components. Skipping them early is the whole speedup, so this is the guard on it.
    func testBracesInsideAFenceAreNotComponents() {
        let md = """
        Here is some JSON:

        ```json
        {"component":"table","id":"t1","props":{"columns":[],"rows":[]}}
        ```

        That was just an example.
        """
        for seg in parse(md) {
            if case .component = seg.kind {
                XCTFail("a fenced object was treated as a component")
            }
        }
    }

    /// And the mirror: an UNfenced component object must still be picked up.
    func testStandaloneObjectOutsideAFenceIsAComponent() {
        let md = """
        Before.

        {"component":"callout","id":"c1","schema_v":1,"status":"ready","props":{"body":"hi"},"fallback_text":"hi"}

        After.
        """
        let found = parse(md).contains { if case .component = $0.kind { return true }; return false }
        XCTAssertTrue(found, "an unfenced component object was not recognised")
    }

    /// Text after a CLOSED fence is outside it again — an off-by-one in the toggle walk would
    /// silently stop recognising every component following the first code block.
    func testFenceStateResetsAfterClosing() {
        let md = """
        ```
        {"not":"a component"}
        ```

        {"component":"callout","id":"c2","schema_v":1,"status":"ready","props":{"body":"x"},"fallback_text":"x"}
        """
        let found = parse(md).contains { if case .component = $0.kind { return true }; return false }
        XCTAssertTrue(found, "a component after a closed fence was missed — fence state did not reset")
    }

    /// The performance guard. The pathological input is a long fenced JSON block: previously every
    /// brace triggered both a full-prefix split AND a walk toward endIndex.
    func testLargeFencedJSONParsesQuickly() {
        var body = "Here are the results:\n\n```json\n"
        for i in 0..<400 {
            body += "{\"id\":\(i),\"name\":\"item \(i)\",\"nested\":{\"a\":1,\"b\":[1,2,3]}}\n"
        }
        body += "```\n\nDone."

        let start = Date()
        _ = parse(body)
        let elapsed = Date().timeIntervalSince(start)
        // Measured at ~36ms before this change. The bound is deliberately loose so it fails only on a
        // genuine regression to quadratic behaviour, not on a slow CI machine.
        XCTAssertLessThan(elapsed, 0.15, "parse took \(Int(elapsed * 1000))ms — the fence fast-path likely regressed")
    }
}
