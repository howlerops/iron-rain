package claudecode

import (
	"os"
	"testing"
)

// The sweep decides whether to KILL a process, so its predicate is tested exhaustively and in
// isolation. Nothing in this file signals anything.

const testSidecar = "/Users/someone/.oculus/claude-sidecar/sidecar.mjs"

// TestSweepableAcceptsOnlyAbandonedSidecars pins the one shape we are allowed to kill: our exact
// sidecar path, run by node, owned by us, reparented to init because the daemon that spawned it died.
func TestSweepableAcceptsOnlyAbandonedSidecars(t *testing.T) {
	orphan := proc{pid: 4242, ppid: 1, pgid: 4242, uid: 501, command: "node " + testSidecar}
	if !sweepable(orphan, testSidecar, 900, 501) {
		t.Fatal("an abandoned sidecar with our exact path must be sweepable — nothing else can ever reap it")
	}
}

// TestSweepableSparesLiveDaemonsSidecar is the case that matters most: a user may legitimately run a
// second daemon, and killing ITS sidecar destroys work in progress. A live daemon's sidecar is a
// direct child of that daemon, so its ppid is never 1 — that is the whole guarantee.
func TestSweepableSparesLiveDaemonsSidecar(t *testing.T) {
	live := proc{pid: 4242, ppid: 3111, pgid: 4242, uid: 501, command: "node " + testSidecar}
	if sweepable(live, testSidecar, 900, 501) {
		t.Fatal("KILLED ANOTHER LIVE DAEMON'S SIDECAR — ppid != 1 must always veto")
	}
}

func TestSweepableVetoes(t *testing.T) {
	const selfPID, selfUID = 900, 501
	cases := []struct {
		name string
		p    proc
		why  string
	}{
		{
			"another user's process",
			proc{pid: 4242, ppid: 1, pgid: 4242, uid: 502, command: "node " + testSidecar},
			"the daemon must never signal a process owned by a different uid",
		},
		{
			"a different install's sidecar",
			proc{pid: 4242, ppid: 1, pgid: 4242, uid: selfUID, command: "node /opt/other/sidecar.mjs"},
			"only the exact path THIS daemon runs may be swept",
		},
		{
			"the path as a substring of a longer argument",
			proc{pid: 4242, ppid: 1, pgid: 4242, uid: selfUID, command: "node " + testSidecar + ".bak"},
			"the path must match a whole argv element, not a prefix of one",
		},
		{
			"some other program that merely mentions the path",
			proc{pid: 4242, ppid: 1, pgid: 4242, uid: selfUID, command: "tail -f " + testSidecar},
			"argv[0] must be the node runtime, or an editor/grep/tail holding the path gets killed",
		},
		{
			"a bare node process",
			proc{pid: 4242, ppid: 1, pgid: 4242, uid: selfUID, command: "node"},
			"matching on the runtime alone would kill the user's own node servers",
		},
		{
			"our own pid",
			proc{pid: selfPID, ppid: 1, pgid: selfPID, uid: selfUID, command: "node " + testSidecar},
			"the sweep must never target the daemon running it",
		},
		{
			"init",
			proc{pid: 1, ppid: 1, pgid: 1, uid: selfUID, command: "node " + testSidecar},
			"pid 1 is off limits however the row parsed",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if sweepable(tc.p, testSidecar, selfPID, selfUID) {
				t.Fatalf("swept a process it must spare: %s", tc.why)
			}
		})
	}
}

// TestSweepableRefusesWeakPaths: an unresolved or relative sidecar path would match far too much, so
// the sweep declines to run at all rather than kill on a loose match.
func TestSweepableRefusesWeakPaths(t *testing.T) {
	p := proc{pid: 4242, ppid: 1, pgid: 4242, uid: 501, command: "node sidecar.mjs"}
	for _, path := range []string{"", "sidecar.mjs", "./sidecar.mjs"} {
		if sweepable(p, path, 900, 501) {
			t.Fatalf("swept on a non-absolute sidecar path %q", path)
		}
	}
}

// TestParsePSRejectsUnparseableRows: a row we can't read cleanly must be dropped, never guessed at —
// a misparsed pid is a kill aimed at an arbitrary process.
func TestParsePSRejectsUnparseableRows(t *testing.T) {
	for _, line := range []string{"", "   ", "PID PPID PGID UID COMMAND", "1 2 3 node foo", "42 1 42"} {
		if _, ok := parsePS(line); ok {
			t.Fatalf("parsed a row it should have rejected: %q", line)
		}
	}
	p, ok := parsePS("  4242     1  4242   501 node " + testSidecar)
	if !ok {
		t.Fatal("failed to parse a well-formed ps row")
	}
	if p.pid != 4242 || p.ppid != 1 || p.pgid != 4242 || p.uid != 501 {
		t.Fatalf("misparsed ps row: %+v", p)
	}
	if p.command != "node "+testSidecar {
		t.Fatalf("command = %q", p.command)
	}
}

// TestSweepFindsNothingForAnUnusedPath is the end-to-end shape of the sweep against the real process
// table: it must be a clean no-op — no error, no kill — when nothing on the machine matches.
func TestSweepFindsNothingForAnUnusedPath(t *testing.T) {
	if pids := SweepOrphanSidecars(t.TempDir() + "/nonexistent-sidecar.mjs"); len(pids) != 0 {
		t.Fatalf("swept %v for a path no process can be running", pids)
	}
}

// TestListProcsSeesThisProcess sanity-checks the ps parsing against the live process table: if the
// column layout ever changed under us, the sweep would silently stop finding anything at all.
func TestListProcsSeesThisProcess(t *testing.T) {
	procs, err := listProcs()
	if err != nil {
		t.Skipf("ps unavailable: %v", err)
	}
	self := os.Getpid()
	for _, p := range procs {
		if p.pid == self {
			if p.uid != os.Getuid() {
				t.Fatalf("ps reported uid %d for our own process, want %d", p.uid, os.Getuid())
			}
			return
		}
	}
	t.Fatal("listProcs did not find the test process itself — the ps column layout has changed")
}
