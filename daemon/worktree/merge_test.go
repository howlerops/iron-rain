package worktree

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitOut is `git` with its output, for the assertions below. The plain `git` helper in
// catchup_test.go covers the side-effect-only calls.
func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// newRepo builds a repo on `main` with one commit and a feature branch carrying another.
func newRepo(t *testing.T) (root, feature string) {
	t.Helper()
	root = t.TempDir()
	git(t, root, "init", "-q", "-b", "main")
	git(t, root, "config", "user.email", "t@example.com")
	git(t, root, "config", "user.name", "T")
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, root, "add", "-A")
	git(t, root, "commit", "-qm", "first")

	feature = "agent/work"
	git(t, root, "checkout", "-q", "-b", feature)
	if err := os.WriteFile(filepath.Join(root, "b.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, root, "add", "-A")
	git(t, root, "commit", "-qm", "agent work")
	git(t, root, "checkout", "-q", "main")
	return root, feature
}

// An untracked file must not block the merge.
//
// The dirty check was `git status --porcelain`, which reports untracked files by default. So a stray
// .DS_Store or a scratch notes.md refused the merge with "commit or stash them first" — advice that
// does not even apply to an untracked file, delivered to someone on a phone who cannot act on it.
// git itself merges happily over untracked files the merge does not touch.
func TestAnUntrackedFileDoesNotBlockTheMerge(t *testing.T) {
	root, feature := newRepo(t)
	if err := os.WriteFile(filepath.Join(root, "scratch-notes.md"), []byte("my notes"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := MergeIntoDefault(context.Background(), root, feature); err != nil {
		t.Fatalf("the merge was refused because of an untracked file: %v\n\n"+
			"From a phone there is nothing the user can do about this, and the advice it gives "+
			"(\"commit or stash them\") does not apply to an untracked file anyway.", err)
	}
	if got := gitOut(t, root, "log", "--oneline", "-1", "--format=%s"); !strings.Contains(got, "Merge "+feature) {
		t.Errorf("head is %q — the merge did not land", got)
	}
}

// A genuinely dirty tracked file must STILL block it: merging over uncommitted human work is not
// ours to do, and that is the whole reason the check exists.
func TestUncommittedTrackedChangesStillBlockTheMerge(t *testing.T) {
	root, feature := newRepo(t)
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("edited by a human\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := MergeIntoDefault(context.Background(), root, feature); err == nil {
		t.Error("the merge proceeded over uncommitted changes to a tracked file")
	}
}

// The user's checkout must be where they left it.
//
// MergeIntoDefault checks out the base branch in the user's MAIN checkout and restored nothing, on
// either path. Someone working on feature/x who tapped Merge was silently left on main: their
// editor's open files changed underneath them, and their next commit went to the wrong branch.
func TestTheCheckoutIsReturnedToTheBranchTheUserWasOn(t *testing.T) {
	root, feature := newRepo(t)
	git(t, root, "checkout", "-q", "-b", "my-other-work")
	before := currentBranch(root)
	if before != "my-other-work" {
		t.Fatalf("precondition: on %q", before)
	}

	if err := MergeIntoDefault(context.Background(), root, feature); err != nil {
		t.Fatal(err)
	}

	if after := currentBranch(root); after != before {
		t.Errorf("the checkout was left on %q; the user was on %q.\n\nTheir open files changed "+
			"underneath them and their next commit lands on the wrong branch.", after, before)
	}
	// And the merge still happened, on the base branch where it belongs.
	if got := gitOut(t, root, "log", "--oneline", "-1", "--format=%s", "main"); !strings.Contains(got, "Merge "+feature) {
		t.Errorf("main's head is %q — the merge did not land", got)
	}
}

// Restoring must also happen when the merge FAILS, which is the path that used to leave the worst
// mess: the merge aborted and the checkout stranded on the base branch.
func TestTheCheckoutIsRestoredEvenWhenTheMergeFails(t *testing.T) {
	root, _ := newRepo(t)
	// A conflicting branch: both sides change a.txt.
	git(t, root, "checkout", "-q", "-b", "conflicting", "main")
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("theirs\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, root, "add", "-A")
	git(t, root, "commit", "-qm", "theirs")
	git(t, root, "checkout", "-q", "main")
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("ours\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(t, root, "add", "-A")
	git(t, root, "commit", "-qm", "ours")

	git(t, root, "checkout", "-q", "-b", "my-other-work")
	if err := MergeIntoDefault(context.Background(), root, "conflicting"); err == nil {
		t.Fatal("expected a conflict")
	}
	if after := currentBranch(root); after != "my-other-work" {
		t.Errorf("after a FAILED merge the checkout was left on %q, not %q", after, "my-other-work")
	}
}
