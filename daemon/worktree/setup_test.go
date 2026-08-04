package worktree

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfig(t *testing.T) {
	repo := t.TempDir()
	if _, ok, _ := func() (Config, bool, error) { return LoadConfig(repo) }(); ok {
		t.Fatal("expected no config when .oculus/project.json is absent")
	}
	_ = os.MkdirAll(filepath.Join(repo, ".oculus"), 0o755)
	_ = os.WriteFile(filepath.Join(repo, ".oculus", "project.json"),
		[]byte(`{"setup":"true","copy":[".env"],"portRange":[4000,4099],"skipHooks":true}`), 0o644)

	cfg, ok, err := LoadConfig(repo)
	if err != nil || !ok {
		t.Fatalf("LoadConfig ok=%v err=%v", ok, err)
	}
	if cfg.Setup != "true" || len(cfg.Copy) != 1 || cfg.Copy[0] != ".env" || !cfg.SkipHooks {
		t.Fatalf("parsed config = %+v", cfg)
	}
	if len(cfg.PortRange) != 2 || cfg.PortRange[0] != 4000 || cfg.PortRange[1] != 4099 {
		t.Fatalf("portRange = %v", cfg.PortRange)
	}
}

func TestBootstrap_CopyPortSetup(t *testing.T) {
	repo := t.TempDir()
	// A gitignored file that must be copied into the worktree, and a setup command.
	if err := os.WriteFile(filepath.Join(repo, ".env"), []byte("SECRET=1"), 0o600); err != nil {
		t.Fatal(err)
	}
	worktree := t.TempDir() // stand-in for a created worktree dir

	cfg := Config{
		Setup: "echo ran-$OCULUS_PORT > setup-marker",
		Copy:  []string{".env"},
	}
	reserved := map[int]bool{}
	port, ok := AllocPort(4000, 4099, reserved)
	if !ok {
		t.Fatal("AllocPort failed")
	}
	if !reserved[port] {
		t.Error("allocated port not marked reserved")
	}
	res, err := Bootstrap(context.Background(), repo, worktree, cfg, port, SetupTrust{Allowed: true})
	if err != nil {
		t.Fatal(err)
	}

	// .env copied.
	if b, err := os.ReadFile(filepath.Join(worktree, ".env")); err != nil || string(b) != "SECRET=1" {
		t.Fatalf(".env not copied: %v %q", err, b)
	}
	// Port threaded through.
	if res.Port != port || res.Port < 4000 || res.Port > 4099 {
		t.Fatalf("port %d (alloc %d) out of range", res.Port, port)
	}
	// Setup ran in the worktree with OCULUS_PORT exported.
	marker, err := os.ReadFile(filepath.Join(worktree, "setup-marker"))
	if err != nil {
		t.Fatalf("setup did not run: %v", err)
	}
	if got, want := string(marker), "ran-"+itoa(res.Port)+"\n"; got != want {
		t.Errorf("setup marker = %q, want %q", got, want)
	}
	if !res.SetupOK {
		t.Error("SetupOK false")
	}
}

// TestBootstrap_UntrustedSetupDoesNotRun is the regression test for the escalation this gate exists
// to stop: a steerer writes <repoRoot>/.oculus/project.json (fs.write is capSteer) and then asks for
// a worktree (session.create is capSteer), and the daemon runs their string through `sh -c` as the
// owner. Without an affirmative trust decision the command must not execute — and the caller must be
// able to SAY that it didn't.
func TestBootstrap_UntrustedSetupDoesNotRun(t *testing.T) {
	repo, wt := t.TempDir(), t.TempDir()
	pwned := filepath.Join(wt, "pwned")
	cfg := Config{Setup: "touch " + pwned}

	res, err := Bootstrap(context.Background(), repo, wt, cfg, 0, SetupTrust{Reason: "not approved"})
	if err != nil {
		t.Fatalf("an untrusted setup must be skipped, not an error: %v", err)
	}
	if _, err := os.Stat(pwned); err == nil {
		t.Fatal("SECURITY: the untrusted setup command executed")
	}
	if res.SetupOK {
		t.Error("SetupOK must be false when the command never ran")
	}
	if !res.SetupSkipped || res.SetupReason != "not approved" {
		t.Errorf("skip must be reported with its reason, got skipped=%v reason=%q", res.SetupSkipped, res.SetupReason)
	}
	if res.SetupCommand != cfg.Setup {
		t.Errorf("SetupCommand = %q, want the command that was skipped so the user can see it", res.SetupCommand)
	}
}

// TestBootstrap_ZeroTrustFailsClosed pins the zero value: a caller that forgets to decide gets a
// worktree with no shell command run, not the other way round.
func TestBootstrap_ZeroTrustFailsClosed(t *testing.T) {
	wt := t.TempDir()
	pwned := filepath.Join(wt, "pwned")
	res, err := Bootstrap(context.Background(), t.TempDir(), wt, Config{Setup: "touch " + pwned}, 0, SetupTrust{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(pwned); err == nil {
		t.Fatal("SECURITY: the zero SetupTrust value allowed execution")
	}
	if res.SetupReason == "" {
		t.Error("a skip with no caller-supplied reason must still carry one — a silent skip is undebuggable")
	}
}

// TestBootstrap_TrustedSetupRuns is the other half: trust granted means the feature still works.
func TestBootstrap_TrustedSetupRuns(t *testing.T) {
	wt := t.TempDir()
	marker := filepath.Join(wt, "ran")
	res, err := Bootstrap(context.Background(), t.TempDir(), wt, Config{Setup: "touch " + marker}, 0, SetupTrust{Allowed: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("a trusted setup command must run: %v", err)
	}
	if !res.SetupOK || res.SetupSkipped {
		t.Errorf("SetupOK=%v SetupSkipped=%v, want true/false", res.SetupOK, res.SetupSkipped)
	}
}

// TestBootstrap_UntrustedSetupStillCopiesAndLinks: the trust gate covers the shell command only.
// Copying and symlinking stay put, so a steerer's worktree is usable rather than empty.
func TestBootstrap_UntrustedSetupStillCopiesAndLinks(t *testing.T) {
	repo, wt := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, ".env"), []byte("SECRET=1"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := Config{Setup: "exit 1", Copy: []string{".env"}}
	if _, err := Bootstrap(context.Background(), repo, wt, cfg, 0, SetupTrust{Reason: "nope"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(wt, ".env")); err != nil {
		t.Fatalf(".env should still be copied when only the setup command is withheld: %v", err)
	}
}

func TestBootstrap_SetupFailurePropagates(t *testing.T) {
	_, err := Bootstrap(context.Background(), t.TempDir(), t.TempDir(), Config{Setup: "exit 3"}, 0, SetupTrust{Allowed: true})
	if err == nil {
		t.Fatal("expected setup failure to propagate")
	}
}

// TestCopyPath_PreservesSymlinksAndCircular guards against copyPath dereferencing
// symlinks (which blows up node_modules copies) and recursing forever on circular
// symlinked directories.
func TestCopyPath_PreservesSymlinks(t *testing.T) {
	src := t.TempDir()
	// A regular file, a symlink to it, and a directory containing a symlink that
	// points back up to its own parent (a circular link, as pnpm trees produce).
	if err := os.WriteFile(filepath.Join(src, "real.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real.txt", filepath.Join(src, "link.txt")); err != nil {
		t.Skipf("symlinks unsupported: %v", err)
	}
	sub := filepath.Join(src, "pkg")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	// Circular: pkg/self -> .. (the src dir). Following this would recurse forever.
	if err := os.Symlink("..", filepath.Join(sub, "self")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	dst := filepath.Join(t.TempDir(), "out")
	if err := copyPath(src, dst); err != nil {
		t.Fatalf("copyPath: %v", err)
	}

	// The file symlink is recreated as a symlink, not dereferenced into a copy.
	fi, err := os.Lstat(filepath.Join(dst, "link.txt"))
	if err != nil {
		t.Fatalf("link.txt not copied: %v", err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Error("link.txt was dereferenced; want a symlink")
	}
	if target, _ := os.Readlink(filepath.Join(dst, "link.txt")); target != "real.txt" {
		t.Errorf("link.txt target = %q, want real.txt", target)
	}
	// The circular link is recreated as a link (no recursion / duplication).
	sfi, err := os.Lstat(filepath.Join(dst, "pkg", "self"))
	if err != nil {
		t.Fatalf("pkg/self not copied: %v", err)
	}
	if sfi.Mode()&os.ModeSymlink == 0 {
		t.Error("pkg/self was followed; want a symlink")
	}
	// The regular file still copied by value.
	if b, err := os.ReadFile(filepath.Join(dst, "real.txt")); err != nil || string(b) != "hi" {
		t.Fatalf("real.txt = %q, %v", b, err)
	}
}

func TestAllocPort_SkipsReserved(t *testing.T) {
	// Reserve the whole small range but one, and confirm we get the free one.
	reserved := map[int]bool{}
	lo, hi := 47210, 47212
	for p := lo; p <= hi; p++ {
		reserved[p] = true
	}
	delete(reserved, 47211)
	if p, ok := allocPort(lo, hi, reserved); !ok || p != 47211 {
		t.Fatalf("allocPort = %d,%v want 47211,true", p, ok)
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [20]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(b[pos:])
}

func TestBootstrap_LinksNodeModulesByDefault(t *testing.T) {
	repo := t.TempDir()
	wt := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, "node_modules", "left-pad"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "node_modules", "left-pad", "index.js"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := Bootstrap(context.Background(), repo, wt, Config{}, 0, SetupTrust{Allowed: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Linked) != 1 || res.Linked[0] != "node_modules" {
		t.Fatalf("expected node_modules auto-linked, got %v", res.Linked)
	}
	fi, err := os.Lstat(filepath.Join(wt, "node_modules"))
	if err != nil || fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("worktree node_modules should be a symlink (err=%v mode=%v)", err, fi.Mode())
	}
	if _, err := os.Stat(filepath.Join(wt, "node_modules", "left-pad", "index.js")); err != nil {
		t.Fatalf("shared package not reachable through the link: %v", err)
	}
}

func TestBootstrap_NoAutoLinkOptOut(t *testing.T) {
	repo := t.TempDir()
	wt := t.TempDir()
	_ = os.MkdirAll(filepath.Join(repo, "node_modules"), 0o755)
	res, err := Bootstrap(context.Background(), repo, wt, Config{NoAutoLink: true}, 0, SetupTrust{Allowed: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Linked) != 0 {
		t.Fatalf("NoAutoLink should skip linking, got %v", res.Linked)
	}
	if _, err := os.Lstat(filepath.Join(wt, "node_modules")); err == nil {
		t.Fatal("node_modules should NOT have been linked when NoAutoLink is set")
	}
}

func TestBootstrap_ExplicitLinkList(t *testing.T) {
	repo := t.TempDir()
	wt := t.TempDir()
	_ = os.MkdirAll(filepath.Join(repo, ".venv", "bin"), 0o755)
	res, err := Bootstrap(context.Background(), repo, wt, Config{Link: []string{".venv"}}, 0, SetupTrust{Allowed: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Linked) != 1 || res.Linked[0] != ".venv" {
		t.Fatalf("expected .venv linked, got %v", res.Linked)
	}
}
