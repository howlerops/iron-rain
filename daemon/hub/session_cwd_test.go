package hub

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/howlerops/oculus/daemon/project"
)

// cwdHub returns a hub with one registered project and a worktree base, plus a relocated HOME whose
// state directory holds the key material this is all about.
func cwdHub(t *testing.T) (*Hub, string, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".oculus", "worktrees", "proj", "feature"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".oculus", "daemon.key"), []byte("KEY"), 0o600); err != nil {
		t.Fatal(err)
	}
	proj := filepath.Join(home, "code", "proj")
	if err := os.MkdirAll(filepath.Join(proj, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	h := New()
	reg, err := project.Load(filepath.Join(t.TempDir(), "projects.json"))
	if err != nil {
		t.Fatalf("project registry: %v", err)
	}
	if _, err := reg.Add(proj); err != nil {
		t.Fatalf("register project: %v", err)
	}
	h.SetProjects(reg)
	h.SetWorktreeBase(filepath.Join(home, ".oculus", "worktrees"))
	return h, home, proj
}

// TestSessionCwdRefusesStateDirForEveryone is the point of the whole change: the refusal cannot be
// role-conditional. Roles are OFF by default, and roleRegistry.role then reports every connection as
// the owner — so a rule that only bit a steerer would do nothing on the installs where this was
// found. Nobody has a reason to run an agent inside the daemon's state directory.
func TestSessionCwdRefusesStateDirForEveryone(t *testing.T) {
	h, home, _ := cwdHub(t)
	state := filepath.Join(home, ".oculus")

	for _, role := range []string{RoleOwner, RoleSteerer, ""} {
		ctx := withRequesterRole(context.Background(), role)
		for _, cwd := range []string{state, filepath.Join(state, "worktrees", "..", "sub")} {
			err := h.validateSessionCwd(ctx, cwd)
			if err == nil {
				t.Fatalf("role %q: session cwd %s was accepted", role, cwd)
			}
			if !strings.Contains(err.Error(), ".oculus") {
				t.Errorf("role %q: refusal should name the directory, got %q", role, err)
			}
		}
	}

	// The other stores a chosen cwd reaches are refused on the same terms.
	for _, cwd := range []string{
		filepath.Join(home, ".ssh"),
		filepath.Join(home, ".aws"),
		filepath.Join(home, "Library", "Keychains"),
	} {
		if err := h.validateSessionCwd(withRequesterRole(context.Background(), RoleOwner), cwd); err == nil {
			t.Errorf("session cwd %s was accepted", cwd)
		}
	}
}

// TestSessionCwdKeepsOwnerFlows pins what must NOT break. The owner's folder picker names a directory
// no registry has heard of yet (autoProjects turns it into a project afterwards); restore replays a
// stored worktree cwd; a multi-repo workspace lives under the worktree base. All three arrive here
// as a plain absolute path with no project id.
func TestSessionCwdKeepsOwnerFlows(t *testing.T) {
	h, home, proj := cwdHub(t)

	ok := []struct {
		what string
		cwd  string
	}{
		{"registered project", proj},
		{"a directory inside it", filepath.Join(proj, "src")},
		{"a session worktree", filepath.Join(home, ".oculus", "worktrees", "proj", "feature")},
		{"a multi-repo workspace layout", filepath.Join(home, ".oculus", "worktrees", "ws-1")},
		{"a folder the owner just picked", filepath.Join(home, "elsewhere", "newrepo")},
		{"no cwd at all", ""},
	}
	for _, c := range ok {
		if err := h.validateSessionCwd(withRequesterRole(context.Background(), RoleOwner), c.cwd); err != nil {
			t.Errorf("%s (%s) was refused: %v", c.what, c.cwd, err)
		}
	}
	// Internal callers — loops, fan-out, restore — carry no role at all and must keep working.
	for _, c := range ok {
		if err := h.validateSessionCwd(context.Background(), c.cwd); err != nil {
			t.Errorf("internal caller: %s (%s) was refused: %v", c.what, c.cwd, err)
		}
	}
}

// TestSessionCwdSteererMustUseAKnownPlace: where roles ARE enforced, a steerer was invited to drive
// an agent, not to choose which of the owner's directories it runs in — and they cannot register a
// project themselves (project.add is capOwner). Their sessions stay inside places the owner already
// chose, which is also what keeps them out of the next credential directory nobody thought to name.
func TestSessionCwdSteererMustUseAKnownPlace(t *testing.T) {
	h, home, proj := cwdHub(t)
	ctx := withRequesterRole(context.Background(), RoleSteerer)

	for _, cwd := range []string{proj, filepath.Join(proj, "src"), filepath.Join(home, ".oculus", "worktrees", "proj", "feature")} {
		if err := h.validateSessionCwd(ctx, cwd); err != nil {
			t.Errorf("a steerer should still reach %s: %v", cwd, err)
		}
	}
	for _, cwd := range []string{home, filepath.Join(home, "elsewhere"), "/etc", "/"} {
		if err := h.validateSessionCwd(ctx, cwd); err == nil {
			t.Errorf("a steerer was allowed to start a session in %s", cwd)
		}
	}
}

// TestSessionCwdRequiresAbsolute: a relative cwd would be resolved against the daemon's own process
// directory, which under launchd is "/" — a meaningless location for every caller, so it is a bug
// worth reporting rather than a path worth guessing at.
func TestSessionCwdRequiresAbsolute(t *testing.T) {
	h, _, _ := cwdHub(t)
	for _, cwd := range []string{"code/proj", "./x", ".."} {
		if err := h.validateSessionCwd(context.Background(), cwd); err == nil {
			t.Errorf("relative cwd %q was accepted", cwd)
		}
	}
}
