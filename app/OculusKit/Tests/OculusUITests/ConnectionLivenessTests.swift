import XCTest
@testable import OculusUI
@testable import OculusKit

/// Tests for the swap moment: what the app does when it comes back from the user's pocket.
///
/// Every assertion here defends against the app LYING about the connection. A phone that was
/// suspended for an hour wakes holding `connected == true`, a frozen backoff sleep, and a socket
/// that may have died forty minutes ago — none of which the OS reports. The failure mode is a
/// "Connected" pill above a transcript that will never move again, which is worse than an honest
/// error because the user waits instead of acting.
@MainActor
final class ConnectionLivenessTests: XCTestCase {

    private func frame(_ type: String, session: String, role: String = "assistant", text: String) -> Data {
        Data("""
        {"type":"\(type)","payload":{"session_id":"\(session)","role":"\(role)","text":"\(text)"}}
        """.utf8)
    }

    private func session(_ id: String) -> Session {
        try! JSONDecoder().decode(Session.self, from: Data("""
        {"id":"\(id)","provider":"opencode","status":"running","cwd":"/tmp"}
        """.utf8))
    }

    // MARK: - Backoff

    /// The retry schedule: attempt IMMEDIATELY, then 2s, 4s, 8s, capped at 15s.
    ///
    /// The cap matters as much as the growth: an unbounded doubling means a phone that was offline
    /// on the train comes back to a retry loop sleeping for minutes, and the user sees
    /// "Reconnecting…" long after the network returned.
    func testReconnectBackoffIsImmediateThenExponentialCappedAt15() {
        var d: UInt64 = 0
        var seq: [UInt64] = [d]
        for _ in 0..<6 { d = Model.nextReconnectDelay(d); seq.append(d) }
        XCTAssertEqual(seq, [0, 2, 4, 8, 15, 15, 15],
                       "first attempt must be immediate, then double, then hold at 15s")
    }

    /// The backoff sleep does not run while the process is suspended: a loop that had just started a
    /// 15s wait when the phone went into a pocket still has ~15s left to wait when it comes out. If
    /// foregrounding cannot cancel it, the user stares at "Reconnecting…" for the remainder of a
    /// delay that was scheduled against wall-clock time the app never experienced.
    func testForegroundCancelsAFrozenBackoffSleep() {
        let m = Model()
        m.wsURL = "ws://127.0.0.1:9/ws"
        m.daemonPubHex = String(repeating: "0", count: 64)
        m.secret = "unit"
        m.reconnectWanted = true
        m.scheduleReconnect()
        XCTAssertTrue(m.reconnecting, "a dropped connection must arm the retry loop")

        m.cancelReconnectBackoff()
        XCTAssertFalse(m.reconnecting,
                       "foregrounding must be able to abandon a backoff frozen since the pocket")
        m.disconnect()
    }

    // MARK: - Foreground probe

    /// `connected` is a claim about a socket nobody has touched since before the suspension. The
    /// probe is the only thing that can turn that claim back into a fact, and a probe the daemon
    /// never answers must take the connection down rather than leave the pill saying "Connected".
    func testForegroundProbeFailureTearsDownAStaleConnection() async {
        let m = Model()
        m.connected = true
        m.status = "Connected"

        let healthy = await m.revalidateConnection()
        XCTAssertFalse(healthy, "with no live client the probe cannot succeed")
        XCTAssertFalse(m.connected, "an unanswered probe must clear the connection, not keep it")
        XCTAssertNotEqual(m.status, "Connected",
                          "a stale 'Connected' is the exact lie this whole path exists to prevent")
    }

    /// A healthy connection must survive foregrounding untouched — a probe that tears down a working
    /// socket would turn every app switch into a reconnect and a transcript reload.
    func testRevalidateIsANoOpWhenNotConnected() async {
        let m = Model()
        m.connected = false
        m.status = "Not connected"
        let healthy = await m.revalidateConnection()
        XCTAssertFalse(healthy)
        XCTAssertEqual(m.status, "Not connected", "revalidation must not rewrite a status it didn't change")
    }

    // MARK: - Honest offline messaging

    /// All routes failed seconds after the Mac was answering: it did not move, it did not lose its
    /// address — overwhelmingly it went to sleep. Saying "can't reach this Mac" sends the user
    /// hunting for a network problem that doesn't exist.
    func testOfflineDetailBlamesSleepWhenTheMacWasJustReachable() {
        let now = Date()
        let recent = Model.unreachableDetail(lastConnected: now.addingTimeInterval(-120), now: now)
        XCTAssertTrue(recent.localizedCaseInsensitiveContains("asleep"),
                      "a Mac reachable two minutes ago is asleep, not unreachable: got \(recent)")
    }

    /// Nothing was ever reached, or not for a long time — we genuinely do not know why, so don't
    /// invent a diagnosis.
    func testOfflineDetailStaysGenericWithoutARecentSighting() {
        let now = Date()
        XCTAssertFalse(Model.unreachableDetail(lastConnected: nil, now: now)
            .localizedCaseInsensitiveContains("asleep"))
        XCTAssertFalse(Model.unreachableDetail(lastConnected: now.addingTimeInterval(-86_400), now: now)
            .localizedCaseInsensitiveContains("asleep"),
                       "a Mac last seen yesterday tells us nothing about sleep")
    }

    // MARK: - Reconnect repaints instead of blanking

    /// The reconnect path used to `messages.removeAll()` and drop the painted set, so every swap —
    /// including the two-second blip the keepalive now catches — blanked the conversation until a
    /// relay round trip refilled it. Repaint in place: keep what is on screen, seed the reconciler
    /// with the frames that produced it, and let the attach replay be compared against them.
    func testReopenPreservesTheOnScreenTranscript() async {
        let m = Model()
        m.daemonPubHex = "pub"
        let a = frame(MessageType.sessionMessage, session: "s1", text: "one")
        let b = frame(MessageType.sessionMessage, session: "s1", text: "two")
        m.transcriptHydrated["s1"] = [a, b]
        m.sessionID = "s1"
        XCTAssertTrue(m.paintFromCache("s1"))
        m.finishReconcileForTestSetup()   // the open's own reconcile completed before the drop
        m.currentSession = session("s1")
        XCTAssertEqual(m.messages.count, 2)

        await m.reopenCurrentSession()

        XCTAssertEqual(m.messages.count, 2,
                       "the transcript must stay on screen across a reconnect, not blank")
        XCTAssertEqual(m.messages.map(\.text), ["one", "two"])
        XCTAssertEqual(m.transcriptPainted, [a, b],
                       "the painted set must still describe what is on screen, or the replay is appended blind")
        XCTAssertTrue(m.transcriptReconciling,
                      "the attach replay must be buffered for reconciliation, not applied on top")

        // The daemon replays exactly what is already up: the existing reconciler must add nothing.
        m.transcriptReplayBuffer = [a, b]
        m.finishReconcile()
        XCTAssertEqual(m.messages.count, 2, "an identical attach replay must not duplicate the transcript")
    }

    /// A reconnect whose replay carries new turns (the agent kept working while the phone was away)
    /// must splice only the new rows in — that is the whole promise of swapping devices mid-work.
    func testReopenSplicesWorkDoneWhileAway() async {
        let m = Model()
        m.daemonPubHex = "pub"
        let a = frame(MessageType.sessionMessage, session: "s1", text: "one")
        let b = frame(MessageType.sessionMessage, session: "s1", text: "two")
        m.transcriptHydrated["s1"] = [a]
        m.sessionID = "s1"
        _ = m.paintFromCache("s1")
        m.finishReconcileForTestSetup()
        m.currentSession = session("s1")

        await m.reopenCurrentSession()
        m.transcriptReplayBuffer = [a, b]
        m.finishReconcile()

        XCTAssertEqual(m.messages.map(\.text), ["one", "two"],
                       "the turn that landed while the phone was in a pocket must splice in exactly once")
    }
}

private extension Model {
    /// Closes a reconcile the way a real replay would, so a test can start from "session open and
    /// settled" without faking the flag directly.
    func finishReconcileForTestSetup() {
        transcriptReplayBuffer = transcriptPainted
        finishReconcile()
    }
}
