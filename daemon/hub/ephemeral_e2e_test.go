package hub_test

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/howlerops/oculus/daemon/crypto"
	"github.com/howlerops/oculus/daemon/hub"
	"github.com/howlerops/oculus/daemon/protocol"
	"github.com/howlerops/oculus/daemon/store"
)

// TestEphemeralSessionNotPersisted proves the "just chat" harness: an ephemeral session opens and
// works like any other, is flagged Ephemeral, but is NOT written to the durable store (so it
// vanishes on restart and never clutters the saved session list) — while a normal session IS.
func TestEphemeralSessionNotPersisted(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	h := hub.New()
	h.SetStore(db)
	h.Register(&cwdProvider{})
	daemonKP, _ := crypto.GenerateKeyPair()
	conn := connectClient(t, h, daemonKP)
	r := newReader(conn)

	// Ephemeral "just chat" session — no project.
	send(t, conn, "e1", protocol.TypeSessionCreate, protocol.SessionCreate{Provider: "fake", Ephemeral: true})
	var chat protocol.Session
	if err := json.Unmarshal(r.waitOK(t, "e1"), &chat); err != nil {
		t.Fatal(err)
	}
	if !chat.Ephemeral {
		t.Error("session should be flagged Ephemeral")
	}
	if chat.Name != "Chat" {
		t.Errorf("ephemeral default label = %q, want Chat", chat.Name)
	}

	// A normal session for contrast.
	send(t, conn, "n1", protocol.TypeSessionCreate, protocol.SessionCreate{Provider: "fake", Cwd: t.TempDir()})
	var normal protocol.Session
	_ = json.Unmarshal(r.waitOK(t, "n1"), &normal)

	// The durable store must hold ONLY the normal session — the ephemeral one isn't persisted.
	recs, err := db.Sessions()
	if err != nil {
		t.Fatal(err)
	}
	for _, rec := range recs {
		if rec.ID == chat.ID {
			t.Fatal("ephemeral session was persisted to the store; it must not be")
		}
	}
	foundNormal := false
	for _, rec := range recs {
		if rec.ID == normal.ID {
			foundNormal = true
		}
	}
	if !foundNormal {
		t.Error("the normal session should be persisted")
	}
}
