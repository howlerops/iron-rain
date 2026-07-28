package hub_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/crypto"
	"github.com/howlerops/oculus/daemon/hub"
	"github.com/howlerops/oculus/daemon/protocol"
	"github.com/howlerops/oculus/daemon/store"
)

// TestDeleteStoppedSessionRemovesRecord is the regression for "I delete a session and it keeps
// coming back." A session that couldn't re-attach after a restart is STOPPED/restartable — not a
// live managed session — so the old session.stop handler (which only touched live sessions) left its
// durable record in place, and it reappeared on every session.list. Deleting a stopped session must
// drop the record for good.
func TestDeleteStoppedSessionRemovesRecord(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")

	// Daemon #1: create a session; it's persisted.
	db1, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	h1 := hub.New()
	h1.SetStore(db1)
	h1.Register(&cwdProvider{})
	daemonKP, _ := crypto.GenerateKeyPair()
	conn1 := connectClient(t, h1, daemonKP)
	r1 := newReader(conn1)
	send(t, conn1, "c1", protocol.TypeSessionCreate, protocol.SessionCreate{Provider: "fake", Cwd: t.TempDir()})
	var created protocol.Session
	_ = json.Unmarshal(r1.waitOK(t, "c1"), &created)
	db1.Close() // commit + "shut down"

	// Daemon #2 ("restart"): cwdProvider isn't an Attacher, so the session restores as STOPPED.
	db2, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	h2 := hub.New()
	h2.SetStore(db2)
	h2.Register(&cwdProvider{})
	h2.RestoreSessions(context.Background(), 7*24*time.Hour)
	conn2 := connectClient(t, h2, daemonKP)
	r2 := newReader(conn2)

	// It shows up as a stopped/restartable session.
	send(t, conn2, "l1", protocol.TypeSessionList, struct{}{})
	var before protocol.SessionList
	_ = json.Unmarshal(r2.waitOK(t, "l1"), &before)
	found := false
	for _, s := range before.Sessions {
		if s.ID == created.ID {
			found = true
			if s.Status != protocol.StatusStopped {
				t.Fatalf("expected stopped, got %q", s.Status)
			}
		}
	}
	if !found {
		t.Fatal("stopped session missing from the list")
	}

	// Delete it.
	send(t, conn2, "d1", protocol.TypeSessionStop, protocol.SessionRef{SessionID: created.ID})
	r2.waitOK(t, "d1")

	// It must be gone from the list AND from the durable store — permanently.
	send(t, conn2, "l2", protocol.TypeSessionList, struct{}{})
	var after protocol.SessionList
	_ = json.Unmarshal(r2.waitOK(t, "l2"), &after)
	for _, s := range after.Sessions {
		if s.ID == created.ID {
			t.Fatal("deleted stopped session is STILL in the session list")
		}
	}
	recs, _ := db2.Sessions()
	for _, rec := range recs {
		if rec.ID == created.ID {
			t.Fatal("deleted stopped session is STILL in the durable store — it will come back")
		}
	}
}
