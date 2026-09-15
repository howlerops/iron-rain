package hub

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/howlerops/oculus/daemon/agent"
	"github.com/howlerops/oculus/daemon/protocol"
	"github.com/howlerops/oculus/daemon/store"
)

// Paging by a cursor the DAEMON assigned, rather than a count the client derived.
//
// The count-based pager computes `len(all) - loaded`, which is only correct if the client's tally
// agrees exactly with the daemon's ring. It could not: the client tallied by message type, guessing
// which frames the daemon had stored, and that guess was wrong in three consecutive sweeps. Every
// over-count asks for a page starting before the transcript actually ends — a hole the client has no
// way to detect, because a short page looks exactly like the start of the conversation.
//
// With a sequence carried on the frame there is nothing to agree about.
func TestPagingByCursorReturnsTheFramesBeforeIt(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := &Hub{db: db, sessions: map[string]*managedSession{}}
	sess := &forkSess{ch: make(chan agent.Event, 4), id: "pager"}
	m := newManagedSession(h, sess, sessionMeta{})
	m.ringFromStart = true

	// Twelve durable messages, each one sequenced by the daemon as it is stored.
	var seqs []int64
	for i := 0; i < 12; i++ {
		raw := encodeMessage(t, sess.ID(), fmt.Sprintf("msg %d", i))
		stamped := m.appendDurable(sess.ID(), fmt.Sprintf("m%d", i), raw)
		m.broadcast(stamped)
		s := frameSeq(stamped)
		if s == 0 {
			t.Fatalf("frame %d was stored without a sequence — the client has no cursor to page from", i)
		}
		seqs = append(seqs, s)
	}

	// Page back from the 9th frame's seq, asking for 4.
	page, more := m.historyPageBefore(seqs[8], 4)
	if !more {
		t.Error("reported no more history with eight frames still behind the cursor")
	}
	var texts []string
	for _, raw := range page {
		var f struct {
			Seq     int64 `json:"seq"`
			Payload struct {
				Text string `json:"text"`
			} `json:"payload"`
		}
		if json.Unmarshal(raw, &f) != nil {
			continue
		}
		texts = append(texts, f.Payload.Text)
		if f.Seq >= seqs[8] {
			t.Errorf("page contains seq %d, at or past the cursor %d — the client already has that "+
				"frame and would render it twice", f.Seq, seqs[8])
		}
	}
	want := []string{"msg 4", "msg 5", "msg 6", "msg 7"}
	if len(texts) != len(want) {
		t.Fatalf("page has %d frames %v, want %d %v", len(texts), texts, len(want), want)
	}
	for i := range want {
		if texts[i] != want[i] {
			t.Fatalf("page = %v, want %v — a cursor page must be exactly the frames before it, with "+
				"no gap and no overlap", texts, want)
		}
	}

	// And paging from the very first seq reaches the front rather than inventing more.
	if _, more := m.historyPageBefore(seqs[0], 4); more {
		t.Error("reported more history before the first frame")
	}
}

// Unsequenced frames — streaming deltas, transient status — ride along inside a page but must never
// act as its boundary: they hold no position in the durable transcript, so treating one as a cursor
// would page from a number that means nothing.
func TestUnsequencedFramesDoNotActAsACursor(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := &Hub{db: db, sessions: map[string]*managedSession{}}
	sess := &forkSess{ch: make(chan agent.Event, 4), id: "mixed"}
	m := newManagedSession(h, sess, sessionMeta{})
	m.ringFromStart = true

	first := m.appendDurable(sess.ID(), "m0", encodeMessage(t, sess.ID(), "stored one"))
	m.broadcast(first)
	// A delta: broadcast, never stored, therefore no seq.
	delta, err := (agent.Event{Type: protocol.TypeOutputDelta,
		Payload: protocol.OutputDelta{SessionID: sess.ID(), Text: "streaming…"}}).Encode()
	if err != nil {
		t.Fatal(err)
	}
	m.broadcast(delta)
	if frameSeq(delta) != 0 {
		t.Fatal("an unstored frame carries a sequence, which would let it move a cursor it has no place in")
	}
	second := m.appendDurable(sess.ID(), "m1", encodeMessage(t, sess.ID(), "stored two"))
	m.broadcast(second)

	// Paging before the second stored frame must yield the first AND the delta between them.
	page, _ := m.historyPageBefore(frameSeq(second), 1)
	if len(page) != 2 {
		t.Fatalf("page has %d frames, want 2 (the stored frame and the unsequenced delta that "+
			"followed it) — deltas ride along even though they cannot be a boundary", len(page))
	}
}

// A provider RE-STREAM must still be suppressed, which the cursor alone does not do.
//
// This is the half of the old dedup that survives, and the reason the plan's "client dedup dies" was
// too broad. opencode and claude-code push their own history back through the pump on recover and on
// a late attach. The DATABASE deduplicates those by message id — no second row — but they are still
// broadcast, and now they carry a NEW sequence, because the counter advances whether or not the row
// was inserted. A cursor therefore cannot recognise them: to the client they look like fresh frames
// with fresh positions, and the conversation renders twice.
//
// Caught by the existing replay tests when the dedup was deleted; kept here as the statement of why.
func TestAReStreamedFrameIsSuppressedEvenThoughItsSeqIsNew(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := &Hub{db: db, sessions: map[string]*managedSession{}}
	sess := &forkSess{ch: make(chan agent.Event, 4), id: "restream"}
	m := newManagedSession(h, sess, sessionMeta{})
	m.ringFromStart = true

	original := encodeMessage(t, sess.ID(), "the only reply")
	first := m.appendDurable(sess.ID(), "msg-1", original)
	m.broadcast(first)

	// The provider re-streams the same message. Same content, same msg id — a new sequence.
	second := m.appendDurable(sess.ID(), "msg-1", original)
	if frameSeq(second) == frameSeq(first) {
		t.Fatal("precondition: the re-stream kept the original sequence, so this test proves nothing")
	}

	// A subscriber that already holds the first copy must not be sent the second.
	s := &subscriber{ch: make(chan []byte, 16), done: make(chan struct{})}
	s.rememberReplay([][]byte{first}, replayGrace)
	if !s.seen(second) {
		t.Fatal("a re-streamed frame was not recognised as one the subscriber already has.\n\n" +
			"Its sequence is new, so no cursor can catch it; without the content dedup the whole " +
			"conversation renders a second time on every recover and late attach.")
	}
}
