package hub

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/agent"
	"github.com/howlerops/oculus/daemon/store"
)

// A scratch session must stay unpersisted however it is touched.
//
// addSession deliberately refuses to write a record for an ephemeral session — a "just chat" or the
// fan-out judge — but persistSessionAt had no such guard, and both the periodic TTL touch and
// setSessionMode call it directly. The record addSession declined to write got written anyway on the
// next touch.
//
// That is not cosmetic. The prune's whole invariant is "no sessions row means the transcript rows
// are orphans", so a scratch session that acquires a row keeps its rows alive forever — and it then
// appears in the session list after a restart, which is the one place these are meant never to be.
func TestAnEphemeralSessionIsNeverPersisted(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	h := &Hub{db: db, sessions: map[string]*managedSession{}}
	m := newManagedSession(h, &forkSess{ch: make(chan agent.Event, 4), id: "scratch"}, sessionMeta{
		ephemeral: true, label: "fan-out judge",
	})

	// Both direct callers: the periodic TTL touch, and a mode change.
	h.persistSessionAt(m, time.Now().Unix())
	h.persistSession(m)

	recs, err := db.Sessions()
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	for _, r := range recs {
		if r.ID == "scratch" {
			t.Fatalf("an ephemeral session was persisted: %+v.\n\n"+
				"addSession refused to write this record; the periodic touch wrote it anyway. Its "+
				"transcript rows are now permanently non-orphaned, so the TTL prune can never reclaim "+
				"them, and the session reappears in the list on the next restart.", r)
		}
	}

	// The guard must not swallow ordinary sessions.
	real := newManagedSession(h, &forkSess{ch: make(chan agent.Event, 4), id: "normal"}, sessionMeta{cwd: "/tmp"})
	h.persistSession(real)
	recs, err = db.Sessions()
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	found := false
	for _, r := range recs {
		if r.ID == "normal" {
			found = true
		}
	}
	if !found {
		t.Fatal("an ordinary session was not persisted — the ephemeral guard is too broad, and a " +
			"session that cannot be persisted does not survive a restart")
	}
}
