package store

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"fmt"
	"io"
	"time"
)

// Transcript archiving: old history is COMPRESSED AND KEPT, not deleted.
//
// It used to be deleted. pruneTranscript ran after every append and dropped everything beyond the
// most recent 5000 events per session, permanently — so a long-running session silently lost its
// own beginning, and the durable transcript (the thing that exists to survive a provider that can't
// answer) was bounded while the conversation it backs up was not.
//
// That cap was never what kept the database small: PruneSessions already reclaims whole sessions on
// a TTL and vacuums the freed pages. The row cap only ever punished long, ACTIVE sessions — exactly
// the ones whose history is worth keeping. So the oldest events now move into compressed chunks
// that can be read back on demand, and the TTL remains the real bound on disk.
//
// Chunks are gzipped length-prefixed frames rather than newline-delimited ones: a frame is opaque
// bytes to this layer, and a delimiter that assumes anything about its contents is a decoding bug
// waiting for the first frame that contains it.
const (
	// liveTranscriptRows is how many events stay in the hot table, queryable without decompression.
	// Everything older is archived, never dropped.
	liveTranscriptRows = 4000
	// archiveChunkRows is how many events move into one compressed chunk. Bigger chunks compress
	// better (shared JSON structure across events) but cost more to decompress for a single page
	// read; a few thousand small frames is the range where both stay cheap.
	archiveChunkRows = 2000
)

// ArchiveStats reports what a session has stored, live and archived.
type ArchiveStats struct {
	LiveRows      int
	ArchivedRows  int
	ArchivedBytes int64 // compressed, on disk
	RawBytes      int64 // uncompressed original size
}

// archiveOldTranscript moves a session's oldest live events into a compressed chunk once the live
// table has grown past liveTranscriptRows. Best-effort: on any failure the live rows are LEFT ALONE,
// because losing history to a failed archive would be worse than an oversized table.
func (s *Store) archiveOldTranscript(sessionID string) {
	if s == nil || s.db == nil {
		return
	}
	var live int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM transcript_events WHERE session_id = ?`, sessionID).Scan(&live); err != nil {
		return
	}
	if live <= liveTranscriptRows {
		return
	}
	rows, err := s.db.Query(
		`SELECT seq, raw FROM transcript_events WHERE session_id = ? ORDER BY seq ASC LIMIT ?`,
		sessionID, archiveChunkRows,
	)
	if err != nil {
		return
	}
	var (
		seqs   []int64
		frames [][]byte
		raw    int64
	)
	for rows.Next() {
		var seq int64
		var b []byte
		if err := rows.Scan(&seq, &b); err != nil {
			rows.Close()
			return
		}
		seqs = append(seqs, seq)
		frames = append(frames, b)
		raw += int64(len(b))
	}
	rows.Close()
	if err := rows.Err(); err != nil || len(frames) == 0 {
		return
	}

	blob, err := packFrames(seqs, frames)
	if err != nil {
		return
	}
	// Write the chunk and drop the live rows in ONE transaction: a crash between them would either
	// duplicate the history or lose it, and losing it is the failure this whole change exists to stop.
	tx, err := s.db.Begin()
	if err != nil {
		return
	}
	from, to := seqs[0], seqs[len(seqs)-1]
	if _, err := tx.Exec(
		`INSERT OR REPLACE INTO transcript_archive(session_id, from_seq, to_seq, rows, raw_bytes, blob, created_at)
		 VALUES(?, ?, ?, ?, ?, ?, ?)`,
		sessionID, from, to, len(frames), raw, blob, time.Now().Unix(),
	); err != nil {
		_ = tx.Rollback()
		return
	}
	if _, err := tx.Exec(
		`DELETE FROM transcript_events WHERE session_id = ? AND seq >= ? AND seq <= ?`,
		sessionID, from, to,
	); err != nil {
		_ = tx.Rollback()
		return
	}
	_ = tx.Commit()
}

// ArchivedBefore returns up to limit events with seq < beforeSeq, oldest-first, read back out of the
// compressed chunks. This is what makes archived history REOPENABLE: paging past the live window
// decompresses only the chunks that overlap the requested range.
func (s *Store) ArchivedBefore(sessionID string, beforeSeq int64, limit int) ([][]byte, error) {
	if s == nil || s.db == nil || limit <= 0 {
		return nil, nil
	}
	// Newest chunks first so a page request near the live window doesn't decompress the whole history.
	rows, err := s.db.Query(
		`SELECT from_seq, blob FROM transcript_archive
		 WHERE session_id = ? AND from_seq < ? ORDER BY from_seq DESC`,
		sessionID, beforeSeq,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var collected [][]byte // newest-first while gathering; reversed before returning
	for rows.Next() {
		var from int64
		var blob []byte
		if err := rows.Scan(&from, &blob); err != nil {
			return nil, err
		}
		frames, fseqs, err := unpackFrames(blob)
		if err != nil {
			continue // a corrupt chunk must not sink the whole read — skip it and keep going
		}
		for i := len(frames) - 1; i >= 0; i-- {
			if fseqs[i] >= beforeSeq {
				continue
			}
			collected = append(collected, frames[i])
			if len(collected) >= limit {
				break
			}
		}
		if len(collected) >= limit {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Flip to oldest-first, the order every caller renders in.
	out := make([][]byte, len(collected))
	for i, f := range collected {
		out[len(collected)-1-i] = f
	}
	return out, nil
}

// ArchiveStatsFor reports a session's live/archived split — what `session.info` shows and what makes
// the compression ratio visible instead of a claim.
func (s *Store) ArchiveStatsFor(sessionID string) (ArchiveStats, error) {
	var st ArchiveStats
	if s == nil || s.db == nil {
		return st, nil
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM transcript_events WHERE session_id = ?`, sessionID).Scan(&st.LiveRows); err != nil {
		return st, err
	}
	// COALESCE: a session with no archives must report zeroes, not a NULL scan error.
	err := s.db.QueryRow(
		`SELECT COALESCE(SUM(rows),0), COALESCE(SUM(LENGTH(blob)),0), COALESCE(SUM(raw_bytes),0)
		 FROM transcript_archive WHERE session_id = ?`, sessionID,
	).Scan(&st.ArchivedRows, &st.ArchivedBytes, &st.RawBytes)
	return st, err
}

// packFrames gzips each frame behind a 12-byte header: its seq, then its length.
//
// The seq is stored per frame rather than derived from the chunk's from_seq plus an index, because
// a session's seqs are NOT contiguous — the hub advances the counter on every event including ones
// the transcript dedups away (it is a position counter, not an identity), so archived runs contain
// gaps. Deriving them would silently mis-address every frame after the first gap, and the only
// symptom would be paging that returns slightly wrong history.
//
// Lengths rather than a delimiter: frames are opaque bytes here, and any delimiter is a decoding
// bug waiting for the first frame that happens to contain it.
func packFrames(seqs []int64, frames [][]byte) ([]byte, error) {
	if len(seqs) != len(frames) {
		return nil, fmt.Errorf("archive: %d seqs for %d frames", len(seqs), len(frames))
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	var hdr [12]byte
	for i, f := range frames {
		if int64(len(f)) > int64(^uint32(0)) { // absurd frame; refuse rather than truncate silently
			return nil, fmt.Errorf("transcript frame too large to archive: %d bytes", len(f))
		}
		binary.BigEndian.PutUint64(hdr[0:8], uint64(seqs[i]))
		binary.BigEndian.PutUint32(hdr[8:12], uint32(len(f)))
		if _, err := zw.Write(hdr[:]); err != nil {
			return nil, err
		}
		if _, err := zw.Write(f); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// unpackFrames reverses packFrames, returning each frame with the exact seq it was stored under.
func unpackFrames(blob []byte) ([][]byte, []int64, error) {
	zr, err := gzip.NewReader(bytes.NewReader(blob))
	if err != nil {
		return nil, nil, err
	}
	defer zr.Close()
	var (
		frames [][]byte
		seqs   []int64
		hdr    [12]byte
	)
	for {
		if _, err := io.ReadFull(zr, hdr[:]); err != nil {
			if err == io.EOF {
				break
			}
			return nil, nil, err
		}
		seq := int64(binary.BigEndian.Uint64(hdr[0:8]))
		n := binary.BigEndian.Uint32(hdr[8:12])
		f := make([]byte, n)
		if _, err := io.ReadFull(zr, f); err != nil {
			return nil, nil, err
		}
		frames = append(frames, f)
		seqs = append(seqs, seq)
	}
	return frames, seqs, nil
}
