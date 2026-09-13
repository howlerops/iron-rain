import XCTest
@testable import OculusUI
@testable import OculusKit

/// A collaborator's message must not be swallowed by our own echo-suppression.
///
/// We append every sent prompt locally for instant feedback, and the provider echoes the same user
/// message back — so the client drops an incoming user message whose text is already on screen. That
/// check matched on TEXT ALONE, with no author. A second person agreeing with "yes", "continue" or
/// "go ahead" says exactly what is already there, so their message was dropped silently: never
/// rendered, never stored, while their own client showed it as sent.
///
/// The echo this suppresses is by definition ours; a message carrying someone else's author cannot
/// be it.
@MainActor
final class CollaboratorMessageTests: XCTestCase {

    private func userMessage(_ text: String, author: String?, session: String = "s1") -> Data {
        let a = author.map { "\"author\":\"\($0)\"," } ?? ""
        return Data(#"{"type":"session.message","payload":{"session_id":"\#(session)",\#(a)"role":"user","text":"\#(text)"}}"#.utf8)
    }

    private func apply(_ m: Model, _ raw: Data) {
        guard let env = try? Protocol.envelope(raw) else { return XCTFail("bad frame") }
        m.applyEvent(env, raw: raw)
    }

    func testACollaboratorSayingTheSameThingIsStillShown() {
        let m = Model()
        m.sessionID = "s1"
        m.identity = "jacob"

        // Our own message, shown locally, then echoed back by the provider.
        m.messages.append(ChatMessage(role: .user, text: "continue"))
        apply(m, userMessage("continue", author: "jacob"))
        XCTAssertEqual(m.messages.filter { $0.role == .user }.count, 1,
                       "our own echo must still be suppressed")

        // A DIFFERENT person says the same word.
        apply(m, userMessage("continue", author: "sam"))
        XCTAssertEqual(m.messages.filter { $0.role == .user }.count, 2,
                       "the collaborator's message was dropped because it matched text already on screen")
    }
}
