package procutil

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// alive reports whether pid still exists (signal 0 delivers nothing, it only probes).
func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

// waitGone polls until pid disappears or the deadline passes.
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

// TestTerminateGroupKillsGrandchildren is the whole reason this package exists. `npx foo` is a node
// wrapper that forks the real server; killing only the direct child (all exec.CommandContext and
// Process.Kill do) leaves that grandchild running, holding its port, invisible to the daemon.
func TestTerminateGroupKillsGrandchildren(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "grandchild.pid")
	// A shell that forks a long sleeper (the "grandchild"), records its pid, then waits.
	script := "sleep 60 & echo $! > " + pidFile + "; sleep 60"
	cmd := exec.Command("sh", "-c", script)
	Isolate(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	// Reap concurrently. Without a live Wait the killed child lingers as a ZOMBIE, and a zombie still
	// answers kill(pid, 0) — so polling liveness would report it "alive" forever.
	waited := make(chan struct{})
	go func() { _, _ = cmd.Process.Wait(); close(waited) }()

	// Wait for the grandchild pid to be recorded.
	var gpid int
	for i := 0; i < 200; i++ {
		if b, err := os.ReadFile(pidFile); err == nil {
			if p, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && p > 0 {
				gpid = p
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if gpid == 0 {
		t.Fatal("grandchild never recorded its pid")
	}
	if !alive(gpid) {
		t.Fatal("grandchild should be running before we terminate")
	}

	TerminateGroup(cmd)

	select {
	case <-waited:
	case <-time.After(2 * time.Second):
		t.Error("direct child survived TerminateGroup")
	}
	if !waitGone(gpid, 2*time.Second) {
		// Clean up so a failing test doesn't leak a 60s sleeper.
		_ = syscall.Kill(gpid, syscall.SIGKILL)
		t.Fatal("GRANDCHILD SURVIVED — process-group kill did not reach the whole tree")
	}
}

// TestTerminateGroupIsSafeOnDeadAndUnstarted guards the paths that run during shutdown races.
func TestTerminateGroupIsSafeOnDeadAndUnstarted(t *testing.T) {
	TerminateGroup(nil)
	TerminateGroup(exec.Command("true")) // never started: Process is nil

	cmd := exec.Command("true")
	Isolate(cmd)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}
	_ = cmd.Wait()
	TerminateGroup(cmd) // already reaped
	TerminateGroup(cmd) // idempotent
}

// TestIsolateSetsProcessGroupAndWaitDelay locks the two settings the rest of the daemon relies on.
func TestIsolateSetsProcessGroupAndWaitDelay(t *testing.T) {
	cmd := exec.Command("true")
	Isolate(cmd)
	if cmd.SysProcAttr == nil || !cmd.SysProcAttr.Setpgid {
		t.Error("Isolate must set Setpgid so the child leads its own process group")
	}
	if cmd.WaitDelay == 0 {
		t.Error("Isolate must set WaitDelay so Wait can't block forever on a grandchild-held pipe")
	}

	// Isolate must not clobber a SysProcAttr the caller already configured.
	pre := exec.Command("true")
	pre.SysProcAttr = &syscall.SysProcAttr{Foreground: false}
	pre.WaitDelay = 3 * time.Second
	Isolate(pre)
	if !pre.SysProcAttr.Setpgid || pre.WaitDelay != 3*time.Second {
		t.Error("Isolate must preserve a caller-set WaitDelay and augment existing SysProcAttr")
	}
}
