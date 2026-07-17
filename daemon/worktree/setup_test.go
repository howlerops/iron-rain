package worktree

import (
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
	res, err := Bootstrap(repo, worktree, cfg, port)
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

func TestBootstrap_SetupFailurePropagates(t *testing.T) {
	_, err := Bootstrap(t.TempDir(), t.TempDir(), Config{Setup: "exit 3"}, 0)
	if err == nil {
		t.Fatal("expected setup failure to propagate")
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
