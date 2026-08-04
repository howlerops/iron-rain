package claudecode

// Startup sweep: reap claude-code sidecars left behind by a PREVIOUS daemon instance.
//
// The other two layers cover every exit the daemon can participate in — the sidecar exits on stdin
// EOF (sidecar.mjs), and Hub.Shutdown explicitly Closes every session on a graceful stop. Neither
// covers SIGKILL: `kill -9`, an OOM kill, a panic in the runtime, a hard power loss. No cleanup code
// runs there by definition, and because procutil.Isolate puts each sidecar in its own process group
// the survivors are not signalled either — they are reparented to launchd and keep running, each one
// holding an Agent SDK connection and a live `claude` child of its own, for as long as the machine
// stays up. Restart the daemon a few times after crashes and you get what was measured on one user's
// machine: 143 orphaned sidecars, 284 processes counting the children, 12.7 GB resident, the oldest a
// week old. This layer is the only thing that can ever end those, and startup is the only moment it
// can run.
//
// SAFETY is the entire design of this file. Killing a sidecar that belongs to a LIVE daemon would
// destroy a user's running work, and running more than one daemon at a time is legitimate. The whole
// decision lives in sweepable, which is a pure function precisely so it can be tested exhaustively
// without signalling anything.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// sweepGrace is how long the batch of orphans gets between SIGTERM and SIGKILL. One shared window for
// the whole batch rather than one each: this runs on the daemon's startup path, which the user is
// waiting on, and the processes die independently.
const sweepGrace = 500 * time.Millisecond

// proc is one row of the process table — only as much of it as the sweep decision needs.
type proc struct {
	pid, ppid, pgid, uid int
	command              string
}

// sweepable reports whether p is an orphaned sidecar that THIS daemon may kill. Every clause is a
// veto, and each one protects a specific process we must never touch:
//
//   - ppid == 1 is the decisive one. A sidecar's parent is always the daemon that spawned it — we
//     start it with a plain exec.Command, there is no double fork anywhere in the path — so for as
//     long as its daemon lives, its ppid is that daemon's pid. ppid 1 means it has been reparented to
//     launchd/init, which can only happen once its parent is gone. That single fact is what makes a
//     second, LIVE daemon's sidecars unkillable by this sweep: theirs still point at it.
//   - uid must be ours. We never signal another user's processes whatever they are running, and the
//     daemon has no business doing so even when it could.
//   - argv[0] must be the node runtime and one of its arguments must be our EXACT sidecar path. Not
//     "node", not "contains the word sidecar" — the absolute path this daemon was configured with,
//     matched as a whole argv element. A user's own node server does not match; a sidecar from a
//     different install or checkout does not match; and requiring argv[0] to be node means a `grep`
//     or an editor that merely happens to have the path on its command line does not match either.
//   - pid > 1 and pid != ours: paranoia against a malformed ps row costing us init or the daemon.
//
// Note the failure mode of every clause is "we do not kill it". If the sidecar is ever launched some
// other way (a wrapper script, a node named something else), this degrades to a no-op rather than to
// a wrong kill, which is the only acceptable direction for it to be wrong in.
func sweepable(p proc, sidecarPath string, selfPID, selfUID int) bool {
	if sidecarPath == "" || !filepath.IsAbs(sidecarPath) {
		return false // a relative or unresolved path is far too weak a thing to kill on
	}
	if p.pid <= 1 || p.pid == selfPID {
		return false
	}
	if p.ppid != 1 {
		return false // still has a live parent, and that parent may be another daemon
	}
	if p.uid != selfUID {
		return false
	}
	fields := strings.Fields(p.command)
	if len(fields) < 2 || filepath.Base(fields[0]) != "node" {
		return false
	}
	for _, arg := range fields[1:] {
		if arg == sidecarPath {
			return true
		}
	}
	return false
}

// parsePS turns one `ps -axwwo pid=,ppid=,pgid=,uid=,command=` line into a proc. A line we cannot
// parse cleanly is rejected rather than guessed at — this feeds a kill decision. Whitespace in the
// command is collapsed, which is harmless because sweepable re-splits it into argv fields anyway;
// the one thing it costs is that a sidecar installed under a path containing a space will not match,
// and so will not be killed, which is the safe direction.
func parsePS(line string) (proc, bool) {
	f := strings.Fields(line)
	if len(f) < 5 {
		return proc{}, false
	}
	nums := make([]int, 4)
	for i := 0; i < 4; i++ {
		n, err := strconv.Atoi(f[i])
		if err != nil {
			return proc{}, false
		}
		nums[i] = n
	}
	return proc{pid: nums[0], ppid: nums[1], pgid: nums[2], uid: nums[3], command: strings.Join(f[4:], " ")}, true
}

// listProcs snapshots the process table via ps(1). There is no portable syscall for this and the
// daemon's primary platform is macOS, which has no /proc, so ps is the tool. The `=` suffix on each
// column suppresses the header, making every line pure data.
func listProcs() ([]proc, error) {
	out, err := exec.Command("ps", "-axwwo", "pid=,ppid=,pgid=,uid=,command=").Output()
	if err != nil {
		return nil, err
	}
	var procs []proc
	for _, line := range strings.Split(string(out), "\n") {
		if p, ok := parsePS(line); ok {
			procs = append(procs, p)
		}
	}
	return procs, nil
}

// signalOrphan delivers sig to one orphan. It signals the process GROUP only when the process leads
// its own group (pgid == pid, which procutil.Isolate guarantees for anything the daemon spawned),
// because a negative pid names an entire group and signalling one we do not own could reach processes
// that have nothing to do with us. A non-leader gets the bare pid instead: that leaves its children
// behind, which is worse for us and still strictly safer for the user.
func signalOrphan(p proc, sig syscall.Signal) {
	if p.pgid == p.pid {
		if err := syscall.Kill(-p.pid, sig); err == nil {
			return
		}
	}
	_ = syscall.Kill(p.pid, sig)
}

// SweepOrphanSidecars kills claude-code sidecars left behind by a previous daemon instance and
// returns the pids it signalled. sidecarPath must be the absolute path to the sidecar.mjs this daemon
// runs; anything else is ignored (see sweepable).
//
// Called once per daemon start, from New — before this daemon has spawned any child of its own, which
// is what makes "ppid == 1 means abandoned" unambiguous here. SIGTERM first so a sidecar new enough
// to have the EOF/signal handling can shut the Agent SDK down cleanly and take its `claude` child
// with it; SIGKILL after a shared grace for the ones that are wedged or too old to cooperate.
func SweepOrphanSidecars(sidecarPath string) []int {
	procs, err := listProcs()
	if err != nil {
		return nil // no process table, no sweep: never guess
	}
	selfPID, selfUID := os.Getpid(), os.Getuid()
	var orphans []proc
	for _, p := range procs {
		if sweepable(p, sidecarPath, selfPID, selfUID) {
			orphans = append(orphans, p)
		}
	}
	if len(orphans) == 0 {
		return nil
	}
	pids := make([]int, 0, len(orphans))
	for _, p := range orphans {
		signalOrphan(p, syscall.SIGTERM)
		pids = append(pids, p.pid)
	}
	time.Sleep(sweepGrace)
	for _, p := range orphans {
		// Signal 0 probes liveness without delivering anything, so we only escalate on the ones that
		// actually ignored the TERM.
		if syscall.Kill(p.pid, 0) == nil {
			signalOrphan(p, syscall.SIGKILL)
		}
	}
	return pids
}
