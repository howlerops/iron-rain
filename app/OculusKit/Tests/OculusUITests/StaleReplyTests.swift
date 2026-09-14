import XCTest
@testable import OculusUI
@testable import OculusKit

/// A reply that arrives after the thing it describes has changed.
///
/// The app is full of `await` calls whose result is applied to shared state — the open session's
/// model list, the open file's contents. Every one of them captures what it was asked about, and
/// several never checked, on resumption, that it was still the thing on screen. Tapping two sessions
/// in a row or two files in a row is enough: the first request is still in flight when the second
/// selection lands, and it then overwrites it.
///
/// Cancellation does not save you here. `Model.request` is a bare `withCheckedThrowingContinuation`
/// with no cancellation handler, so `Task.cancel()` marks the task and the await resumes anyway.
@MainActor
final class StaleReplyTests: XCTestCase {

    /// The model picker describes the OPEN session. A reply for one the user has left must not be
    /// applied — nor must its failure branch, which would blank the list for a session that has one.
    func testAModelListForAClosedSessionIsDiscarded() async {
        let m = Model()
        m.sessionID = "ses_B"
        m.sessionModels = [ModelInfo(id: "opencode/gpt-5", name: "GPT-5", provider: "opencode")]
        m.modelEditable = true

        // No client: loadModels' request fails and it takes the else-branch, which clears the list.
        // With the guard, a reply about ses_A cannot touch state describing ses_B either way.
        await m.loadModels(sessionID: "ses_A")

        XCTAssertEqual(m.sessionModels.map(\.id), ["opencode/gpt-5"],
                       "a reply about a session the user has already left rewrote the open session's "
                       + "model picker — it now offers models belonging to a different agent")
        XCTAssertTrue(m.modelEditable, "and reported the open session's model as unchangeable")
    }

    /// Same shape, same file, different list.
    func testACommandListForAClosedSessionIsDiscarded() async {
        let m = Model()
        m.sessionID = "ses_B"
        m.commands = [try! ProtocolCoding.decoder().decode(SlashCommand.self, from: Data(#"{"name":"review"}"#.utf8))]

        await m.loadCommands(sessionID: "ses_A")

        XCTAssertEqual(m.commands.map(\.name), ["review"],
                       "the slash-command menu was replaced with another session's")
    }

    /// And the guard must not fire for the session that IS open — otherwise the picker never loads.
    func testAReplyForTheOpenSessionIsStillApplied() async {
        let m = Model()
        m.sessionID = "ses_A"
        m.sessionModels = [ModelInfo(id: "stale", name: "stale", provider: "x")]

        await m.loadModels(sessionID: "ses_A") // fails (no client) → the else-branch must run

        XCTAssertTrue(m.sessionModels.isEmpty,
                      "the guard is rejecting replies for the session that is actually open")
    }
}
