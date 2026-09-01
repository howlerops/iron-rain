import XCTest
@testable import OculusUI
@testable import OculusKit

/// Tests for the on-device transcript cache.
///
/// The value of the cache is entirely in its reconciliation: painting a stale transcript is only
/// acceptable because the daemon's replay is then compared against it EXACTLY and any disagreement
/// throws the cache away. These tests exercise that comparison, because a bug in it corrupts a
/// conversation silently — the worst failure this feature can have.
@MainActor
final class TranscriptCacheTests: XCTestCase {

    private func frame(_ type: String, session: String, role: String = "assistant", text: String) -> Data {
        // Shaped like a real daemon frame: the client decodes session_id generically and the payload
        // by type, so both must be present.
        let json = """
        {"type":"\(type)","payload":{"session_id":"\(session)","role":"\(role)","text":"\(text)"}}
        """
        return Data(json.utf8)
    }

    private func statusFrame(session: String, status: String) -> Data {
        Data("""
        {"type":"session.status","payload":{"session_id":"\(session)","status":"\(status)"}}
        """.utf8)
    }

    // MARK: - What may be cached

    /// Usage double-counts on replay because its handler ACCUMULATES, so it must never be cached.
    /// This is the difference between a cost meter and a fabricated one.
    func testUsageFramesAreNeverCached() {
        let raw = Data("""
        {"type":"session.usage","payload":{"session_id":"s1","input_tokens":10,"output_tokens":5}}
        """.utf8)
        XCTAssertFalse(Model.cacheable(raw, session: "s1"),
                       "session.usage accumulates on apply — caching it inflates cost on every open")
    }

    /// An approval is a question that was already answered. Replaying it resurrects a dead modal.
    func testApprovalRequestsAreNeverCached() {
        let raw = Data("""
        {"type":"approval.request","payload":{"session_id":"s1","approval_id":"a1","tool":"bash"}}
        """.utf8)
        XCTAssertFalse(Model.cacheable(raw, session: "s1"))
    }

    /// Running/idle describe a moment; the error marker is history. Caching the former pins a
    /// spinner on a session that finished hours ago.
    func testOnlyErrorStatusIsCached() {
        XCTAssertFalse(Model.cacheable(statusFrame(session: "s1", status: "running"), session: "s1"))
        XCTAssertFalse(Model.cacheable(statusFrame(session: "s1", status: "idle"), session: "s1"))
        XCTAssertTrue(Model.cacheable(statusFrame(session: "s1", status: "error"), session: "s1"))
    }

    /// Another session's frames (a subscribed sub-agent) must not land in this session's cache.
    func testFramesFromAnotherSessionAreRejected() {
        let raw = frame(MessageType.sessionMessage, session: "other", text: "hi")
        XCTAssertFalse(Model.cacheable(raw, session: "s1"))
        XCTAssertTrue(Model.cacheable(raw, session: "other"))
    }

    /// Global broadcasts carry no session id at all.
    func testGlobalBroadcastsAreRejected() {
        let raw = Data(#"{"type":"session.list","payload":{"sessions":[]}}"#.utf8)
        XCTAssertFalse(Model.cacheable(raw, session: "s1"))
    }

    // MARK: - Reconciliation

    /// The common case: the daemon replays exactly what we already painted. Nothing should change,
    /// and nothing should be duplicated — this is what makes reopening free.
    func testIdenticalReplayLeavesTranscriptUntouched() {
        let m = Model()
        m.daemonPubHex = "pub"
        let a = frame(MessageType.sessionMessage, session: "s1", text: "one")
        let b = frame(MessageType.sessionMessage, session: "s1", text: "two")
        m.transcriptHydrated["s1"] = [a, b]
        m.sessionID = "s1"
        XCTAssertTrue(m.paintFromCache("s1"))
        let painted = m.messages.count
        XCTAssertEqual(painted, 2, "both cached messages should paint")

        // The daemon replays the same two frames.
        m.transcriptReplayBuffer = [a, b]
        m.finishReconcile()
        XCTAssertEqual(m.messages.count, painted, "an identical replay must not duplicate anything")
    }

    /// The replay carries more than we had: only the new frames should be applied.
    func testLongerReplayAppendsOnlyTheNewFrames() {
        let m = Model()
        m.daemonPubHex = "pub"
        let a = frame(MessageType.sessionMessage, session: "s1", text: "one")
        let b = frame(MessageType.sessionMessage, session: "s1", text: "two")
        let c = frame(MessageType.sessionMessage, session: "s1", text: "three")
        m.transcriptHydrated["s1"] = [a, b]
        m.sessionID = "s1"
        _ = m.paintFromCache("s1")
        XCTAssertEqual(m.messages.count, 2)

        m.transcriptReplayBuffer = [a, b, c]
        m.finishReconcile()
        XCTAssertEqual(m.messages.count, 3, "exactly one new message should have been added")
        XCTAssertEqual(m.messages.last?.text, "three")
    }

    /// The cache disagrees with the daemon — a session rewritten by a takeover, say. Splicing across
    /// that would leave a transcript with a hole, so the cache must be discarded wholesale.
    func testDisagreeingReplayRebuildsFromScratch() {
        let m = Model()
        m.daemonPubHex = "pub"
        m.transcriptHydrated["s1"] = [
            frame(MessageType.sessionMessage, session: "s1", text: "stale one"),
            frame(MessageType.sessionMessage, session: "s1", text: "stale two"),
        ]
        m.sessionID = "s1"
        _ = m.paintFromCache("s1")
        XCTAssertEqual(m.messages.count, 2)

        m.transcriptReplayBuffer = [
            frame(MessageType.sessionMessage, session: "s1", text: "real one"),
        ]
        m.finishReconcile()
        XCTAssertEqual(m.messages.count, 1, "the stale transcript must be replaced, not merged")
        XCTAssertEqual(m.messages.first?.text, "real one")
        XCTAssertEqual(m.transcriptPainted.count, 1, "the painted set must track the rebuild")
    }

    /// A replay carrying no transcript frames at all (a session whose history is gone) must not wipe
    /// what we painted — that would turn a working offline transcript into a blank pane.
    func testEmptyReplayKeepsThePaintedTranscript() {
        let m = Model()
        m.daemonPubHex = "pub"
        m.transcriptHydrated["s1"] = [frame(MessageType.sessionMessage, session: "s1", text: "kept")]
        m.sessionID = "s1"
        _ = m.paintFromCache("s1")
        m.transcriptReplayBuffer = [statusFrame(session: "s1", status: "idle")]
        m.finishReconcile()
        XCTAssertEqual(m.messages.count, 1)
        XCTAssertEqual(m.messages.first?.text, "kept")
    }

    // MARK: - Paint

    /// Painting must NOT leave the loader or the settle machinery engaged — that was the whole point.
    /// A cache hit that still shows a skeleton has bought nothing.
    func testCachePaintSkipsTheLoader() {
        let m = Model()
        m.daemonPubHex = "pub"
        m.transcriptHydrated["s1"] = [frame(MessageType.sessionMessage, session: "s1", text: "hello")]
        m.sessionID = "s1"
        XCTAssertTrue(m.paintFromCache("s1"))
        XCTAssertFalse(m.transcriptSettling, "a painted transcript must not be hidden behind the skeleton")
        XCTAssertFalse(m.dedupReplay, "the text-equality de-duplicator must stay disarmed on this path")
        XCTAssertGreaterThan(m.messages.count, 0)
    }

    /// Nothing cached → the caller must fall back to the existing skeleton-and-wait path.
    func testPaintReportsMissWhenNothingCached() {
        let m = Model()
        m.sessionID = "s1"
        XCTAssertFalse(m.paintFromCache("s1"))
        XCTAssertEqual(m.messages.count, 0)
    }

    /// A reply cached as deltas must paint with its TEXT, not just as a sealed empty bubble.
    ///
    /// The first version of this test asserted only `streaming == false` — the property the code
    /// happened to implement — and passed while every cached reply rendered blank, because sealing the
    /// row dropped the buffered text on the floor. claude-code, pi and the CLI providers never
    /// broadcast a finalized message, so for them EVERY cached reply is deltas: this is the whole
    /// conversation, not an edge case.
    func testPaintKeepsStreamedText() {
        let m = Model()
        m.daemonPubHex = "pub"
        m.transcriptHydrated["s1"] = [
            Data(#"{"type":"output.delta","payload":{"session_id":"s1","text":"hello "}}"#.utf8),
            Data(#"{"type":"output.delta","payload":{"session_id":"s1","text":"world"}}"#.utf8),
        ]
        m.sessionID = "s1"
        _ = m.paintFromCache("s1")
        XCTAssertEqual(m.messages.last?.text, "hello world",
                       "cached deltas must fold into the row — a sealed EMPTY bubble is the bug")
        XCTAssertEqual(m.messages.last?.streaming, false,
                       "and it must not blink a caret on text that finished hours ago")
    }

    /// An ephemeral "just chat" is never persisted by the daemon. Writing it to the device cache
    /// would make "not saved" untrue on the one machine the user actually holds.
    func testEphemeralSessionsAreNeverCached() async {
        let m = Model()
        m.daemonPubHex = "unit-eph"
        m.sessionID = "eph1"
        m.ephemeralSessionIDs.insert("eph1")
        let raw = frame(MessageType.sessionMessage, session: "eph1", text: "secret")
        m.captureFrames([raw], session: "eph1")
        await m.flushCaptured()
        let stored = await TranscriptCache.shared.frames(daemon: "unit-eph", session: "eph1")
        XCTAssertTrue(stored.isEmpty, "an ephemeral chat must leave nothing on disk")
    }

    /// Repeated byte-identical delta frames are normal (a newline, a single token). An anchor that
    /// latches onto the FIRST match of one frame concludes the replay is new content and re-applies
    /// the whole conversation — which then gets written back and compounds on every open.
    func testReconcileAnchorsOnTheLongestOverlap() {
        let m = Model()
        m.daemonPubHex = "pub"
        let nl = Data(#"{"type":"output.delta","payload":{"session_id":"s1","text":"\n"}}"#.utf8)
        let a = frame(MessageType.sessionMessage, session: "s1", text: "one")
        let b = frame(MessageType.sessionMessage, session: "s1", text: "two")
        m.transcriptHydrated["s1"] = [nl, a, nl, b]
        m.sessionID = "s1"
        _ = m.paintFromCache("s1")
        let painted = m.messages.count

        // The daemon replays the same tail. Nothing new should be applied.
        m.transcriptReplayBuffer = [nl, a, nl, b]
        m.finishReconcile()
        XCTAssertEqual(m.messages.count, painted,
                       "a replay identical to the painted tail must add nothing, despite repeated deltas")
    }

    /// The barrier must not fire before the replay has even begun: on a slow link the subscribe
    /// response is still in flight when the cap expires, and clearing the flag would let the whole
    /// replay render underneath the painted copy with de-duplication disarmed.
    func testReconcileStaysArmedUntilFramesArrive() {
        let m = Model()
        m.daemonPubHex = "pub"
        m.transcriptHydrated["s1"] = [frame(MessageType.sessionMessage, session: "s1", text: "hi")]
        m.sessionID = "s1"
        _ = m.paintFromCache("s1")
        XCTAssertTrue(m.transcriptReconciling)
        m.finishReconcile() // cap fires with an empty buffer
        XCTAssertTrue(m.transcriptReconciling,
                      "an empty buffer means the replay has not started — stay armed rather than fail open")
    }

    // MARK: - Store

    func testStoreRoundTripsAndForgets() async {
        let cache = TranscriptCache.shared
        let a = frame(MessageType.sessionMessage, session: "sx", text: "one")
        let b = frame(MessageType.sessionMessage, session: "sx", text: "two")
        await cache.forget(daemon: "unit-test", session: "sx")
        await cache.append(daemon: "unit-test", session: "sx", frames: [a, b])
        let got = await cache.frames(daemon: "unit-test", session: "sx")
        XCTAssertEqual(got, [a, b], "frames must come back byte-identical and in order")

        await cache.forget(daemon: "unit-test", session: "sx")
        let after = await cache.frames(daemon: "unit-test", session: "sx")
        XCTAssertTrue(after.isEmpty)
    }

    /// Deleting a session must take its cached transcript with it. The cache holds the machine's
    /// source code and the conversation about it; leaving that on the phone after the user deleted
    /// the session is exactly the wrong default.
    func testDeletingASessionPurgesItsCache() async {
        let cache = TranscriptCache.shared
        let f = frame(MessageType.sessionMessage, session: "doomed", text: "secret")
        await cache.append(daemon: "unit-del", session: "doomed", frames: [f])

        let m = Model()
        m.daemonPubHex = "unit-del"
        m.transcriptHydrated["doomed"] = [f]
        m.forgetCached("doomed")
        try? await Task.sleep(nanoseconds: 300_000_000) // the disk purge is fire-and-forget

        XCTAssertNil(m.transcriptHydrated["doomed"], "the in-memory copy must go immediately")
        let left = await cache.frames(daemon: "unit-del", session: "doomed")
        XCTAssertTrue(left.isEmpty, "the on-disk copy must go too")
    }

    /// Unpairing a Mac must take its transcripts with it, and leave other Macs alone.
    func testForgetDaemonIsScoped() async {
        let cache = TranscriptCache.shared
        let f = frame(MessageType.sessionMessage, session: "s", text: "x")
        await cache.append(daemon: "unit-A", session: "s", frames: [f])
        await cache.append(daemon: "unit-B", session: "s", frames: [f])
        await cache.forgetDaemon("unit-A")
        let a = await cache.frames(daemon: "unit-A", session: "s")
        let b = await cache.frames(daemon: "unit-B", session: "s")
        XCTAssertTrue(a.isEmpty, "the unpaired Mac's transcripts must be gone")
        XCTAssertEqual(b.count, 1, "another Mac's transcripts must survive")
        await cache.forgetDaemon("unit-B")
    }
}

/// Leaving a session must clear the frame-level record, or the NEXT session is reconciled against it.
///
/// `resetTranscriptCacheState()` had exactly ONE call site in the whole app — openSession's. attach()
/// and newSession() cleared `messages` and nothing in the transcript-cache group, so
/// `transcriptPainted` kept the previous session's frames and the next reconcile compared a fresh
/// session's replay against a transcript that was never its own. Stashing first is what keeps the
/// outgoing session's warm cache intact, so this asserts both halves.
@MainActor
final class LeavingASessionClearsTheCacheTests: XCTestCase {

    private func frame(_ session: String, _ text: String) -> Data {
        Data(#"{"type":"session.message","payload":{"session_id":"\#(session)","role":"assistant","text":"\#(text)"}}"#.utf8)
    }

    func testNewSessionClearsPaintedFramesAndKeepsTheOldCache() {
        let m = Model()
        m.sessionID = "a"
        m.transcriptPainted = [frame("a", "session A said this")]

        m.newSession()

        XCTAssertTrue(m.transcriptPainted.isEmpty,
                      "the previous session's frames survived into a new session; the next reconcile "
                      + "compares a fresh replay against them")
        XCTAssertEqual(m.transcriptHydrated["a"]?.count, 1,
                       "the outgoing session's warm cache was thrown away instead of stashed")
    }
}
