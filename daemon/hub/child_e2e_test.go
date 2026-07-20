package hub_test

import (
	"encoding/json"
	"testing"

	"github.com/howlerops/oculus/daemon/crypto"
	"github.com/howlerops/oculus/daemon/hub"
	"github.com/howlerops/oculus/daemon/protocol"
)

// TestSessionChildE2E drives the delegation primitive through the real wire: create a parent,
// then session.child, and assert the child comes back linked to the parent with its subtask.
func TestSessionChildE2E(t *testing.T) {
	prov := &cwdProvider{}
	h := hub.New()
	h.Register(prov)

	daemonKP, _ := crypto.GenerateKeyPair()
	conn := connectClient(t, h, daemonKP)
	r := newReader(conn)

	// Parent session.
	send(t, conn, "c1", protocol.TypeSessionCreate, protocol.SessionCreate{Provider: "fake", Cwd: "/repo"})
	var parent protocol.Session
	if err := json.Unmarshal(r.waitOK(t, "c1"), &parent); err != nil {
		t.Fatalf("parent decode: %v", err)
	}

	// Delegate a subtask → a scoped child linked back to the parent.
	send(t, conn, "c2", protocol.TypeSessionChild, protocol.SessionChild{
		ParentSessionID: parent.ID, Subtask: "Add retries", Files: []string{"http/client.go"},
	})
	var child protocol.Session
	if err := json.Unmarshal(r.waitOK(t, "c2"), &child); err != nil {
		t.Fatalf("child decode: %v", err)
	}
	if child.ID == parent.ID {
		t.Fatalf("child reused parent id %s", child.ID)
	}
	if child.ParentID != parent.ID {
		t.Errorf("child.ParentID = %q, want %q", child.ParentID, parent.ID)
	}
	if child.Subtask != "Add retries" {
		t.Errorf("child.Subtask = %q, want 'Add retries'", child.Subtask)
	}
	if child.Cwd != "/repo" { // inherits the parent's working directory
		t.Errorf("child.Cwd = %q, want /repo", child.Cwd)
	}

	// A child with an unknown parent is a clean error, not a panic.
	send(t, conn, "c3", protocol.TypeSessionChild, protocol.SessionChild{ParentSessionID: "nope", Subtask: "x"})
	r.waitFor(t, "error c3", func(e protocol.Envelope) bool {
		return e.Type == protocol.TypeError && e.ID == "c3"
	})
}
