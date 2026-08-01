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

    /// A frame captured mid-stream leaves a row marked streaming; painting it later must not leave a
    /// caret blinking on text that finished hours ago.
    func testPaintSealsStreamingRows() {
        let m = Model()
        m.daemonPubHex = "pub"
        m.transcriptHydrated["s1"] = [
            Data(#"{"type":"output.delta","payload":{"session_id":"s1","text":"partial"}}"#.utf8)
        ]
        m.sessionID = "s1"
        _ = m.paintFromCache("s1")
        XCTAssertEqual(m.messages.last?.streaming, false,
                       "a cached partial must paint as finished text, not as a live stream")
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
