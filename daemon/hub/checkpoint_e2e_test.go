package hub_test

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/crypto"
	"github.com/howlerops/oculus/daemon/hub"
	"github.com/howlerops/oculus/daemon/project"
	"github.com/howlerops/oculus/daemon/protocol"
)

// TestCheckpointE2E drives the checkpoint/rollback plumbing through the real wire: create a worktree
// session, save a checkpoint, list it back, and restore it — the timeline-rollback capability.
func TestCheckpointE2E(t *testing.T) {
	repo := gitRepo(t) // helper from fanout_e2e_test.go (same package)
	reg, _ := project.Load(filepath.Join(t.TempDir(), "projects.json"))
	proj, _ := reg.Add(repo)

	h := hub.New()
	h.Register(&cwdProvider{})
	h.SetProjects(reg)
	daemonKP, _ := crypto.GenerateKeyPair()
	conn := connectClient(t, h, daemonKP)
	r := newReader(conn)

	// A worktree session (checkpoints require a worktree). Unique workspace name so reruns don't
	// collide on the shared ~/.oculus/worktrees base.
	ws := fmt.Sprintf("cp-%d", time.Now().UnixNano())
	send(t, conn, "c1", protocol.TypeSessionCreate, protocol.SessionCreate{
		Provider: "fake", ProjectID: proj.ID, Worktree: true, WorkspaceName: ws,
	})
	var sess protocol.Session
	if err := json.Unmarshal(r.waitOK(t, "c1"), &sess); err != nil {
		t.Fatalf("create decode: %v", err)
	}

	// Save a checkpoint → list comes back with one entry carrying a sha.
	send(t, conn, "k1", protocol.TypeCheckpointCreate, protocol.CheckpointCreate{SessionID: sess.ID, Label: "before risky edit"})
	var list protocol.CheckpointList
	if err := json.Unmarshal(r.waitOK(t, "k1"), &list); err != nil {
		t.Fatalf("checkpoint decode: %v", err)
	}
	if len(list.Checkpoints) != 1 {
		t.Fatalf("got %d checkpoints, want 1", len(list.Checkpoints))
	}
	cp := list.Checkpoints[0]
	if cp.SHA == "" || cp.Label != "before risky edit" {
		t.Fatalf("bad checkpoint: %+v", cp)
	}

	// Restore it → OK (the git round-trip itself is covered by worktree.TestSnapshotAndRestore).
	send(t, conn, "k2", protocol.TypeCheckpointRestore, protocol.CheckpointRestore{SessionID: sess.ID, SHA: cp.SHA})
	r.waitOK(t, "k2")

	// A checkpoint on a non-worktree / unknown session is a clean error.
	send(t, conn, "k3", protocol.TypeCheckpointCreate, protocol.CheckpointCreate{SessionID: "nope"})
	r.waitFor(t, "error k3", func(e protocol.Envelope) bool { return e.Type == protocol.TypeError && e.ID == "k3" })
}
