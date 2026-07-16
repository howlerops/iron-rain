// Package discovery autodetects active agent sessions on the host so they show up
// in the app with no manual config — the "continue my terminal session on my phone"
// handoff. It finds running `opencode serve` processes (and enumerates their live
// sessions) and recent claude-code session transcripts.
//
// See ../../docs/plan-native-ade.md ("Session autodetection") and ../../skills/oculus-discovery.
package discovery

import (
	"bufio"
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/howlerops/oculus/daemon/agent/opencode"
	"github.com/howlerops/oculus/daemon/protocol"
)

// OpenCodeServer is a running `opencode serve` discovered on the host.
type OpenCodeServer struct {
	URL string
	PID int
}

// Listener is a listening TCP socket owned by a process.
type Listener struct {
	Command string
	PID     int
	Port    int
}

// ClaudeSession is a claude-code session transcript found in the local store.
type ClaudeSession struct {
	ID      string
	Cwd     string
	Path    string
	ModTime time.Time
}

var listenPortRE = regexp.MustCompile(`^\[?[0-9a-fA-F:.*]+\]?:(\d+)$`)

// ParseListeners parses `lsof -nP -iTCP -sTCP:LISTEN` output into listeners.
func ParseListeners(out []byte) []Listener {
	var ls []Listener
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 9 || f[0] == "COMMAND" {
			continue
		}
		pid, err := strconv.Atoi(f[1])
		if err != nil {
			continue
		}
		port := 0
		for _, tok := range f {
			if m := listenPortRE.FindStringSubmatch(tok); m != nil {
				if p, err := strconv.Atoi(m[1]); err == nil {
					port = p
				}
			}
		}
		if port == 0 {
			continue
		}
		ls = append(ls, Listener{Command: f[0], PID: pid, Port: port})
	}
	return ls
}

// lsof lists listening TCP sockets; overridable in tests.
var lsof = func(ctx context.Context) ([]byte, error) {
	return exec.CommandContext(ctx, "lsof", "-nP", "-iTCP", "-sTCP:LISTEN").Output()
}

// FindOpenCodeServers returns running opencode servers by scanning listening sockets.
func FindOpenCodeServers(ctx context.Context) ([]OpenCodeServer, error) {
	out, err := lsof(ctx)
	if err != nil {
		return nil, err
	}
	seen := map[int]bool{}
	var servers []OpenCodeServer
	for _, l := range ParseListeners(out) {
		if !strings.HasPrefix(l.Command, "opencode") || seen[l.Port] {
			continue
		}
		seen[l.Port] = true
		servers = append(servers, OpenCodeServer{
			URL: "http://127.0.0.1:" + strconv.Itoa(l.Port),
			PID: l.PID,
		})
	}
	return servers, nil
}

// DefaultClaudeProjectsDir is ~/.claude/projects.
func DefaultClaudeProjectsDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".claude", "projects")
}

// FindClaudeSessions lists session transcripts modified within `within` of now.
// A missing store is not an error (claude-code may not be installed).
func FindClaudeSessions(projectsDir string, within time.Duration, now time.Time) ([]ClaudeSession, error) {
	entries, err := os.ReadDir(projectsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	cutoff := now.Add(-within)
	var out []ClaudeSession
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		proj := filepath.Join(projectsDir, e.Name())
		files, err := os.ReadDir(proj)
		if err != nil {
			continue
		}
		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".jsonl") {
				continue
			}
			info, err := f.Info()
			if err != nil || info.ModTime().Before(cutoff) {
				continue
			}
			out = append(out, ClaudeSession{
				ID:      strings.TrimSuffix(f.Name(), ".jsonl"),
				Cwd:     decodeProjectDir(e.Name()),
				Path:    filepath.Join(proj, f.Name()),
				ModTime: info.ModTime(),
			})
		}
	}
	return out, nil
}

// decodeProjectDir best-effort restores a cwd from claude-code's directory encoding
// ("/" -> "-", leading "-"). Lossy for paths that themselves contain "-".
func decodeProjectDir(name string) string {
	if name == "" {
		return ""
	}
	return "/" + strings.TrimPrefix(strings.ReplaceAll(name, "-", "/"), "/")
}

// listOpenCodeSessions enumerates a server's live sessions; overridable in tests.
var listOpenCodeSessions = func(ctx context.Context, url string) ([]protocol.Session, error) {
	return opencode.New(url).List(ctx)
}

// Scan discovers active agent artifacts on the host and returns them as protocol
// items ready to send to the app. Best-effort: a failing scan of one kind does not
// block the others.
func Scan(ctx context.Context) ([]protocol.Discovered, error) {
	servers, _ := FindOpenCodeServers(ctx)
	claude, _ := FindClaudeSessions(DefaultClaudeProjectsDir(), 24*time.Hour, time.Now())
	return combine(ctx, servers, claude, listOpenCodeSessions), nil
}

func combine(
	ctx context.Context,
	servers []OpenCodeServer,
	claude []ClaudeSession,
	list func(context.Context, string) ([]protocol.Session, error),
) []protocol.Discovered {
	items := []protocol.Discovered{}
	for _, s := range servers {
		items = append(items, protocol.Discovered{
			Provider: "opencode", Kind: protocol.KindServer, URL: s.URL, PID: s.PID,
		})
		sessions, err := list(ctx, s.URL)
		if err != nil {
			continue
		}
		for _, sess := range sessions {
			items = append(items, protocol.Discovered{
				Provider: "opencode", Kind: protocol.KindSession,
				URL: s.URL, SessionID: sess.ID, Title: sess.Title,
			})
		}
	}
	for _, c := range claude {
		items = append(items, protocol.Discovered{
			Provider: "claude-code", Kind: protocol.KindSession,
			SessionID: c.ID, Cwd: c.Cwd, Path: c.Path,
		})
	}
	return items
}
