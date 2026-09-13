import XCTest
@testable import OculusUI
@testable import OculusKit

/// An unrecognised turn state must not be treated as "the turn is over".
///
/// applyTurnState's `default:` set busy = false, and the sub-agent seal tested for OPEN states and
/// treated everything else as finished. Both are allowlists pointed the wrong way: a state this
/// build does not know is far more likely to be a NEWER daemon's new non-terminal state than a
/// terminal one. Guessing "terminal" unlocks the composer mid-turn — letting a send race a running
/// agent — and seals every sub-agent lane on a turn that is still going.
@MainActor
final class TurnStateTests: XCTestCase {

    private func turnState(_ state: String, session: String = "s1") -> Data {
        Data(#"{"type":"turn.state","payload":{"session_id":"\#(session)","turn_id":"t1","state":"\#(state)"}}"#.utf8)
    }

    private func apply(_ m: Model, _ raw: Data) {
        guard let env = try? Protocol.envelope(raw) else { return XCTFail("bad frame") }
        m.applyEvent(env, raw: raw)
    }

    func testAnUnknownStateDoesNotUnlockTheComposer() {
        let m = Model()
        m.sessionID = "s1"
        apply(m, turnState("running"))
        XCTAssertTrue(m.busy, "precondition: a running turn holds the composer")

        // A state from a newer daemon that this build has never heard of.
        apply(m, turnState("consolidating"))
        XCTAssertTrue(m.busy,
                      "an unrecognised turn state unlocked the composer — a send can now race a running agent")
    }

    func testKnownTerminalStatesStillRelease() {
        for state in ["idle", "error"] {
            let m = Model()
            m.sessionID = "s1"
            apply(m, turnState("running"))
            apply(m, turnState(state))
            XCTAssertFalse(m.busy, "\(state) is terminal and must release the composer")
        }
    }

    func testRunningStatesStillHold() {
        // awaiting_approval is deliberately excluded: it releases the composer on purpose, so the
        // user can answer. The point here is the states that mean "the agent is still working".
        for state in ["running", "stalled", "recovering"] {
            let m = Model()
            m.sessionID = "s1"
            apply(m, turnState(state))
            XCTAssertTrue(m.busy, "\(state) is not terminal and must hold the composer")
        }
    }
}
