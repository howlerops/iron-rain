package opencode_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/agent/opencode"
	"github.com/howlerops/oculus/daemon/protocol"
)

// sseServer serves a fixed set of SSE frames to /event and stubs the rest of the API.
func sseServer(t *testing.T, sessionID string, frames ...string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/event":
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			for _, f := range frames {
				fmt.Fprint(w, f)
				if fl, ok := w.(http.Flusher); ok {
					fl.Flush()
				}
			}
			<-r.Context().Done()
		case r.Method == http.MethodPost && r.URL.Path == "/session":
			fmt.Fprintf(w, `{"id":%q,"directory":"/tmp"}`, sessionID)
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// message.updated is the one SSE case that decoded no sessionID and filtered on nothing, so it
// claimed EVERY session's cost, tokens and provider errors as its own.
//
// opencode partitions /event by ?directory= and nothing finer. Two sessions in one folder is not an
// edge case — it is a worktree with two lanes, a fan-out, or a sub-agent — and both were subscribed
// to the same stream. So a model outage in one lane painted an unrelated session red while the lane
// that actually failed showed nothing, and its token spend was billed to whoever happened to be
// listening.
func TestUsageAndErrorsFromAnotherSessionAreNotClaimed(t *testing.T) {
	other := `data: {"type":"message.updated","properties":{"info":{"id":"m-other","sessionID":"s2",` +
		`"role":"assistant","time":{"created":1,"completed":2},"cost":9.99,"tokens":{"input":9999,"output":9999},` +
		`"error":{"name":"APIError","data":{"message":"a failure in someone else's session"}}}}}` + "\n\n"

	srv := sseServer(t, "s1", other)
	sess, err := opencode.New(srv.URL).Create(context.Background(), "/tmp", "hi")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer sess.Close()

	settle := time.After(3 * time.Second)
	for {
		select {
		case ev, ok := <-sess.Events():
			if !ok {
				return
			}
			switch p := ev.Payload.(type) {
			case protocol.SessionUsage:
				t.Fatalf("session s1 claimed s2's usage: %+v.\n\nEvery session sharing a directory reads "+
					"the same /event stream, so this is every other session's spend landing on this one's "+
					"budget meter — and the budget stop acts on it.", p)
			case protocol.SessionStatus:
				if p.Status == protocol.StatusError {
					t.Fatalf("session s1 was painted red by s2's provider error: %q", p.Detail)
				}
			}
		case <-settle:
			return
		}
	}
}

// The filter must not go the other way: this session's OWN usage still has to arrive, or the fix
// would simply silence the budget meter for everyone.
func TestOwnUsageIsStillAttributed(t *testing.T) {
	mine := `data: {"type":"message.updated","properties":{"info":{"id":"m1","sessionID":"s1",` +
		`"role":"assistant","time":{"created":1,"completed":2},"cost":0.25,"tokens":{"input":100,"output":50}}}}` + "\n\n"

	srv := sseServer(t, "s1", mine)
	sess, err := opencode.New(srv.URL).Create(context.Background(), "/tmp", "hi")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer sess.Close()

	settle := time.After(5 * time.Second)
	for {
		select {
		case ev, ok := <-sess.Events():
			if !ok {
				t.Fatal("stream ended before this session's own usage arrived")
			}
			if u, isUsage := ev.Payload.(protocol.SessionUsage); isUsage {
				if u.SessionID != "s1" {
					t.Fatalf("usage attributed to %q, want s1", u.SessionID)
				}
				return
			}
		case <-settle:
			t.Fatal("this session's own usage never arrived — the sessionID filter is too strict, and a " +
				"budget meter that reports nothing is no better than one that reports someone else's spend")
		}
	}
}

// A `task` sub-agent's usage DOES belong here: it runs under this session's turn and its tokens are
// this session's spend. The filter accepts announced children for exactly that reason.
func TestSubAgentUsageIsAttributedToTheParent(t *testing.T) {
	child := `data: {"type":"session.created","properties":{"info":{"id":"sub1","parentID":"s1","title":"review the auth flow"}}}` + "\n\n"
	childUsage := `data: {"type":"message.updated","properties":{"info":{"id":"m-sub","sessionID":"sub1",` +
		`"role":"assistant","time":{"created":1,"completed":2},"cost":0.5,"tokens":{"input":10,"output":20}}}}` + "\n\n"

	srv := sseServer(t, "s1", child, childUsage)
	sess, err := opencode.New(srv.URL).Create(context.Background(), "/tmp", "hi")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer sess.Close()

	settle := time.After(5 * time.Second)
	for {
		select {
		case ev, ok := <-sess.Events():
			if !ok {
				t.Fatal("stream ended before the sub-agent's usage arrived")
			}
			if _, isUsage := ev.Payload.(protocol.SessionUsage); isUsage {
				return
			}
		case <-settle:
			t.Fatal("a `task` sub-agent's tokens never reached the parent's meter — they are spent under " +
				"the parent's turn and its budget, so dropping them under-reports every fan-out")
		}
	}
}
