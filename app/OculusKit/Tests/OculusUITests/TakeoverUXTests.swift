import XCTest
@testable import OculusUI
@testable import OculusKit

/// Tests for the takeover seam (roadmap Phase 3 items 2 + 5) and for the header's refusal to
/// conflate "the socket is down" with "the agent is idle" (Phase 2 item 6).
///
/// Everything asserted here is a DERIVATION, not a view: which terminal sessions are worth
/// offering, what we have to warn about before stealing one, the command that hands a session
/// back to the terminal, and which of the two independent facts a status word describes. The
/// SwiftUI that renders these is not tested — see the note at the bottom of this file.
final class TakeoverUXTests: XCTestCase {

    // MARK: fixtures

    private func discovered(_ id: String,
                            provider: String = "claude-code",
                            kind: String = DiscoveredKind.session,
                            title: String? = nil,
                            cwd: String? = "/Users/x/proj",
                            updated: Int? = 1000,
                            live: Bool? = nil) -> Discovered {
        var d = Discovered(provider: provider, kind: kind)
        d.sessionID = id
        d.title = title
        d.cwd = cwd
        d.updatedAt = updated
        d.live = live
        return d
    }

    private func session(_ id: String, provider: String = "claude-code", status: String = SessionStatusValue.idle) -> Session {
        try! JSONDecoder().decode(Session.self, from: Data("""
        {"id":"\(id)","provider":"\(provider)","status":"\(status)","cwd":"/Users/x/proj"}
        """.utf8))
    }

    // MARK: - the "Continue from terminal" strip (Phase 3 item 5)

    /// Only real sessions get offered. A discovered opencode *server* is infrastructure, not
    /// something a human can continue, and putting it in the strip would make the headline
    /// takeover affordance fire on a row that does nothing.
    func testCandidatesIgnoreNonSessionArtifacts() {
        let items = TerminalTakeover.candidates(
            discovered: [discovered("srv", kind: DiscoveredKind.server), discovered("s1")],
            managed: [])
        XCTAssertEqual(items.map(\.sessionID), ["s1"])
    }

    /// A session we already took over is in the sidebar already. Offering it again invites the
    /// user to attach twice to the same terminal — the exact concurrent-writer bug the warning
    /// in this file exists to prevent.
    func testCandidatesDropAlreadyManagedSessions() {
        let items = TerminalTakeover.candidates(
            discovered: [discovered("s1"), discovered("s2")],
            managed: [session("s1")])
        XCTAssertEqual(items.map(\.sessionID), ["s2"])
    }

    /// Live sessions first (that is the thing you actually want to hop into), then most recent.
    func testCandidatesRankLiveThenRecent() {
        let items = TerminalTakeover.candidates(
            discovered: [discovered("old", updated: 10),
                         discovered("new", updated: 900),
                         discovered("live", updated: 1, live: true)],
            managed: [])
        XCTAssertEqual(items.map(\.sessionID), ["live", "new", "old"])
    }

    /// The strip sits above the session list; it must not become the session list.
    func testCandidatesRespectLimit() {
        let ds = (0..<10).map { discovered("s\($0)", updated: 100 - $0) }
        XCTAssertEqual(TerminalTakeover.candidates(discovered: ds, managed: [], limit: 3).count, 3)
    }

    /// The strip is conditional: nothing discovered (or everything already managed) means no
    /// strip at all, rather than an empty header taking up the top of the sidebar forever.
    func testStripHiddenWhenNothingToOffer() {
        XCTAssertTrue(TerminalTakeover.candidates(discovered: [], managed: []).isEmpty)
        XCTAssertTrue(TerminalTakeover.candidates(discovered: [discovered("s1")],
                                                  managed: [session("s1")]).isEmpty)
    }

    /// A row with no title still has to say what it is — falling back to the folder, then the id.
    func testCandidateTitleFallsBackToFolderThenID() {
        XCTAssertEqual(TerminalTakeover.candidates(discovered: [discovered("s1", title: "Fix auth")], managed: []).first?.title,
                       "Fix auth")
        XCTAssertEqual(TerminalTakeover.candidates(discovered: [discovered("s1", title: nil, cwd: "/Users/x/oculus")], managed: []).first?.title,
                       "oculus")
        XCTAssertEqual(TerminalTakeover.candidates(discovered: [discovered("s1", title: nil, cwd: nil)], managed: []).first?.title,
                       "s1")
    }

    // MARK: - warn before stealing a Live session (Phase 3 item 2)

    /// An idle transcript has no turn to lose and nobody else typing into it — confirming there
    /// would be a dialog that teaches the user to dismiss dialogs.
    func testNoWarningForAnIdleDiscoveredSession() {
        XCTAssertNil(TerminalTakeover.warning(provider: "opencode", live: false))
        XCTAssertNil(TerminalTakeover.warning(provider: "claude-code", live: false))
    }

    /// Taking over a LIVE opencode session gives you shared control — which is to say a second
    /// writer on a session that may be mid-turn. The dialog has to name both risks, because
    /// "Take over?" alone tells the user nothing they can weigh.
    func testLiveOpencodeWarningNamesInFlightTurnAndSecondWriter() throws {
        let w = try XCTUnwrap(TerminalTakeover.warning(provider: "opencode", live: true))
        XCTAssertTrue(w.message.localizedCaseInsensitiveContains("in flight"), w.message)
        XCTAssertTrue(w.message.localizedCaseInsensitiveContains("terminal"), w.message)
        XCTAssertTrue(w.message.localizedCaseInsensitiveContains("both"), w.message)
        XCTAssertFalse(w.confirm.isEmpty)
    }

    /// claude-code cannot be co-driven (the SDK has no multi-client attach), so its takeover is a
    /// FORK. That is a different loss than opencode's and must not share opencode's wording.
    func testLiveClaudeWarningSaysForkNotSharedControl() throws {
        let w = try XCTUnwrap(TerminalTakeover.warning(provider: "claude-code", live: true))
        XCTAssertTrue(w.message.localizedCaseInsensitiveContains("fork"), w.message)
        XCTAssertTrue(w.message.localizedCaseInsensitiveContains("in flight"), w.message)
        XCTAssertNotEqual(w.message, TerminalTakeover.warning(provider: "opencode", live: true)?.message)
    }

    // MARK: - a way back to the terminal (Phase 3 item 2)

    /// The whole point: a copyable command that lands the phone conversation back in a terminal.
    func testResumeCommandForAClaudeUUID() {
        XCTAssertEqual(TerminalTakeover.resumeCommand(provider: "claude-code",
                                                      sessionID: "6f3c1b2a-1111-4222-8333-444455556666"),
                       "claude --resume 6f3c1b2a-1111-4222-8333-444455556666")
    }

    /// `cc_…` is OUR id, not claude's. Pasting `claude --resume cc_abc123` into a terminal fails
    /// ("not a valid session id"), so offering the command at all would be a lie.
    func testNoResumeCommandForOurOwnSessionID() {
        XCTAssertNil(TerminalTakeover.resumeCommand(provider: "claude-code", sessionID: "cc_abc123"))
    }

    /// opencode has no `--resume <uuid>` handback; the terminal is still attached to the live
    /// session, so there is nothing to copy.
    func testNoResumeCommandForOtherProviders() {
        XCTAssertNil(TerminalTakeover.resumeCommand(provider: "opencode",
                                                    sessionID: "6f3c1b2a-1111-4222-8333-444455556666"))
    }

    // MARK: - connection state is not session status (Phase 2 item 6)

    /// The bug this whole group defends: one `model.status` string carried BOTH facts, so a dead
    /// socket rendered in the place a session state belongs and the user read "Disconnected" as
    /// "this agent stopped".
    func testDisconnectedIsAConnectionFactNotASessionState() {
        let h = deriveHeaderStatus(connected: false, connecting: false,
                                   rawStatus: "Disconnected", sessionStatus: SessionStatusValue.running,
                                   busy: false, awaitingApproval: false)
        XCTAssertEqual(h.connection, .offline)
        XCTAssertEqual(h.connectionLabel, "Disconnected")
        XCTAssertEqual(h.session, "working…", "the session was running when we last heard; the socket dying doesn't change that")
        XCTAssertTrue(h.stale, "offline means every session word is last-known, and must be shown as such")
    }

    /// Connection words must NEVER leak into the session slot, even when nothing else is known.
    func testConnectionWordsNeverBecomeTheSessionLabel() {
        for raw in ["Disconnected", "Connecting…", "Reconnecting…", "Connect failed", "Not connected"] {
            let h = deriveHeaderStatus(connected: false, connecting: false, rawStatus: raw,
                                       sessionStatus: nil, busy: false, awaitingApproval: false)
            XCTAssertEqual(h.session, "unknown", "\(raw) is a transport fact, not a session state")
        }
    }

    /// A healthy connection shows no connection chip at all — the absence IS the signal.
    func testConnectedShowsNoConnectionChip() {
        let h = deriveHeaderStatus(connected: true, connecting: false, rawStatus: "Connected",
                                   sessionStatus: SessionStatusValue.idle, busy: false, awaitingApproval: false)
        XCTAssertEqual(h.connection, .connected)
        XCTAssertNil(h.connectionLabel)
        XCTAssertEqual(h.session, "idle")
        XCTAssertFalse(h.stale)
    }

    func testConnectingIsItsOwnPhase() {
        let h = deriveHeaderStatus(connected: false, connecting: true, rawStatus: "Connecting…",
                                   sessionStatus: SessionStatusValue.idle, busy: false, awaitingApproval: false)
        XCTAssertEqual(h.connection, .connecting)
        XCTAssertEqual(h.connectionLabel, "Connecting…")
        XCTAssertEqual(h.session, "idle")
    }

    /// Live local knowledge outranks the last broadcast snapshot, in the order the user cares about.
    func testApprovalOutranksBusyOutranksSnapshot() {
        let approval = deriveHeaderStatus(connected: true, connecting: false, rawStatus: "running",
                                          sessionStatus: SessionStatusValue.running, busy: true, awaitingApproval: true)
        XCTAssertEqual(approval.session, "awaiting approval")
        let busy = deriveHeaderStatus(connected: true, connecting: false, rawStatus: "idle",
                                      sessionStatus: SessionStatusValue.idle, busy: true, awaitingApproval: false)
        XCTAssertEqual(busy.session, "working…")
    }

    /// A session-status token in `rawStatus` (that's what session.status broadcasts write there)
    /// is still usable when no Session row is in hand.
    func testRawSessionTokenIsUsedWhenNoSnapshotExists() {
        let h = deriveHeaderStatus(connected: true, connecting: false, rawStatus: SessionStatusValue.error,
                                   sessionStatus: nil, busy: false, awaitingApproval: false)
        XCTAssertEqual(h.session, "Error")
    }

    func testStoppedSessionReadsAsStoppedNotOffline() {
        let h = deriveHeaderStatus(connected: true, connecting: false, rawStatus: "Connected",
                                   sessionStatus: SessionStatusValue.stopped, busy: false, awaitingApproval: false)
        XCTAssertNil(h.connectionLabel)
        XCTAssertEqual(h.session, "stopped")
    }
}

// NOT TESTED, deliberately: the SwiftUI that consumes all of the above — the strip's rows in
// SessionSidebar, the confirmation dialog in NewSessionView, the "Continue in terminal" menu item
// and the chat header's connection banner. They are declarative view bodies with no branching left
// in them (every decision was moved into the functions above precisely so it could be asserted),
// and a test that instantiated them could only assert that SwiftUI initialisers do not throw —
// which would pass identically against an empty body.
