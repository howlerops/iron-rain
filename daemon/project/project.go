// Package project is the daemon's registry of folders ("projects") that sessions can
// be spawned in. A project is a directory on the host, optionally a git repo (which
// enables worktree sessions). The registry persists to a JSON file (e.g.
// ~/.oculus/projects.json).
package project

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Project is a registered folder.
type Project struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Path          string `json:"path"`
	IsGitRepo     bool   `json:"is_git_repo"`
	DefaultBranch string `json:"default_branch,omitempty"`
	// Source is "manual" (added via the Projects UI) or "auto" (discovered from an
	// active agent's working directory). Empty in legacy records; treat as "manual".
	Source string `json:"source,omitempty"`
}

// Registry is a persisted, concurrency-safe set of projects, deduped by path.
type Registry struct {
	mu     sync.Mutex
	saveMu sync.Mutex // serializes disk writes so they don't hold mu during I/O
	path   string     // persistence file ("" = in-memory only)
	list   []Project
}

// Load reads the registry from file (a missing file yields an empty registry).
func Load(path string) (*Registry, error) {
	r := &Registry{path: path}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return r, nil
		}
		return nil, err
	}
	if len(data) == 0 {
		return r, nil
	}
	if err := json.Unmarshal(data, &r.list); err != nil {
		return nil, fmt.Errorf("project registry %s: %w", path, err)
	}
	return r, nil
}

// Add registers dir explicitly via the Projects UI (source "manual"). Idempotent by
// absolute path; adding a path that was auto-discovered promotes it to "manual".
func (r *Registry) Add(dir string) (Project, error) { return r.add(dir, "manual") }

// AddAuto registers dir as auto-discovered from an active agent's cwd (source "auto").
// If the path was already added manually, the "manual" source is preserved.
func (r *Registry) AddAuto(dir string) (Project, error) { return r.add(dir, "auto") }

// add registers dir with the given source. It validates dir exists and is a directory,
// detects whether it's a git repo, and records its current branch.
func (r *Registry) add(dir, source string) (Project, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return Project{}, err
	}
	abs = filepath.Clean(abs)
	fi, err := os.Stat(abs)
	if err != nil {
		return Project{}, fmt.Errorf("project path %s: %w", abs, err)
	}
	if !fi.IsDir() {
		return Project{}, fmt.Errorf("project path %s is not a directory", abs)
	}

	// Detect git state before taking the lock: gitInfo shells out to git (two blocking
	// subprocesses) and must not freeze the whole registry. A redundant computation by a
	// racing duplicate Add is cheap versus serializing all registry access behind spawns.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	isRepo, branch := gitInfo(ctx, abs)

	r.mu.Lock()
	// Dedup: same path -> return the existing entry. A manual add promotes a previously
	// auto-discovered project so the user's explicit "keep" survives.
	for i, p := range r.list {
		if p.Path == abs {
			if source == "manual" && p.Source != "manual" {
				r.list[i].Source = "manual"
				out := r.list[i]
				r.mu.Unlock()
				if err := r.save(); err != nil {
					return Project{}, err
				}
				return out, nil
			}
			r.mu.Unlock()
			return p, nil
		}
	}
	p := Project{
		ID:            "proj_" + shortHash(abs),
		Name:          filepath.Base(abs),
		Path:          abs,
		IsGitRepo:     isRepo,
		DefaultBranch: branch,
		Source:        source,
	}
	r.list = append(r.list, p)
	r.mu.Unlock()
	if err := r.save(); err != nil {
		return Project{}, err
	}
	return p, nil
}

// List returns a copy of the registered projects.
func (r *Registry) List() []Project {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Project, len(r.list))
	copy(out, r.list)
	return out
}

// Get returns the project with id, if present.
func (r *Registry) Get(id string) (Project, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, p := range r.list {
		if p.ID == id {
			return p, true
		}
	}
	return Project{}, false
}

// Remove deletes the project with id (no error if absent).
func (r *Registry) Remove(id string) error {
	r.mu.Lock()
	out := r.list[:0]
	for _, p := range r.list {
		if p.ID != id {
			out = append(out, p)
		}
	}
	r.list = out
	r.mu.Unlock()
	return r.save()
}

// save persists the registry. It must be called WITHOUT r.mu held: it snapshots the list
// under a brief lock and then does the JSON marshal + disk write outside the lock so
// readers (List/Get) aren't blocked on I/O. saveMu serializes concurrent saves so the
// atomic temp-file rename can't lose a newer snapshot to an older one.
func (r *Registry) save() error {
	if r.path == "" {
		return nil
	}
	r.saveMu.Lock()
	defer r.saveMu.Unlock()

	r.mu.Lock()
	snap := make([]Project, len(r.list))
	copy(snap, r.list)
	path := r.path
	r.mu.Unlock()

	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	// Write to a temp file and rename for atomicity (never a torn/partial registry).
	tmp, err := os.CreateTemp(dir, ".projects-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:12]
}

// gitInfo reports whether dir is inside a git work tree and its current branch. It uses
// exec.CommandContext so a hung git (index.lock contention, a credential/GPG prompt, a
// network-backed work tree) is killed when ctx is cancelled or its deadline expires.
func gitInfo(ctx context.Context, dir string) (isRepo bool, branch string) {
	if out, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--is-inside-work-tree").Output(); err != nil || strings.TrimSpace(string(out)) != "true" {
		return false, ""
	}
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return true, ""
	}
	return true, strings.TrimSpace(string(out))
}
