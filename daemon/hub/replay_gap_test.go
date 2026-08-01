package hub

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/howlerops/oculus/daemon/agent"
	"github.com/howlerops/oculus/daemon/store"
)

type replayFakeSess struct{ id string }

func (f *replayFakeSess) ID() string                                    { return f.id }
func (f *replayFakeSess) Provider() string                              { return "fake" }
func (f *replayFakeSess) Events() <-chan agent.Event                    { return nil }
func (f *replayFakeSess) Prompt(context.Context, string) error          { return nil }
func (f *replayFakeSess) Stop(context.Context) error                    { return nil }
func (f *replayFakeSess) Close() error                                  { return nil }
func (f *replayFakeSess) Respond(context.Context, string, string) error { return nil }

// TestRestoredSessionReplaysDurableHistory is the regression for "This conversation is empty".
//
// After a daemon restart a restored session's ring starts empty and then gains whatever happens
// next — a single status frame is enough. The old logic only reached for the durable transcript when
// the ring was TRIMMED or completely EMPTY, so that one frame closed both doors and every client
// opening the session saw nothing while its real history sat in SQLite.
func TestRestoredSessionReplaysDurableHistory(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := &Hub{db: db, sessions: map[string]*managedSession{}}

	const sid = "ses_restored"
	for i, text := range []string{"first", "second", "third"} {
		if _, err := db.AppendTranscript(sid, int64(i+1), "", []byte(`{"type":"session.message","payload":{"text":"`+text+`"}}`)); err != nil {
			t.Fatal(err)
		}
	}

	m := &managedSession{hub: h, sess: &replayFakeSess{id: sid}}
	// A restored session: durable rows exist, so the ring does not start at the conversation's start.
	seq, _ := db.MaxTranscriptSeq(sid)
	m.ringFromStart = seq == 0
	if m.ringFromStart {
		t.Fatal("a session with durable rows must not be treated as ring-complete")
	}
	// One post-restart frame lands in the ring — exactly the condition that used to hide history.
	m.transcript = [][]byte{[]byte(`{"type":"session.status","payload":{"status":"idle"}}`)}

	replay := m.replayFrames()
	if len(replay) != 4 {
		t.Fatalf("replay = %d frames, want 3 durable + 1 live", len(replay))
	}
	if string(replay[0]) != `{"type":"session.message","payload":{"text":"first"}}` {
		t.Errorf("durable history must lead the replay, got %s", replay[0])
	}
}

// TestFreshSessionDoesNotDoubleReplay: a session created in THIS process has its whole history in
// the ring, so prepending the durable copy would show every message twice.
func TestFreshSessionDoesNotDoubleReplay(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := &Hub{db: db, sessions: map[string]*managedSession{}}
	const sid = "ses_fresh"
	raw := []byte(`{"type":"session.message","payload":{"text":"only"}}`)
	_, _ = db.AppendTranscript(sid, 1, "", raw)

	m := &managedSession{hub: h, sess: &replayFakeSess{id: sid}, ringFromStart: true}
	m.transcript = [][]byte{raw}

	replay := m.replayFrames()
	if len(replay) != 1 {
		t.Fatalf("replay = %d frames, want just the ring (no durable duplicate)", len(replay))
	}
}

// TestReplayNeverSendsAFrameTwice: the durable transcript and the ring overlap by construction —
// every event broadcast after an attach is also persisted. Concatenating them delivered many frames
// twice, which the client used to paper over with a text-equality de-duplicator. The daemon owes the
// client a clean transcript.
func TestReplayNeverSendsAFrameTwice(t *testing.T) {
	a := []byte(`{"type":"session.message","payload":{"text":"one"}}`)
	b := []byte(`{"type":"session.message","payload":{"text":"two"}}`)
	c := []byte(`{"type":"output.delta","payload":{"text":"live"}}`)

	// Durable holds the finalized history; the ring holds the same two events plus a delta that was
	// never persisted.
	out := mergeDurableAndRing([][]byte{a, b}, [][]byte{a, b, c})
	if len(out) != 3 {
		t.Fatalf("replay = %d frames, want 3 (a, b, c) with no repeats", len(out))
	}
	if string(out[0]) != string(a) || string(out[1]) != string(b) || string(out[2]) != string(c) {
		t.Errorf("order must stay oldest-first, got %s | %s | %s", out[0], out[1], out[2])
	}
}

// A genuinely repeated event — the same prompt sent twice, with no message id to tell them apart —
// appears twice in BOTH sources and must survive as two. Naive set-based de-duplication would eat
// the second one and silently rewrite the conversation.
func TestReplayKeepsGenuineRepeats(t *testing.T) {
	dup := []byte(`{"type":"session.message","payload":{"role":"user","text":"again"}}`)
	out := mergeDurableAndRing([][]byte{dup, dup}, [][]byte{dup, dup})
	if len(out) != 2 {
		t.Fatalf("replay = %d frames, want both occurrences of a legitimately repeated message", len(out))
	}
}

// A ring event with no durable counterpart (deltas, UI components, sub-agent rows and user echoes are
// never persisted) must always survive the merge.
func TestReplayKeepsUnpersistedRingEvents(t *testing.T) {
	msg := []byte(`{"type":"session.message","payload":{"text":"persisted"}}`)
	ui := []byte(`{"type":"ui.component","payload":{"id":"c1"}}`)
	out := mergeDurableAndRing([][]byte{msg}, [][]byte{msg, ui})
	if len(out) != 2 || string(out[1]) != string(ui) {
		t.Fatalf("an unpersisted ring event must survive, got %d frames", len(out))
	}
}
