package hub

import (
	"encoding/json"
	"path/filepath"
	"sync"
	"testing"

	"github.com/howlerops/oculus/daemon/protocol"
	"github.com/howlerops/oculus/daemon/store"
)

func sourceHub(t *testing.T) *Hub {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return &Hub{db: db, sessions: map[string]*managedSession{}}
}

func durableTypes(t *testing.T, h *Hub, sid string) []string {
	t.Helper()
	rows, err := h.db.Transcript(sid)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, r := range rows {
		var f struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(r, &f)
		out = append(out, f.Type)
	}
	return out
}

// TestUserPromptSurvivesRestart is the hole that made restart recovery lossy: the durable transcript
// stored the AGENT's half of the conversation and not the user's. Reopening a restarted pi or CLI
// session showed answers to questions that had vanished.
func TestUserPromptSurvivesRestart(t *testing.T) {
	h := sourceHub(t)
	m := &managedSession{hub: h, sess: &replayFakeSess{id: "s1"}}

	m.broadcastUserEcho("please refactor the parser", "phone")

	got := durableTypes(t, h, "s1")
	if len(got) != 1 || got[0] != protocol.TypeSessionMessage {
		t.Fatalf("durable rows = %v, want the user's own message persisted", got)
	}
	rows, _ := h.db.Transcript("s1")
	var msg struct {
		Payload struct {
			Role string `json:"role"`
			Text string `json:"text"`
		} `json:"payload"`
	}
	_ = json.Unmarshal(rows[0], &msg)
	if msg.Payload.Role != "user" || msg.Payload.Text != "please refactor the parser" {
		t.Errorf("persisted %+v, want the user's prompt verbatim", msg.Payload)
	}
}

// Generative-UI cards and sub-agent rows render as real transcript content, so a restart that drops
// them leaves visible holes in the conversation.
func TestRenderableFramesArePersisted(t *testing.T) {
	h := sourceHub(t)
	m := &managedSession{hub: h, sess: &replayFakeSess{id: "s2"}}

	m.persistRenderable(protocol.TypeUIComponent, []byte(`{"type":"ui.component","payload":{"session_id":"s2","id":"c1"}}`))
	m.persistRenderable(protocol.TypeSessionSubAgent, []byte(`{"type":"session.subagent","payload":{"parent_id":"s2","id":"k1"}}`))

	got := durableTypes(t, h, "s2")
	if len(got) != 2 {
		t.Fatalf("durable rows = %v, want the UI card and the sub-agent row kept", got)
	}
}

// TestSyntheticEndOfTurnIsBroadcast: the daemon used to WRITE the end-of-turn assistant message to
// SQLite without broadcasting it, so it existed in one source and not the other — and merging the two
// rendered every reply twice. Broadcasting it makes the sources agree and removes the need for the
// text-matching special case that papered over it.
func TestSyntheticEndOfTurnIsBroadcast(t *testing.T) {
	h := sourceHub(t)
	m := &managedSession{hub: h, sess: &replayFakeSess{id: "s3"}}
	m.asstAccum.WriteString("the answer")

	m.finalizeTurnTranscript()

	m.mu.Lock()
	ring := len(m.transcript)
	m.mu.Unlock()
	if ring != 1 {
		t.Fatalf("ring holds %d frames, want the synthetic reply broadcast like every other frame", ring)
	}
	if got := durableTypes(t, h, "s3"); len(got) != 1 {
		t.Fatalf("durable rows = %v, want the reply persisted exactly once", got)
	}
}

// The durable sequence is written from the provider pump AND from the hub goroutine (user echoes), so
// it must be safe under concurrency. Run with -race.
func TestDurableSequenceIsRaceSafe(t *testing.T) {
	h := sourceHub(t)
	m := &managedSession{hub: h, sess: &replayFakeSess{id: "s4"}}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			m.broadcastUserEcho("msg", "dev")
		}(i)
	}
	wg.Wait()
	if rows, _ := h.db.Transcript("s4"); len(rows) != 20 {
		t.Errorf("stored %d rows, want all 20 concurrent appends", len(rows))
	}
}
