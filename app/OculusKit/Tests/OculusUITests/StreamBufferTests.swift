import XCTest
@testable import OculusUI
@testable import OculusKit

/// Buffered agent tokens must survive a row being appended underneath them.
///
/// flushStream cleared `streamBuffer` unconditionally but only wrote it when `messages.last` was
/// streaming. Anything that appended a non-streaming row between a delta and its flush therefore
/// deleted up to a flush interval of the agent's output — permanently, because the daemon does not
/// resend what it already streamed. Two user-triggered paths do exactly that: sending a follow-up
/// appends the user row, and a generative-UI action appends its optimistic echo. The composer stays
/// live during a run by design, so this was reachable just by typing while the agent was talking.
@MainActor
final class StreamBufferTests: XCTestCase {

    private func delta(_ text: String, session: String = "s1") -> Data {
        Data(#"{"type":"output.delta","payload":{"session_id":"\#(session)","text":"\#(text)"}}"#.utf8)
    }

    private func apply(_ m: Model, _ raw: Data) {
        guard let env = try? Protocol.envelope(raw) else { return XCTFail("bad frame") }
        m.applyEvent(env, raw: raw)
    }

    func testTokensSurviveARowAppendedBeneathThem() {
        let m = Model()
        m.sessionID = "s1"
        apply(m, delta("the agent was mid-sentence"))

        // A user row lands before the buffered tokens have been flushed — exactly what sending a
        // follow-up mid-stream does.
        m.messages.append(ChatMessage(role: .user, text: "actually, do this instead"))
        m.flushStreamForTests()

        let assistantText = m.messages.filter { $0.role == .assistant }.map(\.text).joined()
        XCTAssertTrue(assistantText.contains("the agent was mid-sentence"),
                      "the agent's buffered tokens were discarded when a row was appended beneath them; "
                      + "rows were \(m.messages.map { "\($0.role):\($0.text)" })")
    }

    func testTokensStillLandNormallyWhenNothingInterferes() {
        let m = Model()
        m.sessionID = "s1"
        apply(m, delta("hello "))
        apply(m, delta("world"))
        m.flushStreamForTests()
        let assistantText = m.messages.filter { $0.role == .assistant }.map(\.text).joined()
        XCTAssertEqual(assistantText, "hello world")
    }
}
