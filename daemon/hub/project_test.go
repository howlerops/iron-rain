package hub_test

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/howlerops/oculus/daemon/agent"
	"github.com/howlerops/oculus/daemon/crypto"
	"github.com/howlerops/oculus/daemon/hub"
	"github.com/howlerops/oculus/daemon/project"
	"github.com/howlerops/oculus/daemon/protocol"
	"github.com/howlerops/oculus/daemon/transport"
)

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

func (s *idleSession) ID() string                           { return "sess_x" }
func (s *idleSession) Provider() string                     { return "fake" }
func (s *idleSession) Events() <-chan agent.Event           { return s.events }
func (s *idleSession) Prompt(context.Context, string) error { return nil }
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
