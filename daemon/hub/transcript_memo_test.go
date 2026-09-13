package hub

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/store"
)

// Assembling a restored session's history is expensive, and it was assembled from scratch every
// single time anyone asked for it.
//
// fullHistory reads the WHOLE durable transcript out of SQLite and merges it into the live ring,
// sha256-ing every frame on both sides to line them up. subscribe() calls it, and so does every
// "Show earlier messages" page — so reading back through a long conversation re-derived the entire
// conversation once per page, and switching between two restored sessions did it on every switch.
//
// These tests pin both halves of the memo: that it is consulted, and that it can never answer with
// a history older than the sources it was built from. The second half is the one that matters —
// a stale memo means a replay that is missing the message that just arrived, and this file's own
// comments record two separate occasions on which a conversation went silently missing.
func TestHistoryIsAssembledOncePerVersionOfItsSources(t *testing.T) {
	m, db := memoSession(t)

	first := m.fullHistory()
	if len(first) != 4 {
		t.Fatalf("history = %d frames, want 3 durable + 1 ring", len(first))
	}
	if got := m.histBuilds.Load(); got != 1 {
		t.Fatalf("builds = %d after the first call, want 1", got)
	}

	// Nothing has been written. Asking again must not re-read the transcript.
	second := m.fullHistory()
	if got := m.histBuilds.Load(); got != 1 {
		t.Errorf("builds = %d after a second call with no writes, want 1 — every subscribe and "+
			"every history page is re-deriving the whole conversation", got)
	}
	if !sameFrames(first, second) {
		t.Error("the memo returned a different history than the build it memoized")
	}

	// A RING write must invalidate it: this is the live half of the conversation.
	m.broadcast([]byte(`{"type":"session.message","payload":{"text":"live"}}`))
	third := m.fullHistory()
	if got := m.histBuilds.Load(); got != 2 {
		t.Errorf("builds = %d after a ring write, want 2", got)
	}
	if !hasFrameContaining(third, "live") {
		t.Error("a message broadcast after the memo was built is missing from the history — " +
			"a client subscribing now would not see the agent's latest reply")
	}

	// A DURABLE write must invalidate it too: the ring is untouched when a finalized message is
	// persisted against a row that already exists, so keying on the ring alone would miss this.
	m.appendDurable(m.sess.ID(), "msg-4", []byte(`{"type":"session.message","payload":{"text":"persisted"}}`))
	fourth := m.fullHistory()
	if got := m.histBuilds.Load(); got != 3 {
		t.Errorf("builds = %d after a durable write, want 3", got)
	}
	if !hasFrameContaining(fourth, "persisted") {
		t.Error("a durably persisted message is missing from the history the memo served")
	}
	_ = db
}

// The memo must not keep a second copy of a transcript nobody is reading. It is released by the
// heartbeat sweep, which is the only thing that visits every session on a timer.
func TestAnUnreadHistoryMemoIsReleased(t *testing.T) {
	m, _ := memoSession(t)
	m.fullHistory()

	m.expireHistoryCache(time.Now())
	m.histMu.Lock()
	held := m.histCache != nil
	m.histMu.Unlock()
	if !held {
		t.Error("the memo was dropped while it was still fresh — the sweep is evicting on every tick")
	}

	m.expireHistoryCache(time.Now().Add(histCacheTTL + time.Second))
	m.histMu.Lock()
	held = m.histCache != nil
	m.histMu.Unlock()
	if held {
		t.Errorf("a memo unread for longer than %s is still retained — a session the user opened "+
			"once and left holds a full second copy of its transcript forever", histCacheTTL)
	}

	// And it rebuilds correctly afterwards rather than serving an emptied cache.
	if got := m.fullHistory(); len(got) != 4 {
		t.Errorf("history after eviction = %d frames, want 4", len(got))
	}
}

// Callers sub-slice and append to what fullHistory returns (historyPage does both). If they were
// handed the memo's own slice, an append into its spare capacity would rewrite the memo's frames
// and every later reader would get a transcript with someone else's paging artifacts in it.
func TestTheMemoIsNotAliasedToItsCallers(t *testing.T) {
	m, _ := memoSession(t)
	got := m.fullHistory()

	// Exactly what historyPage does to the array it is handed.
	page := got[:2]
	_ = append(page, []byte(`{"type":"session.message","payload":{"text":"scribble"}}`))

	after := m.fullHistory()
	if hasFrameContaining(after, "scribble") {
		t.Error("a caller appending to its page wrote through into the memoized history")
	}
	if !hasFrameContaining(after, "third") {
		t.Error("a caller appending to its page overwrote a frame of the memoized history")
	}
}

// memoSession builds a RESTORED session — durable rows plus a ring that does not start at the
// conversation's beginning — which is the only shape that reaches the expensive assembly path.
func memoSession(t *testing.T) (*managedSession, *store.Store) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	h := &Hub{db: db, sessions: map[string]*managedSession{}}

	const sid = "ses_memo"
	for i, text := range []string{"first", "second", "third"} {
		if _, err := db.AppendTranscript(sid, int64(i+1), "", []byte(`{"type":"session.message","payload":{"text":"`+text+`"}}`)); err != nil {
			t.Fatal(err)
		}
	}
	m := &managedSession{hub: h, sess: &replayFakeSess{id: sid}}
	seq, _ := db.MaxTranscriptSeq(sid)
	m.txSeq = seq // seeded past the restored rows, as restoreSessions does
	m.ringFromStart = false
	m.recordOnly([]byte(`{"type":"session.status","payload":{"status":"idle"}}`))
	return m, db
}

func sameFrames(a, b [][]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if string(a[i]) != string(b[i]) {
			return false
		}
	}
	return true
}

func hasFrameContaining(frames [][]byte, needle string) bool {
	for _, f := range frames {
		if strings.Contains(string(f), needle) {
			return true
		}
	}
	return false
}
