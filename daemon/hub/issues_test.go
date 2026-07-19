package hub_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/crypto"
	"github.com/howlerops/oculus/daemon/hub"
	"github.com/howlerops/oculus/daemon/issues"
	"github.com/howlerops/oculus/daemon/project"
	"github.com/howlerops/oculus/daemon/protocol"
)

// fakeIssueProvider: one assigned issue; records write-back calls.
type fakeIssueProvider struct {
	mu                    sync.Mutex
	transitions, comments int
}

func (f *fakeIssueProvider) Name() string { return "linear" }
func (f *fakeIssueProvider) ListAssigned(context.Context) ([]issues.Issue, error) {
	return []issues.Issue{{
		ID: "i1", Key: "ENG-1", Title: "Fix the bug", Body: "details",
		BranchName: "eng-1-fix", TeamID: "team1", Provider: "linear", Category: "todo",
	}}, nil
}
func (f *fakeIssueProvider) WorkflowStates(context.Context, string) ([]issues.State, error) {
	return []issues.State{{ID: "s2", Name: "In Progress", Category: "in_progress"}}, nil
}
func (f *fakeIssueProvider) Comment(context.Context, string, string) error {
	f.mu.Lock()
	f.comments++
	f.mu.Unlock()
	return nil
}
func (f *fakeIssueProvider) Transition(context.Context, string, string) error {
	f.mu.Lock()
	f.transitions++
	f.mu.Unlock()
	return nil
}
func (f *fakeIssueProvider) Detail(context.Context, string) (issues.Issue, []issues.Comment, error) {
	return issues.Issue{ID: "i1", Key: "ENG-1", Title: "Fix the bug", Provider: "linear"},
		[]issues.Comment{{ID: "c1", Author: "jacob", Body: "hi"}}, nil
}
func (f *fakeIssueProvider) Update(_ context.Context, _ string, _ issues.UpdateFields) (issues.Issue, error) {
	return issues.Issue{ID: "i1", Key: "ENG-1", Title: "Fix the bug", Provider: "linear"}, nil
}
func (f *fakeIssueProvider) EditComment(context.Context, string, string) error { return nil }
func (f *fakeIssueProvider) FetchImage(context.Context, string) (string, []byte, error) {
	return "image/png", []byte{0x89, 0x50}, nil
}

// TestIssueLaunch: list assigned issues, then launch an agent on one — it runs in a
// worktree on the issue's branch, links the ticket, and writes back (transition + comment).
func TestIssueLaunch(t *testing.T) {
	repo := t.TempDir()
	gitInitRepo(t, repo)
	reg, _ := project.Load(t.TempDir() + "/p.json")
	prov := &cwdProvider{}
	h := hub.New()
	h.Register(prov)
	h.SetProjects(reg)
	h.SetWorktreeBase(t.TempDir())

	fake := &fakeIssueProvider{}
	mgr := issues.NewManager("", h.BroadcastIssues)
	mgr.AddProvider("linear", fake)
	_ = mgr.Refresh(context.Background())
	h.SetIssues(mgr)

	daemonKP, _ := crypto.GenerateKeyPair()
	conn := connectClient(t, h, daemonKP)
	r := newReader(conn)

	send(t, conn, "p1", protocol.TypeProjectAdd, protocol.ProjectAdd{Path: repo})
	var proj protocol.Project
	_ = json.Unmarshal(r.waitOK(t, "p1"), &proj)

	// issue.list returns the assigned issue.
	send(t, conn, "il", protocol.TypeIssueList, struct{}{})
	var list protocol.IssueList
	_ = json.Unmarshal(r.waitOK(t, "il"), &list)
	if len(list.Issues) != 1 || list.Issues[0].Key != "ENG-1" || list.Issues[0].BranchName != "eng-1-fix" {
		t.Fatalf("issue.list = %+v", list.Issues)
	}

	// issue.launch runs an agent in a worktree on the issue's branch, linked to the ticket.
	send(t, conn, "lx", protocol.TypeIssueLaunch, protocol.IssueLaunch{
		IssueID: "i1", Provider: "linear", ProjectID: proj.ID, AgentProvider: "fake", Worktree: true,
	})
	var sess protocol.Session
	if err := json.Unmarshal(r.waitOK(t, "lx"), &sess); err != nil {
		t.Fatal(err)
	}
	if sess.IssueKey != "ENG-1" || sess.Branch != "oculus/eng-1-fix" {
		t.Fatalf("launched session = %+v (want issue ENG-1 on oculus/eng-1-fix)", sess)
	}
	if got := prov.cwd(); got == "" || got == repo {
		t.Fatalf("agent cwd = %q, expected a worktree", got)
	}

	// Write-back (transition + comment) happens async.
	deadline := time.After(2 * time.Second)
	for {
		fake.mu.Lock()
		tr, cm := fake.transitions, fake.comments
		fake.mu.Unlock()
		if tr >= 1 && cm >= 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("write-back not performed: transitions=%d comments=%d", tr, cm)
		case <-time.After(10 * time.Millisecond):
		}
	}
}
