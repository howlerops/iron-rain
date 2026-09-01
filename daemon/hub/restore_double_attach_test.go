package hub

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/agent"
	"github.com/howlerops/oculus/daemon/agent/opencode"
)

// Restore must not re-attach a session the daemon is already bound to.
//
// RestoreSessions runs in a background goroutine (main.go) while the websocket listener is already
// accepting, so a client that reconnects and re-opens its session binds that id BEFORE restore
// reaches its record. The client handler checks h.managed() first; the restore loop checked nothing
// at all. The result was one conversation with two provider subscriptions — observed in a live log as
// the same id logging "opencode: attach" twice, and one turn logging "turn end (idle)" three times.
//
// This is the deterministic form of that race: bind the session first, then run restore.
func TestRestoreSkipsAnAlreadyLiveSession(t *testing.T) {
	dir := t.TempDir()
	st := &ocStub{knownSession: "ses_dup", dir: dir}
	srv := httptest.NewServer(st)
	defer srv.Close()

	h, db := restoreHub(t)
	saveRecord(t, db, "ses_dup", "opencode", persistedMeta{Cwd: dir, ProviderURL: srv.URL})

	// The client attach that won the race, through the same code the handler uses.
	sess, err := opencode.New(srv.URL).Attach(context.Background(), "ses_dup", dir)
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	first := h.addSession(sess, sessionMeta{cwd: dir, providerURL: srv.URL})
	hitsAfterFirst := st.hits.Load()

	defer func() { _ = sess.Close() }() // let httptest.Server.Close finish

	h.RestoreSessions(context.Background(), 7*24*time.Hour)

	if got := h.managed("ses_dup"); got != first {
		t.Fatal("restore replaced a live binding — the client's subscription was torn down under it")
	}
	if after := st.hits.Load(); after != hitsAfterFirst {
		t.Fatalf("restore re-attached a session the daemon already owned: %d extra provider request(s) — "+
			"that is a second /event subscription and a second history replay for one conversation",
			after-hitsAfterFirst)
	}
}

// Watchers must survive a rebind.
//
// Subscriptions live on the managedSession, and newManagedSession always builds an EMPTY subs map —
// so every rebind (recover, restore-after-race, take-over) stranded everyone who had the session
// open. Their app kept rendering the transcript it already had, with no error and no reconnect,
// while every new frame went to a map they were no longer in. The session simply stopped updating on
// every device except the one that caused the rebind, and nothing re-subscribes them because from
// the client's side nothing happened.
func TestRebindCarriesWatchersToTheNewBinding(t *testing.T) {
	h := &Hub{sessions: map[string]*managedSession{}}

	first := &subSess{ch: make(chan agent.Event, 4)}
	m1 := h.addSession(first, sessionMeta{ephemeral: true})

	watcher := &subscriber{conn: subscriberConnID, ch: make(chan []byte, 8), done: make(chan struct{})}
	m1.mu.Lock()
	m1.subs[subscriberConnID] = watcher
	m1.mu.Unlock()

	// Rebind the same id to a new provider session, as a recover or take-over does.
	second := &subSess{ch: make(chan agent.Event, 4)}
	m2 := h.addSession(second, sessionMeta{ephemeral: true})
	if m2 == m1 {
		t.Fatal("expected a new binding")
	}

	m2.mu.Lock()
	got := len(m2.subs)
	carried := m2.subs[subscriberConnID] == watcher
	m2.mu.Unlock()
	if got == 0 || !carried {
		t.Fatalf("the new binding has %d watcher(s) — everyone watching this session went silent "+
			"with no error and no reconnect", got)
	}

	// And the old binding must not keep delivering to them, or every frame arrives twice.
	m1.mu.Lock()
	left := len(m1.subs)
	m1.mu.Unlock()
	if left != 0 {
		t.Errorf("the previous binding still holds %d watcher(s); both pumps would deliver to them", left)
	}
}
