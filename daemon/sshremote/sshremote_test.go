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
	// A fixed-option runner (no keepalive noise) so the invocation shape is exact.
	r := &Runner{SSHOptions: []string{"-o", "BatchMode=yes", "-o", "ConnectTimeout=8"}}
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

func TestPortForwardArgsInInvocation(t *testing.T) {
	r := &Runner{SSHOptions: []string{"-o", "BatchMode=yes"}}
	h := Host{SSHTarget: "box", Forwards: []PortForward{{LocalPort: 3000, RemotePort: 3000}, {LocalPort: 8080, RemotePort: 80}}}
	args := r.sshArgs(h, "true")
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "-L 3000:localhost:3000") {
		t.Errorf("missing first forward: %v", args)
	}
	if !strings.Contains(joined, "-L 8080:localhost:80") {
		t.Errorf("missing second forward: %v", args)
	}
	// Forwards come before the target, which is before the command.
	iL := indexOf(args, "3000:localhost:3000")
	iTarget := indexOf(args, "box")
	if iL < 0 || iTarget < 0 || iL > iTarget {
		t.Errorf("forwards must precede the target: %v", args)
	}
}

func TestKeepaliveOptionsPresent(t *testing.T) {
	joined := strings.Join(New().SSHOptions, " ")
	if !strings.Contains(joined, "ServerAliveInterval=15") || !strings.Contains(joined, "ServerAliveCountMax=3") {
		t.Errorf("keepalive options missing: %v", New().SSHOptions)
	}
}

func TestRunWithRetryReconnectsThenSucceeds(t *testing.T) {
	calls := 0
	r := &Runner{
		SSHOptions: []string{},
		Retries:    2,
		Exec: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			calls++
			if calls < 3 {
				return []byte("kex_exchange_identification: Connection closed by remote host"), errFake("connection closed")
			}
			return []byte("ok"), nil
		},
	}
	out, err := r.RunWithRetry(context.Background(), Host{SSHTarget: "box"}, "true")
	if err != nil {
		t.Fatalf("should have recovered: %v", err)
	}
	if calls != 3 {
		t.Errorf("expected 3 attempts (2 transient failures + success), got %d", calls)
	}
	if !strings.Contains(out, "ok") {
		t.Errorf("out = %q", out)
	}
}

func TestRunWithRetryDoesNotRetryRealFailure(t *testing.T) {
	calls := 0
	r := &Runner{
		Retries: 3,
		Exec: func(ctx context.Context, name string, args ...string) ([]byte, error) {
			calls++
			return []byte("fatal: not a git repository"), errFake("exit status 128")
		},
	}
	if _, err := r.RunWithRetry(context.Background(), Host{SSHTarget: "box"}, "git status"); err == nil {
		t.Fatal("expected the command failure to propagate")
	}
	if calls != 1 {
		t.Errorf("a non-transient failure must not be retried; got %d calls", calls)
	}
}

func indexOf(ss []string, sub string) int {
	for i, s := range ss {
		if strings.Contains(s, sub) {
			return i
		}
	}
	return -1
}

type errFake string

func (e errFake) Error() string { return string(e) }

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
