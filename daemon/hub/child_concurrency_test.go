package hub

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Concurrent children must not share a working tree.
//
// This is the case worktree isolation exists for, and it shipped defaulted-ON with the justification
// that two children in one directory edit the same files underneath each other. That claim deserved
// a demonstration rather than an assertion, so this builds the real thing: two worktrees off one
// repo, each writing the SAME filename, and checks neither can see the other's content.
//
// Without isolation both children write ./NOTES.md in the parent's directory and the second silently
// destroys the first — no error, no conflict, and each agent believes it succeeded.
func TestConcurrentChildrenDoNotShareAWorkingTree(t *testing.T) {
	repo := t.TempDir()
	initRepo(t, repo)

	base := t.TempDir()
	wtA := filepath.Join(base, "subtask-aaaaaa")
	wtB := filepath.Join(base, "subtask-bbbbbb")
	git(t, repo, "worktree", "add", "-b", "oculus/subtask-aaaaaa", wtA)
	git(t, repo, "worktree", "add", "-b", "oculus/subtask-bbbbbb", wtB)

	// Both children write the same filename, as two agents given related subtasks would.
	write(t, filepath.Join(wtA, "NOTES.md"), "child A's work\n")
	write(t, filepath.Join(wtB, "NOTES.md"), "child B's work\n")

	got := func(p string) string {
		b, err := exec.Command("cat", filepath.Join(p, "NOTES.md")).Output()
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		return strings.TrimSpace(string(b))
	}
	if a := got(wtA); a != "child A's work" {
		t.Fatalf("child A's file was clobbered: %q", a)
	}
	if b := got(wtB); b != "child B's work" {
		t.Fatalf("child B's file was clobbered: %q", b)
	}

	// And each is on its own branch, so the work is separable afterwards rather than tangled.
	branch := func(p string) string {
		out, err := exec.Command("git", "-C", p, "rev-parse", "--abbrev-ref", "HEAD").Output()
		if err != nil {
			t.Fatalf("branch %s: %v", p, err)
		}
		return strings.TrimSpace(string(out))
	}
	if branch(wtA) == branch(wtB) {
		t.Fatalf("both children on the same branch %q — their commits would interleave", branch(wtA))
	}
}

// The counter-demonstration: WITHOUT isolation, the collision is real and silent. Pinning it keeps
// the default honest — if someone later flips `worktree` off by default, this says what it costs.
func TestSharedDirectoryChildrenClobberEachOther(t *testing.T) {
	shared := t.TempDir()
	write(t, filepath.Join(shared, "NOTES.md"), "child A's work\n")
	write(t, filepath.Join(shared, "NOTES.md"), "child B's work\n") // second child, same path

	b, err := exec.Command("cat", filepath.Join(shared, "NOTES.md")).Output()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "child A") {
		t.Fatal("expected A's work to be gone — this test documents the loss isolation prevents")
	}
}
