import Foundation
import SQLite3

/// A disposable on-device copy of the raw daemon frames that make up a conversation, so reopening a
/// session paints instantly instead of showing a skeleton while the relay round-trips.
///
/// The one idea that makes this safe: **cache the exact bytes off the wire**. `broadcast`
/// (daemon/hub/session.go) hands the same `raw` slice to its ring and to every subscriber, and the
/// durable store keeps that slice verbatim — so a frame we cached and the same frame replayed later
/// are byte-identical. Reconciliation is therefore an exact `Data` comparison, never a fuzzy match on
/// rendered text. Nothing here ever rewrites or truncates a frame; byte identity with the wire IS the
/// correctness argument.
///
/// The cache is authoritative for nothing. The daemon's replay always wins; this only decides what
/// the user looks at during the round trip. That is why it lives in Caches/ — if the OS reclaims it
/// we degrade to exactly today's behaviour.
///
/// Deliberately NOT a sync protocol. An earlier design had the client send a `since` cursor so the
/// daemon could serve only new events. It can't: the durable sequence numbers a strict subset of what
/// the daemon broadcasts (see `persistDurable` — UI components, sub-agent rows, user echoes and
/// to-dos are never stamped), so `seq > since` would silently drop whole classes of message. See
/// docs/transcript-cache-plan.md for the preconditions that would have to hold first.
actor TranscriptCache {
    static let shared = TranscriptCache()

    /// Bump by hand when the shape of a cached frame changes. Deliberately not tied to the app
    /// version: CFBundleVersion is frozen at 1 in both build configs so it would never fire, and
    /// MARKETING_VERSION would wipe every cache on every update — precisely when the user reopens
    /// the app and most wants an instant transcript.
    private static let schemaVersion: Int32 = 1

    /// Total bytes of cached frames. Past this, the least recently opened sessions are dropped whole
    /// — a partially evicted session would paint a transcript with a hole in the middle.
    private static let byteBudget = 24 * 1024 * 1024
    /// Frames kept per session. Comfortably above the daemon's own `replayTailLimit` of 200, so a
    /// reconcile against a full replay still finds its anchor.
    private static let framesPerSession = 600
    /// Sessions untouched for this long are swept on connect.
    private static let ttl: TimeInterval = 7 * 24 * 3600

    private var db: OpaquePointer?
    private var opened = false

    // MARK: - Lifecycle

    private func open() {
        guard !opened else { return }
        opened = true
        let dir = Self.directory()
        try? FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        Self.protect(dir)
        let path = dir.appendingPathComponent("transcript-cache.sqlite3").path
        guard sqlite3_open(path, &db) == SQLITE_OK else { db = nil; return }
        exec("PRAGMA journal_mode=WAL")
        exec("PRAGMA synchronous=NORMAL")
        // Without incremental vacuum, deleted pages are never returned and the byte budget is
        // fiction — the file only ever grows.
        exec("PRAGMA auto_vacuum=INCREMENTAL")
        if version() != Self.schemaVersion {
            exec("DROP TABLE IF EXISTS frames")
            exec("DROP TABLE IF EXISTS sessions")
        }
        exec("""
        CREATE TABLE IF NOT EXISTS frames(
            daemon TEXT NOT NULL, session TEXT NOT NULL, ord INTEGER NOT NULL,
            raw BLOB NOT NULL, ts INTEGER NOT NULL,
            PRIMARY KEY(daemon, session, ord))
        """)
        exec("""
        CREATE TABLE IF NOT EXISTS sessions(
            daemon TEXT NOT NULL, session TEXT NOT NULL,
            last_opened INTEGER NOT NULL, next_ord INTEGER NOT NULL DEFAULT 0,
            PRIMARY KEY(daemon, session))
        """)
        exec("PRAGMA user_version=\(Self.schemaVersion)")
    }

    /// Caches/ and not Application Support/, deliberately: a disposable optimistic cache SHOULD be
    /// purgeable, and this directory is already excluded from Time Machine and device backups — so
    /// conversation content and source code don't silently ride along into a backup.
    private static func directory() -> URL {
        let base = FileManager.default.urls(for: .cachesDirectory, in: .userDomainMask).first
            ?? URL(fileURLWithPath: NSTemporaryDirectory())
        return base.appendingPathComponent("Oculus", isDirectory: true)
    }

    /// Protection is set on the DIRECTORY so SQLite's -wal and -shm side files inherit it; setting it
    /// on the database file alone would leave recent frames readable in the write-ahead log.
    ///
    /// `completeUntilFirstUserAuthentication`, not `complete`: the app has no background modes and no
    /// protected-data plumbing, so `complete` would add failure surface for marginal gain.
    private static func protect(_ dir: URL) {
        #if os(iOS)
        try? FileManager.default.setAttributes(
            [.protectionKey: FileProtectionType.completeUntilFirstUserAuthentication],
            ofItemAtPath: dir.path)
        #else
        try? FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: dir.path)
        #endif
    }

    // MARK: - Reads

    /// The cached frames for a session, oldest first. Empty when there's nothing to paint.
    func frames(daemon: String, session: String) -> [Data] {
        open()
        guard db != nil else { return [] }
        var out: [Data] = []
        var st: OpaquePointer?
        let sql = "SELECT raw FROM frames WHERE daemon=? AND session=? ORDER BY ord"
        guard sqlite3_prepare_v2(db, sql, -1, &st, nil) == SQLITE_OK else { return [] }
        defer { sqlite3_finalize(st) }
        bind(st, 1, daemon); bind(st, 2, session)
        while sqlite3_step(st) == SQLITE_ROW {
            if let p = sqlite3_column_blob(st, 0) {
                out.append(Data(bytes: p, count: Int(sqlite3_column_bytes(st, 0))))
            }
        }
        return out
    }

    // MARK: - Writes

    /// Appends frames for a session and records that it was touched. Called from a debounced batch —
    /// never per frame, and never on the main actor.
    func append(daemon: String, session: String, frames: [Data]) {
        guard !frames.isEmpty else { return }
        open()
        guard db != nil else { return }
        let now = Int64(Date().timeIntervalSince1970)
        exec("BEGIN IMMEDIATE")
        var ord = nextOrd(daemon: daemon, session: session)
        var st: OpaquePointer?
        if sqlite3_prepare_v2(db, "INSERT OR REPLACE INTO frames(daemon,session,ord,raw,ts) VALUES(?,?,?,?,?)", -1, &st, nil) == SQLITE_OK {
            for f in frames {
                sqlite3_reset(st)
                bind(st, 1, daemon); bind(st, 2, session)
                sqlite3_bind_int64(st, 3, ord)
                _ = f.withUnsafeBytes { sqlite3_bind_blob(st, 4, $0.baseAddress, Int32(f.count), nil) }
                sqlite3_bind_int64(st, 5, now)
                sqlite3_step(st)
                ord += 1
            }
            sqlite3_finalize(st)
        }
        upsertSession(daemon: daemon, session: session, nextOrd: ord, touched: now)
        exec("COMMIT")
        trim(daemon: daemon, session: session)
    }

    /// Replaces a session's cache wholesale. Used when reconciliation had to fall back to a full
    /// replace — the cached frames were wrong, so keeping any of them would poison the next open.
    func replace(daemon: String, session: String, frames: [Data]) {
        open()
        guard db != nil else { return }
        exec("BEGIN IMMEDIATE")
        delete(daemon: daemon, session: session, commit: false)
        exec("COMMIT")
        append(daemon: daemon, session: session, frames: frames)
    }

    func touch(daemon: String, session: String) {
        open()
        guard db != nil else { return }
        upsertSession(daemon: daemon, session: session, nextOrd: nil,
                      touched: Int64(Date().timeIntervalSince1970))
    }

    // MARK: - Eviction

    /// Drops one session's frames. Called when the user deletes a session, or when reconciliation
    /// proves the cache was stale.
    func forget(daemon: String, session: String) {
        open()
        guard db != nil else { return }
        exec("BEGIN IMMEDIATE")
        delete(daemon: daemon, session: session, commit: false)
        exec("COMMIT")
        vacuum()
    }

    /// Drops everything for one daemon. Called on unpair — a Mac you removed should leave nothing of
    /// its source code behind on the phone.
    func forgetDaemon(_ daemon: String) {
        open()
        guard db != nil else { return }
        run("DELETE FROM frames WHERE daemon=?", daemon)
        run("DELETE FROM sessions WHERE daemon=?", daemon)
        vacuum()
    }

    /// Age-and-budget sweep, run on connect. Sessions are dropped WHOLE and least-recently-opened
    /// first: evicting the oldest frames of a session instead would leave a transcript that paints
    /// with a hole in the middle, which is worse than not painting at all.
    func sweep() {
        open()
        guard db != nil else { return }
        let cutoff = Int64(Date().addingTimeInterval(-Self.ttl).timeIntervalSince1970)
        exec("DELETE FROM frames WHERE (daemon,session) IN (SELECT daemon,session FROM sessions WHERE last_opened < \(cutoff))")
        exec("DELETE FROM sessions WHERE last_opened < \(cutoff)")
        while totalBytes() > Self.byteBudget {
            guard let victim = oldestSession() else { break }
            exec("BEGIN IMMEDIATE")
            delete(daemon: victim.0, session: victim.1, commit: false)
            exec("COMMIT")
        }
        vacuum()
    }

    /// Keeps the newest `framesPerSession` frames for one session.
    private func trim(daemon: String, session: String) {
        exec("""
        DELETE FROM frames WHERE daemon='\(esc(daemon))' AND session='\(esc(session))' AND ord <= (
            SELECT COALESCE(MAX(ord), 0) - \(Self.framesPerSession)
            FROM frames WHERE daemon='\(esc(daemon))' AND session='\(esc(session))')
        """)
    }

    // MARK: - SQLite plumbing

    private func delete(daemon: String, session: String, commit: Bool) {
        run("DELETE FROM frames WHERE daemon=? AND session=?", daemon, session)
        run("DELETE FROM sessions WHERE daemon=? AND session=?", daemon, session)
        if commit { exec("COMMIT") }
    }

    private func upsertSession(daemon: String, session: String, nextOrd: Int64?, touched: Int64) {
        var st: OpaquePointer?
        let sql = nextOrd == nil
            ? "INSERT INTO sessions(daemon,session,last_opened,next_ord) VALUES(?,?,?,0) ON CONFLICT(daemon,session) DO UPDATE SET last_opened=excluded.last_opened"
            : "INSERT INTO sessions(daemon,session,last_opened,next_ord) VALUES(?,?,?,?) ON CONFLICT(daemon,session) DO UPDATE SET last_opened=excluded.last_opened, next_ord=excluded.next_ord"
        guard sqlite3_prepare_v2(db, sql, -1, &st, nil) == SQLITE_OK else { return }
        defer { sqlite3_finalize(st) }
        bind(st, 1, daemon); bind(st, 2, session)
        sqlite3_bind_int64(st, 3, touched)
        if let n = nextOrd { sqlite3_bind_int64(st, 4, n) }
        sqlite3_step(st)
    }

    private func nextOrd(daemon: String, session: String) -> Int64 {
        var st: OpaquePointer?
        guard sqlite3_prepare_v2(db, "SELECT next_ord FROM sessions WHERE daemon=? AND session=?", -1, &st, nil) == SQLITE_OK else { return 0 }
        defer { sqlite3_finalize(st) }
        bind(st, 1, daemon); bind(st, 2, session)
        return sqlite3_step(st) == SQLITE_ROW ? sqlite3_column_int64(st, 0) : 0
    }

    private func totalBytes() -> Int {
        var st: OpaquePointer?
        guard sqlite3_prepare_v2(db, "SELECT COALESCE(SUM(length(raw)),0) FROM frames", -1, &st, nil) == SQLITE_OK else { return 0 }
        defer { sqlite3_finalize(st) }
        return sqlite3_step(st) == SQLITE_ROW ? Int(sqlite3_column_int64(st, 0)) : 0
    }

    private func oldestSession() -> (String, String)? {
        var st: OpaquePointer?
        guard sqlite3_prepare_v2(db, "SELECT daemon,session FROM sessions ORDER BY last_opened ASC LIMIT 1", -1, &st, nil) == SQLITE_OK else { return nil }
        defer { sqlite3_finalize(st) }
        guard sqlite3_step(st) == SQLITE_ROW,
              let d = sqlite3_column_text(st, 0), let s = sqlite3_column_text(st, 1) else { return nil }
        return (String(cString: d), String(cString: s))
    }

    private func version() -> Int32 {
        var st: OpaquePointer?
        guard sqlite3_prepare_v2(db, "PRAGMA user_version", -1, &st, nil) == SQLITE_OK else { return -1 }
        defer { sqlite3_finalize(st) }
        return sqlite3_step(st) == SQLITE_ROW ? sqlite3_column_int(st, 0) : -1
    }

    private func vacuum() { exec("PRAGMA incremental_vacuum") }

    private func exec(_ sql: String) { sqlite3_exec(db, sql, nil, nil, nil) }

    private func run(_ sql: String, _ args: String...) {
        var st: OpaquePointer?
        guard sqlite3_prepare_v2(db, sql, -1, &st, nil) == SQLITE_OK else { return }
        defer { sqlite3_finalize(st) }
        for (i, a) in args.enumerated() { bind(st, Int32(i + 1), a) }
        sqlite3_step(st)
    }

    /// SQLITE_TRANSIENT: SQLite must copy the string, since the Swift bridge's buffer dies at the end
    /// of this call and the statement outlives it.
    private func bind(_ st: OpaquePointer?, _ idx: Int32, _ s: String) {
        sqlite3_bind_text(st, idx, s, -1, unsafeBitCast(-1, to: sqlite3_destructor_type.self))
    }

    private func esc(_ s: String) -> String { s.replacingOccurrences(of: "'", with: "''") }
}
