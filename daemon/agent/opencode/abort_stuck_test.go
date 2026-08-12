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

// wedged starts a session and drives turn 1 into the wedged state (POST blocks, no idle), asserting
// nothing was aborted along the way. Shared by the two halves of the abort contract below.
func wedged(t *testing.T) (*wedgeStub, interface {
	Prompt(context.Context, string) error
	Close() error
}) {
	t.Helper()
	w := &wedgeStub{msgPosted: make(chan struct{}, 4), aborted: make(chan struct{}, 4), done: make(chan struct{})}
	srv := httptest.NewServer(w)
	ctx := context.Background()
	sess, err := New(srv.URL).Create(ctx, "/repo", "") // no auto-prompt
	if err != nil {
		close(w.done)
		srv.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close(); close(w.done); srv.Close() })

	if err := sess.Prompt(ctx, "start the merge"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-w.msgPosted:
	case <-time.After(3 * time.Second):
		t.Fatal("turn 1 never POSTed a message")
	}
	select {
	case <-w.aborted:
		t.Fatal("aborted on the FIRST turn — nothing was pending to abort")
	case <-time.After(150 * time.Millisecond):
	}
	return w, sess
}

// TestOpenCode_PromptDoesNotAbortUnfinishedTurn is the regression for killing WORKING agents: a plain
// follow-up prompt must never abort the turn in flight.
//
// This adapter used to abort whenever the prior turn hadn't reported idle — but "hasn't reported
// idle" is equally true of a healthy three-hour migration, so typing a follow-up while your agent
// worked destroyed the work, and it looked like the message had crashed the agent. Only a caller
// holding real evidence of a wedge may kill a turn; see PromptUnsticking below.
func TestOpenCode_PromptDoesNotAbortUnfinishedTurn(t *testing.T) {
	w, sess := wedged(t)

	if err := sess.Prompt(context.Background(), "continue?"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-w.aborted:
		t.Fatal("a plain follow-up prompt ABORTED the in-flight turn — that destroys working agents")
	case <-time.After(1 * time.Second):
		// good: the message queues, and opencode runs it when the turn yields
	}
}

// TestOpenCode_PromptUnstickingAbortsWedgedTurn is the other half, and the original regression for
// the "I sent continue?/status? and got nothing back" pile-up: once the turn engine has JUDGED the
// turn wedged, the unsticking send must abort it first, because opencode runs a session serially and
// the message would otherwise queue behind the hang forever.
func TestOpenCode_PromptUnstickingAbortsWedgedTurn(t *testing.T) {
	w, sess := wedged(t)

	u, ok := sess.(interface {
		PromptUnsticking(context.Context, string) error
	})
	if !ok {
		t.Fatal("opencode session no longer implements agent.Unsticker — a wedged turn can never be recovered")
	}
	if err := u.PromptUnsticking(context.Background(), "continue?"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-w.aborted:
		// good — the stuck turn was aborted so the follow-up can run
	case <-time.After(3 * time.Second):
		t.Fatal("unsticking prompt did NOT abort the wedged turn — it would queue behind it forever")
	}
}
