package hub

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/howlerops/oculus/daemon/agent"
	"github.com/howlerops/oculus/daemon/protocol"
	"github.com/howlerops/oculus/daemon/store"
)

// The user's half of the conversation must be persisted whether or not the daemon knows who sent it.
//
// Persisting it and echoing it were one function, called only when the author was known — so a
// client that had not identified itself had its prompts recorded nowhere. Restoring a pi or CLI
// session then showed answers to questions that had vanished, which is precisely the symptom the
// durable user-half was added to fix, still present for anyone the daemon could not name.
//
// The echo stays gated: sending an unattributed copy back renders the message twice on the device
// that just typed it.
func TestAnUnattributedPromptIsStillPersisted(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()
	h := &Hub{db: db, sessions: map[string]*managedSession{}}

	m := newManagedSession(h, &forkSess{ch: make(chan agent.Event, 4), id: "anon"}, sessionMeta{})
	sub := &subscriber{ch: make(chan []byte, 32), done: make(chan struct{})}
	m.mu.Lock()
	m.subs[subscriberConnID] = sub
	m.mu.Unlock()

	const prompt = "refactor the parser"
	m.recordUserMessage(prompt, "", false) // an unidentified client: author unknown, no echo

	if !historyHasUserText(m, prompt) {
		t.Fatal("an unattributed prompt was not persisted.\n\n" +
			"Persistence was gated on the same condition as the echo, so a client the daemon could not " +
			"name had its questions dropped — and a restored pi or CLI session shows the answers with " +
			"nothing they are answering.")
	}
	if got := drainAll(sub.ch); len(got) != 0 {
		t.Errorf("an unattributed prompt was echoed to subscribers (%d frames) — the device that sent it "+
			"renders its own message a second time", len(got))
	}

	// An attributed one is persisted AND echoed, so a second device can show who sent it.
	const named = "and add a test"
	m.recordUserMessage(named, "phone", true)
	if !historyHasUserText(m, named) {
		t.Error("an attributed prompt was not persisted")
	}
	if got := drainAll(sub.ch); len(got) != 1 {
		t.Errorf("an attributed prompt produced %d echo frames, want 1", len(got))
	}
}

func historyHasUserText(m *managedSession, want string) bool {
	for _, raw := range m.fullHistory() {
		var f struct {
			Type    string `json:"type"`
			Payload struct {
				Role string `json:"role"`
				Text string `json:"text"`
			} `json:"payload"`
		}
		if json.Unmarshal(raw, &f) != nil {
			continue
		}
		if f.Type == protocol.TypeSessionMessage && f.Payload.Role == "user" && strings.Contains(f.Payload.Text, want) {
			return true
		}
	}
	return false
}
