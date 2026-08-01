import Foundation
import OculusKit

/// The Model's half of the on-device transcript cache: what gets captured, what paints on open, and
/// how the painted transcript is reconciled against the daemon's replay.
///
/// The shape of it. Opening a session used to mean: clear everything, show a skeleton, wait for the
/// relay to deliver the tail. Now, if we already hold this session's frames in memory, we paint them
/// in the SAME tick that we set `sessionID` — so SwiftUI commits once and `ChatView` builds a scroll
/// view that is already full and already bottom-anchored. Then the daemon's replay arrives and is
/// reconciled against what we painted.
///
/// Reconciliation is exact, not heuristic. Every cached frame is the literal bytes off the wire, and
/// the daemon hands the identical slice to its ring and to every subscriber — so "did we already show
/// this?" is a `Data` comparison. That is deliberately unlike the old `dedupReplay`, which compared
/// trimmed message TEXT and could therefore drop a genuinely repeated line.
extension Model {

    // MARK: - What is safe to cache

    /// Frames that can be replayed into a fresh transcript and produce the same result.
    ///
    /// The exclusions are load-bearing, not tidiness:
    /// - `session.usage` ACCUMULATES in its handler, so replaying it double-counts cost on every open.
    /// - `approval.request` would resurrect a modal for a decision already made.
    /// - `session.status` running/idle would pin a false spinner; only the error marker is history.
    /// - `session.todos` is re-sent in full by the daemon constantly, and `openSession` clears it.
    /// - `turn.state` is transient by construction and synthesized fresh into every replay.
    /// - Global broadcasts (session.list, activity, participants) aren't session history at all.
    static let cacheableTypes: Set<String> = [
        MessageType.sessionMessage, MessageType.outputDelta, MessageType.thinking,
        MessageType.sessionTool, MessageType.uiComponent, MessageType.sessionSubAgent,
    ]

    /// True when this frame belongs in the cache for `session`.
    static func cacheable(_ raw: Data, env: Envelope, session: String) -> Bool {
        guard let fs = try? env.payload(as: FrameSessionID.self), fs.sessionID == session else { return false }
        if env.type == MessageType.sessionStatus {
            // Only the error marker is durable history; running/idle describe a moment.
            return (try? env.payload(as: SessionStatus.self))?.status == SessionStatusValue.error
        }
        return cacheableTypes.contains(env.type)
    }

    /// Cheap re-check of a raw frame without a decoded envelope to hand.
    static func cacheable(_ raw: Data, session: String) -> Bool {
        guard let env = try? Protocol.envelope(raw) else { return false }
        return cacheable(raw, env: env, session: session)
    }

    // MARK: - Hydration (ahead of the tap)

    /// Loads a session's cached frames into memory so a later open can paint without awaiting.
    ///
    /// This MUST happen before the tap. Any `await` between `sessionID = id` and populating
    /// `messages` yields a render pass with the new `.id` and an empty transcript — which is exactly
    /// the "opens at the top of the conversation" bug the settle machinery exists to hide.
    func hydrate(_ sessionIDs: [String]) async {
        let pub = daemonPubHex
        guard !pub.isEmpty else { return }
        for sid in sessionIDs where transcriptHydrated[sid] == nil {
            let frames = await TranscriptCache.shared.frames(daemon: pub, session: sid)
            if !frames.isEmpty { transcriptHydrated[sid] = frames }
        }
    }

    /// Warms the cache for the sessions most likely to be opened next: the one that was open last,
    /// then the most recently active. Bounded — hydrating everything would read the whole database on
    /// every connect for sessions the user won't touch.
    func hydrateLikelySessions() async {
        var ids: [String] = []
        if let last = UserDefaults.standard.string(forKey: "oculus.lastSession.\(daemonPubHex)") { ids.append(last) }
        ids += sessions
            .sorted { ($0.updatedAt ?? 0) > ($1.updatedAt ?? 0) }
            .prefix(8).map(\.id)
        await hydrate(Array(NSOrderedSet(array: ids)).compactMap { $0 as? String })
        await TranscriptCache.shared.sweep()
    }

    // MARK: - Paint

    /// Paints a session from its hydrated frames. Returns false if there was nothing to paint, in
    /// which case the caller falls back to the skeleton-and-wait path unchanged.
    ///
    /// Everything here is synchronous on purpose — see `hydrate`.
    func paintFromCache(_ id: String) -> Bool {
        guard let frames = transcriptHydrated[id], !frames.isEmpty else { return false }
        transcriptPainted = frames
        for raw in frames {
            if let env = try? Protocol.envelope(raw) { applyEvent(env, raw: raw) }
        }
        // A frame cached mid-stream leaves a row marked `streaming`, which would animate a caret for
        // text that finished long ago. The replay will restate it if it's genuinely still running.
        finalizeStreamingForCache()
        transcriptReconciling = true
        transcriptReplayBuffer = []
        transcriptAnchorGuardUntil = Date().addingTimeInterval(20)
        armReconcileCap()
        return true
    }

    // MARK: - Reconcile

    /// Buffers a replay frame instead of applying it. Returns true when the frame was taken.
    func bufferForReconcile(_ raw: Data, env: Envelope) -> Bool {
        guard transcriptReconciling, let sid = sessionID else { return false }
        // Trailers the daemon synthesizes onto a replay (turn.state, the page bracket) are state, not
        // history — apply them immediately so the turn indicator and the Load-earlier affordance stay
        // live while the transcript reconciles.
        if Model.nonRingFrameTypes.contains(env.type) { return false }
        guard let fs = try? env.payload(as: FrameSessionID.self), fs.sessionID == sid else { return false }
        transcriptReplayBuffer.append(raw)
        bumpReconcile()
        return true
    }

    /// A quiet-period barrier: the replay is done when frames stop arriving for a beat. Capped, so an
    /// actively streaming session reconciles rather than buffering forever.
    private func bumpReconcile() {
        transcriptReconcileTask?.cancel()
        transcriptReconcileTask = Task { @MainActor [weak self] in
            try? await Task.sleep(nanoseconds: 160_000_000)
            guard !Task.isCancelled else { return }
            self?.finishReconcile()
        }
    }

    private func armReconcileCap() {
        transcriptReconcileCap?.cancel()
        transcriptReconcileCap = Task { @MainActor [weak self] in
            try? await Task.sleep(nanoseconds: 1_500_000_000)
            guard !Task.isCancelled else { return }
            self?.finishReconcile()
        }
    }

    /// Decides whether what we painted agrees with what the daemon sent.
    ///
    /// Because both sides are the same bytes from the same daemon, the comparison is exact. Three
    /// outcomes: the replay is entirely contained in what we painted (keep it — the common case, and
    /// zero re-render); the replay extends past it (append only the new frames); or it disagrees
    /// (throw the cache away and rebuild — never splice across a hole).
    func finishReconcile() {
        guard transcriptReconciling, let sid = sessionID else { return }
        transcriptReconciling = false
        transcriptReconcileTask?.cancel(); transcriptReconcileCap?.cancel()
        let buffer = transcriptReplayBuffer
        transcriptReplayBuffer = []
        guard !buffer.isEmpty else { return }

        let arrived = buffer.filter { Model.cacheable($0, session: sid) }
        var spliceFrom: Int? = arrived.isEmpty ? 0 : nil
        if let first = arrived.first, let i = transcriptPainted.lastIndex(of: first) {
            let overlap = Array(transcriptPainted[i...])
            if overlap.count <= arrived.count, Array(arrived[0..<overlap.count]) == overlap {
                spliceFrom = overlap.count
            }
        }

        if let from = spliceFrom {
            var skipped = 0
            for raw in buffer {
                if Model.cacheable(raw, session: sid), skipped < from { skipped += 1; continue }
                if let env = try? Protocol.envelope(raw) { applyEvent(env, raw: raw) }
            }
            let fresh = Array(arrived.dropFirst(from))
            transcriptPainted.append(contentsOf: fresh)
            captureFrames(fresh, session: sid)
        } else {
            // The cache disagreed with the daemon. Rebuild from the replay alone and throw the cached
            // frames away — keeping any of them would poison the next open too.
            messages.removeAll()
            clearChildState()
            resetDaemonEventCount()
            transcriptPainted = arrived
            for raw in buffer {
                if let env = try? Protocol.envelope(raw) { applyEvent(env, raw: raw) }
            }
            transcriptHydrated[sid] = arrived
            let pub = daemonPubHex
            Task { await TranscriptCache.shared.replace(daemon: pub, session: sid, frames: arrived) }
        }
    }

    // MARK: - Capture

    /// Records a live frame for the open session. Buffered in memory and flushed on a debounce — the
    /// receive loop runs on the main actor, and a synchronous write per frame would put file I/O on
    /// the same actor that drives the UI, at streaming rates.
    func captureFrame(_ raw: Data, env: Envelope) {
        guard let sid = sessionID, !daemonPubHex.isEmpty,
              Model.cacheable(raw, env: env, session: sid) else { return }
        // Inside the guard window, a frame byte-identical to one already on screen is the provider
        // re-streaming its history after an attach. Byte identity makes this strictly more precise
        // than the text-equality de-duplication it replaces.
        if Date() < transcriptAnchorGuardUntil, transcriptPainted.contains(raw) { return }
        transcriptPainted.append(raw)
        captureFrames([raw], session: sid)
    }

    func captureFrames(_ frames: [Data], session: String) {
        guard !frames.isEmpty, !daemonPubHex.isEmpty else { return }
        transcriptWriteBuffer[session, default: []].append(contentsOf: frames)
        transcriptWriteTask?.cancel()
        transcriptWriteTask = Task { @MainActor [weak self] in
            try? await Task.sleep(nanoseconds: 900_000_000)
            guard !Task.isCancelled else { return }
            await self?.flushCaptured()
        }
    }

    func flushCaptured() async {
        let pending = transcriptWriteBuffer
        transcriptWriteBuffer = [:]
        let pub = daemonPubHex
        guard !pub.isEmpty else { return }
        for (sid, frames) in pending {
            await TranscriptCache.shared.append(daemon: pub, session: sid, frames: frames)
        }
    }

    /// Forgets a session everywhere — used when the user deletes it, so its source code doesn't
    /// linger on the device after the conversation is gone.
    func forgetCached(_ sid: String) {
        transcriptHydrated[sid] = nil
        transcriptWriteBuffer[sid] = nil
        let pub = daemonPubHex
        Task { await TranscriptCache.shared.forget(daemon: pub, session: sid) }
    }
}

/// A frame's session id, decoded alone so any event type can be attributed without knowing its full
/// payload shape.
struct FrameSessionID: Decodable {
    let sessionID: String?
    enum CodingKeys: String, CodingKey { case sessionID = "session_id" }
}
