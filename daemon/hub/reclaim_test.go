package hub

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/store"
	"github.com/howlerops/oculus/daemon/worktree"
)

// reclaimRepo builds a one-commit repo and a worktree off it, plus the persisted session record
// that would point at them.
func reclaimRepo(t *testing.T) (db *store.Store, root, head, wtPath string) {
	t.Helper()
	root = t.TempDir()
	run := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run(root, "init", "-q")
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run(root, "add", "-A")
	run(root, "commit", "-qm", "first")
	out, err := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	head = string(out[:len(out)-1])

	wt, err := worktree.Create(t.TempDir(), root, "variant")
	if err != nil {
		t.Fatal(err)
	}
	db, err = store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	blob, _ := json.Marshal(persistedMeta{WorktreePath: wt.Path, RepoRoot: root, BaseCommit: head})
	// Stamp it well in the past so it is already expired.
	if err := db.SaveSession(store.SessionRecord{ID: "s1", Provider: "opencode", Cwd: wt.Path, Meta: string(blob)},
		time.Now().Add(-30*24*time.Hour).Unix()); err != nil {
		t.Fatal(err)
	}
	return db, root, head, wt.Path
}

// TestExpiredSessionReclaimsItsCleanWorktree is the dangling-worktree hole: session TTL pruning
// deletes DB records only, so a worktree outlived the record that was the only thing remembering
// it existed — invisible in every UI and unreachable through the app, forever. An abandoned fanout
// is the ordinary way to get there, since nothing else ever tears its variants down.
func TestExpiredSessionReclaimsItsCleanWorktree(t *testing.T) {
	h := New()
	db, _, _, wtPath := reclaimRepo(t)

	h.reclaimExpiredWorktrees(db, time.Now().Unix())

	if _, err := os.Stat(wtPath); !os.IsNotExist(err) {
		t.Fatal("an expired session's untouched worktree was left on disk — it is now unreachable")
	}
}

// TestExpiredSessionKeepsWorktreeWithWork: age is not evidence that work was abandoned. A worktree
// nobody touched for a week can still hold an afternoon of uncommitted changes, and the prune runs
// on a timer with no human watching.
func TestExpiredSessionKeepsWorktreeWithWork(t *testing.T) {
	h := New()
	db, _, _, wtPath := reclaimRepo(t)
	if err := os.WriteFile(filepath.Join(wtPath, "unsaved.txt"), []byte("real work\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	h.reclaimExpiredWorktrees(db, time.Now().Unix())

	if _, err := os.Stat(filepath.Join(wtPath, "unsaved.txt")); err != nil {
		t.Fatalf("THE PRUNE DELETED UNCOMMITTED WORK: %v", err)
	}
}

// TestReclaimIgnoresSessionsThatAreNotExpired: the cutoff must actually gate this. A live session's
// worktree is in active use.
func TestReclaimIgnoresSessionsThatAreNotExpired(t *testing.T) {
	h := New()
	db, _, _, wtPath := reclaimRepo(t)

	// A cutoff far in the past: nothing qualifies as expired.
	h.reclaimExpiredWorktrees(db, time.Now().Add(-365*24*time.Hour).Unix())

	if _, err := os.Stat(wtPath); err != nil {
		t.Fatalf("removed the worktree of a session that had not expired: %v", err)
	}
}
