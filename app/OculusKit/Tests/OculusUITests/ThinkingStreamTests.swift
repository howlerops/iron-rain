import XCTest
@testable import OculusUI
@testable import OculusKit

/// The model's reasoning must render as reasoning, and must seal when it ends.
///
/// Reasoning and the answer both stream through one `streamBuffer`, and flushStream folded that
/// buffer into the last streaming ASSISTANT row — appending a fresh assistant row when there wasn't
/// one. So thinking tokens could never reach the `.thinking` row they were buffered for.
///
/// Two visible failures followed. The reasoning appeared as a full-weight answer bubble instead of
/// the dimmed italic brain-icon row, so the user could not tell the model's scratch work from its
/// reply. And finalizeThinking calls flushStream and then seals `messages.last` — which by then was
/// the row flushStream had just appended — leaving the real thinking row `streaming: true` forever,
/// as a bare brain icon with no text.
///
/// Every harness emits thinking deltas, so this was every turn with reasoning turned on.
@MainActor
final class ThinkingStreamTests: XCTestCase {

    private func apply(_ m: Model, _ raw: Data) {
        guard let env = try? Protocol.envelope(raw) else { return XCTFail("bad frame") }
        m.applyEvent(env, raw: raw)
    }

    private func thinking(_ text: String) -> Data {
        Data(#"{"type":"thinking.delta","payload":{"session_id":"s1","text":"\#(text)"}}"#.utf8)
    }

    private func answer(_ text: String) -> Data {
        Data(#"{"type":"output.delta","payload":{"session_id":"s1","text":"\#(text)"}}"#.utf8)
    }

    func testReasoningLandsInTheThinkingRowNotTheAnswer() {
        let m = Model()
        m.sessionID = "s1"
        apply(m, thinking("let me check the config"))
        m.flushStreamForTests()

        let thinkingText = m.messages.filter { $0.role == .thinking }.map(\.text).joined()
        let answerText = m.messages.filter { $0.role == .assistant }.map(\.text).joined()

        XCTAssertTrue(thinkingText.contains("let me check the config"),
                      "the thinking row is empty; rows were \(m.messages.map { "\($0.role):\($0.text)" })")
        XCTAssertFalse(answerText.contains("let me check the config"),
                       "the model's scratch work rendered as its ANSWER — a full-weight bubble the "
                       + "user cannot tell apart from the reply")
    }

    func testTheThinkingRowSealsWhenTheAnswerBegins() {
        let m = Model()
        m.sessionID = "s1"
        apply(m, thinking("weighing two options"))
        apply(m, answer("Here is what I found."))
        m.flushStreamForTests()

        let stillStreaming = m.messages.filter { $0.role == .thinking && $0.streaming }
        XCTAssertTrue(stillStreaming.isEmpty,
                      "a thinking row is still streaming after the answer started — it renders as a "
                      + "bare brain icon with no text, forever")

        let thinkingText = m.messages.filter { $0.role == .thinking }.map(\.text).joined()
        let answerText = m.messages.filter { $0.role == .assistant }.map(\.text).joined()
        XCTAssertTrue(thinkingText.contains("weighing two options"))
        XCTAssertTrue(answerText.contains("Here is what I found."))
        XCTAssertFalse(answerText.contains("weighing two options"),
                       "the reasoning leaked into the answer bubble")
    }

    /// Interleaving is the real shape: reason, answer, reason again. Each burst has to land in its
    /// own row, and nothing may be lost at a role boundary.
    func testInterleavedReasoningAndAnswerStaySeparate() {
        let m = Model()
        m.sessionID = "s1"
        apply(m, thinking("first thought"))
        apply(m, answer("partial answer. "))
        apply(m, thinking("second thought"))
        apply(m, answer("rest of the answer."))
        m.flushStreamForTests()

        let thinkingText = m.messages.filter { $0.role == .thinking }.map(\.text).joined()
        let answerText = m.messages.filter { $0.role == .assistant }.map(\.text).joined()
        for fragment in ["first thought", "second thought"] {
            XCTAssertTrue(thinkingText.contains(fragment), "lost reasoning: \(fragment)")
            XCTAssertFalse(answerText.contains(fragment), "reasoning leaked into the answer: \(fragment)")
        }
        for fragment in ["partial answer. ", "rest of the answer."] {
            XCTAssertTrue(answerText.contains(fragment), "lost answer text: \(fragment)")
        }
    }
}
