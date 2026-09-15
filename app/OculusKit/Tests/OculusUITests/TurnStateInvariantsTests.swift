import XCTest
@testable import OculusUI
@testable import OculusKit

/// Turn Engine stage 5, client half: recorded `turn.state` sequences fed through the REAL envelope
/// ingress, asserting what the user ends up looking at.
///
/// The daemon-side chaos suite (daemon/hub/turnengine_e2e_test.go) proves the daemon converges on
/// the truth. It cannot prove the client renders that truth, and the failure everyone actually
/// experienced lived on this side: a spinner that never stopped, or "No response from the agent"
/// over a turn that was working fine. So these drive `applyEvent` — the same function the socket
/// calls — rather than poking `busy` directly, because the mapping from state to UI is the thing
/// under test and reaching past it would test nothing.
@MainActor
final class TurnStateInvariantsTests: XCTestCase {

    private let sid = "s_turn"

    private func model() -> Model {
        let m = Model()
        m.sessionID = sid
        return m
    }

    /// Builds the exact bytes the daemon puts on the wire for one turn.state.
    private func feed(_ m: Model, _ state: String, reason: String? = nil, nudges: Int? = nil) {
        var payload: [String: Any] = ["session_id": sid, "turn_id": "t1", "state": state]
        if let reason { payload["reason"] = reason }
        if let nudges { payload["nudges"] = nudges }
        let frame: [String: Any] = ["type": "turn.state", "payload": payload]
        let raw = try! JSONSerialization.data(withJSONObject: frame)
        guard let env = try? Protocol.envelope(raw) else {
            return XCTFail("turn.state did not decode — every assertion below would be vacuous")
        }
        m.applyEvent(env, raw: raw)
    }

    /// The spinner runs for exactly the states that mean "work is happening", and stops for every
    /// state that means it is not.
    ///
    /// `stalled` is in the busy set on purpose: the daemon is nudging an agent it still believes is
    /// alive, so the composer must stay locked or an accidental send races the nudge. `needs_you` is
    /// not — that one is waiting on a human.
    func testSpinnerRunsIffTheTurnIsActuallyRunning() {
        for state in ["running", "stalled", "recovering"] {
            let m = model()
            feed(m, state)
            XCTAssertTrue(m.busy, "turn.state=\(state) left the composer unlocked; a send now races "
                          + "an agent the daemon believes is still working")
        }
        for state in ["idle", "error", "needs_you", "abandoned"] {
            let m = model()
            feed(m, "running")
            feed(m, state, reason: "because")
            XCTAssertFalse(m.busy, "turn.state=\(state) left the spinner running — this is the "
                           + "forever-spinner the whole Turn Engine was built to end")
        }
    }

    /// "No response from the agent" is the daemon's `abandoned` verdict and nothing else.
    ///
    /// The client used to reach this conclusion itself, on a timer, and it was wrong constantly: a
    /// long build or a phone in a pocket both look exactly like a dead agent from here. A state that
    /// merely means "stopped making progress" must never render as one.
    func testNoResponseIsShownOnlyForAbandoned() {
        for state in ["running", "stalled", "needs_you", "idle"] {
            let m = model()
            feed(m, state, reason: "it stopped making progress")
            XCTAssertNil(m.actionError,
                         "turn.state=\(state) raised an error banner. Nothing failed — and an error "
                         + "banner that cries wolf is how people learn to ignore the real one.")
        }
        let m = model()
        feed(m, "abandoned", reason: "agent unreachable for 2m0s")
        XCTAssertEqual(m.actionErrorTitle, "No response from the agent")
        XCTAssertEqual(m.actionError, "agent unreachable for 2m0s",
                       "abandoned dropped the daemon's reason, so the one screen that explains what "
                       + "happened says only that something did")
    }

    /// Busy must never stick after a turn closes — including when the states arrive out of the
    /// textbook order, which over a reconnect they do.
    func testBusyNeverSticksAfterAClose() {
        let sequences: [[String]] = [
            ["running", "idle"],
            ["running", "stalled", "needs_you"],
            ["running", "recovering", "abandoned"],
            ["running", "stalled", "running", "idle"],       // nudge worked; it went back to work
            ["idle", "running", "idle"],                     // a second turn in the same session
            ["running", "abandoned", "abandoned"],           // a repeated terminal frame
        ]
        for seq in sequences {
            let m = model()
            for s in seq { feed(m, s, reason: "r") }
            XCTAssertFalse(m.busy, "after \(seq.joined(separator: " → ")) the composer is still locked")
        }
    }

    /// A state from a NEWER daemon must not unlock the composer.
    ///
    /// An unknown value is far likelier to be a new non-terminal state than a new terminal one, and
    /// guessing "terminal" unlocks the composer mid-turn. Guessing the other way costs a stuck
    /// spinner until the next known frame, which the next heartbeat supplies.
    func testAnUnknownStateLeavesTheComposerAlone() {
        let m = model()
        feed(m, "running")
        feed(m, "reticulating_splines")
        XCTAssertTrue(m.busy, "an unrecognised turn state unlocked the composer mid-turn; a send "
                      + "now races a running agent")
    }

    /// A turn.state for a DIFFERENT session must not touch this one.
    ///
    /// Sub-agents and fanout mean several sessions stream at once over one socket, so a frame for a
    /// sibling arriving here would stop the spinner on the session the user is actually watching.
    func testAnotherSessionsTurnIsIgnored() {
        let m = model()
        feed(m, "running")
        let other: [String: Any] = ["type": "turn.state",
                                    "payload": ["session_id": "someone_else", "turn_id": "t9",
                                                "state": "abandoned", "reason": "gone"]]
        let raw = try! JSONSerialization.data(withJSONObject: other)
        m.applyEvent(try! Protocol.envelope(raw), raw: raw)
        XCTAssertTrue(m.busy, "a sibling session's abandoned turn stopped this session's spinner")
        XCTAssertNil(m.actionError, "a sibling session's failure raised an error on this one")
    }

    /// The nudge count reaches the user as a count.
    ///
    /// "Stuck — nudged 2×" and "Stuck — nudging" say different things: one says the daemon has been
    /// trying for a while, which is the difference between waiting and intervening.
    func testNudgeCountIsSurfaced() {
        let m = model()
        feed(m, "stalled", nudges: 2)
        XCTAssertEqual(m.status, "Stuck — nudged 2×")
        let fresh = model()
        feed(fresh, "stalled")
        XCTAssertEqual(fresh.status, "Stuck — nudging")
    }

    /// Envelope.seq must decode as a number and stay monotonic through the decoder.
    ///
    /// Stage 4 made seq the client's cursor. It is the one field where a silent decode failure is
    /// invisible — everything renders, paging just quietly stops being gap-safe — so it is asserted
    /// on the decoded value rather than on the JSON.
    func testSeqDecodesAndOrders() throws {
        var last: Int64 = 0
        for n in [1, 2, 3, 9_007_199_254_740_993] as [Int64] {
            let raw = try JSONSerialization.data(withJSONObject: [
                "type": "session.message", "seq": NSNumber(value: n),
                "payload": ["session_id": sid, "role": "assistant", "text": "x"]])
            let env = try Protocol.envelope(raw)
            guard let seq = env.seq else {
                return XCTFail("seq did not decode for \(n); the client's paging cursor is silently nil")
            }
            XCTAssertEqual(seq, n, "seq \(n) decoded as \(seq) — beyond Double's exact range this "
                           + "is what a JSON number parsed as floating point does, and it lands on "
                           + "a cursor the daemon will never match")
            XCTAssertGreaterThan(seq, last)
            last = seq
        }
    }
}
