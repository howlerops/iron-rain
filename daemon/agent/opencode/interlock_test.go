package opencode_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/agent"
	"github.com/howlerops/oculus/daemon/agent/opencode"
	"github.com/howlerops/oculus/daemon/protocol"
)

// A second send must not end the first one's turn.
//
// opencode's message POST blocks server-side for the whole turn, and the completion path was written
// as though the sender that returns is the only one in flight. It is not: the turn engine's Nudge
// and PromptUnsticking both go through sendParts, so a supervisor nudge during a long turn gives two
// concurrent POSTs. The nudge's POST returns first, and with no interlock it cleared turnPending and
// emitted StatusIdle for the turn still running — the session reported finished while its agent
// worked on, which is a lie the reconciler believes.
func TestASecondSendDoesNotEndTheRunningTurn(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	var mu sync.Mutex
	posts := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/event":
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			<-r.Context().Done()
		case r.Method == http.MethodPost && r.URL.Path == "/session":
			fmt.Fprint(w, `{"id":"s1","directory":"/tmp"}`)
		case r.Method == http.MethodPost && r.URL.Path == "/session/s1/message":
			mu.Lock()
			posts++
			first := posts == 1
			mu.Unlock()
			if first {
				<-release // the long turn: still running when the nudge's POST comes back
			}
			_, _ = w.Write([]byte(`{}`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	// Order matters: cleanups run LIFO, so unblock (registered second) runs FIRST. Without it a
	// t.Fatal below leaves the handler parked on <-release, srv.Close() waits for its handlers, and
	// the failure presents as a test-binary timeout with no message at all.
	t.Cleanup(srv.Close)
	t.Cleanup(unblock)

	sess, err := opencode.New(srv.URL).Create(context.Background(), "/tmp", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer sess.Close()

	idle := make(chan struct{}, 4)
	go func() {
		for ev := range sess.Events() {
			if st, ok := ev.Payload.(protocol.SessionStatus); ok && st.Status == protocol.StatusIdle {
				idle <- struct{}{}
			}
		}
	}()

	// The user's turn: its POST blocks until we release it.
	if err := sess.Prompt(context.Background(), "the long turn"); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	waitForPosts(t, &mu, &posts, 1)

	// The supervisor nudge, whose POST returns immediately.
	n, ok := sess.(agent.Nudger)
	if !ok {
		t.Fatal("the opencode session no longer implements agent.Nudger")
	}
	if err := n.Nudge(context.Background(), "still there?"); err != nil {
		t.Fatalf("nudge: %v", err)
	}
	waitForPosts(t, &mu, &posts, 2)

	select {
	case <-idle:
		t.Fatal("the session was declared idle while its turn was still running.\n\n" +
			"The nudge's POST returned first and claimed the ending for a turn it did not own. The " +
			"transcript seals, \"agent finished\" is pushed, and the turn engine stops supervising a " +
			"session whose agent is still working.")
	case <-time.After(1500 * time.Millisecond):
	}

	// Now let the real turn finish: THAT one ends the turn.
	unblock()
	select {
	case <-idle:
	case <-time.After(10 * time.Second):
		t.Fatal("the turn never reached idle once its own POST returned — the interlock swallowed the " +
			"ending instead of deferring it")
	}
}

func waitForPosts(t *testing.T, mu *sync.Mutex, posts *int, want int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := *posts
		mu.Unlock()
		if n >= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("only %d message POSTs arrived, want %d", *posts, want)
}
