package transcript

import (
	"fmt"
	"testing"
)

// The handle cache must be bounded.
//
// One descriptor per session id was kept for the daemon's whole lifetime — including sessions that
// ended hours ago and every session ever restored from disk — so a long-lived daemon leaked handles
// until it hit the process limit, at which point nothing anywhere could open a file. The cache
// exists to avoid an open() per append on the ACTIVE session, which a small cache serves as well.
func TestOpenHandlesAreBounded(t *testing.T) {
	s := New(t.TempDir())
	if s == nil {
		t.Fatal("store did not open")
	}
	for i := 0; i < maxOpenFiles*4; i++ {
		if err := s.Append(fmt.Sprintf("sess_%d", i), Entry{Kind: "user", Text: "hi"}); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	if got := len(s.fhs); got > maxOpenFiles {
		t.Errorf("%d handles held open, cap is %d — this grows until the process runs out", got, maxOpenFiles)
	}
}

// Eviction must not lose data: a session written to again after being evicted still appends.
func TestAppendStillWorksAfterEviction(t *testing.T) {
	s := New(t.TempDir())
	first := "sess_first"
	if err := s.Append(first, Entry{Kind: "user", Text: "one"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxOpenFiles*2; i++ { // push the first one out of the cache
		_ = s.Append(fmt.Sprintf("filler_%d", i), Entry{Kind: "user", Text: "x"})
	}
	if err := s.Append(first, Entry{Kind: "user", Text: "two"}); err != nil {
		t.Fatalf("append after eviction: %v", err)
	}
	entries, err := s.Read(first)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected both entries to survive eviction, got %d", len(entries))
	}
	if entries[0].Text != "one" || entries[1].Text != "two" {
		t.Errorf("entries out of order or lost: %+v", entries)
	}
}
