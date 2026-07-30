// Package procutil is the daemon's one place for child-process hygiene.
//
// Every long-lived child we spawn (the claude-code sidecar, pi, a BYO CLI agent, the managed
// opencode server, an LSP server, an MCP server) is really a process TREE: `npx foo` is a node
// wrapper that forks the real server, `pi` may shell out, a CLI agent spawns compilers. Killing only
// the direct child — which is all exec.CommandContext and Process.Kill() do — leaves the grandchildren
// running, holding ports and file handles, invisible to the daemon that started them.
//
// Setpgid puts each child in its OWN process group; killing the NEGATIVE pid then signals the whole
// group. That is the difference between "the session ended" and "the session ended and a node process
// is still pinning port 4096 an hour later".
//
// Use it as:
//
//	cmd := exec.CommandContext(ctx, bin, args...)
//	procutil.Isolate(cmd)          // before Start
//	...
//	procutil.TerminateGroup(cmd)   // graceful TERM, then KILL, for the whole tree
package procutil

import (
	"os/exec"
	"syscall"
	"time"
)

// killGrace is how long a child's process group gets to exit after SIGTERM before we SIGKILL it.
// Short on purpose: this runs on daemon shutdown, where the user is waiting.
const killGrace = 500 * time.Millisecond

// Isolate prepares cmd so it can be terminated as a tree. It must be called BEFORE cmd.Start().
//
// Setpgid makes the child a process-group leader (pgid == its pid), so TerminateGroup can signal the
// child and everything it spawns. WaitDelay bounds the other half of the problem: when a context is
// cancelled, exec kills the direct child but Wait() can still block forever on inherited stdout/stderr
// pipes held open by a surviving grandchild. WaitDelay forces those pipes closed so Wait returns.
func Isolate(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
	if cmd.WaitDelay == 0 {
		cmd.WaitDelay = killGrace
	}
}

// TerminateGroup signals the child's whole process group: SIGTERM, a short grace period, then
// SIGKILL. It is safe to call on a process that has already exited, on one that was never started,
// and more than once.
//
// It deliberately does NOT call Wait — the goroutine that started the process owns that. Callers who
// need to know when it's really gone should Wait separately.
func TerminateGroup(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	pid := cmd.Process.Pid
	if pid <= 0 {
		return
	}
	// Negative pid = "the whole process group". Fall back to the bare process if the group is gone
	// (ESRCH) or was never created because Isolate wasn't called.
	if err := syscall.Kill(-pid, syscall.SIGTERM); err != nil {
		_ = cmd.Process.Signal(syscall.SIGTERM)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 10; i++ {
			time.Sleep(killGrace / 10)
			// Signal 0 probes liveness without delivering anything.
			if syscall.Kill(-pid, 0) != nil {
				return // group is gone
			}
		}
	}()
	select {
	case <-done:
		return
	case <-time.After(killGrace):
	}
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil {
		_ = cmd.Process.Kill()
	}
}
