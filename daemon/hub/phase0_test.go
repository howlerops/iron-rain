package hub

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/howlerops/oculus/daemon/protocol"
	"github.com/howlerops/oculus/daemon/store"
)

// TestPromptlessSessionSeedsIdle: a session created WITHOUT a prompt has nothing in flight, so it
// must not present as running. It used to inherit an unseeded status and the sidebar showed "Live"
// over an empty conversation — the UI's first lie of the session.
func TestPromptlessSessionSeedsIdle(t *testing.T) {
	m := &managedSession{}
	m.seedStatus(protocol.StatusIdle)
	m.mu.Lock()
	got, ended := m.lastStatus, m.turnEnded
	m.mu.Unlock()
	if got != protocol.StatusIdle {
		t.Errorf("status = %q, want idle — nothing is in flight", got)
	}
	if !ended {
		t.Error("a promptless session has no open turn, so the turn must read as ended")
	}
}

// TestReplayExcludesUsage: session.usage ACCUMULATES in the client's handler, so replaying it on
// every open inflates the cost meter without bound. The authoritative totals ride on the OK reply to
// session.info, not on the transcript.
func TestReplayExcludesUsage(t *testing.T) {
	usage := []byte(`{"type":"session.usage","payload":{"session_id":"s","input_tokens":10}}`)
	msg := []byte(`{"type":"session.message","payload":{"session_id":"s","text":"hi"}}`)
	out := stripNonReplayable([][]byte{usage, msg, usage})
	if len(out) != 1 {
		t.Fatalf("replay kept %d frames, want only the message — usage double-counts on apply", len(out))
	}
	var f struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(out[0], &f)
	if f.Type != protocol.TypeSessionMessage {
		t.Errorf("kept %q, want the message", f.Type)
	}
}

// TestAttachPersistsCwdAndURL is the data half of surviving a daemon restart.
//
// session.attach persisted an EMPTY sessionMeta, so a taken-over session recorded neither the
// directory it belongs to nor the provider URL it came from. After a restart the daemon could not
// reconstruct the attach, and the session reopened empty or pointed at the wrong project — the shape
// of a reported "I lost all the work in that session".
func TestAttachPersistsCwdAndURL(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	h := &Hub{db: db, sessions: map[string]*managedSession{}}

	meta := sessionMeta{cwd: "/Users/x/project", providerURL: "http://127.0.0.1:4096"}
	m := &managedSession{hub: h, sess: &replayFakeSess{id: "ses_attached"}, meta: meta}
	h.persistSession(m)

	recs, err := db.Sessions()
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, r := range recs {
		if r.ID != "ses_attached" {
			continue
		}
		found = true
		if r.Cwd != "/Users/x/project" {
			t.Errorf("persisted cwd = %q, want the attached directory", r.Cwd)
		}
		var pm persistedMeta
		if err := json.Unmarshal([]byte(r.Meta), &pm); err != nil {
			t.Fatalf("meta: %v", err)
		}
		if pm.ProviderURL != "http://127.0.0.1:4096" {
			t.Errorf("persisted provider URL = %q, want the server we attached to — without it the restore cannot find the session again", pm.ProviderURL)
		}
	}
	if !found {
		t.Fatal("attached session was not persisted at all")
	}
}

// TestApprovalResolvedEntersSessionHistory: the approval REQUEST is part of the transcript, so its
// answer must be too. Announcing the resolution hub-wide only reaches clients connected at that
// moment — a phone opening the session afterwards replayed the question without the answer and
// resurrected a modal for a decision already made.
func TestApprovalResolvedEntersSessionHistory(t *testing.T) {
	m := &managedSession{sess: &replayFakeSess{id: "s1"}}
	raw := []byte(`{"type":"approval.resolved","payload":{"approval_id":"a1","decision":"allow"}}`)
	m.broadcast(raw)

	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.transcript) != 1 {
		t.Fatalf("ring holds %d frames, want the resolution recorded as history", len(m.transcript))
	}
	var f struct {
		Type string `json:"type"`
	}
	_ = json.Unmarshal(m.transcript[0], &f)
	if f.Type != protocol.TypeApprovalResolved {
		t.Errorf("recorded %q, want approval.resolved", f.Type)
	}
}
