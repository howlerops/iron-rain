package activity

import (
	"path/filepath"
	"testing"
)

// TestNeedsYouSupersedesPerSession is the "6 needs-you, one session" bug: a wedged fan-out emitted a
// needs-you per stuck event, the badge counted all of them, and answering cleared one — leaving five
// phantoms pointing at the same place. The newest ask supersedes older UNREAD ones for the SAME
// session; other sessions and read history are untouched.
func TestNeedsYouSupersedesPerSession(t *testing.T) {
	path := filepath.Join(t.TempDir(), "activity.jsonl")
	s := New(path, 100)

	s.Record(Event{Kind: KindNeedsInput, NeedsYou: true, SessionID: "sess-A", Title: "ask 1"})
	s.Record(Event{Kind: KindNeedsInput, NeedsYou: true, SessionID: "sess-A", Title: "ask 2"})
	s.Record(Event{Kind: KindError, NeedsYou: true, SessionID: "sess-A", Title: "ask 3"})
	s.Record(Event{Kind: KindNeedsInput, NeedsYou: true, SessionID: "sess-B", Title: "other session"})
	s.Record(Event{Kind: KindFinished, SessionID: "sess-A", Title: "not a needs-you"})

	unreadNeeds := func(st *Store) map[string]int {
		out := map[string]int{}
		for _, e := range st.Recent() {
			if e.NeedsYou && !e.Read {
				out[e.SessionID]++
			}
		}
		return out
	}

	got := unreadNeeds(s)
	if got["sess-A"] != 1 {
		t.Fatalf("sess-A should have exactly 1 live needs-you, got %d", got["sess-A"])
	}
	if got["sess-B"] != 1 {
		t.Fatalf("another session's needs-you must be untouched, got %d", got["sess-B"])
	}
	// The superseded entries are read history, not deleted — the feed still tells the story.
	total := 0
	for _, e := range s.Recent() {
		if e.SessionID == "sess-A" && e.NeedsYou {
			total++
		}
	}
	if total != 3 {
		t.Errorf("superseded events should remain in the feed as read, got %d", total)
	}

	// Durability: the flips must survive a reload, or the phantoms come back on restart.
	s2 := New(path, 100)
	got2 := unreadNeeds(s2)
	if got2["sess-A"] != 1 || got2["sess-B"] != 1 {
		t.Fatalf("supersede did not survive reload: %v", got2)
	}
}

// TestNeedsYouWithoutSessionIsNotDeduped: an event with no session id has nothing to supersede by —
// collapsing unrelated global alerts would hide real ones.
func TestNeedsYouWithoutSessionIsNotDeduped(t *testing.T) {
	s := New(filepath.Join(t.TempDir(), "a.jsonl"), 100)
	s.Record(Event{Kind: KindError, NeedsYou: true, Title: "global 1"})
	s.Record(Event{Kind: KindError, NeedsYou: true, Title: "global 2"})
	n := 0
	for _, e := range s.Recent() {
		if e.NeedsYou && !e.Read {
			n++
		}
	}
	if n != 2 {
		t.Fatalf("session-less needs-you must not supersede each other, got %d live", n)
	}
}
