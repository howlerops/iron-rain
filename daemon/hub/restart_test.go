package hub_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/crypto"
	"github.com/howlerops/oculus/daemon/hub"
	"github.com/howlerops/oculus/daemon/protocol"
	"github.com/howlerops/oculus/daemon/store"
)

// TestRestartStoppedSession: a session started by the app whose provider can't re-attach after a
// daemon restart (a CLI-style provider that isn't an Attacher) is KEPT as a "stopped" session —
// not silently deleted — and can be restarted into a fresh live session in the same context.
func TestRestartStoppedSession(t *testing.T) {
	dbPath := t.TempDir() + "/oculus.db"
	daemonKP, _ := crypto.GenerateKeyPair()

	// --- Daemon #1: create a session; addSession persists a durable record.
	db1, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	h1 := hub.New()
	h1.SetStore(db1)
	h1.Register(&cwdProvider{})
	conn1 := connectClient(t, h1, daemonKP)
	r1 := newReader(conn1)

	send(t, conn1, "c1", protocol.TypeSessionCreate, protocol.SessionCreate{Provider: "fake", Cwd: t.TempDir()})
	var created protocol.Session
	if err := json.Unmarshal(r1.waitOK(t, "c1"), &created); err != nil {
		t.Fatal(err)
	}
	oldID := created.ID
	if oldID == "" {
		t.Fatal("create returned no session id")
	}
	db1.Close() // simulate daemon shutdown (record is committed on the persist above)

	// --- Daemon #2 (a "restart"): same store, provider is NOT an Attacher → session is stopped.
	db2, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	h2 := hub.New()
	h2.SetStore(db2)
	h2.Register(&cwdProvider{})
	h2.RestoreSessions(context.Background(), 7*24*time.Hour)

	conn2 := connectClient(t, h2, daemonKP)
	r2 := newReader(conn2)

	// It lists as stopped + restartable (rather than having vanished).
	send(t, conn2, "l1", protocol.TypeSessionList, struct{}{})
	var list protocol.SessionList
	if err := json.Unmarshal(r2.waitOK(t, "l1"), &list); err != nil {
		t.Fatal(err)
	}
	var found *protocol.Session
	for i := range list.Sessions {
		if list.Sessions[i].ID == oldID {
			found = &list.Sessions[i]
		}
	}
	if found == nil {
		t.Fatalf("stopped session %s missing from list %+v", oldID, list.Sessions)
	}
	if found.Status != protocol.StatusStopped || !found.Restartable {
		t.Fatalf("want stopped+restartable, got status=%q restartable=%v", found.Status, found.Restartable)
	}

	// Restart → a NEW live session; the old stopped id is gone.
	send(t, conn2, "rs1", protocol.TypeSessionRestart, protocol.SessionRef{SessionID: oldID})
	var revived protocol.Session
	if err := json.Unmarshal(r2.waitOK(t, "rs1"), &revived); err != nil {
		t.Fatal(err)
	}
	if revived.ID == "" || revived.Status == protocol.StatusStopped {
		t.Fatalf("restart should return a live session, got %+v", revived)
	}

	send(t, conn2, "l2", protocol.TypeSessionList, struct{}{})
	var list2 protocol.SessionList
	if err := json.Unmarshal(r2.waitOK(t, "l2"), &list2); err != nil {
		t.Fatal(err)
	}
	// No stopped entry remains for the old id, and the revived session is live in the list.
	// (A real provider mints a new id on restart; the test's fake reuses one, which is also fine —
	// the record is upserted in place.)
	present := false
	for _, s := range list2.Sessions {
		if s.ID == oldID && s.Status == protocol.StatusStopped {
			t.Fatalf("session still stopped after restart: %+v", s)
		}
		if s.ID == revived.ID {
			present = true
		}
	}
	if !present {
		t.Fatalf("revived session %s not present in list after restart", revived.ID)
	}
}
