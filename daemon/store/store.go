// Package store is the daemon's durable local state, backed by a single SQLite
// database (~/.oculus/oculus.db). It uses the pure-Go modernc.org/sqlite driver so
// the daemon builds and cross-compiles without cgo. SQLite (via libSQL) keeps the
// door open to a Turso embedded-replica later without an SQL rewrite.
//
// Today it persists user-set session names so a rename survives daemon restarts.
package store

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// Store is a handle to the daemon's local SQLite database. Its methods are safe for
// concurrent use (database/sql owns an internal connection pool).
type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the SQLite database at path and applies migrations.
func Open(path string) (*Store, error) {
	// _pragma busy_timeout so concurrent writers wait briefly instead of erroring with
	// SQLITE_BUSY; journal_mode=WAL for better read/write concurrency.
	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// A local file DB serializes writes anyway; a single connection avoids WAL lock churn.
	db.SetMaxOpenConns(1)
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate() error {
	// Enable incremental auto-vacuum so freed pages (pruned sessions/names) can be
	// reclaimed with PRAGMA incremental_vacuum instead of the DB growing forever.
	// auto_vacuum only takes effect on an empty DB or after a VACUUM, so if a legacy
	// DB was created with the default (NONE=0) we switch the mode and VACUUM once.
	var mode int
	if err := s.db.QueryRow(`PRAGMA auto_vacuum`).Scan(&mode); err == nil && mode == 0 {
		if _, err := s.db.Exec(`PRAGMA auto_vacuum=INCREMENTAL`); err == nil {
			_, _ = s.db.Exec(`VACUUM`) // applies the auto_vacuum change to the existing file
		}
	}
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS session_names (
		id         TEXT PRIMARY KEY,
		name       TEXT NOT NULL,
		updated_at INTEGER NOT NULL
	)`); err != nil {
		return err
	}
	// Managed-session records, so sessions survive a daemon restart (the daemon
	// re-attaches them on startup). meta is a JSON blob of the hub's sessionMeta.
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS sessions (
		id         TEXT PRIMARY KEY,
		provider   TEXT NOT NULL,
		cwd        TEXT,
		meta       TEXT,
		updated_at INTEGER NOT NULL
	)`); err != nil {
		return err
	}
	// Handoff index: one row per agent-authored handoff/progress file
	// (.oculus/handoff/<session>.md). Lets the app discover a session's externalized
	// state and later seed scoped child sessions without replaying a transcript. cwd is
	// indexed so handoffs can be listed per working directory / workspace.
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS handoffs (
		session_id TEXT PRIMARY KEY,
		cwd        TEXT NOT NULL,
		path       TEXT NOT NULL,
		title      TEXT,
		summary    TEXT,
		updated_at INTEGER NOT NULL
	)`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS handoffs_cwd ON handoffs(cwd)`); err != nil {
		return err
	}
	// Durable per-session transcript: the finalized conversation (user + assistant messages, completed
	// tool cards, errors) as raw encoded protocol-event bytes, replayed to a (re)subscribing client
	// instead of the memory-bound ring buffer — so history survives daemon restarts and long sessions,
	// for EVERY provider (opencode/claude-code re-stream their history on attach and seed this; pi/cli
	// have no history of their own, so this is their only durable record). msg_id is the provider's
	// stable message id when known; a UNIQUE(session_id,msg_id) with INSERT OR IGNORE dedups a
	// provider re-replaying its history on re-attach. NULL msg_id (status/synthetic) never dedups.
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS transcript_events (
		session_id TEXT NOT NULL,
		seq        INTEGER NOT NULL,
		msg_id     TEXT,
		raw        BLOB NOT NULL,
		ts         INTEGER NOT NULL,
		PRIMARY KEY(session_id, seq)
	)`); err != nil {
		return err
	}
	_, err := s.db.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS transcript_msgid ON transcript_events(session_id, msg_id) WHERE msg_id IS NOT NULL`)
	return err
}

// Close releases the database handle.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// SetName persists a user-set name for a session. A blank name clears the row so the
// session reverts to its derived title (mirrors the in-memory rename semantics).
func (s *Store) SetName(id, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		_, err := s.db.Exec(`DELETE FROM session_names WHERE id = ?`, id)
		return err
	}
	_, err := s.db.Exec(
		`INSERT INTO session_names(id, name, updated_at) VALUES(?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET name = excluded.name, updated_at = excluded.updated_at`,
		id, name, time.Now().Unix(),
	)
	return err
}

// Name returns the persisted name for a session, if one was set.
func (s *Store) Name(id string) (string, bool) {
	var name string
	err := s.db.QueryRow(`SELECT name FROM session_names WHERE id = ?`, id).Scan(&name)
	if err != nil {
		return "", false
	}
	return name, name != ""
}

// Names returns every persisted session name keyed by session id.
func (s *Store) Names() (map[string]string, error) {
	rows, err := s.db.Query(`SELECT id, name FROM session_names`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		out[id] = name
	}
	return out, rows.Err()
}

// SessionRecord is a persisted managed session, enough to re-attach it on startup.
// Meta is an opaque JSON blob (the hub's sessionMeta) the caller marshals/unmarshals.
type SessionRecord struct {
	ID       string
	Provider string
	Cwd      string
	Meta     string // JSON
}

// SaveSession upserts a session record and stamps updated_at (used as the TTL/liveness
// clock — the hub touches live sessions so only stale rows expire).
func (s *Store) SaveSession(r SessionRecord, now int64) error {
	_, err := s.db.Exec(
		`INSERT INTO sessions(id, provider, cwd, meta, updated_at) VALUES(?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET provider=excluded.provider, cwd=excluded.cwd,
		   meta=excluded.meta, updated_at=excluded.updated_at`,
		r.ID, r.Provider, r.Cwd, r.Meta, now,
	)
	return err
}

// TouchSessions bumps updated_at for the given live session ids so they never expire while
// running (the TTL clock is liveness, not creation time). No-op for an empty list.
func (s *Store) TouchSessions(ids []string, now int64) error {
	if len(ids) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	stmt, err := tx.Prepare(`UPDATE sessions SET updated_at = ? WHERE id = ?`)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	defer stmt.Close()
	for _, id := range ids {
		if _, err := stmt.Exec(now, id); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

// DeleteSession removes a session record (on stop/delete or a failed re-attach).
func (s *Store) DeleteSession(id string) error {
	_, err := s.db.Exec(`DELETE FROM sessions WHERE id = ?`, id)
	return err
}

// Sessions returns all persisted session records.
func (s *Store) Sessions() ([]SessionRecord, error) {
	rows, err := s.db.Query(`SELECT id, provider, cwd, meta FROM sessions`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SessionRecord
	for rows.Next() {
		var r SessionRecord
		if err := rows.Scan(&r.ID, &r.Provider, &r.Cwd, &r.Meta); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// PruneSessions deletes session records not updated since `cutoff` (unix seconds) and, if
// anything was removed, reclaims the freed pages via incremental_vacuum. Returns the count.
func (s *Store) PruneSessions(cutoff int64) (int, error) {
	res, err := s.db.Exec(`DELETE FROM sessions WHERE updated_at < ?`, cutoff)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	// Evict transcripts (and handoffs) whose session no longer exists — TTL-pruned or deleted — so the
	// durable transcript can't outlive its session and grow the DB with orphaned rows. Every persisted
	// session has a `sessions` row (ephemeral scratch sessions never write a transcript), so "not in
	// sessions" is exactly the orphan set.
	_, _ = s.db.Exec(`DELETE FROM transcript_events WHERE session_id NOT IN (SELECT id FROM sessions)`)
	if n > 0 {
		_, _ = s.db.Exec(`PRAGMA incremental_vacuum`) // reclaim freed pages to disk
	}
	return int(n), nil
}

// HandoffRecord is an indexed agent-authored handoff file for a session.
type HandoffRecord struct {
	SessionID string
	Cwd       string
	Path      string
	Title     string
	Summary   string
	UpdatedAt int64
}

// UpsertHandoff records (or refreshes) a session's handoff file in the index.
func (s *Store) UpsertHandoff(r HandoffRecord) error {
	_, err := s.db.Exec(
		`INSERT INTO handoffs(session_id, cwd, path, title, summary, updated_at)
		 VALUES(?, ?, ?, ?, ?, ?)
		 ON CONFLICT(session_id) DO UPDATE SET cwd=excluded.cwd, path=excluded.path,
		   title=excluded.title, summary=excluded.summary, updated_at=excluded.updated_at`,
		r.SessionID, r.Cwd, r.Path, r.Title, r.Summary, r.UpdatedAt,
	)
	return err
}

// Handoffs returns indexed handoffs, most-recent first. If cwd is non-empty, only handoffs
// under that working directory are returned.
func (s *Store) Handoffs(cwd string) ([]HandoffRecord, error) {
	q := `SELECT session_id, cwd, path, title, summary, updated_at FROM handoffs`
	var args []any
	if cwd != "" {
		q += ` WHERE cwd = ?`
		args = append(args, cwd)
	}
	q += ` ORDER BY updated_at DESC`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HandoffRecord
	for rows.Next() {
		var r HandoffRecord
		if err := rows.Scan(&r.SessionID, &r.Cwd, &r.Path, &r.Title, &r.Summary, &r.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Handoff returns the indexed handoff for a single session, if one exists.
func (s *Store) Handoff(sessionID string) (HandoffRecord, bool) {
	var r HandoffRecord
	err := s.db.QueryRow(
		`SELECT session_id, cwd, path, title, summary, updated_at FROM handoffs WHERE session_id = ?`,
		sessionID,
	).Scan(&r.SessionID, &r.Cwd, &r.Path, &r.Title, &r.Summary, &r.UpdatedAt)
	if err != nil {
		return HandoffRecord{}, false
	}
	return r, true
}

// DeleteHandoff removes a session's handoff from the index (on session delete).
func (s *Store) DeleteHandoff(sessionID string) error {
	_, err := s.db.Exec(`DELETE FROM handoffs WHERE session_id = ?`, sessionID)
	return err
}
