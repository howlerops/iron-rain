package store

import "time"

// maxTranscriptRows caps how many events a single Transcript() read returns — a bound on the READ,
// not on what is kept. Older history lives on in compressed archive chunks and is paged back in on
// demand (see archive.go); nothing is deleted for being old.
const maxTranscriptRows = 5000

// AppendTranscript persists one finalized transcript event (raw encoded protocol-event bytes) for a
// session. When msgID != "" the write is idempotent on (session_id, msg_id) — so a provider that
// re-replays its history on re-attach doesn't duplicate rows. seq orders events within a session.
// Returns whether a row was actually inserted (false = deduped).
// UpsertRenderable writes a row that ADVANCES an existing one rather than duplicating it.
//
// AppendTranscript is INSERT OR IGNORE, which is exactly right for dedup — a re-streamed message
// must not append twice. But a card is not a message: a sub-agent lane is written as "running" and
// later sealed "done", under the same stable id. IGNORE silently dropped the seal, so on reload the
// lane replayed in the state it STARTED in and spun forever for a sub-agent that had finished.
//
// The row keeps its original seq, because seq is its position in the conversation and the card
// belongs where it first appeared, not at the end.
func (s *Store) UpsertRenderable(sessionID string, seq int64, msgID string, raw []byte) error {
	if s == nil || s.db == nil || msgID == "" {
		return nil
	}
	// The partial index's WHERE clause is required in the conflict target for SQLite to match it.
	_, err := s.db.Exec(
		`INSERT INTO transcript_events(session_id, seq, msg_id, raw, ts) VALUES(?, ?, ?, ?, ?)
		 ON CONFLICT(session_id, msg_id) WHERE msg_id IS NOT NULL
		 DO UPDATE SET raw = excluded.raw, ts = excluded.ts`,
		sessionID, seq, msgID, raw, time.Now().Unix(),
	)
	return err
}

func (s *Store) AppendTranscript(sessionID string, seq int64, msgID string, raw []byte) (bool, error) {
	if s == nil || s.db == nil {
		return false, nil
	}
	var mid interface{}
	if msgID != "" {
		mid = msgID
	} // else NULL → never dedups
	res, err := s.db.Exec(
		`INSERT OR IGNORE INTO transcript_events(session_id, seq, msg_id, raw, ts) VALUES(?, ?, ?, ?, ?)`,
		sessionID, seq, mid, raw, time.Now().Unix(),
	)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		// ARCHIVE the overflow rather than deleting it. This used to call pruneTranscript, which
		// DELETEd everything past the cap — so a long session permanently lost its own beginning,
		// and the durable transcript (the fallback for when a provider can't answer) was bounded
		// while the conversation it backs up was not.
		s.archiveOldTranscript(sessionID)
	}
	return n > 0, nil
}

// Transcript returns a session's durable finalized events in order (oldest first), capped to the most
// recent maxTranscriptRows so replay to a new subscriber is bounded.
func (s *Store) Transcript(sessionID string) ([][]byte, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	rows, err := s.db.Query(
		`SELECT raw FROM (
			SELECT seq, raw FROM transcript_events WHERE session_id = ? ORDER BY seq DESC LIMIT ?
		 ) ORDER BY seq ASC`,
		sessionID, maxTranscriptRows,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out [][]byte
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		out = append(out, raw)
	}
	return out, rows.Err()
}

// MaxTranscriptSeq returns the highest seq stored for a session (0 if none) — so a fresh managed
// session continues numbering after a daemon restart instead of colliding with persisted rows.
func (s *Store) MaxTranscriptSeq(sessionID string) (int64, error) {
	if s == nil || s.db == nil {
		return 0, nil
	}
	var seq int64
	// COALESCE so an empty session yields 0, not a scan error.
	err := s.db.QueryRow(`SELECT COALESCE(MAX(seq), 0) FROM transcript_events WHERE session_id = ?`, sessionID).Scan(&seq)
	return seq, err
}

// OldestTranscriptSeq reports the lowest seq still LIVE for a session (anything below it is either
// archived or was never written). Written into *out so a session with no live rows reports 0 rather
// than forcing every caller to special-case an error.
func (s *Store) OldestTranscriptSeq(sessionID string, out *int64) error {
	if s == nil || s.db == nil || out == nil {
		return nil
	}
	return s.db.QueryRow(
		`SELECT COALESCE(MIN(seq), 0) FROM transcript_events WHERE session_id = ?`, sessionID,
	).Scan(out)
}

// DeleteTranscript removes a session's durable transcript (called when the session is permanently
// deleted, alongside DeleteSession).
func (s *Store) DeleteTranscript(sessionID string) error {
	if s == nil || s.db == nil {
		return nil
	}
	_, err := s.db.Exec(`DELETE FROM transcript_events WHERE session_id = ?`, sessionID)
	// The archive holds most of a long session's history, so a "permanent delete" that skipped it
	// would leave the bulk of the conversation on disk after the user asked for it to be gone.
	_, _ = s.db.Exec(`DELETE FROM transcript_archive WHERE session_id = ?`, sessionID)
	return err
}

// DedupeRenderables removes duplicate generative-UI and sub-agent rows left by an earlier build that
// wrote them with a NULL message id — which the unique index treats as never-a-duplicate, so every
// daemon restart appended another copy of the same card.
//
// Keeps the EARLIEST row for each (type, payload id), because that is the one whose position in the
// sequence reflects when the card actually appeared in the conversation. Runs once at startup and is
// a no-op on a clean store.
func (s *Store) DedupeRenderables() (int, error) {
	if s == nil || s.db == nil {
		return 0, nil
	}
	res, err := s.db.Exec(`
        DELETE FROM transcript_events
        WHERE msg_id IS NULL
          AND json_extract(raw, '$.type') IN ('ui.component', 'session.subagent')
          AND json_extract(raw, '$.payload.id') IS NOT NULL
          AND seq > (
            SELECT MIN(t2.seq) FROM transcript_events t2
            WHERE t2.session_id = transcript_events.session_id
              AND json_extract(t2.raw, '$.type') = json_extract(transcript_events.raw, '$.type')
              AND json_extract(t2.raw, '$.payload.id') = json_extract(transcript_events.raw, '$.payload.id')
          )`)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// MoveTranscript re-keys a session's stored history onto a new session id, and reports how many rows
// moved.
//
// Restart is why this exists. It cannot reuse the old id — the provider mints a new one — so it
// creates a fresh session and deletes the old record. Everything a user would notice was carried
// across (name, model, mode) EXCEPT the conversation itself, which is keyed by session id: the
// history stayed behind under a record that had just been deleted, so a restarted session came back
// completely empty and its rows were orphaned in the database.
//
// The archive is moved too, or history older than the retention window would survive the restart
// while recent history did not — the opposite of what anyone would expect.
func (s *Store) MoveTranscript(oldID, newID string) (int, error) {
	if oldID == "" || newID == "" || oldID == newID {
		return 0, nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	// The destination is a brand-new session id, so it holds no rows and the (session_id, seq)
	// primary key cannot collide. Guarded anyway: a partially-completed earlier move would otherwise
	// abort the whole transaction and strand the history in two places.
	if _, err := tx.Exec(`DELETE FROM transcript_events WHERE session_id = ?`, newID); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`DELETE FROM transcript_archive WHERE session_id = ?`, newID); err != nil {
		return 0, err
	}
	res, err := tx.Exec(`UPDATE transcript_events SET session_id = ? WHERE session_id = ?`, newID, oldID)
	if err != nil {
		return 0, err
	}
	moved, _ := res.RowsAffected()
	if _, err := tx.Exec(`UPDATE transcript_archive SET session_id = ? WHERE session_id = ?`, newID, oldID); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return int(moved), nil
}
