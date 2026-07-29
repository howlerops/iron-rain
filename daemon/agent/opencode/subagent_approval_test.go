package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/protocol"
)

// subAgentApprovalStub raises a permission under a CHILD (`task` sub-agent) session id. The bug it
// guards: the daemon used to DROP any permission whose sessionID != the parent, so a fanned-out
// sub-agent that needed approval blocked forever server-side, the parent's task tool never returned,
// and the session was wedged with no restart able to clear it. The stub records which session path an
// approval answer is POSTed to, so the test can prove the answer reaches the CHILD, not the parent.
type subAgentApprovalStub struct {
	permPath chan string   // the /session/{sid}/permissions/{id} path that received the answer
	done     chan struct{} // closed by the test to release hanging handlers so srv.Close() finishes
}

func (s *subAgentApprovalStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/session":
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "ses_parent", "title": "stub"})

	case r.Method == http.MethodGet && r.URL.Path == "/event":
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)
		flush := func(s string) {
			fmt.Fprintf(w, "data: %s\n\n", s)
			if fl != nil {
				fl.Flush()
			}
		}
		// A sub-agent is spawned (registers ses_child under childIDs), then IT asks for permission.
		flush(`{"type":"session.created","properties":{"info":{"id":"ses_child","parentID":"ses_parent","title":"explore"}}}`)
		flush(`{"type":"permission.asked","properties":{"id":"perm1","sessionID":"ses_child","permission":"bash","patterns":["rm -rf build"]}}`)
		select {
		case <-r.Context().Done():
		case <-s.done:
		}

	case strings.Contains(r.URL.Path, "/permissions/"):
		// An approval answer landed — report the exact path so the test can check the session id.
		select {
		case s.permPath <- r.URL.Path:
		default:
		}
		w.WriteHeader(http.StatusOK)

	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/message"):
		select {
		case <-r.Context().Done():
		case <-s.done:
		}

	default:
		w.WriteHeader(http.StatusOK)
	}
}

// TestOpenCode_SubAgentApprovalRoutesToChild is the regression for the "multi-layered opencode agents
// get stuck; restart doesn't help" bug: a sub-agent permission must (a) be SURFACED (not dropped) and
// (b) be ANSWERED against the sub-agent's own session path — otherwise the sub-agent blocks forever.
func TestOpenCode_SubAgentApprovalRoutesToChild(t *testing.T) {
	stub := &subAgentApprovalStub{permPath: make(chan string, 1), done: make(chan struct{})}
	srv := httptest.NewServer(stub)
	defer srv.Close()
	ctx := context.Background()
	sess, err := New(srv.URL).Create(ctx, "/repo", "go")
	if err != nil {
		close(stub.done)
		t.Fatal(err)
	}
	defer sess.Close()
	defer close(stub.done)

	// (a) The sub-agent's approval must be surfaced, not silently dropped.
	var approvalID string
	deadline := time.After(4 * time.Second)
	for approvalID == "" {
		select {
		case ev := <-sess.Events():
			if ev.Type == protocol.TypeApprovalRequest {
				ar := ev.Payload.(protocol.ApprovalRequest)
				approvalID = ar.ApprovalID
				if !strings.Contains(ar.Detail, "sub-agent") {
					t.Errorf("expected sub-agent approval to be tagged, got detail=%q", ar.Detail)
				}
			}
		case <-deadline:
			t.Fatal("sub-agent permission was never surfaced as an approval (it was dropped)")
		}
	}

	// (b) Answering it must POST to the CHILD's session path, or the sub-agent stays blocked.
	if err := sess.Respond(ctx, approvalID, protocol.DecisionAllow); err != nil {
		t.Fatal(err)
	}
	select {
	case path := <-stub.permPath:
		if !strings.Contains(path, "/session/ses_child/permissions/") {
			t.Fatalf("approval answered against the WRONG session: %s (want /session/ses_child/permissions/)", path)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("approval answer never reached the server")
	}
}
