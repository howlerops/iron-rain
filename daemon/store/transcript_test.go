package store

import (
	"path/filepath"
	"testing"
)

// TestTranscriptRoundTripAndDedup covers the durable transcript: ordered append/read, provider-id
// dedup (a re-replayed message doesn't duplicate), NULL-msg-id never dedups, seq continuation, and
// delete.
func TestTranscriptRoundTripAndDedup(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "tx.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Append three finalized events in order.
	for i, ev := range []struct {
		seq   int64
		msgID string
		raw   string
	}{{1, "m1", "hello"}, {2, "m2", "world"}, {3, "", "status"}} {
		ins, err := db.AppendTranscript("s1", ev.seq, ev.msgID, []byte(ev.raw))
		if err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
		if !ins {
			t.Fatalf("append %d should have inserted", i)
		}
	}

	// Re-appending m1 (same provider msg id, new seq) must DEDUP — no duplicate row.
	if ins, err := db.AppendTranscript("s1", 4, "m1", []byte("hello-again")); err != nil || ins {
		t.Fatalf("re-append of m1 should dedup (ins=%v err=%v)", ins, err)
	}
	// A second NULL-msg-id event must NOT dedup (two distinct status rows are fine).
	if ins, err := db.AppendTranscript("s1", 5, "", []byte("status2")); err != nil || !ins {
		t.Fatalf("NULL msg_id should always insert (ins=%v err=%v)", ins, err)
	}

	got, err := db.Transcript("s1")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"hello", "world", "status", "status2"} // in seq order, m1 not duplicated
	if len(got) != len(want) {
		t.Fatalf("got %d events, want %d: %v", len(got), len(want), rawStrings(got))
	}
	for i := range want {
		if string(got[i]) != want[i] {
			t.Fatalf("event %d = %q, want %q", i, got[i], want[i])
		}
	}

	// seq continues from the max after a "restart".
	if mx, _ := db.MaxTranscriptSeq("s1"); mx != 5 {
		t.Fatalf("max seq = %d, want 5", mx)
	}

	// Delete wipes it.
	if err := db.DeleteTranscript("s1"); err != nil {
		t.Fatal(err)
	}
	if got, _ := db.Transcript("s1"); len(got) != 0 {
		t.Fatalf("after delete, got %d events, want 0", len(got))
	}
}

func rawStrings(b [][]byte) []string {
	out := make([]string, len(b))
	for i := range b {
		out[i] = string(b[i])
	}
	return out
}

// TestTranscriptOrphanEviction: PruneSessions must drop transcripts whose session no longer has a
// record (TTL-pruned or deleted), while keeping transcripts for sessions that still exist.
func TestTranscriptOrphanEviction(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "orphan.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// "live" has a session record; "orphan" does not (its session was already gone).
	if err := db.SaveSession(SessionRecord{ID: "live", Provider: "fake"}, 9_999_999_999); err != nil {
		t.Fatal(err)
	}
	_, _ = db.AppendTranscript("live", 1, "a", []byte("keep"))
	_, _ = db.AppendTranscript("orphan", 1, "b", []byte("drop"))

	// cutoff 0 prunes no sessions, but the orphan sweep still runs.
	if _, err := db.PruneSessions(0); err != nil {
		t.Fatal(err)
	}
	if got, _ := db.Transcript("orphan"); len(got) != 0 {
		t.Fatalf("orphan transcript should be evicted, got %d rows", len(got))
	}
	if got, _ := db.Transcript("live"); len(got) != 1 {
		t.Fatalf("live session's transcript should survive, got %d rows", len(got))
	}
}
