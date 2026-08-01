package store

import (
	"path/filepath"
	"testing"
)

// TestSequencePolicyDoesNotLoseHistory pins down a property I got wrong while debugging a report of
// lost work, and nearly shipped a "fix" for.
//
// seq is half of PRIMARY KEY(session_id, seq), so it LOOKS as though a caller that advances the
// counter only on a real insert would stall after the first msg_id dedup and then collide forever,
// silently dropping the rest of the conversation. It does not: a row rejected by INSERT OR IGNORE
// never occupies its sequence number, so the next event reuses it successfully and the counter
// self-corrects. Both policies keep every distinct message.
//
// The test asserts that for BOTH policies, because the dangerous change here would be one that makes
// either of them start dropping rows.
func TestSequencePolicyDoesNotLoseHistory(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	const sid = "ses_x"

	// Simulate the broken policy: only advance on insert.
	stalled := int64(0)
	appendStalling := func(msgID, text string) {
		next := stalled + 1
		ok, err := s.AppendTranscript(sid, next, msgID, []byte(`{"text":"`+text+`"}`))
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			stalled = next
		}
	}
	appendStalling("m1", "first")
	appendStalling("m1", "first-restreamed") // deduped by msg_id -> counter stalls at 1
	appendStalling("m2", "second")           // collides with seq 1 -> SILENTLY DROPPED
	appendStalling("m3", "third")            // and so does everything after it

	rows, err := s.Transcript(sid)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Errorf("stall-on-dedup policy stored %d rows, want 3: a rejected insert frees its seq, so the counter recovers", len(rows))
	}

	// The correct policy: always advance. Every distinct message survives.
	s2, err := Open(filepath.Join(t.TempDir(), "t2.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	seq := int64(0)
	appendAdvancing := func(msgID, text string) {
		seq++
		if _, err := s2.AppendTranscript(sid, seq, msgID, []byte(`{"text":"`+text+`"}`)); err != nil {
			t.Fatal(err)
		}
	}
	appendAdvancing("m1", "first")
	appendAdvancing("m1", "first-restreamed") // deduped by msg_id, as intended
	appendAdvancing("m2", "second")
	appendAdvancing("m3", "third")

	rows2, err := s2.Transcript(sid)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows2) != 3 {
		t.Errorf("advancing policy stored %d rows, want 3 — a re-streamed message must not cost the session its future history", len(rows2))
	}
}
