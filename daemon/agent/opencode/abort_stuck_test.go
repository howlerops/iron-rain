package opencode

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// wedgeStub models a session whose turn WEDGES server-side: POST /message blocks and never returns
// (like an agent bash step hung on $EDITOR), and /event never sends session.idle. It records whether
// POST /abort was called so the test can prove a follow-up prompt aborts the stuck turn first.
type wedgeStub struct {
	msgPosted chan struct{}
	aborted   chan struct{}
	done      chan struct{}
}

func (w *wedgeStub) ServeHTTP(rw http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/session":
		_ = json.NewEncoder(rw).Encode(map[string]any{"id": "ses_x", "title": "stub"})
	case r.Method == http.MethodGet && r.URL.Path == "/event":
		rw.Header().Set("Content-Type", "text/event-stream")
		rw.WriteHeader(http.StatusOK)
		if f, ok := rw.(http.Flusher); ok {
			f.Flush()
		}
		select { // never sends idle → the turn stays "pending"
		case <-r.Context().Done():
		case <-w.done:
		}
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/abort"):
		select {
		case w.aborted <- struct{}{}:
		default:
		}
		rw.WriteHeader(http.StatusOK)
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/message"):
		select {
		case w.msgPosted <- struct{}{}:
		default:
		}
		select { // the turn never completes (wedged server-side)
		case <-r.Context().Done():
		case <-w.done:
		}
	default:
		rw.WriteHeader(http.StatusOK)
	}
}

// TestOpenCode_NewPromptAbortsWedgedTurn is the regression for the "I sent continue?/status? and got
// nothing back" pile-up: when a prior turn never reached idle (wedged server-side), a new prompt must
// ABORT the stuck turn first instead of queuing behind it (opencode runs a session serially).
func TestOpenCode_NewPromptAbortsWedgedTurn(t *testing.T) {
	w := &wedgeStub{msgPosted: make(chan struct{}, 4), aborted: make(chan struct{}, 4), done: make(chan struct{})}
	srv := httptest.NewServer(w)
	defer srv.Close()
	ctx := context.Background()
	sess, err := New(srv.URL).Create(ctx, "/repo", "") // no auto-prompt
	if err != nil {
		close(w.done)
		t.Fatal(err)
	}
	defer sess.Close()
	defer close(w.done)

	// Turn 1 — wedges (POST blocks, no idle).
	if err := sess.Prompt(ctx, "start the merge"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-w.msgPosted:
	case <-time.After(3 * time.Second):
		t.Fatal("turn 1 never POSTed a message")
	}
	// No abort should have happened yet (nothing to abort).
	select {
	case <-w.aborted:
		t.Fatal("aborted on the FIRST turn — nothing was pending to abort")
	case <-time.After(150 * time.Millisecond):
	}

	// Turn 2 — the follow-up. It must abort the wedged turn 1 first.
	if err := sess.Prompt(ctx, "continue?"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-w.aborted:
		// good — the stuck turn was aborted so the follow-up can run
	case <-time.After(3 * time.Second):
		t.Fatal("follow-up prompt did NOT abort the wedged turn — it would queue behind it forever")
	}
}
