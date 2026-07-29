package store

import "time"

// maxTranscriptRows caps how many finalized events we keep per session in the durable transcript, so
// a runaway session can't grow the DB without bound. Generous — this is finalized messages/tools, not
// per-token deltas, so a very long conversation still fits.
const maxTranscriptRows = 5000

// AppendTranscript persists one finalized transcript event (raw encoded protocol-event bytes) for a
// session. When msgID != "" the write is idempotent on (session_id, msg_id) — so a provider that
// re-replays its history on re-attach doesn't duplicate rows. seq orders events within a session.
// Returns whether a row was actually inserted (false = deduped).
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
		s.pruneTranscript(sessionID) // keep each session's transcript bounded to the most recent rows
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

// pruneTranscript trims a session's transcript to the most recent maxTranscriptRows (called after
// appends so the table stays bounded).
func (s *Store) pruneTranscript(sessionID string) {
	if s == nil || s.db == nil {
		return
	}
	_, _ = s.db.Exec(
		`DELETE FROM transcript_events WHERE session_id = ? AND seq <= (
			SELECT COALESCE(MIN(seq), 0) FROM (
				SELECT seq FROM transcript_events WHERE session_id = ? ORDER BY seq DESC LIMIT ?
			)
		) - 1`,
		sessionID, sessionID, maxTranscriptRows,
	)
}

// DeleteTranscript removes a session's durable transcript (called when the session is permanently
// deleted, alongside DeleteSession).
func (s *Store) DeleteTranscript(sessionID string) error {
	if s == nil || s.db == nil {
		return nil
	}
	_, err := s.db.Exec(`DELETE FROM transcript_events WHERE session_id = ?`, sessionID)
	return err
}
