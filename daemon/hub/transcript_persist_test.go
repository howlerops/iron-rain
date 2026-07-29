package hub

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/howlerops/oculus/daemon/agent"
	"github.com/howlerops/oculus/daemon/protocol"
	"github.com/howlerops/oculus/daemon/store"
)

type txFakeSess struct{ ch chan agent.Event }

func (f *txFakeSess) ID() string                                     { return "s1" }
func (f *txFakeSess) Provider() string                              { return "fake" }
func (f *txFakeSess) Events() <-chan agent.Event                    { return f.ch }
func (f *txFakeSess) Prompt(context.Context, string) error          { return nil }
func (f *txFakeSess) Respond(context.Context, string, string) error { return nil }
func (f *txFakeSess) Stop(context.Context) error                    { return nil }
func (f *txFakeSess) Close() error                                  { return nil }

func enc(t *testing.T, ev agent.Event) []byte {
	raw, err := ev.Encode()
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// TestDurableTranscriptPersistAndReplay: a turn that streams a user message + assistant DELTAS (no
// finalized assistant message, like claude-code/pi) persists the user msg + a SYNTHETIC assistant msg
// on idle; a re-subscribe with an empty memory buffer replays exactly that from SQLite.
func TestDurableTranscriptPersistAndReplay(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "d.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := New()
	h.SetStore(db)
	m := newManagedSession(h, &txFakeSess{ch: make(chan agent.Event, 8)}, sessionMeta{})

	userEv := agent.Event{Type: protocol.TypeSessionMessage, Payload: protocol.SessionMessage{SessionID: "s1", Role: "user", Text: "hi"}}
	m.persistDurable(userEv, enc(t, userEv))
	for _, d := range []string{"hel", "lo"} {
		dv := agent.Event{Type: protocol.TypeOutputDelta, Payload: protocol.OutputDelta{SessionID: "s1", Text: d}}
		m.persistDurable(dv, enc(t, dv)) // accumulated, not written
	}
	m.finalizeTurnTranscript() // writes the synthetic assistant "hello"

	rows, err := db.Transcript("s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("want 2 durable events (user + assistant), got %d", len(rows))
	}
	// Decode + verify roles/text survived the round-trip.
	roles := map[string]string{}
	for _, raw := range rows {
		env, err := protocol.Decode(raw)
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		if env.Type != protocol.TypeSessionMessage {
			t.Fatalf("unexpected persisted type %q", env.Type)
		}
		var msg protocol.SessionMessage
		if err := env.Unmarshal(&msg); err != nil {
			t.Fatal(err)
		}
		roles[msg.Role] = msg.Text
	}
	if roles["user"] != "hi" {
		t.Fatalf("user text = %q, want hi", roles["user"])
	}
	if roles["assistant"] != "hello" {
		t.Fatalf("assistant text = %q, want hello (accumulated from deltas)", roles["assistant"])
	}

	// A provider that DID emit a finalized assistant message must NOT also get a synthetic one.
	asstEv := agent.Event{Type: protocol.TypeSessionMessage, Payload: protocol.SessionMessage{SessionID: "s1", Role: "assistant", Text: "real", MsgID: "m9"}}
	m.persistDurable(asstEv, enc(t, asstEv))
	dv := agent.Event{Type: protocol.TypeOutputDelta, Payload: protocol.OutputDelta{SessionID: "s1", Text: "ignored"}}
	m.persistDurable(dv, enc(t, dv))
	m.finalizeTurnTranscript() // should NOT add a synthetic (asstPersisted was set)
	rows2, _ := db.Transcript("s1")
	if len(rows2) != 3 {
		t.Fatalf("want 3 after a real assistant msg (no synthetic dup), got %d", len(rows2))
	}
}
