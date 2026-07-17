package hub_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/howlerops/oculus/daemon/agent"
	"github.com/howlerops/oculus/daemon/crypto"
	"github.com/howlerops/oculus/daemon/hub"
	"github.com/howlerops/oculus/daemon/project"
	"github.com/howlerops/oculus/daemon/protocol"
	"github.com/howlerops/oculus/daemon/transport"
)

func gitInitRepo(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{
		{"init", "-q"}, {"symbolic-ref", "HEAD", "refs/heads/main"},
		{"config", "user.email", "t@t"}, {"config", "user.name", "t"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, out)
		}
	}
	_ = os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o644)
	for _, args := range [][]string{{"add", "."}, {"commit", "-qm", "init"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, out)
		}
	}
}

// cwdProvider records the cwd passed to Create so the test can assert a project's path
// was resolved and threaded through.
type cwdProvider struct {
	mu      sync.Mutex
	lastCwd string
}

func (p *cwdProvider) Name() string                                     { return "fake" }
func (p *cwdProvider) List(context.Context) ([]protocol.Session, error) { return nil, nil }
func (p *cwdProvider) Create(_ context.Context, cwd, _ string) (agent.Session, error) {
	p.mu.Lock()
	p.lastCwd = cwd
	p.mu.Unlock()
	return &idleSession{events: make(chan agent.Event)}, nil
}
func (p *cwdProvider) cwd() string { p.mu.Lock(); defer p.mu.Unlock(); return p.lastCwd }

// idleSession is a no-op session whose event stream just stays open.
type idleSession struct{ events chan agent.Event }

func (s *idleSession) ID() string                                    { return "sess_x" }
func (s *idleSession) Provider() string                              { return "fake" }
func (s *idleSession) Events() <-chan agent.Event                    { return s.events }
func (s *idleSession) Prompt(context.Context, string) error          { return nil }
func (s *idleSession) Respond(context.Context, string, string) error { return nil }
func (s *idleSession) Stop(context.Context) error                    { return nil }
func (s *idleSession) Close() error                                  { close(s.events); return nil }

func send(t *testing.T, c *transport.Conn, id, typ string, payload any) {
	t.Helper()
	raw, err := protocol.Encode(id, typ, payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Send(raw); err != nil {
		t.Fatal(err)
	}
}

// waitOK waits for the OK envelope with the given id and returns its payload bytes.
func (r *clientReader) waitOK(t *testing.T, id string) json.RawMessage {
	t.Helper()
	var out json.RawMessage
	r.waitFor(t, "ok "+id, func(e protocol.Envelope) bool {
		if e.Type == protocol.TypeError && e.ID == id {
			t.Fatalf("got error for %s: %s", id, string(e.Payload))
		}
		if e.Type == protocol.TypeOK && e.ID == id {
			out = e.Payload
			return true
		}
		return false
	})
	return out
}

// TestProjectFlow: add a project, list it, then create a session scoped to it — the
// provider must receive the project's path as cwd.
func TestProjectFlow(t *testing.T) {
	dir := t.TempDir()
	reg, err := project.Load(t.TempDir() + "/projects.json")
	if err != nil {
		t.Fatal(err)
	}
	prov := &cwdProvider{}
	h := hub.New()
	h.Register(prov)
	h.SetProjects(reg)

	daemonKP, _ := crypto.GenerateKeyPair()
	conn := connectClient(t, h, daemonKP)
	r := newReader(conn)

	// Add the project.
	send(t, conn, "p1", protocol.TypeProjectAdd, protocol.ProjectAdd{Path: dir})
	var added protocol.Project
	if err := json.Unmarshal(r.waitOK(t, "p1"), &added); err != nil {
		t.Fatal(err)
	}
	if added.ID == "" || added.Path != dir {
		t.Fatalf("added project = %+v (want path %s)", added, dir)
	}

	// List shows it.
	send(t, conn, "p2", protocol.TypeProjectList, struct{}{})
	var list protocol.ProjectList
	if err := json.Unmarshal(r.waitOK(t, "p2"), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Projects) != 1 || list.Projects[0].ID != added.ID {
		t.Fatalf("project.list = %+v, want [%s]", list.Projects, added.ID)
	}

	// Create a session scoped to the project -> provider gets the project's path as cwd,
	// and the session response carries the project/cwd metadata for grouping.
	send(t, conn, "s1", protocol.TypeSessionCreate, protocol.SessionCreate{Provider: "fake", ProjectID: added.ID})
	var sess protocol.Session
	if err := json.Unmarshal(r.waitOK(t, "s1"), &sess); err != nil {
		t.Fatal(err)
	}
	if got := prov.cwd(); got != dir {
		t.Fatalf("session cwd = %q, want project path %q", got, dir)
	}
	if sess.ProjectID != added.ID || sess.Cwd != dir {
		t.Fatalf("session meta = {ProjectID:%q Cwd:%q}, want {%q %q}", sess.ProjectID, sess.Cwd, added.ID, dir)
	}

	// session.list also carries the metadata.
	send(t, conn, "l1", protocol.TypeSessionList, struct{}{})
	var sl protocol.SessionList
	if err := json.Unmarshal(r.waitOK(t, "l1"), &sl); err != nil {
		t.Fatal(err)
	}
	if len(sl.Sessions) != 1 || sl.Sessions[0].ProjectID != added.ID || sl.Sessions[0].Cwd != dir {
		t.Fatalf("session.list meta = %+v, want project %s cwd %s", sl.Sessions, added.ID, dir)
	}
}

// TestWorktreeSession: creating a session with Worktree=true in a git project runs the
// provider in a fresh worktree (not the repo root) on an oculus/<slug> branch, and the
// session metadata reflects that.
func TestWorktreeSession(t *testing.T) {
	repo := t.TempDir()
	gitInitRepo(t, repo)
	// A setup manifest: copy a gitignored .env and assign a port. Proves bootstrap runs.
	if err := os.WriteFile(filepath.Join(repo, ".env"), []byte("K=v"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.MkdirAll(filepath.Join(repo, ".oculus"), 0o755)
	_ = os.WriteFile(filepath.Join(repo, ".oculus", "project.json"),
		[]byte(`{"copy":[".env"],"portRange":[47120,47139]}`), 0o644)
	reg, _ := project.Load(t.TempDir() + "/projects.json")
	prov := &cwdProvider{}
	h := hub.New()
	h.Register(prov)
	h.SetProjects(reg)
	h.SetWorktreeBase(t.TempDir())

	daemonKP, _ := crypto.GenerateKeyPair()
	conn := connectClient(t, h, daemonKP)
	r := newReader(conn)

	send(t, conn, "p1", protocol.TypeProjectAdd, protocol.ProjectAdd{Path: repo})
	var proj protocol.Project
	_ = json.Unmarshal(r.waitOK(t, "p1"), &proj)

	send(t, conn, "s1", protocol.TypeSessionCreate, protocol.SessionCreate{
		Provider: "fake", ProjectID: proj.ID, Worktree: true, WorkspaceName: "Feature X",
	})
	var sess protocol.Session
	if err := json.Unmarshal(r.waitOK(t, "s1"), &sess); err != nil {
		t.Fatal(err)
	}

	// Provider ran in the worktree, NOT the repo root.
	got := prov.cwd()
	if got == repo || got == "" {
		t.Fatalf("session cwd = %q, expected a worktree path (not the repo root)", got)
	}
	if out, err := exec.Command("git", "-C", got, "rev-parse", "--is-inside-work-tree").Output(); err != nil || strings.TrimSpace(string(out)) != "true" {
		t.Fatalf("session cwd %q is not a git work tree", got)
	}
	if sess.Branch != "oculus/feature-x" {
		t.Errorf("session branch = %q, want oculus/feature-x", sess.Branch)
	}
	if sess.WorkspaceName != "Feature X" || sess.Cwd != got {
		t.Errorf("session meta = {ws:%q cwd:%q}, want {Feature X, %q}", sess.WorkspaceName, sess.Cwd, got)
	}
	// Bootstrap ran: the gitignored .env was copied into the worktree, and a port assigned.
	if b, err := os.ReadFile(filepath.Join(got, ".env")); err != nil || string(b) != "K=v" {
		t.Errorf(".env not bootstrapped into worktree: %v %q", err, b)
	}
	if sess.Port < 47120 || sess.Port > 47139 {
		t.Errorf("session port = %d, want in [47120,47139]", sess.Port)
	}
}

// TestWorktreeFinish: after a worktree session makes changes, worktree.diff shows them
// and worktree.remove tears down the worktree + drops the session.
func TestWorktreeFinish(t *testing.T) {
	repo := t.TempDir()
	gitInitRepo(t, repo)
	reg, _ := project.Load(t.TempDir() + "/projects.json")
	prov := &cwdProvider{}
	h := hub.New()
	h.Register(prov)
	h.SetProjects(reg)
	h.SetWorktreeBase(t.TempDir())

	daemonKP, _ := crypto.GenerateKeyPair()
	conn := connectClient(t, h, daemonKP)
	r := newReader(conn)

	send(t, conn, "p1", protocol.TypeProjectAdd, protocol.ProjectAdd{Path: repo})
	var proj protocol.Project
	_ = json.Unmarshal(r.waitOK(t, "p1"), &proj)
	send(t, conn, "s1", protocol.TypeSessionCreate, protocol.SessionCreate{
		Provider: "fake", ProjectID: proj.ID, Worktree: true, WorkspaceName: "finish",
	})
	var sess protocol.Session
	_ = json.Unmarshal(r.waitOK(t, "s1"), &sess)

	// Simulate the agent editing a file in the worktree.
	if err := os.WriteFile(filepath.Join(sess.Cwd, "f"), []byte("edited by agent"), 0o644); err != nil {
		t.Fatal(err)
	}

	// worktree.diff surfaces the change.
	send(t, conn, "d1", protocol.TypeWorktreeDiff, protocol.WorktreeDiff{SessionID: sess.ID})
	var wd protocol.WorktreeDiff
	_ = json.Unmarshal(r.waitOK(t, "d1"), &wd)
	if !strings.Contains(wd.Diff, "edited by agent") {
		t.Fatalf("diff missing change:\n%s", wd.Diff)
	}

	// worktree.remove tears it down (force: it's dirty).
	send(t, conn, "rm", protocol.TypeWorktreeRemove, protocol.WorktreeRemove{SessionID: sess.ID, Force: true})
	r.waitOK(t, "rm")
	if _, err := os.Stat(sess.Cwd); !os.IsNotExist(err) {
		t.Errorf("worktree still exists after remove: %v", err)
	}
	send(t, conn, "l1", protocol.TypeSessionList, struct{}{})
	var sl protocol.SessionList
	_ = json.Unmarshal(r.waitOK(t, "l1"), &sl)
	if len(sl.Sessions) != 0 {
		t.Errorf("session.list = %+v, want empty after remove", sl.Sessions)
	}
}
