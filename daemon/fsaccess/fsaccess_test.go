package fsaccess

import (
	"os"
	"path/filepath"
	"runtime"
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
