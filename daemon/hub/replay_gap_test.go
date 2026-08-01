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
