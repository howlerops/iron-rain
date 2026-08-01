package activity

import (
	"path/filepath"
	"testing"
)

// TestClearNeedsYouClearsOnlyThatSession is the "the badge outlives the reason" bug: you answer the
// approval on the Mac and the phone keeps a red needs-you dot for a session that no longer wants
// anything. Clearing must be scoped to ONE session (another session's live ask is untouched), must
// leave the events in the feed as read history, and must survive a daemon restart.
func TestClearNeedsYouClearsOnlyThatSession(t *testing.T) {
	path := filepath.Join(t.TempDir(), "activity.jsonl")
	s := New(path, 100)

	s.Record(Event{Kind: KindNeedsInput, NeedsYou: true, SessionID: "sess-A", Title: "approve bash"})
	s.Record(Event{Kind: KindNeedsInput, NeedsYou: true, SessionID: "sess-B", Title: "approve write"})
	s.Record(Event{Kind: KindFinished, SessionID: "sess-A", Title: "finished"})

	flipped := s.ClearNeedsYou("sess-A")
	if len(flipped) != 1 {
		t.Fatalf("ClearNeedsYou should report the 1 event it flipped, got %d", len(flipped))
	}
	if !flipped[0].Read || flipped[0].SessionID != "sess-A" {
		t.Fatalf("flipped event must be the session's own, marked read: %+v", flipped[0])
	}

	unread := func(st *Store) map[string]int {
		out := map[string]int{}
		for _, e := range st.Recent() {
			if e.NeedsYou && !e.Read {
				out[e.SessionID]++
			}
		}
		return out
	}
	if got := unread(s); got["sess-A"] != 0 {
		t.Fatalf("sess-A should have no live needs-you after clearing, got %d", got["sess-A"])
	} else if got["sess-B"] != 1 {
		t.Fatalf("another session's needs-you must survive, got %d", got["sess-B"])
	}
	// The event stays in the feed (read), so the history still tells the story.
	kept := 0
	for _, e := range s.Recent() {
		if e.SessionID == "sess-A" && e.NeedsYou {
			kept++
		}
	}
	if kept != 1 {
		t.Fatalf("cleared needs-you must remain in the feed as read history, got %d", kept)
	}
	// A second clear is a no-op — nothing left to flip, so nothing to re-broadcast.
	if again := s.ClearNeedsYou("sess-A"); len(again) != 0 {
		t.Fatalf("second clear should flip nothing, got %d", len(again))
	}

	// Durability: an in-memory-only flip would resurrect the phantom badge on the next daemon start.
	if got := unread(New(path, 100)); got["sess-A"] != 0 || got["sess-B"] != 1 {
		t.Fatalf("clear did not survive reload: %v", got)
	}
}
