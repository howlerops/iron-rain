package worktree

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
)

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
}

// Result reports what Bootstrap did.
type Result struct {
	Port    int      `json:"port,omitempty"` // 0 = none allocated
	Copied  []string `json:"copied,omitempty"`
	SetupOK bool     `json:"setup_ok"`
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
// repo root, optionally disable shared git hooks, then run the setup command. A non-zero
// port is exported to setup as OCULUS_PORT (allocate it via AllocPort under your own lock
// so concurrent worktrees don't collide).
func Bootstrap(repoRoot, worktreePath string, cfg Config, port int) (Result, error) {
	res := Result{Port: port}

	for _, pat := range cfg.Copy {
		matches, _ := filepath.Glob(filepath.Join(repoRoot, pat))
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

	if cfg.SkipHooks {
		// Point hooks at a nonexistent dir so the repo's shared hooks don't fire here.
		_ = exec.Command("git", "-C", worktreePath, "config", "core.hooksPath", filepath.Join(worktreePath, ".oculus-nohooks")).Run()
	}

	if cfg.Setup != "" {
		cmd := exec.Command("sh", "-c", cfg.Setup)
		cmd.Dir = worktreePath
		cmd.Env = os.Environ()
		if res.Port != 0 {
			cmd.Env = append(cmd.Env, "OCULUS_PORT="+strconv.Itoa(res.Port))
		}
		if out, err := cmd.CombinedOutput(); err != nil {
			return res, fmt.Errorf("setup %q failed: %v: %s", cfg.Setup, err, out)
		}
		res.SetupOK = true
	}
	return res, nil
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
func copyPath(src, dst string) error {
	fi, err := os.Stat(src)
	if err != nil {
		return err
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
