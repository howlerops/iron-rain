package hub

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// The tests below all put the ring-only frame LAST — the one arrangement that cannot expose a
// reordering bug. That is exactly how an order-destroying merge shipped, so these now assert
// POSITION, not just count.

func ids(frames [][]byte) string {
	out := ""
	for _, f := range frames {
		var h struct {
			Payload struct {
				ID string `json:"id"`
			} `json:"payload"`
		}
		_ = json.Unmarshal(f, &h)
		out += h.Payload.ID + " "
	}
	return strings.TrimSpace(out)
}

func fr(id string) []byte {
	return []byte(`{"type":"session.message","payload":{"id":"` + id + `"}}`)
}

// TestJoinPreservesBroadcastOrder is the regression for the reordering merge.
//
// A restored session replays its durable history, then runs a new turn. The ring holds that turn:
// the user's echo, the tool card, and the assistant's reply — and only the reply and the tool are
// persisted. Concatenating "all durable, then the ring leftovers" put the echo AFTER the reply it
// prompted.
func TestJoinPreservesBroadcastOrder(t *testing.T) {
	durable := [][]byte{fr("old1"), fr("old2"), fr("tool"), fr("reply")}
	ring := [][]byte{fr("echo"), fr("tool"), fr("reply")}

	got := ids(joinHistory(durable, ring))
	want := "old1 old2 echo tool reply"
	if got != want {
		t.Errorf("join order = %q, want %q — the ring's position is the truth", got, want)
	}
}

// A ring frame with no durable counterpart, sitting in the MIDDLE of the turn, must stay in the
// middle. Deltas and running tool cards are only meaningful in position.
func TestJoinKeepsUnpersistedFramesInPlace(t *testing.T) {
	durable := [][]byte{fr("msg1"), fr("msg2")}
	ring := [][]byte{fr("msg1"), fr("delta"), fr("msg2")}
	got := ids(joinHistory(durable, ring))
	if got != "msg1 delta msg2" {
		t.Errorf("join = %q, want msg1 delta msg2", got)
	}
}

// Nothing may be sent twice: the sources overlap by construction.
func TestJoinNeverSendsAFrameTwice(t *testing.T) {
	durable := [][]byte{fr("a"), fr("b")}
	ring := [][]byte{fr("a"), fr("b"), fr("c")}
	got := ids(joinHistory(durable, ring))
	if got != "a b c" {
		t.Errorf("join = %q, want a b c with no repeats", got)
	}
}

// A genuinely repeated event — the same prompt sent twice, no id to distinguish them — appears twice
// in both sources and must survive as two.
func TestJoinKeepsGenuineRepeats(t *testing.T) {
	dup := []byte(`{"type":"session.message","payload":{"role":"user","text":"again"}}`)
	out := joinHistory([][]byte{dup, dup}, [][]byte{dup, dup})
	if len(out) != 2 {
		t.Fatalf("join = %d frames, want both occurrences", len(out))
	}
}

// Durable history older than the ring window leads.
func TestJoinPutsOlderDurableFirst(t *testing.T) {
	durable := [][]byte{fr("ancient"), fr("recent")}
	ring := [][]byte{fr("recent"), fr("live")}
	got := ids(joinHistory(durable, ring))
	if got != "ancient recent live" {
		t.Errorf("join = %q, want ancient recent live", got)
	}
}

// TestTailCapKeepsMeaningfulHistory: one streamed reply contributes hundreds of output.delta frames.
// A naive replay[len-limit:] lands entirely inside that run, slices off every message, and renders an
// empty conversation — the exact symptom this work exists to fix.
func TestTailCapKeepsMeaningfulHistory(t *testing.T) {
	var replay [][]byte
	for i := 0; i < 5; i++ {
		replay = append(replay, fr("msg"))
	}
	for i := 0; i < 400; i++ {
		replay = append(replay, []byte(`{"type":"output.delta","payload":{"text":"x"}}`))
	}
	out := boundTail(replay, 3)
	msgs := 0
	for _, f := range out {
		var h struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(f, &h)
		if h.Type == "session.message" {
			msgs++
		}
	}
	if msgs < 3 {
		t.Errorf("tail kept %d messages, want at least the 3 it was capped to — a cap that keeps only deltas renders empty", msgs)
	}
}

func TestTailCapPassesShortReplaysThrough(t *testing.T) {
	replay := [][]byte{fr("a"), fr("b")}
	if len(boundTail(replay, 200)) != 2 {
		t.Error("a replay under the cap must pass through untouched")
	}
}

// TestReStreamAfterSubscribeIsSuppressed: the replay is assembled from a SNAPSHOT of the ring, but a
// self-replaying provider pushes its history through broadcast AFTER that snapshot — on recover, and
// on an attach for a session the daemon could not re-attach at startup. De-duplicating the snapshot
// alone left the client showing its conversation twice.
func TestReStreamAfterSubscribeIsSuppressed(t *testing.T) {
	s := &subscriber{ch: make(chan []byte, 8), done: make(chan struct{})}
	a, b := fr("one"), fr("two")
	s.rememberReplay([][]byte{a, b}, time.Minute)

	if !s.seen(a) || !s.seen(b) {
		t.Fatal("frames already delivered in the replay must be suppressed")
	}
	// Each replay frame suppresses exactly ONE copy: a genuinely repeated message later in the
	// conversation must still get through.
	if s.seen(a) {
		t.Error("a second occurrence is real content, not a re-stream — it must be delivered")
	}
	if s.seen(fr("new")) {
		t.Error("a frame that was never in the replay must always be delivered")
	}
}

// Once the window closes the tracking is dropped, so the map cannot grow for the life of a
// long-running connection and a genuine repeat is never mistaken for a re-stream.
func TestReStreamWindowExpires(t *testing.T) {
	s := &subscriber{ch: make(chan []byte, 8), done: make(chan struct{})}
	a := fr("one")
	s.rememberReplay([][]byte{a}, -time.Second) // already expired
	if s.seen(a) {
		t.Error("past the window, a repeat is genuine content and must be delivered")
	}
	if s.delivered != nil {
		t.Error("the tracking map must be released when the window closes")
	}
}

// TestJoinDropsTheSyntheticEcho: for claude-code, pi and the CLI the daemon SYNTHESISES an
// end-of-turn assistant message from accumulated deltas and writes it to SQLite without ever
// broadcasting it. It therefore has no byte twin in the ring — while the deltas that built its text
// do. Merging durable with the ring emitted both and rendered every reply twice.
func TestJoinDropsTheSyntheticEcho(t *testing.T) {
	synthetic := []byte(`{"type":"session.message","payload":{"role":"assistant","text":"hello world"}}`)
	d1 := []byte(`{"type":"output.delta","payload":{"text":"hello "}}`)
	d2 := []byte(`{"type":"output.delta","payload":{"text":"world"}}`)

	out := joinHistory([][]byte{synthetic}, [][]byte{d1, d2})
	if len(out) != 2 {
		t.Fatalf("join emitted %d frames, want just the two deltas — the synthetic echo duplicates them", len(out))
	}
}

// A provider's REAL assistant message carries a message id and must always survive, even when its
// text happens to appear in the streamed run.
func TestJoinKeepsRealAssistantMessages(t *testing.T) {
	real := []byte(`{"type":"session.message","payload":{"role":"assistant","text":"hello world","msg_id":"m1"}}`)
	d1 := []byte(`{"type":"output.delta","payload":{"text":"hello world"}}`)
	out := joinHistory([][]byte{real}, [][]byte{d1})
	if len(out) != 2 {
		t.Errorf("join emitted %d frames, want the real message kept alongside the delta", len(out))
	}
}

// A user's message is never a synthetic echo, whatever it says.
func TestJoinKeepsUserMessagesThatEchoText(t *testing.T) {
	user := []byte(`{"type":"session.message","payload":{"role":"user","text":"hello world"}}`)
	d1 := []byte(`{"type":"output.delta","payload":{"text":"hello world"}}`)
	out := joinHistory([][]byte{user}, [][]byte{d1})
	if len(out) != 2 {
		t.Errorf("join emitted %d frames, want the user's own message kept", len(out))
	}
}

// TestJoinPrefersTheRingsCopyOfACard: a generative-UI card advances state — emitted `running`, then
// updated to `ready`. The durable copy from an earlier run and the freshly re-derived ring copy are
// therefore the SAME card with different bytes, which byte matching cannot see: it served both, and
// the conversation grew a duplicate card on every restart.
func TestJoinPrefersTheRingsCopyOfACard(t *testing.T) {
	stale := []byte(`{"type":"ui.component","payload":{"id":"plan","status":"running"}}`)
	fresh := []byte(`{"type":"ui.component","payload":{"id":"plan","status":"ready"}}`)

	out := joinHistory([][]byte{stale}, [][]byte{fresh})
	if len(out) != 1 {
		t.Fatalf("join emitted %d frames, want 1 — the same card in two states is still one card", len(out))
	}
	if string(out[0]) != string(fresh) {
		t.Errorf("kept the stale copy; the ring holds the newer state")
	}
}

// A card the ring does NOT have must survive from the durable store — that is the whole point of
// persisting it for providers that never re-stream.
func TestJoinKeepsCardsTheRingLacks(t *testing.T) {
	old := []byte(`{"type":"ui.component","payload":{"id":"older","status":"ready"}}`)
	out := joinHistory([][]byte{old}, [][]byte{fr("live")})
	if len(out) != 2 {
		t.Errorf("join emitted %d frames, want the older card kept alongside the live one", len(out))
	}
}
