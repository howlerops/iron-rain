package hub

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

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
