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
