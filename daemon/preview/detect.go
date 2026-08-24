package preview

import (
	"context"
	"os"
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
	cmd  string
}

// infraCommands are processes that listen inside a project directory but are NOT the user's dev
// server — they are the machinery running the agents.
//
// This is not hypothetical: `opencode serve` runs with cwd set to the project it serves, so a
// session in that project would otherwise have opencode's own HTTP port named as its preview. The
// daemon does the same when started from a checkout. Naming either would point the user's preview
// URL at the agent harness instead of their app.
//
// Matched on the executable name, not a path, because lsof reports a truncated command. Deliberately
// NOT excluding "node": vite, next and most JS dev servers are node, and excluding it would discard
// exactly what this feature exists to find.
var infraCommands = map[string]bool{
	"opencode": true,
	"oculusd":  true,
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
	dirs := make(map[string]string, len(paths)) // sessionID -> resolved dir
	for id, dir := range paths {
		if d := resolve(dir); d != "" {
			dirs[id] = d
		}
	}

	return attribute(ls, dirs), true
}

// attribute maps each listener to the session that owns it, and picks one port per session.
//
// Split out from Detect so the rules can be tested directly — the interesting behaviour is all here,
// while Detect's other job is shelling out to lsof, which a test cannot meaningfully drive.
//
// Each listener goes to AT MOST ONE session: the deepest directory match. Without that a session
// opened on a repo root claims the servers of every worktree beneath it, and the parent's preview
// URL silently serves a child's app.
//
// Sessions sharing an IDENTICAL directory both keep the port. It really is both of theirs, and
// choosing one would make the result depend on map iteration order.
func attribute(ls []listener, dirs map[string]string) map[string]int {
	out := map[string]int{}
	for _, l := range ls {
		if l.cwd == "" {
			continue
		}
		owners, depth := []string(nil), -1
		for id, dir := range dirs {
			if !underResolved(l.cwd, dir) {
				continue
			}
			switch d := len(dir); {
			case d > depth:
				owners, depth = []string{id}, d
			case d == depth:
				owners = append(owners, id)
			}
		}
		for _, id := range owners {
			if cur, ok := out[id]; !ok || better(l.port, cur) {
				out[id] = l.port
			}
		}
	}
	return out
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

	// -Fcpn adds the command name so agent infrastructure can be told apart from a dev server.
	// -b avoids kernel calls that can BLOCK. Without it lsof intermittently hung for the full
	// timeout on this machine — measured at a steady 4.1s against a 4s deadline — which is what
	// produced the flapping. Blocking calls are how lsof stalls on an unresponsive mount, and none
	// of what they add is needed here: we only want pids, ports and cwds.
	out, err := exec.CommandContext(ctx, "lsof", "-b", "-w", "-nP", "-iTCP", "-sTCP:LISTEN", "-Fcpn").Output()
	if err != nil && len(out) == 0 {
		return nil, false // hung, missing, or refused — report failure, do NOT report "none"
	}
	var ls []listener
	pid, cmd := 0, ""
	self := os.Getpid()
	for _, line := range strings.Split(string(out), "\n") {
		if len(line) < 2 {
			continue
		}
		switch line[0] {
		case 'p':
			pid, _ = strconv.Atoi(line[1:])
		case 'c':
			cmd = line[1:]
		case 'n':
			p := portOfAddr(line[1:])
			if p <= 0 || pid <= 0 || pid == self || infraCommands[cmd] {
				continue
			}
			ls = append(ls, listener{pid: pid, port: p, cmd: cmd})
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

// devPorts are the ports frameworks conventionally use. A listener on one of these is far more
// likely to be the thing a person wants to look at than a debugger or language server, which take
// whatever ephemeral port they are given.
var devPorts = map[int]bool{
	3000: true, 3001: true, 4000: true, 4200: true, 4321: true, 5000: true,
	5173: true, 5174: true, 8000: true, 8080: true, 8081: true, 1313: true, 9000: true,
}

// better reports whether candidate beats current as "the session's dev server".
//
// A conventional dev port wins outright; otherwise the lower port does. Lowest-alone was the first
// heuristic and it is wrong often enough to matter: a debugger on :9229 or an LSP on a low ephemeral
// port would outrank a Vite server on :5173 purely by number.
func better(candidate, current int) bool {
	cd, cur := devPorts[candidate], devPorts[current]
	if cd != cur {
		return cd
	}
	return candidate < current
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
