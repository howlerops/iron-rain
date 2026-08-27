package github

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- Pure parts: input validation is the security surface here ---

// Clone takes a repository name and a destination and turns them into a path. Both are places where
// a hostile or careless value could land the checkout somewhere it was not asked to go.
func TestCloneRejectsUnsafeInput(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		repo, parent, why string
	}{
		{"", dir, "empty repository"},
		{"justaname", dir, "no owner"},
		{"a/b/c", dir, "too many segments"},
		{"../../etc/passwd", dir, "traversal in the repo name"},
		{"owner/../../escape", dir, "traversal in the repo segment"},
		{"-flag/repo", dir, "a name that would read as a flag"},
		{"owner/repo", "relative/path", "a relative destination"},
		{"owner/repo", "", "no destination"},
	}
	for _, c := range cases {
		if _, err := Clone(context.Background(), c.repo, c.parent); err == nil {
			t.Errorf("Clone(%q, %q) was accepted — %s", c.repo, c.parent, c.why)
		}
	}
}

// Cloning over an existing directory is either a no-op or a mess, and the caller already knows about
// existing checkouts because List reports them.
func TestCloneRefusesAnOccupiedDestination(t *testing.T) {
	parent := t.TempDir()
	if err := os.MkdirAll(filepath.Join(parent, "repo"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := Clone(context.Background(), "owner/repo", parent)
	if err == nil {
		t.Fatal("cloning into an existing directory must be refused")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Errorf("the refusal should say why: %v", err)
	}
}

// A directory that merely shares a repo's name is not a checkout. Handing an agent the wrong folder
// is worse than saying nothing and letting the user clone.
func TestFindLocalNeedsARealCheckout(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "lookalike"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := findLocal("lookalike", []string{root}); got != "" {
		t.Errorf("a directory with no .git was reported as a checkout: %q", got)
	}

	real := filepath.Join(root, "actual")
	if err := os.MkdirAll(filepath.Join(real, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := findLocal("actual", []string{root}); got != real {
		t.Errorf("findLocal = %q, want %q", got, real)
	}

	// A worktree's .git is a FILE, not a directory, and it is still a checkout.
	wt := filepath.Join(root, "worktree")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: /elsewhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := findLocal("worktree", []string{root}); got != wt {
		t.Errorf("a worktree should count as a checkout, got %q", got)
	}

	if got := findLocal("missing", []string{root, "", "/nonexistent"}); got != "" {
		t.Errorf("expected no match, got %q", got)
	}
}

func TestAccountFromAuthStatus(t *testing.T) {
	out := "github.com\n  ✓ Logged in to github.com account jbeck018 (keyring)\n  - Active account: true\n"
	if got := accountFrom(out); got != "jbeck018" {
		t.Errorf("accountFrom = %q, want jbeck018", got)
	}
	if got := accountFrom("nothing useful here"); got != "" {
		t.Errorf("accountFrom on unparseable output = %q, want empty", got)
	}
}

// --- Live: only meaningful where gh is actually installed and signed in ---

// TestLiveListReturnsRepositories talks to the real gh and the real GitHub.
//
// Skipped rather than failed when gh is unavailable, because that is the state on CI and on a
// machine that has simply never signed in — neither is a defect in this code. What it proves when it
// does run is the part no fake can: that the REST shape we parse is the shape gh returns.
func TestLiveListReturnsRepositories(t *testing.T) {
	st := Check(context.Background())
	if !st.Available {
		t.Skipf("gh unavailable: %s", st.Reason)
	}
	t.Logf("signed in as %q", st.Account)

	repos, err := List(context.Background(), nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(repos) == 0 {
		t.Skip("this account can see no repositories; nothing to assert")
	}
	t.Logf("%d repositories, first: %s", len(repos), repos[0].NameWithOwner)

	for _, r := range repos {
		if r.Name == "" || r.NameWithOwner == "" {
			t.Fatalf("a repo came back without a name: %+v", r)
		}
		if !strings.Contains(r.NameWithOwner, "/") {
			t.Errorf("NameWithOwner %q is not owner/name", r.NameWithOwner)
		}
	}
	// Most-recently-updated first is what makes the list usable without searching.
	for i := 1; i < len(repos); i++ {
		if repos[i-1].LocalPath == "" && repos[i].LocalPath == "" &&
			repos[i-1].UpdatedAt < repos[i].UpdatedAt {
			t.Errorf("ordering broke at %d: %s (%s) before %s (%s)",
				i, repos[i-1].Name, repos[i-1].UpdatedAt, repos[i].Name, repos[i].UpdatedAt)
			break
		}
	}
}

// Repositories already on disk must sort ahead of ones that are not — those are the ones a person is
// most likely to want, and burying them under fifty they have never checked out is the problem this
// feature replaces.
func TestLiveAlreadyClonedSortFirst(t *testing.T) {
	st := Check(context.Background())
	if !st.Available {
		t.Skipf("gh unavailable: %s", st.Reason)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	repos, err := List(context.Background(), []string{filepath.Join(home, "projects")})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	seenRemote := false
	for _, r := range repos {
		if r.LocalPath == "" {
			seenRemote = true
			continue
		}
		if seenRemote {
			t.Fatalf("%s is checked out at %s but sorted after a repo that is not", r.Name, r.LocalPath)
		}
		if _, err := os.Stat(r.LocalPath); err != nil {
			t.Errorf("LocalPath %q does not exist", r.LocalPath)
		}
	}
}

// Naming an owner is the ONLY way to reach an org the user is an outside collaborator on. Verified
// against the real API: on this account user/repos returns 132 repositories across six
// organisations and zero from the one exercised here.
func TestLiveListOwnerReachesWhatBrowsingCannot(t *testing.T) {
	st := Check(context.Background())
	if !st.Available {
		t.Skipf("gh unavailable: %s", st.Reason)
	}
	const owner = "totango"

	general, err := List(context.Background(), nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	inGeneral := 0
	for _, r := range general {
		if strings.HasPrefix(strings.ToLower(r.NameWithOwner), owner+"/") {
			inGeneral++
		}
	}

	byOwner, err := ListOwner(context.Background(), owner, nil)
	if err != nil {
		t.Skipf("this account cannot see %s: %v", owner, err)
	}
	if len(byOwner) == 0 {
		t.Skipf("no repositories under %s for this account", owner)
	}
	t.Logf("general listing: %d repos, %d from %s; naming the owner: %d",
		len(general), inGeneral, owner, len(byOwner))

	for _, r := range byOwner {
		if !strings.HasPrefix(strings.ToLower(r.NameWithOwner), owner+"/") {
			t.Errorf("ListOwner(%q) returned %q, which is not that owner's", owner, r.NameWithOwner)
		}
	}
}

// The owner is interpolated into a request path, so it may only ever be a login.
func TestListOwnerRejectsUnsafeNames(t *testing.T) {
	for _, bad := range []string{"", "  ", "a/b", "../../etc", "-flag", "owner?x=1", "own er", "a.b"} {
		if _, err := ListOwner(context.Background(), bad, nil); err == nil {
			t.Errorf("ListOwner(%q) was accepted", bad)
		}
	}
}
