package hub

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/howlerops/oculus/daemon/protocol"
	"github.com/howlerops/oculus/daemon/store"
)

// TestConversationSurvivesRestarts is the regression net for a whole CLASS of bug.
//
// Three separate defects shipped this cycle while every unit test stayed green — the transcript
// cache was entirely inert, the relay tore down healthy connections, and generative-UI cards
// multiplied on every restart. Each was found by replaying a REAL session against a live daemon,
// because each only appears once a conversation has been through the create → work → restart →
// reopen cycle. This test performs that cycle in process, so the next one fails here instead of on
// someone's phone.
//
// It deliberately asserts what a READER would see — every message present, exactly once — rather
// than any internal representation.
func TestConversationSurvivesRestarts(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "e2e.db")
	const sid = "ses_e2e"

	// --- Run 1: a conversation happens. ---
	db1, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	h1 := &Hub{db: db1, sessions: map[string]*managedSession{}}
	m1 := &managedSession{hub: h1, sess: &replayFakeSess{id: sid}, ringFromStart: true}

	m1.broadcastUserEcho("refactor the parser", "phone")
	assistant := []byte(`{"type":"session.message","payload":{"session_id":"ses_e2e","role":"assistant","text":"done","msg_id":"a1"}}`)
	m1.appendDurable(sid, "a1", assistant)
	m1.broadcast(assistant)
	tool := []byte(`{"type":"session.tool","payload":{"session_id":"ses_e2e","id":"t1","status":"completed","name":"bash"}}`)
	m1.appendDurable(sid, "tool:t1", tool)
	m1.broadcast(tool)
	card := []byte(`{"type":"ui.component","payload":{"session_id":"ses_e2e","id":"plan","component":"checklist"}}`)
	m1.persistRenderable(protocol.TypeUIComponent, card)
	m1.broadcast(card)
	db1.Close()

	// --- Runs 2 and 3: the daemon restarts twice. Each time the provider re-streams its history,
	// which is how the same content reaches the ring again. Nothing may accumulate. ---
	for run := 2; run <= 3; run++ {
		db, err := store.Open(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		h := &Hub{db: db, sessions: map[string]*managedSession{}}
		// A RESTORED session: its ring starts empty and fills from the provider's re-stream.
		m := &managedSession{hub: h, sess: &replayFakeSess{id: sid}, ringFromStart: false}
		m.broadcast(assistant) // opencode-style self-replay
		m.persistRenderable(protocol.TypeUIComponent, card)
		m.broadcast(card)

		replay := m.replayFrames()
		counts, dupes := summarize(t, replay)

		if dupes > 0 {
			t.Errorf("run %d: %d duplicated frame(s) — the conversation would render twice", run, dupes)
		}
		if counts["session.message"] != 2 {
			t.Errorf("run %d: %d messages, want 2 (the user's prompt and the reply); user prompts were once dropped entirely",
				run, counts["session.message"])
		}
		if counts["session.tool"] != 1 {
			t.Errorf("run %d: %d tool cards, want 1 — a re-stream carries no tools, so this comes from the durable store",
				run, counts["session.tool"])
		}
		if counts["ui.component"] != 1 {
			t.Errorf("run %d: %d cards, want exactly 1 — cards multiplied on every restart when persisted without a stable key",
				run, counts["ui.component"])
		}
		db.Close()
	}
}

// TestRestoredEmptyRingStillReplays: the "This conversation is empty" report. A restored session's
// ring starts empty and one status frame used to be enough to hide the durable transcript entirely.
func TestRestoredEmptyRingStillReplays(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "e2e2.db")
	const sid = "ses_empty"
	db, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for i := 1; i <= 3; i++ {
		raw := []byte(`{"type":"session.message","payload":{"session_id":"ses_empty","role":"assistant","text":"hours of work"}}`)
		if _, err := db.AppendTranscript(sid, int64(i), "", raw); err != nil {
			t.Fatal(err)
		}
	}
	h := &Hub{db: db, sessions: map[string]*managedSession{}}
	m := &managedSession{hub: h, sess: &replayFakeSess{id: sid}, ringFromStart: false}
	// The single post-restart frame that used to close both doors.
	m.transcript = [][]byte{[]byte(`{"type":"session.status","payload":{"session_id":"ses_empty","status":"idle"}}`)}

	counts, _ := summarize(t, m.replayFrames())
	if counts["session.message"] != 3 {
		t.Errorf("replayed %d messages, want 3 — a restored session with one status frame must still serve its history",
			counts["session.message"])
	}
}

func summarize(t *testing.T, frames [][]byte) (counts map[string]int, dupes int) {
	t.Helper()
	counts = map[string]int{}
	seen := map[string]int{}
	for _, f := range frames {
		var h struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(f, &h) != nil {
			continue
		}
		counts[h.Type]++
		seen[string(f)]++
	}
	for _, n := range seen {
		if n > 1 {
			dupes += n - 1
		}
	}
	return counts, dupes
}
