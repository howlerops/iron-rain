package cli

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Stopping a CLI turn has to kill what the agent FORKED, not just the agent.
//
// cli calls procutil.Isolate with a comment saying "a CLI agent forks compilers/test runners —
// Stop() must kill the tree", and then relied on cancel() alone. procutil's own package doc says
// what that does: exec.CommandContext kills the direct child and leaves the grandchildren running.
// Isolate makes it worse in isolation — the child gets its OWN process group, so the daemon's group
// signals do not reach it either.
//
// A codex/gemini/aider turn that runs `npm test` and is then stopped therefore left the test runner
// alive: reparented to launchd, holding CPU, ports and file handles, invisible to the daemon, once
// per interrupted turn. claudecode already fixed exactly this and its comment records what it cost —
// 143 orphaned sidecars, 284 processes, 12.7 GB resident.
const forkingAgent = `#!/bin/sh
sleep 300 &
echo $! > "$OCULUS_TEST_PIDFILE"
echo "working"
sleep 300
`

func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

func waitGone(pid int, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if !alive(pid) {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return !alive(pid)
}

func TestStopKillsTheWholeProcessTree(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "agent.sh")
	if err := os.WriteFile(script, []byte(forkingAgent), 0o755); err != nil {
		t.Fatal(err)
	}
	pidFile := filepath.Join(dir, "child.pid")
	t.Setenv("OCULUS_TEST_PIDFILE", pidFile)

	p := NewProvider(Config{Name: "faker", Command: script, Args: []string{"{prompt}"}})
	sess, err := p.Create(context.Background(), dir, "")
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	go func() {
		for range sess.Events() {
		}
	}()
	if err := sess.Prompt(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}

	pid := waitForPid(t, pidFile)
	if !alive(pid) {
		t.Fatalf("the forked child (pid %d) never started; this test would prove nothing", pid)
	}

	if err := sess.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !waitGone(pid, 5*time.Second) {
		t.Errorf("the agent's forked child (pid %d) SURVIVED Stop(). Every interrupted turn leaves "+
			"its test runner or compiler running, reparented to launchd, holding CPU and ports, with "+
			"nothing in the daemon aware of it.", pid)
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}

// Close is the other entry point — deleting a session, or the daemon shutting down — and it has the
// same obligation.
func TestCloseKillsTheWholeProcessTree(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "agent.sh")
	if err := os.WriteFile(script, []byte(forkingAgent), 0o755); err != nil {
		t.Fatal(err)
	}
	pidFile := filepath.Join(dir, "child.pid")
	t.Setenv("OCULUS_TEST_PIDFILE", pidFile)

	p := NewProvider(Config{Name: "faker", Command: script, Args: []string{"{prompt}"}})
	sess, err := p.Create(context.Background(), dir, "")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for range sess.Events() {
		}
	}()
	if err := sess.Prompt(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}
	pid := waitForPid(t, pidFile)

	if err := sess.Close(); err != nil {
		t.Fatal(err)
	}
	if !waitGone(pid, 5*time.Second) {
		t.Errorf("the agent's forked child (pid %d) survived Close()", pid)
		_ = syscall.Kill(pid, syscall.SIGKILL)
	}
}

func waitForPid(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if b, err := os.ReadFile(path); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && pid > 0 {
				return pid
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the agent never recorded its forked child's pid")
	return 0
}
