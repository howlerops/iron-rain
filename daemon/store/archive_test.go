package store

import (
	"bytes"
	"fmt"
	"path/filepath"
	"testing"
)

func archiveStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// frame builds a recognisable event body so a round-trip can prove WHICH event came back, not just
// that the right number of bytes did.
func frame(i int) []byte {
	return []byte(fmt.Sprintf(`{"type":"session.message","payload":{"text":"event-%d"}}`, i))
}

// TestArchiveKeepsEverything is the whole point of the change: a session past the live cap must
// still be able to produce its OLDEST event. Before this, appends past 5000 rows DELETEd the
// beginning of the conversation and it was gone from disk permanently.
func TestArchiveKeepsEverything(t *testing.T) {
	s := archiveStore(t)
	total := liveTranscriptRows + archiveChunkRows + 10
	for i := 0; i < total; i++ {
		if _, err := s.AppendTranscript("s1", int64(i+1), fmt.Sprintf("m%d", i), frame(i)); err != nil {
			t.Fatal(err)
		}
	}

	st, err := s.ArchiveStatsFor("s1")
	if err != nil {
		t.Fatal(err)
	}
	if st.ArchivedRows == 0 {
		t.Fatal("nothing was archived — the live table just grew, so the cap isn't doing its job")
	}
	if got := st.LiveRows + st.ArchivedRows; got != total {
		t.Fatalf("live+archived = %d, want %d — %d events were LOST", got, total, total-got)
	}

	// The very first event must still be reachable.
	var oldestLive int64
	if err := s.OldestTranscriptSeq("s1", &oldestLive); err != nil {
		t.Fatal(err)
	}
	old, err := s.ArchivedBefore("s1", oldestLive, total)
	if err != nil {
		t.Fatal(err)
	}
	if len(old) == 0 {
		t.Fatal("no archived events came back")
	}
	if !bytes.Equal(old[0], frame(0)) {
		t.Fatalf("oldest archived event is %q, want the session's FIRST event", old[0])
	}
	// And it must come back oldest-first, in order.
	for i := 1; i < len(old); i++ {
		if bytes.Equal(old[i], old[i-1]) {
			t.Fatal("duplicate frames in the archived page")
		}
	}
}

// TestArchiveSurvivesSeqGaps: a session's seqs are NOT contiguous — the hub advances the counter on
// events the transcript dedups away — so a chunk covers a range with holes in it. Deriving each
// frame's seq from from_seq+index would mis-address everything after the first hole, and the only
// symptom would be paging that quietly returns the wrong history.
func TestArchiveSurvivesSeqGaps(t *testing.T) {
	s := archiveStore(t)
	seq := int64(0)
	total := liveTranscriptRows + archiveChunkRows + 10
	for i := 0; i < total; i++ {
		seq += int64(1 + i%3) // irregular strides: gaps of 1, 2 and 3
		if _, err := s.AppendTranscript("s1", seq, fmt.Sprintf("m%d", i), frame(i)); err != nil {
			t.Fatal(err)
		}
	}

	var oldestLive int64
	if err := s.OldestTranscriptSeq("s1", &oldestLive); err != nil {
		t.Fatal(err)
	}
	old, err := s.ArchivedBefore("s1", oldestLive, total)
	if err != nil {
		t.Fatal(err)
	}
	if len(old) == 0 {
		t.Fatal("no archived events came back")
	}
	if !bytes.Equal(old[0], frame(0)) {
		t.Fatalf("gapped seqs mis-addressed the archive: first event is %q", old[0])
	}
	// Every archived frame must be one we actually wrote, in the order we wrote it.
	for i, f := range old {
		if !bytes.Equal(f, frame(i)) {
			t.Fatalf("archived event %d is %q, want %q — ordering or addressing is wrong", i, f, frame(i))
		}
	}
}

// TestArchiveRoundTripsExactBytes: frames are opaque, so packing must not assume anything about
// their contents — including that they contain no newlines, which is exactly the assumption a
// delimiter-based format would make.
func TestArchiveRoundTripsExactBytes(t *testing.T) {
	frames := [][]byte{
		[]byte("plain"),
		[]byte("with\nnewlines\nand \x00 nulls"),
		{},
		[]byte(`{"nested":"json with \"quotes\" and \n escapes"}`),
	}
	seqs := []int64{5, 9, 12, 40}
	blob, err := packFrames(seqs, frames)
	if err != nil {
		t.Fatal(err)
	}
	got, gotSeqs, err := unpackFrames(blob)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(frames) {
		t.Fatalf("round-trip returned %d frames, want %d", len(got), len(frames))
	}
	for i := range frames {
		if !bytes.Equal(got[i], frames[i]) {
			t.Fatalf("frame %d round-tripped as %q, want %q", i, got[i], frames[i])
		}
		if gotSeqs[i] != seqs[i] {
			t.Fatalf("frame %d seq round-tripped as %d, want %d", i, gotSeqs[i], seqs[i])
		}
	}
}

// TestArchiveCompresses records that the trade is actually worth making: transcript frames share so
// much structure that a chunk should shrink several-fold. If this ever regresses to ~1x, archiving
// is costing CPU for nothing and the design should be revisited.
func TestArchiveCompresses(t *testing.T) {
	s := archiveStore(t)
	total := liveTranscriptRows + archiveChunkRows + 10
	for i := 0; i < total; i++ {
		if _, err := s.AppendTranscript("s1", int64(i+1), fmt.Sprintf("m%d", i), frame(i)); err != nil {
			t.Fatal(err)
		}
	}
	st, err := s.ArchiveStatsFor("s1")
	if err != nil {
		t.Fatal(err)
	}
	if st.RawBytes == 0 || st.ArchivedBytes == 0 {
		t.Fatalf("no archive byte accounting: %+v", st)
	}
	ratio := float64(st.RawBytes) / float64(st.ArchivedBytes)
	if ratio < 3 {
		t.Fatalf("compression ratio %.1fx (%d raw → %d stored) — too low to justify the CPU",
			ratio, st.RawBytes, st.ArchivedBytes)
	}
	t.Logf("archive compression: %d raw → %d stored (%.1fx)", st.RawBytes, st.ArchivedBytes, ratio)
}

// TestDeleteTranscriptRemovesArchives: a permanent delete must take the cold history too. Most of a
// long session's bytes live in the archive, so skipping it would leave the bulk of the conversation
// on disk after the user asked for it to be gone.
func TestDeleteTranscriptRemovesArchives(t *testing.T) {
	s := archiveStore(t)
	total := liveTranscriptRows + archiveChunkRows + 10
	for i := 0; i < total; i++ {
		if _, err := s.AppendTranscript("s1", int64(i+1), fmt.Sprintf("m%d", i), frame(i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.DeleteTranscript("s1"); err != nil {
		t.Fatal(err)
	}
	st, err := s.ArchiveStatsFor("s1")
	if err != nil {
		t.Fatal(err)
	}
	if st.LiveRows != 0 || st.ArchivedRows != 0 {
		t.Fatalf("delete left history behind: %+v", st)
	}
}
