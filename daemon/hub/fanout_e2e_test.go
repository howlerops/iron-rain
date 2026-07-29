package hub_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/howlerops/oculus/daemon/crypto"
	"github.com/howlerops/oculus/daemon/hub"
	"github.com/howlerops/oculus/daemon/project"
	"github.com/howlerops/oculus/daemon/protocol"
)

// gitRepo makes a temp git repo with one commit so worktree.Create can branch off it.
func gitRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"symbolic-ref", "HEAD", "refs/heads/main"},
		{"config", "user.email", "t@t"},
		{"config", "user.name", "t"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "."}, {"commit", "-qm", "init"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, out)
		}
	}
	return dir
}

// TestFanoutE2E drives the parallel fan-out primitive through the real wire: fan one prompt across 3
// agents in isolated worktrees, and assert they come back as ONE group with distinct variants, each
// on its own branch — the "race several approaches, compare, merge the winner" workflow.
func TestFanoutE2E(t *testing.T) {
	repo := gitRepo(t)
	reg, err := project.Load(filepath.Join(t.TempDir(), "projects.json"))
	if err != nil {
		t.Fatal(err)
	}
	proj, err := reg.Add(repo)
	if err != nil {
		t.Fatal(err)
	}

	h := hub.New()
	h.Register(&cwdProvider{})
	h.SetProjects(reg)

	daemonKP, _ := crypto.GenerateKeyPair()
	conn := connectClient(t, h, daemonKP)
	r := newReader(conn)

	send(t, conn, "f1", protocol.TypeFanoutCreate, protocol.FanoutCreate{
		Provider: "fake", ProjectID: proj.ID, Prompt: "implement the feature", Count: 3,
	})
	var res protocol.FanoutResult
	if err := json.Unmarshal(r.waitOK(t, "f1"), &res); err != nil {
		t.Fatalf("fanout decode: %v", err)
	}
	if res.Group == "" {
		t.Fatal("fan-out group id is empty")
	}
	if len(res.SessionIDs) != 3 {
		t.Fatalf("started %d variants, want 3: %v", len(res.SessionIDs), res.SessionIDs)
	}

	// The session list must show all three as one group with variants 0,1,2 and distinct branches.
	send(t, conn, "l1", protocol.TypeSessionList, struct{}{})
	var list protocol.SessionList
	if err := json.Unmarshal(r.waitOK(t, "l1"), &list); err != nil {
		t.Fatalf("list decode: %v", err)
	}
	variants := map[int]bool{}
	branches := map[string]bool{}
	inGroup := 0
	for _, s := range list.Sessions {
		if s.FanoutGroup != res.Group {
			continue
		}
		inGroup++
		variants[s.FanoutVariant] = true
		if s.Branch != "" {
			branches[s.Branch] = true
		}
	}
	if inGroup != 3 {
		t.Fatalf("session list has %d sessions in the fan-out group, want 3", inGroup)
	}
	for i := 0; i < 3; i++ {
		if !variants[i] {
			t.Errorf("missing fan-out variant %d", i)
		}
	}
	if len(branches) != 3 {
		t.Errorf("variants share branches (%d distinct), want 3 isolated worktrees", len(branches))
	}
}

// TestFanoutResolveTearsDownLosers: after racing 3 variants, resolving the group (keeping one)
// removes the OTHER variants + their worktrees, so a decided fan-out doesn't leave orphans piling up.
// The kept winner survives and is no longer tagged to the (now gone) group.
func TestFanoutResolveTearsDownLosers(t *testing.T) {
	repo := gitRepo(t)
	reg, _ := project.Load(filepath.Join(t.TempDir(), "projects.json"))
	proj, _ := reg.Add(repo)
	h := hub.New()
	h.Register(&cwdProvider{})
	h.SetProjects(reg)
	daemonKP, _ := crypto.GenerateKeyPair()
	conn := connectClient(t, h, daemonKP)
	r := newReader(conn)

	send(t, conn, "f1", protocol.TypeFanoutCreate, protocol.FanoutCreate{
		Provider: "fake", ProjectID: proj.ID, Prompt: "go", Count: 3,
	})
	var res protocol.FanoutResult
	if err := json.Unmarshal(r.waitOK(t, "f1"), &res); err != nil {
		t.Fatalf("fanout decode: %v", err)
	}
	if len(res.SessionIDs) != 3 {
		t.Fatalf("want 3 variants, got %d", len(res.SessionIDs))
	}
	winner := res.SessionIDs[0]

	// Resolve: keep the winner, discard the rest.
	send(t, conn, "r1", protocol.TypeFanoutResolve, protocol.FanoutResolve{Group: res.Group, Keep: winner, Force: true})
	var resolved protocol.FanoutResolved
	if err := json.Unmarshal(r.waitOK(t, "r1"), &resolved); err != nil {
		t.Fatalf("resolve decode: %v", err)
	}
	if len(resolved.Removed) != 2 {
		t.Fatalf("resolve removed %d variants, want 2 (failed=%v)", len(resolved.Removed), resolved.Failed)
	}

	// The list must now contain ONLY the winner from that group, and it must be untagged.
	send(t, conn, "l1", protocol.TypeSessionList, struct{}{})
	var list protocol.SessionList
	if err := json.Unmarshal(r.waitOK(t, "l1"), &list); err != nil {
		t.Fatalf("list decode: %v", err)
	}
	inGroup, winnerLive, winnerUntagged := 0, false, false
	for _, s := range list.Sessions {
		if s.FanoutGroup == res.Group {
			inGroup++
		}
		if s.ID == winner {
			winnerLive = true
			winnerUntagged = s.FanoutGroup == ""
		}
	}
	if inGroup != 0 {
		t.Errorf("group still has %d tagged sessions after resolve, want 0", inGroup)
	}
	if !winnerLive {
		t.Error("winner was removed — resolve should keep it")
	}
	if !winnerUntagged {
		t.Error("winner still carries the fan-out group tag after resolve")
	}
}

// TestFanoutClampsAndValidates: count is clamped to >=2 and an empty prompt is rejected.
func TestFanoutClampsAndValidates(t *testing.T) {
	repo := gitRepo(t)
	reg, _ := project.Load(filepath.Join(t.TempDir(), "projects.json"))
	proj, _ := reg.Add(repo)
	h := hub.New()
	h.Register(&cwdProvider{})
	h.SetProjects(reg)
	daemonKP, _ := crypto.GenerateKeyPair()
	conn := connectClient(t, h, daemonKP)
	r := newReader(conn)

	// Empty prompt → error.
	send(t, conn, "e1", protocol.TypeFanoutCreate, protocol.FanoutCreate{Provider: "fake", ProjectID: proj.ID, Count: 3})
	r.waitFor(t, "error e1", func(e protocol.Envelope) bool { return e.Type == protocol.TypeError && e.ID == "e1" })

	// Count 1 → clamped to 2.
	send(t, conn, "c1", protocol.TypeFanoutCreate, protocol.FanoutCreate{Provider: "fake", ProjectID: proj.ID, Prompt: "go", Count: 1})
	var res protocol.FanoutResult
	if err := json.Unmarshal(r.waitOK(t, "c1"), &res); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(res.SessionIDs) != 2 {
		t.Errorf("count 1 clamped to %d variants, want 2", len(res.SessionIDs))
	}
}
