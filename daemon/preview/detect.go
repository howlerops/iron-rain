package preview

import (
	"context"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Detection answers "which port did this session's dev server actually bind?" without the project
// having to declare anything.
//
// The manifest route (portRange -> OCULUS_PORT) only works when a repo opts in AND its start script
// honours the variable. Neither is true by default: `npm run dev` binds whatever vite.config says,
// usually 5173, and most repos have no .oculus/project.json at all. So a feature that depends on the
// manifest is invisible to almost everyone.
//
// The match is by WORKING DIRECTORY rather than process tree. A process tree would be tighter, but
// it only covers providers whose processes are our children — claude-code's sidecar is, opencode's
// server is not, since it runs independently and outlives any one session. Directory matching works
// the same for every provider, and a dev server started for a worktree runs in that worktree.

// listener is one process listening on a TCP port.
type listener struct {
	pid  int
	port int
	cwd  string
}

// Detect returns sessionID -> port for every session whose worktree contains a listening process.
//
// `paths` maps session id to its working directory. Sessions with no directory are skipped: without
// one there is nothing to attribute a port to, and guessing would hand a session someone else's
// server.
func Detect(ctx context.Context, paths map[string]string) (map[string]int, bool) {
	if len(paths) == 0 {
		return nil, true
	}
	ls, ok := listeners(ctx)
	if !ok {
		// The scan FAILED — that is not the same as "nothing is listening", and conflating the two
		// is what made names flap: a hung lsof looked exactly like every dev server stopping at
		// once, so the poller released every name and re-registered seconds later.
		return nil, false
	}
	if len(ls) == 0 {
		return nil, true
	}
	// Resolve each side ONCE. under() used to resolve both arguments on every comparison, which is
	// O(sessions x listeners) EvalSymlinks syscalls per poll — about 23,000 with 51 sessions on this
	// machine, every 4 seconds, for a result that never changes between pairs.
	for i := range ls {
		ls[i].cwd = resolve(ls[i].cwd)
	}
	out := map[string]int{}
	for id, dir := range paths {
		dir = resolve(dir)
		if dir == "" {
			continue // not an absolute, existing directory — nothing to attribute a port to
		}
		best := 0
		for _, l := range ls {
			if l.cwd == "" || !underResolved(l.cwd, dir) {
				continue
			}
			// Lowest port wins when a session has several. Dev servers cluster in the low ranges
			// (3000/5173/8080) while tooling — debuggers, HMR side-channels, language servers —
			// tends to land on high ephemeral ports, so the lowest is the better guess at "the thing
			// a person wants to look at".
			if best == 0 || l.port < best {
				best = l.port
			}
		}
		if best > 0 {
			out[id] = best
		}
	}
	return out, true
}

// under reports whether path is dir or sits inside it, comparing RESOLVED paths.
//
// Symlink resolution is not optional on macOS: /tmp is a symlink to /private/tmp, so a session whose
// worktree is "/tmp/lab" is reported by lsof as "/private/tmp/lab" and a plain prefix compare finds
// nothing. Detection silently returned no ports until this was fixed — and the unit tests missed it
// because they compared paths of the same spelling. The same hazard is why fsaccess.NormalizePath
// exists.
func under(path, dir string) bool {
	if path == "" || dir == "" {
		return false
	}
	return underResolved(resolve(path), resolve(dir))
}

// underResolved is under() for paths already put through resolve — the hot path, so it does no
// filesystem work at all.
func underResolved(path, dir string) bool {
	if path == "" || dir == "" {
		return false
	}
	if path == dir {
		return true
	}
	return strings.HasPrefix(path, dir+string(filepath.Separator))
}

// resolve cleans a path and follows symlinks where it can. A path that does not exist (a worktree
// removed mid-poll) still normalises, so a miss degrades to "no match" rather than an error.
//
// Empty and relative inputs return "" rather than being resolved. filepath.Clean("") is ".", and
// EvalSymlinks(".") is the DAEMON'S OWN working directory — so without this guard a session with no
// directory would be attributed whatever happened to be listening in the daemon's cwd. Only an
// absolute path can identify a session's tree.
func resolve(p string) string {
	p = strings.TrimSpace(p)
	if p == "" || !filepath.IsAbs(p) {
		return ""
	}
	p = filepath.Clean(p)
	if p == string(filepath.Separator) {
		return "" // root is never a session directory, and matching it would claim every process
	}
	if r, err := filepath.EvalSymlinks(p); err == nil && r != "" {
		p = r
	}
	p = strings.TrimSuffix(p, string(filepath.Separator))
	// filepath.EvalSymlinks("") returns "." with NO error on macOS, so a path that trims to nothing
	// comes back as the current directory rather than an error. Reject it explicitly.
	if p == "" || p == "." {
		return ""
	}
	return p
}

// listeners enumerates listening TCP sockets and the working directory of each owning process.
//
// Two lsof calls rather than one per process: the first lists listeners, the second asks for the cwd
// of exactly those pids. On a busy machine there may be dozens of listeners but the second call is
// still one exec, which is what keeps this cheap enough to poll.
func listeners(ctx context.Context) ([]listener, bool) {
	ctx, cancel := context.WithTimeout(ctx, scanTimeout)
	defer cancel()

	// -b avoids kernel calls that can BLOCK. Without it lsof intermittently hung for the full
	// timeout on this machine — measured at a steady 4.1s against a 4s deadline — which is what
	// produced the flapping. Blocking calls are how lsof stalls on an unresponsive mount, and none
	// of what they add is needed here: we only want pids, ports and cwds.
	out, err := exec.CommandContext(ctx, "lsof", "-b", "-w", "-nP", "-iTCP", "-sTCP:LISTEN", "-Fpn").Output()
	if err != nil && len(out) == 0 {
		return nil, false // hung, missing, or refused — report failure, do NOT report "none"
	}
	var ls []listener
	pid := 0
	for _, line := range strings.Split(string(out), "\n") {
		if len(line) < 2 {
			continue
		}
		switch line[0] {
		case 'p':
			pid, _ = strconv.Atoi(line[1:])
		case 'n':
			if p := portOfAddr(line[1:]); p > 0 && pid > 0 {
				ls = append(ls, listener{pid: pid, port: p})
			}
		}
	}
	if len(ls) == 0 {
		return nil, true
	}
	cwds := cwdsOf(ctx, ls)
	for i := range ls {
		ls[i].cwd = cwds[ls[i].pid]
	}
	return ls, true
}

// scanTimeout bounds a scan. Generous relative to the ~0.1s a healthy scan takes, because the cost
// of being slow is one late name while the cost of being wrong was every name flapping.
const scanTimeout = 10 * time.Second

// cwdsOf returns pid -> working directory for the given listeners, in one lsof call.
func cwdsOf(ctx context.Context, ls []listener) map[int]string {
	seen := map[int]bool{}
	pids := make([]string, 0, len(ls))
	for _, l := range ls {
		if !seen[l.pid] {
			seen[l.pid] = true
			pids = append(pids, strconv.Itoa(l.pid))
		}
	}
	out, err := exec.CommandContext(ctx, "lsof", "-b", "-w", "-a", "-d", "cwd", "-Fpn", "-p", strings.Join(pids, ",")).Output()
	if err != nil && len(out) == 0 {
		return map[int]string{}
	}
	res := map[int]string{}
	pid := 0
	for _, line := range strings.Split(string(out), "\n") {
		if len(line) < 2 {
			continue
		}
		switch line[0] {
		case 'p':
			pid, _ = strconv.Atoi(line[1:])
		case 'n':
			if pid > 0 {
				res[pid] = line[1:]
			}
		}
	}
	return res
}

// portOfAddr pulls 5173 out of "127.0.0.1:5173" or "*:5173" or "[::1]:5173".
//
// Only loopback and wildcard binds count. A dev server bound to a specific LAN address is not
// something the preview proxy can reach at 127.0.0.1, and naming it would produce a URL that fails.
func portOfAddr(addr string) int {
	i := strings.LastIndex(addr, ":")
	if i < 0 {
		return 0
	}
	host, portStr := addr[:i], addr[i+1:]
	switch {
	case host == "*", host == "", host == "127.0.0.1", host == "localhost",
		host == "[::1]", host == "[::]", host == "::1":
	default:
		return 0
	}
	p, err := strconv.Atoi(portStr)
	if err != nil || p <= 0 || p > 65535 {
		return 0
	}
	return p
}
