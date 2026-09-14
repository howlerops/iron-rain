package opencode_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/agent/opencode"
)

// A retried approval must keep going to the session that RAISED it.
//
// Respond deleted the approval→session mapping before the POST that can fail. The todowrite
// auto-allow path retries three times, so once the first attempt failed the mapping was gone and
// attempts 2 and 3 were addressed to the PARENT — a session that never asked. The retry could not
// succeed by construction: the sub-agent stayed blocked server-side on a permission that was
// deliberately never shown to anyone, and the parent's `task` tool never returned.
func TestApprovalRetryStillTargetsTheSubAgent(t *testing.T) {
	var mu sync.Mutex
	var permPaths []string
	attempts := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/event":
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			flush := func() {
				if f, ok := w.(http.Flusher); ok {
					f.Flush()
				}
			}
			// A `task` sub-agent, then a todowrite permission raised under the CHILD's id. todowrite
			// is suppressed as bookkeeping, so it is auto-allowed with the retry loop — no card is
			// ever shown, which is why a silent failure here has nothing to act on.
			fmt.Fprint(w, `data: {"type":"session.created","properties":{"info":{"id":"sub1","parentID":"s1","title":"sub"}}}`+"\n\n")
			flush()
			fmt.Fprint(w, `data: {"type":"permission.updated","properties":{"id":"p1","sessionID":"sub1","permission":"todowrite"}}`+"\n\n")
			flush()
			<-r.Context().Done()
		case r.Method == http.MethodPost && r.URL.Path == "/session":
			fmt.Fprint(w, `{"id":"s1","directory":"/tmp"}`)
		case strings.Contains(r.URL.Path, "/permissions/"):
			mu.Lock()
			attempts++
			n := attempts
			permPaths = append(permPaths, r.URL.Path)
			mu.Unlock()
			if n == 1 {
				http.Error(w, "transient", http.StatusInternalServerError) // the failure that erased the mapping
				return
			}
			_, _ = w.Write([]byte(`{}`))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()

	sess, err := opencode.New(srv.URL).Create(context.Background(), "/tmp", "hi")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer sess.Close()

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		done := attempts >= 2
		mu.Unlock()
		if done {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(permPaths) < 2 {
		t.Fatalf("the auto-allow retry never ran (paths=%v); this test proves nothing without it", permPaths)
	}
	for i, p := range permPaths {
		if !strings.Contains(p, "/session/sub1/permissions/") {
			t.Errorf("attempt %d posted to %q, want the sub-agent's path /session/sub1/permissions/p1.\n\n"+
				"The mapping that records which session raised the approval was deleted before the POST, "+
				"so every retry after a failure is addressed to the parent. The sub-agent stays blocked "+
				"server-side on a permission nobody was shown, and the parent's `task` tool never returns.",
				i+1, p)
		}
	}
}

// A delete the server refused must not be reported as a delete that happened.
func TestDeleteSurfacesANonSuccessStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/event":
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			if f, ok := w.(http.Flusher); ok {
				f.Flush() // the client waits on the headers; without this the stream GET just times out
			}
			<-r.Context().Done()
		case r.Method == http.MethodPost && r.URL.Path == "/session":
			fmt.Fprint(w, `{"id":"s1","directory":"/tmp"}`)
		case r.Method == http.MethodDelete:
			http.Error(w, "nope", http.StatusInternalServerError)
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	defer srv.Close()

	sess, err := opencode.New(srv.URL).Create(context.Background(), "/tmp", "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer sess.Close()

	d, ok := sess.(interface{ Delete(context.Context) error })
	if !ok {
		t.Fatal("the opencode session no longer implements agent.Deleter")
	}
	if err := d.Delete(context.Background()); err == nil {
		t.Error("a 500 from DELETE /session was reported as a successful delete.\n\n" +
			"The status was never read, so the hub's \"server-side delete failed\" log could not fire " +
			"and the session the user deleted reappears on the next attach with nothing to explain it.")
	}
}
