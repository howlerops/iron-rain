// Package sshremote runs git/agent operations on a remote box over SSH — the foundation for "run the
// workspace on a beefy remote VPS" (SSH remote worktrees). A Host is an ssh target plus a remote
// working directory; a Runner executes commands there. The exec function is injectable so the
// command construction is deterministically unit-testable, with an optional real loopback
// integration test where an ssh server is reachable.
//
// This package is the transport + git layer. Running a full agent SESSION on the remote (streaming a
// remote CLI agent's stdout back through the daemon) builds on Runner but is a separate, larger piece.
package sshremote

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Host is a registered remote: an ssh target (user@host or an ssh_config alias) + a remote path the
// worktree lives in.
type Host struct {
	ID         string        `json:"id"`
	Name       string        `json:"name"`
	SSHTarget  string        `json:"ssh_target"`  // "user@host", "host", or an ~/.ssh/config alias
	RemotePath string        `json:"remote_path"` // absolute path on the remote where the repo/worktree lives
	Forwards   []PortForward `json:"forwards,omitempty"`
}

// PortForward tunnels a local port to a port on the remote (ssh -L), so a dev server running in the
// remote worktree is reachable at http://localhost:<LocalPort> — e.g. for Design Mode / a browser.
type PortForward struct {
	LocalPort  int `json:"local_port"`
	RemotePort int `json:"remote_port"`
}

// forwardArgs builds the `-L local:localhost:remote` flags for the host's port forwards.
func (h Host) forwardArgs() []string {
	var args []string
	for _, f := range h.Forwards {
		if f.LocalPort > 0 && f.RemotePort > 0 {
			args = append(args, "-L", fmt.Sprintf("%d:localhost:%d", f.LocalPort, f.RemotePort))
		}
	}
	return args
}

// ExecFunc runs a local command and returns combined output. Injectable so tests can record
// invocations without spawning ssh.
type ExecFunc func(ctx context.Context, name string, args ...string) ([]byte, error)

// defaultExec runs the real command, returning combined stdout+stderr.
func defaultExec(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	return buf.Bytes(), err
}

// Runner executes commands on remote hosts. Zero value uses the real exec; set Exec for tests.
type Runner struct {
	Exec ExecFunc
	// SSHOptions are prepended to every ssh invocation. BatchMode avoids interactive password prompts
	// (fails fast instead of hanging); ConnectTimeout bounds a dead host; ServerAlive* keep it alive.
	SSHOptions []string
	// Retries is how many EXTRA attempts a transient failure (connection reset/timeout) gets before
	// giving up — the auto-reconnect behavior for a dropped link.
	Retries int
}

// New returns a Runner with sensible non-interactive defaults. ServerAliveInterval/CountMax keep a
// long-lived connection alive through NAT/idle timeouts and detect a dead link within ~45s (the
// basis for auto-reconnect); BatchMode fails fast instead of prompting.
func New() *Runner {
	return &Runner{
		Exec: defaultExec,
		SSHOptions: []string{
			"-o", "BatchMode=yes",
			"-o", "ConnectTimeout=8",
			"-o", "ServerAliveInterval=15",
			"-o", "ServerAliveCountMax=3",
		},
		Retries: 2,
	}
}

// sshArgs builds the argv for `ssh <opts> <forwards> <target> <remoteCmd>`. Exposed (lowercase,
// package-tested) so the exact invocation is verifiable.
func (r *Runner) sshArgs(h Host, remoteCmd string) []string {
	args := append([]string{}, r.SSHOptions...)
	args = append(args, h.forwardArgs()...)
	args = append(args, h.SSHTarget, remoteCmd)
	return args
}

// SSHArgv returns the full ssh argv (options + port forwards + target + remote command) for a
// streaming invocation — used by the hub to run a remote agent session (its port forwards make a
// remote dev server reachable at localhost for Design Mode).
func (r *Runner) SSHArgv(h Host, remoteCmd string) []string {
	return r.sshArgs(h, remoteCmd)
}

// Run executes a raw command string on the remote host, returning its combined output.
func (r *Runner) Run(ctx context.Context, h Host, remoteCmd string) (string, error) {
	if h.SSHTarget == "" {
		return "", fmt.Errorf("remote host has no ssh target")
	}
	execFn := r.Exec
	if execFn == nil {
		execFn = defaultExec
	}
	out, err := execFn(ctx, "ssh", r.sshArgs(h, remoteCmd)...)
	if err != nil {
		return string(out), fmt.Errorf("ssh %s: %v: %s", h.SSHTarget, err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// RunWithRetry runs a command, retrying a TRANSIENT failure (a dropped/timed-out connection) up to
// Retries extra times with a short backoff — so a flaky link auto-reconnects instead of failing the
// operation. A non-transient failure (e.g. the remote command exited non-zero) is returned
// immediately, not retried. time.Sleep is the only side effect between attempts.
func (r *Runner) RunWithRetry(ctx context.Context, h Host, remoteCmd string) (string, error) {
	attempts := r.Retries + 1
	if attempts < 1 {
		attempts = 1
	}
	var out string
	var err error
	for i := 0; i < attempts; i++ {
		out, err = r.Run(ctx, h, remoteCmd)
		if err == nil || !isTransient(err) {
			return out, err
		}
		if i < attempts-1 {
			select {
			case <-ctx.Done():
				return out, ctx.Err()
			case <-time.After(time.Duration(i+1) * time.Second):
			}
		}
	}
	return out, err
}

// isTransient reports whether an ssh failure looks like a connection problem worth retrying (vs. the
// remote command legitimately failing).
func isTransient(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	for _, m := range []string{"connection reset", "connection refused", "connection closed", "timed out", "timeout", "broken pipe", "connection to", "route to host", "operation timed out", "kex_exchange"} {
		if strings.Contains(s, m) {
			return true
		}
	}
	return false
}

// inDir wraps a command so it runs in the host's remote path. Empty path runs in the login dir.
func (h Host) inDir(cmd string) string {
	if h.RemotePath == "" {
		return cmd
	}
	// Single-quote the path defensively; the command itself is a fixed git subcommand.
	return fmt.Sprintf("cd %s && %s", shellQuote(h.RemotePath), cmd)
}

// GitStatus returns `git status --porcelain` from the remote worktree (empty = clean).
func (r *Runner) GitStatus(ctx context.Context, h Host) (string, error) {
	return r.RunWithRetry(ctx, h, h.inDir("git status --porcelain"))
}

// GitDiff returns the remote worktree's diff (uncommitted changes) for review from the app.
func (r *Runner) GitDiff(ctx context.Context, h Host) (string, error) {
	return r.RunWithRetry(ctx, h, h.inDir("git diff"))
}

// Probe checks the host is reachable and the remote path is a git repo — used when adding a host.
func (r *Runner) Probe(ctx context.Context, h Host) error {
	out, err := r.Run(ctx, h, h.inDir("git rev-parse --is-inside-work-tree"))
	if err != nil {
		return err
	}
	if !strings.Contains(out, "true") {
		return fmt.Errorf("remote path %q is not a git worktree", h.RemotePath)
	}
	return nil
}

// shellQuote single-quotes a string for safe embedding in a remote shell command.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
