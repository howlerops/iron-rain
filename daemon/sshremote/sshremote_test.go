package sshremote

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSSHArgsConstruction(t *testing.T) {
	r := New()
	h := Host{SSHTarget: "jacob@build-box", RemotePath: "/home/jacob/proj"}
	args := r.sshArgs(h, "git status")
	want := []string{"-o", "BatchMode=yes", "-o", "ConnectTimeout=8", "jacob@build-box", "git status"}
	if strings.Join(args, "|") != strings.Join(want, "|") {
		t.Fatalf("sshArgs = %v, want %v", args, want)
	}
}

func TestInDirWrapsAndQuotes(t *testing.T) {
	h := Host{RemotePath: "/home/jacob/my proj"}
	got := h.inDir("git status --porcelain")
	want := "cd '/home/jacob/my proj' && git status --porcelain"
	if got != want {
		t.Fatalf("inDir = %q, want %q", got, want)
	}
	// No path → run as-is.
	if (Host{}).inDir("git status") != "git status" {
		t.Fatal("empty path should not wrap")
	}
}

// TestGitStatusInvokesRightCommand uses a fake exec to assert the exact ssh invocation, without a
// real remote — deep coverage of the transport wiring.
func TestGitStatusInvokesRightCommand(t *testing.T) {
	var gotName string
	var gotArgs []string
	r := &Runner{
		SSHOptions: []string{"-o", "BatchMode=yes"},
		Exec: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			gotName = name
			gotArgs = args
			return []byte(" M file.go\n"), nil
		},
	}
	h := Host{SSHTarget: "box", RemotePath: "/repo"}
	out, err := r.GitStatus(context.Background(), h)
	if err != nil {
		t.Fatal(err)
	}
	if gotName != "ssh" {
		t.Errorf("ran %q, want ssh", gotName)
	}
	last := gotArgs[len(gotArgs)-1]
	if last != "cd '/repo' && git status --porcelain" {
		t.Errorf("remote cmd = %q", last)
	}
	if !strings.Contains(out, "file.go") {
		t.Errorf("output not returned: %q", out)
	}
}

func TestRunErrorsWithoutTarget(t *testing.T) {
	r := New()
	if _, err := r.Run(context.Background(), Host{}, "true"); err == nil {
		t.Fatal("expected error for empty ssh target")
	}
}

// TestLoopbackGitStatus is a REAL integration test: if this machine can ssh to localhost
// non-interactively, run GitStatus against a real local repo over ssh. Skipped where sshd/keys
// aren't set up (most CI), so it never flakes — but gives true end-to-end coverage on a dev box.
func TestLoopbackGitStatus(t *testing.T) {
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skip("no ssh binary")
	}
	// Can we ssh to localhost without a prompt?
	probe := exec.Command("ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=4", "localhost", "true")
	if err := probe.Run(); err != nil {
		t.Skip("ssh localhost not available non-interactively; skipping loopback test")
	}

	repo := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"}, {"config", "user.email", "t@t"}, {"config", "user.name", "t"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", repo}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v (%s)", args, err, out)
		}
	}
	// Create an uncommitted file so status is non-empty.
	if err := os.WriteFile(filepath.Join(repo, "dirty.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := New()
	h := Host{SSHTarget: "localhost", RemotePath: repo}
	if err := r.Probe(context.Background(), h); err != nil {
		t.Fatalf("Probe: %v", err)
	}
	out, err := r.GitStatus(context.Background(), h)
	if err != nil {
		t.Fatalf("GitStatus over ssh: %v", err)
	}
	if !strings.Contains(out, "dirty.txt") {
		t.Errorf("remote git status over ssh missing the dirty file: %q", out)
	}
}
