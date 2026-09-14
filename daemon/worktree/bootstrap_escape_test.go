package worktree

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// A worktree's copy patterns could reach outside the repository — and outside the worktree.
//
// Both sides use filepath.Join, which CLEANS "..", so a pattern of "../../.ssh/id_ed25519" read from
// outside repoRoot and wrote to outside worktreePath without erroring. Bootstrap's own comment
// asserted this was impossible ("copying, symlinking and hook-disabling move data around inside a
// directory the caller already chose") and that assertion is precisely what justified leaving Copy
// outside the SetupTrust gate. So this ran with the zero-value, deny-everything trust.
//
// The full chain was capSteer throughout: Config is decoded from <repoRoot>/.oculus/project.json,
// which a steerer can write with fs.write; session.create{Worktree:true} then copies the key out of
// ~/.ssh into the worktrees base; and that base is deliberately carved out of the ~/.oculus
// protection, so fs.read hands it back.
func TestACopyPatternCannotReachOutsideTheRepository(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	wt := filepath.Join(base, "worktrees", "sess")
	secretDir := filepath.Join(base, "secrets")
	for _, d := range []string{repo, wt, secretDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	secret := filepath.Join(secretDir, "id_ed25519")
	if err := os.WriteFile(secret, []byte("PRIVATE KEY"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg := Config{Copy: []string{"../secrets/id_ed25519"}}
	res, err := Bootstrap(context.Background(), repo, wt, cfg, 0, SetupTrust{})
	if err == nil {
		t.Errorf("Bootstrap ACCEPTED a copy pattern that climbs out of the repository (copied %v). "+
			"This runs with deny-everything trust, so nothing else stands between a steerer-writable "+
			"project.json and the owner's private keys.", res.Copied)
	}

	// And nothing may have landed.
	leaked := filepath.Join(base, "worktrees", "secrets", "id_ed25519")
	if _, err := os.Stat(leaked); err == nil {
		t.Errorf("the key was written to %s — outside the worktree entirely, into a directory the "+
			"filesystem guard treats as open", leaked)
	}
}

// The same escape through the link step, which creates a SYMLINK and so does not even need the file
// to be readable at copy time.
func TestALinkPatternCannotReachOutsideTheRepository(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	wt := filepath.Join(base, "worktrees", "sess")
	for _, d := range []string{repo, wt} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(base, "elsewhere"), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := Config{Link: []string{"../elsewhere"}, NoAutoLink: true}
	if _, err := Bootstrap(context.Background(), repo, wt, cfg, 0, SetupTrust{}); err == nil {
		t.Error("Bootstrap ACCEPTED a link pattern that climbs out of the repository — the worktree " +
			"now contains a symlink to a directory outside it, which every later fs.* call follows")
	}
}

// The other direction, so "confined" cannot become "refuses everything": an ordinary nested pattern
// — the normal case, a gitignored .env or a config directory — must still copy.
func TestOrdinaryCopyPatternsStillWork(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	wt := filepath.Join(base, "wt")
	if err := os.MkdirAll(filepath.Join(repo, "config"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, ".env"), []byte("A=1"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "config", "local.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := Config{Copy: []string{".env", "config/local.json"}, NoAutoLink: true}
	res, err := Bootstrap(context.Background(), repo, wt, cfg, 0, SetupTrust{})
	if err != nil {
		t.Fatalf("an ordinary copy was refused: %v", err)
	}
	if len(res.Copied) != 2 {
		t.Fatalf("copied %v, want both files — worktree sessions depend on this to get their "+
			"gitignored configuration", res.Copied)
	}
	for _, rel := range []string{".env", "config/local.json"} {
		if _, err := os.Stat(filepath.Join(wt, rel)); err != nil {
			t.Errorf("%s did not land in the worktree: %v", rel, err)
		}
	}
}
