package hub

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/protocol"
)

// runEvents captures what a run would have broadcast. Standing up a transport client just to read
// two event types would make these tests about the socket instead of about the reaper.
type runEvents struct {
	mu      sync.Mutex
	lines   []string
	results []protocol.RunResult
}

func (r *runEvents) emit(typ string, payload any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch v := payload.(type) {
	case protocol.RunOutput:
		r.lines = append(r.lines, v.Line)
	case protocol.RunResult:
		r.results = append(r.results, v)
	}
}

func (r *runEvents) text() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.lines, "\n")
}

func (r *runEvents) only(t *testing.T) protocol.RunResult {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.results) != 1 {
		t.Fatalf("want exactly ONE run.result (the app ends a run on nothing else), got %d: %+v",
			len(r.results), r.results)
	}
	return r.results[0]
}

// runnerHub is a hub with one session rooted at dir — the minimum runTestLimits needs.
func runnerHub(dir string) *Hub {
	return &Hub{sessions: map[string]*managedSession{
		"s": newManagedSession(nil, nil, sessionMeta{cwd: dir}),
	}}
}

// runBounded runs fn and fails if it hasn't returned in d. Every test here would otherwise HANG on a
// regression rather than fail, which is the same failure mode as the bug.
func runBounded(t *testing.T, d time.Duration, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); fn() }()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("runTestLimits never returned after %s — the read loop is blocked", d)
	}
}

func pidAlive(pid int) bool { return syscall.Kill(pid, 0) == nil }

func waitPidGone(pid int, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if !pidAlive(pid) {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return !pidAlive(pid)
}

func assertRunSlotFree(t *testing.T, h *Hub) {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.runningTests["s"] {
		t.Fatal("runningTests[s] still set — the session's test runner is now permanently disabled")
	}
}

// TestRunTestReapsGrandchildren is the hang this whole change exists for. A test command that leaves a
// background child behind (a dev server the suite booted, a watcher) hands that child the run's output
// pipe. Killing only the direct sh — all exec.CommandContext ever did — leaves the pipe open, so the
// read loop never sees EOF, runTest never returns, no run.result is ever sent, and runningTests stays
// set forever: the test runner silently dies for that session with no error anywhere.
func TestRunTestReapsGrandchildren(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "grandchild.pid")
	// Prints once (so we know it started), forks a long sleeper that outlives it, then goes quiet.
	script := "sleep 60 & echo $! > " + pidFile + "; echo started; sleep 60"

	h := runnerHub(dir)
	ev := &runEvents{}
	runBounded(t, 15*time.Second, func() {
		h.runTestLimits("s", script, 20*time.Second, 400*time.Millisecond, ev.emit)
	})

	res := ev.only(t)
	if res.OK {
		t.Error("a reaped run must report failure")
	}
	if !strings.Contains(ev.text(), "no output for") {
		t.Errorf("the user must be TOLD why the run died, got:\n%s", ev.text())
	}
	assertRunSlotFree(t, h)

	b, err := os.ReadFile(pidFile)
	if err != nil {
		t.Fatalf("grandchild never recorded its pid: %v", err)
	}
	gpid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || gpid <= 0 {
		t.Fatalf("bad grandchild pid %q", b)
	}
	if !waitPidGone(gpid, 3*time.Second) {
		_ = syscall.Kill(gpid, syscall.SIGKILL) // don't leak a 60s sleeper out of a failing test
		t.Fatal("GRANDCHILD SURVIVED — the run was not reaped as a process group")
	}
}

// TestRunTestInactivityReapsQuietCommand covers the plainest wedge: a command that never prints at
// all, the shape of anything blocked on a prompt no one will answer.
func TestRunTestInactivityReapsQuietCommand(t *testing.T) {
	h := runnerHub(t.TempDir())
	ev := &runEvents{}
	start := time.Now()
	runBounded(t, 15*time.Second, func() {
		h.runTestLimits("s", "sleep 60", 30*time.Second, 300*time.Millisecond, ev.emit)
	})
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Errorf("inactivity budget was 300ms but the run took %s", elapsed)
	}
	if res := ev.only(t); res.OK {
		t.Error("a reaped run must report failure")
	}
	if !strings.Contains(ev.text(), "no output for") {
		t.Errorf("missing the reason line, got:\n%s", ev.text())
	}
	assertRunSlotFree(t, h)
}

// TestRunTestInactivitySparesChattyCommand is the other half of the point of an inactivity budget: a
// run that keeps printing is WORKING, and must survive well past the budget. A wall-clock limit alone
// cannot make this distinction, which is why both exist.
func TestRunTestInactivitySparesChattyCommand(t *testing.T) {
	h := runnerHub(t.TempDir())
	ev := &runEvents{}
	// ~1.2s of output in 150ms steps, against a 400ms silence budget: only a clock that resets on
	// every line lets this finish.
	script := "for i in 1 2 3 4 5 6 7 8; do echo tick $i; sleep 0.15; done"
	runBounded(t, 30*time.Second, func() {
		h.runTestLimits("s", script, 60*time.Second, 400*time.Millisecond, ev.emit)
	})

	res := ev.only(t)
	if !res.OK || res.ExitCode != 0 {
		t.Fatalf("a slow but chatty command must run to completion, got ok=%v exit=%d output:\n%s",
			res.OK, res.ExitCode, ev.text())
	}
	if strings.Contains(ev.text(), "stopped by the daemon") {
		t.Errorf("healthy run was reaped:\n%s", ev.text())
	}
	if !strings.Contains(ev.text(), "tick 8") {
		t.Errorf("run was cut short before its last line:\n%s", ev.text())
	}
	assertRunSlotFree(t, h)
}

// TestRunTestWallClockStopsEndlessChatter covers what inactivity structurally cannot see: a command
// that prints forever (a watcher started by mistake, a retry loop) looks permanently healthy.
func TestRunTestWallClockStopsEndlessChatter(t *testing.T) {
	h := runnerHub(t.TempDir())
	ev := &runEvents{}
	runBounded(t, 20*time.Second, func() {
		h.runTestLimits("s", "while :; do echo spam; sleep 0.05; done", 700*time.Millisecond, 30*time.Second, ev.emit)
	})
	if res := ev.only(t); res.OK {
		t.Error("a reaped run must report failure")
	}
	if !strings.Contains(ev.text(), "still running after") {
		t.Errorf("the total-time reap must name itself, got tail:\n%s", tail(ev.text()))
	}
	assertRunSlotFree(t, h)
}

// TestRunTestOneResultOnEveryPath pins the invariant on the paths that never reach a process: an
// unknown session, a project with no detectable test command, and a command that fails to start.
func TestRunTestOneResultOnEveryPath(t *testing.T) {
	t.Run("no session roots", func(t *testing.T) {
		h := &Hub{sessions: map[string]*managedSession{}}
		ev := &runEvents{}
		runBounded(t, 5*time.Second, func() {
			h.runTestLimits("s", "echo hi", time.Minute, time.Minute, ev.emit)
		})
		if res := ev.only(t); res.OK {
			t.Error("want a failed result")
		}
		assertRunSlotFree(t, h)
	})

	t.Run("no detectable command", func(t *testing.T) {
		h := runnerHub(t.TempDir()) // empty dir: no go.mod, no package.json, nothing
		ev := &runEvents{}
		runBounded(t, 5*time.Second, func() {
			h.runTestLimits("s", "", time.Minute, time.Minute, ev.emit)
		})
		if res := ev.only(t); res.OK {
			t.Error("want a failed result")
		}
		if !strings.Contains(ev.text(), "No test command detected") {
			t.Errorf("want the explanatory line, got:\n%s", ev.text())
		}
		assertRunSlotFree(t, h)
	})

	t.Run("failed start", func(t *testing.T) {
		// A cwd that doesn't exist makes exec.Start fail before the process ever runs.
		h := runnerHub(filepath.Join(t.TempDir(), "does-not-exist"))
		ev := &runEvents{}
		runBounded(t, 5*time.Second, func() {
			h.runTestLimits("s", "echo hi", time.Minute, time.Minute, ev.emit)
		})
		if res := ev.only(t); res.OK {
			t.Error("want a failed result")
		}
		assertRunSlotFree(t, h)
	})

	t.Run("normal failure keeps its exit code", func(t *testing.T) {
		h := runnerHub(t.TempDir())
		ev := &runEvents{}
		runBounded(t, 10*time.Second, func() {
			h.runTestLimits("s", "echo boom; exit 3", time.Minute, time.Minute, ev.emit)
		})
		res := ev.only(t)
		if res.OK || res.ExitCode != 3 {
			t.Fatalf("want ok=false exit=3, got ok=%v exit=%d", res.OK, res.ExitCode)
		}
		if !strings.Contains(ev.text(), "boom") {
			t.Errorf("output was not streamed:\n%s", ev.text())
		}
		assertRunSlotFree(t, h)
	})
}

// TestRunTestSecondRunIsRejectedSilently guards the one path that deliberately emits nothing. It must
// not clear the in-flight run's claim, and it must not send a result the app would attribute to that
// still-running run.
func TestRunTestSecondRunIsRejectedSilently(t *testing.T) {
	h := runnerHub(t.TempDir())
	first := &runEvents{}
	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		h.runTestLimits("s", "for i in 1 2 3 4 5 6; do echo tick; sleep 0.1; done", 30*time.Second, 5*time.Second, first.emit)
	}()

	// Wait until the first run has actually claimed the slot.
	claimed := false
	for i := 0; i < 300; i++ {
		h.mu.Lock()
		claimed = h.runningTests["s"]
		h.mu.Unlock()
		if claimed {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !claimed {
		t.Fatal("first run never claimed the session's run slot")
	}

	second := &runEvents{}
	runBounded(t, 5*time.Second, func() {
		h.runTestLimits("s", "echo second", 30*time.Second, 5*time.Second, second.emit)
	})
	second.mu.Lock()
	nres, nlines := len(second.results), len(second.lines)
	second.mu.Unlock()
	if nres != 0 || nlines != 0 {
		t.Fatalf("a rejected run must emit nothing, got %d results / %d lines", nres, nlines)
	}

	select {
	case <-firstDone:
	case <-time.After(30 * time.Second):
		t.Fatal("first run never finished")
	}
	if res := first.only(t); !res.OK {
		t.Errorf("the in-flight run should have finished normally, got ok=%v output:\n%s", res.OK, first.text())
	}
	assertRunSlotFree(t, h)
}

// tail keeps a noisy run's output readable in a failure message.
func tail(s string) string {
	lines := strings.Split(s, "\n")
	if len(lines) > 6 {
		lines = lines[len(lines)-6:]
	}
	return strings.Join(lines, "\n")
}
