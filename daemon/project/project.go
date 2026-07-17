// Package project is the daemon's registry of folders ("projects") that sessions can
// be spawned in. A project is a directory on the host, optionally a git repo (which
// enables worktree sessions). The registry persists to a JSON file (e.g.
// ~/.oculus/projects.json).
package project

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// Project is a registered folder.
type Project struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Path          string `json:"path"`
	IsGitRepo     bool   `json:"is_git_repo"`
	DefaultBranch string `json:"default_branch,omitempty"`
}

// Registry is a persisted, concurrency-safe set of projects, deduped by path.
type Registry struct {
	mu   sync.Mutex
	path string // persistence file ("" = in-memory only)
	list []Project
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

// Add registers dir (idempotent by absolute path). It validates dir exists and is a
// directory, detects whether it's a git repo, and records its current branch.
func (r *Registry) Add(dir string) (Project, error) {
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

	r.mu.Lock()
	defer r.mu.Unlock()
	// Dedup: same path -> return the existing entry.
	for _, p := range r.list {
		if p.Path == abs {
			return p, nil
		}
	}
	isRepo, branch := gitInfo(abs)
	p := Project{
		ID:            "proj_" + shortHash(abs),
		Name:          filepath.Base(abs),
		Path:          abs,
		IsGitRepo:     isRepo,
		DefaultBranch: branch,
	}
	r.list = append(r.list, p)
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
	defer r.mu.Unlock()
	out := r.list[:0]
	for _, p := range r.list {
		if p.ID != id {
			out = append(out, p)
		}
	}
	r.list = out
	return r.save()
}

func (r *Registry) save() error {
	if r.path == "" {
		return nil
	}
	if dir := filepath.Dir(r.path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	data, err := json.MarshalIndent(r.list, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(r.path, data, 0o600)
}

func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])[:12]
}

// gitInfo reports whether dir is inside a git work tree and its current branch.
func gitInfo(dir string) (isRepo bool, branch string) {
	if out, err := exec.Command("git", "-C", dir, "rev-parse", "--is-inside-work-tree").Output(); err != nil || strings.TrimSpace(string(out)) != "true" {
		return false, ""
	}
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return true, ""
	}
	return true, strings.TrimSpace(string(out))
}
