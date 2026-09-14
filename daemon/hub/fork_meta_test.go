package hub

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/howlerops/oculus/daemon/agent"
	"github.com/howlerops/oculus/daemon/protocol"
	"github.com/howlerops/oculus/daemon/store"
	"github.com/howlerops/oculus/daemon/worktree"
)

// forkSess is a session that knows its own id — subSess hardcodes one.
type forkSess struct {
	ch chan agent.Event
	id string
}

func (s *forkSess) ID() string                                    { return s.id }
func (s *forkSess) Provider() string                              { return "fake" }
func (s *forkSess) Events() <-chan agent.Event                    { return s.ch }
func (s *forkSess) Prompt(context.Context, string) error          { return nil }
func (s *forkSess) Respond(context.Context, string, string) error { return nil }
func (s *forkSess) Stop(context.Context) error                    { return nil }
func (s *forkSess) Close() error                                  { return nil }

// attachProvider hands back a session for any id, which is all adoptForkedSession needs.
type attachProvider struct{ last agent.Session }

func (p *attachProvider) Name() string                                     { return "fake" }
func (p *attachProvider) List(context.Context) ([]protocol.Session, error) { return nil, nil }
func (p *attachProvider) Create(context.Context, string, string) (agent.Session, error) {
	return nil, context.Canceled
}
func (p *attachProvider) Attach(_ context.Context, id, _ string) (agent.Session, error) {
	s := &forkSess{ch: make(chan agent.Event, 8), id: id}
	p.last = s
	return s, nil
}

// A fork inherits its parent's PLACE, not its parent's property.
//
// adoptForkedSession copied the parent's entire sessionMeta while its comment claimed it reused
// "project/cwd". The extra fields are live ownership claims: worktreePath/repoRoot say this session
// owns that checkout, so resolving a fan-out tore down the KEPT winner's worktree on behalf of a
// fork that merely descended from it; port says it owns that allocation, so retiring the fork freed
// a port the parent was still serving on; fanoutGroup enrolled the fork as a lane in a competition
// it was never part of; and issueID pointed a second session at the same ticket, which is what
// writes results back to the tracker.
func TestAForkDoesNotInheritTheParentsWorktreeOrGroup(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer db.Close()

	prov := &attachProvider{}
	h := &Hub{db: db, sessions: map[string]*managedSession{}, providers: map[string]agent.Provider{"fake": prov}}

	parentMeta := sessionMeta{
		projectID:     "proj",
		cwd:           "/work/repo",
		workspaceName: "repo",
		branch:        "feature",
		worktreePath:  "/work/wt-parent",
		repoRoot:      "/work/repo",
		baseCommit:    "abc123",
		port:          4096,
		fanoutGroup:   "race-1",
		issueID:       "ISSUE-1",
		issueKey:      "ENG-42",
		issueProvider: "linear",
		members:       []worktree.Member{{Name: "api", Path: "/work/layout/api"}},
	}
	parent := newManagedSession(h, &forkSess{ch: make(chan agent.Event, 8), id: "parent"}, parentMeta)
	h.mu.Lock()
	h.sessions["parent"] = parent
	h.mu.Unlock()

	if err := h.adoptForkedSession(context.Background(), parent, "forked"); err != nil {
		t.Fatalf("adopt: %v", err)
	}

	h.mu.Lock()
	fork := h.sessions["forked"]
	h.mu.Unlock()
	if fork == nil {
		t.Fatal("the fork was never bound")
	}
	fork.mu.Lock()
	got := fork.meta
	fork.mu.Unlock()

	// What "lands in the same place" actually means.
	if got.cwd != parentMeta.cwd || got.projectID != parentMeta.projectID {
		t.Errorf("the fork lost the parent's place: cwd=%q project=%q", got.cwd, got.projectID)
	}
	// What it must not claim.
	if got.worktreePath != "" {
		t.Errorf("fork inherited worktreePath=%q — resolving a fan-out now tears down the kept "+
			"winner's checkout on behalf of a session that only descended from it", got.worktreePath)
	}
	if got.repoRoot != "" || got.baseCommit != "" {
		t.Errorf("fork inherited repoRoot=%q baseCommit=%q, the other half of the worktree claim",
			got.repoRoot, got.baseCommit)
	}
	if got.port != 0 {
		t.Errorf("fork inherited port=%d — retiring the fork frees a port the parent is still serving on", got.port)
	}
	if got.fanoutGroup != "" {
		t.Errorf("fork inherited fanoutGroup=%q — it is now a lane in a race it never entered", got.fanoutGroup)
	}
	if got.issueID != "" || got.issueKey != "" {
		t.Errorf("fork inherited the ticket (%q/%q) — two sessions now write results back to it",
			got.issueID, got.issueKey)
	}
	if len(got.members) != 0 {
		t.Errorf("fork inherited %d workspace members — RemoveWorkspace acts on those", len(got.members))
	}
}
