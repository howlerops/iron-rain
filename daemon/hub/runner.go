package hub

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/howlerops/oculus/daemon/procutil"
	"github.com/howlerops/oculus/daemon/protocol"
)

// detectTestCommand picks a sensible test/build command from the project files in cwd.
// Returns nil if it can't tell (the caller then asks for an explicit command).
func detectTestCommand(cwd string) []string {
	has := func(f string) bool { _, err := os.Stat(filepath.Join(cwd, f)); return err == nil }
	switch {
	case has("go.mod"):
		return []string{"go", "test", "./..."}
	case has("Cargo.toml"):
		return []string{"cargo", "test"}
	case has("package.json"):
		return []string{"npm", "test", "--silent"}
	case has("pyproject.toml"), has("setup.py"), has("pytest.ini"), has("tox.ini"):
		return []string{"pytest"}
	case has("Package.swift"):
		return []string{"swift", "test"}
	case has("Makefile"), has("makefile"):
		return []string{"make", "test"}
	}
	return nil
}

// A run is bounded by TWO independent deadlines, because one of them cannot do the other's job.
//
// The inactivity budget is the one that catches real wedges. A build that is genuinely working keeps
// printing — compiler lines, test names, progress — so it renews its lease continuously and can run
// as long as it honestly needs. A command that is stuck does the opposite: it goes silent. The
// classic cases are a tool that decided to ask a question no one is there to answer ("Password:",
// "Overwrite? [y/N]", a keychain prompt), a process blocked on a lock another process will never
// release, or a network call with no timeout of its own. Wall-clock alone cannot tell those two
// apart: it kills the honest 40-minute build and spares the prompt-blocked one for exactly as long.
//
// The wall-clock budget only exists for what inactivity structurally cannot see: a run that keeps
// producing output forever. `npm test -- --watch` started by mistake, a retry loop, a suite that
// tails a log — all of them look permanently "active". Only a total limit stops those.
const (
	// runWallClock is the absolute ceiling on one run, however healthy it looks. Deliberately
	// generous: with inactivity doing the real wedge-detection this should only ever fire on
	// something that was never going to finish, and cutting short an honest full build/test matrix
	// is a worse outcome than holding the session's single run slot for a while longer.
	runWallClock = 60 * time.Minute

	// runInactivity is how long a run may print NOTHING before we call it wedged. Sized for the
	// quiet-but-working steps real suites actually contain: `go test ./...` prints nothing at all
	// while it compiles a cold package graph, an Xcode/`swift test` build is silent through large
	// compile and link phases, `cargo test` goes quiet linking a big binary, and a dependency fetch
	// can sit on one connection with no line-buffered progress. Those are minutes, not tens of
	// minutes. Past five minutes of total silence the overwhelmingly likely explanation is that the
	// command is waiting for a human who is not there.
	runInactivity = 5 * time.Minute

	// runWaitGrace bounds the final cmd.Wait once output has ended. procutil.Isolate already sets
	// WaitDelay so Wait can't block forever on a pipe a grandchild holds, but this function's
	// contract is that it ALWAYS returns and it must not stake that on os/exec internals.
	runWaitGrace = 5 * time.Second
)

// errRunStopped is handed to the read half of the output pipe when we reap a run, so anything still
// trying to write to it fails immediately instead of blocking on a reader that is gone.
var errRunStopped = errors.New("run stopped by the daemon")

// runTest runs a test/build command in the session's workspace, streaming each output line as
// a run.output event and finishing with a run.result. On failure it fires a TESTS_FAILED push
// so a remote user learns tests broke without watching. One run per session at a time.
func (h *Hub) runTest(sessionID, command string) {
	h.runTestLimits(sessionID, command, runWallClock, runInactivity, h.broadcast)
}

// runTestLimits is runTest with its two deadlines and its event sink injected. The seam exists for
// the tests: the behaviour that matters here is measured in minutes, and a suite that actually waited
// them out would never be run. emit is h.broadcast in production.
//
// The invariant this function must hold, on EVERY path, is: it returns, and it emits exactly one
// run.result. The app treats run.result as the only thing that ends a run, and the hub treats
// runningTests[sessionID] as the thing that gates the next one — so a run that never finishes doesn't
// just hang one spinner, it silently disables the test runner for that session until the daemon is
// restarted, with no error surfacing anywhere.
func (h *Hub) runTestLimits(sessionID, command string, wall, inactivity time.Duration, emit func(string, any)) {
	line := func(s string) { emit(protocol.TypeRunOutput, protocol.RunOutput{SessionID: sessionID, Line: s}) }

	// One run per session. Claimed BEFORE the deferred finisher below is registered, so that the
	// "someone else is already running" return is the one path that legitimately emits nothing and
	// must not clear a claim it doesn't own.
	h.mu.Lock()
	if h.runningTests == nil {
		h.runningTests = map[string]bool{}
	}
	if h.runningTests[sessionID] {
		h.mu.Unlock()
		return
	}
	h.runningTests[sessionID] = true
	label := ""
	if m := h.sessions[sessionID]; m != nil {
		label = m.meta.label
		if label == "" {
			label = m.meta.workspaceName
		}
	}
	h.mu.Unlock()

	cmdStr := command
	notifyOnFail := false // only a run that actually started can have "failed"
	var once sync.Once
	finish := func(ok bool, exit int) {
		once.Do(func() {
			// Release the claim BEFORE announcing the result. The app may start the next run the
			// instant it sees run.result, and being told "a run is already in progress" by a run
			// that has already ended is a race with no recovery visible to the user.
			h.mu.Lock()
			delete(h.runningTests, sessionID)
			h.mu.Unlock()
			emit(protocol.TypeRunResult, protocol.RunResult{SessionID: sessionID, Command: cmdStr, OK: ok, ExitCode: exit})
			if !ok && notifyOnFail {
				h.pushTestsFailed(sessionID, label, cmdStr)
			}
		})
	}
	// once makes a second result impossible; this deferred call makes zero results impossible —
	// including on a panic, and including on a future edit that adds a `return` and forgets to
	// report one. Both halves of the invariant are enforced structurally rather than by review.
	defer func() { finish(false, -1) }()

	roots := h.sessionRoots(sessionID)
	if len(roots) == 0 {
		return
	}
	cwd := roots[0]

	var argv []string
	if strings.TrimSpace(command) != "" {
		argv = []string{"/bin/sh", "-c", command}
	} else if argv = detectTestCommand(cwd); argv == nil {
		line("No test command detected for this project — pass one explicitly.")
		return
	}
	if cmdStr == "" {
		cmdStr = strings.Join(argv, " ")
	}

	line("$ " + cmdStr)

	// Deliberately NOT exec.CommandContext. Its cancellation kills only the direct child — here the
	// /bin/sh — and leaves everything sh spawned still running: a dev server the test suite booted, a
	// file watcher, a compiler. Those grandchildren inherit the output pipe, so the pipe never reaches
	// EOF and the read loop below blocks forever on a "timed out" run. procutil.Isolate puts the run in
	// its own process GROUP so procutil.TerminateGroup can take the whole tree down at once; the
	// watchdog owns every deadline, which leaves a context nothing to do.
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Dir = cwd
	procutil.Isolate(cmd)
	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw
	if err := cmd.Start(); err != nil {
		line(err.Error())
		return
	}
	notifyOnFail = true
	done := make(chan error, 1)
	go func() { done <- cmd.Wait(); pw.Close() }()

	// reap ends the run from outside the read loop. Ordering is the whole point: kill the process
	// GROUP first so nothing can emit another byte, then close the read half of the pipe. That close
	// is what actually unblocks the scanner — an io.Pipe reaches EOF only when every writer has
	// closed, and a surviving grandchild holding the write end never will. Waiting for the scanner to
	// notice on its own is precisely the hang this function used to die in, so we don't.
	reap := func(why string) {
		line(why)
		procutil.TerminateGroup(cmd)
		pr.CloseWithError(errRunStopped)
	}

	// Watchdog. lastOut is the only state shared with the read loop: it stamps every line, we compare
	// the stamp against the inactivity budget. Polling rather than a resettable timer keeps the
	// hot path (one atomic store per output line) free and sidesteps the drain-the-channel race that
	// timer.Reset invites; at minute-scale budgets the sampling granularity costs nothing.
	var lastOut atomic.Int64
	lastOut.Store(time.Now().UnixNano())
	stopped := make(chan struct{}) // closed by the read loop once output has really ended
	go func() {
		start := time.Now()
		tick := min(inactivity/4, wall/4, 5*time.Second)
		if tick < time.Millisecond {
			tick = time.Millisecond
		}
		t := time.NewTicker(tick)
		defer t.Stop()
		for {
			select {
			case <-stopped:
				return
			case now := <-t.C:
				if now.Sub(start) >= wall {
					reap(fmt.Sprintf("— still running after %s — stopped by the daemon (total time limit)", wall))
					return
				}
				if now.Sub(time.Unix(0, lastOut.Load())) >= inactivity {
					reap(fmt.Sprintf("— no output for %s — stopped by the daemon (assumed stuck)", inactivity))
					return
				}
			}
		}
	}()

	sc := bufio.NewScanner(pr)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		lastOut.Store(time.Now().UnixNano())
		line(sc.Text())
	}
	close(stopped)

	ok, exit := false, -1
	select {
	case waitErr := <-done:
		ok = waitErr == nil
		if cmd.ProcessState != nil {
			exit = cmd.ProcessState.ExitCode()
		} else if ok {
			exit = 0
		}
	case <-time.After(runWaitGrace):
		// Wait is still in flight, so cmd.ProcessState is off-limits — reading it here would be a
		// data race, not merely a stale value. Report the run as failed, make sure the tree really is
		// dead, and return; the goroutine parked in Wait finishes on its own.
		line("— the run did not exit after being stopped")
		procutil.TerminateGroup(cmd)
	}
	finish(ok, exit)
}
