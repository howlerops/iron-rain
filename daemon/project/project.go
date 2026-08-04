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
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/howlerops/oculus/daemon/worktree"
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
	// AbsorbedIDs are the IDs of auto-registered worktree projects that collapseAutoWorktrees
	// folded into this repo. They are kept — not just deleted along with the entries — because
	// project IDs are persisted far outside this registry: loops, approval rules and MCP server
	// scopes all store one. Dropping the ID outright would make an unattended loop pinned to a
	// worktree fail with "unknown project" on the first run after the upgrade, with nothing in
	// the UI explaining why. Get resolves these to the surviving repo instead.
	AbsorbedIDs []string `json:"absorbed_ids,omitempty"`
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
	if r.collapseAutoWorktrees() {
		// Persist so the collapse is a one-time cost rather than something recomputed at every
		// daemon start. A failed write is deliberately not fatal: a read-only or full disk must
		// not stop the daemon booting, the in-memory list is already correct for this run, and
		// the next registry mutation rewrites the file anyway.
		_ = r.save()
	}
	return r, nil
}

// collapseAutoWorktrees rewrites a registry left behind by an older daemon, which auto-registered
// each linked worktree as its own project (it resolved cwds with worktree.RepoRoot, whose answer
// inside a worktree is that worktree). Changing the resolver only fixes projects registered from
// now on; without this, every existing user keeps the pile of one-project-per-session-branch
// entries forever, since nothing else ever removes them. Auto entries that resolve to the same
// repo collapse into a single entry for that repo. It reports whether anything changed.
//
// MANUAL entries are left exactly as they are. If someone navigated to a worktree and added it
// through the Projects UI, that is a stated intent to track it separately, and a migration that
// silently deleted it would destroy the user's own configuration. A manual entry that happens to
// sit at a repo root can still ACQUIRE AbsorbedIDs below — that adds an alias without altering the
// entry's name, path or source, and skipping it would break exactly the loops and approval rules
// of the users who registered their repos properly.
func (r *Registry) collapseAutoWorktrees() bool {
	// Resolve every fold first, mutate second. Working out the full picture up front is what lets
	// a worktree merge into a repo entry that appears LATER in the list instead of synthesizing a
	// duplicate of it.
	roots := make(map[int]string) // index in r.list -> repo root, only for entries that must fold
	for i, p := range r.list {
		if p.Source != "auto" || !isLinkedWorktree(p.Path) {
			continue
		}
		root, err := worktree.MainRepoRoot(p.Path)
		// Keep the entry untouched when its repo can't be resolved — the checkout it borrowed from
		// is gone, git is missing, the repo is corrupt. A failed probe is not evidence that an
		// entry is redundant, and deleting a project on one is unrecoverable; a stale row is merely
		// untidy and the user can remove it themselves.
		if err != nil || root == "" || root == p.Path {
			continue
		}
		roots[i] = filepath.Clean(root)
	}
	if len(roots) == 0 {
		return false
	}

	// Paths that keep an entry of their own regardless: if the repo is already registered (however
	// it got there), its worktrees fold INTO that entry rather than conjuring a second one.
	surviving := make(map[string]bool, len(r.list))
	for i, p := range r.list {
		if _, folding := roots[i]; !folding {
			surviving[p.Path] = true
		}
	}

	kept := make([]Project, 0, len(r.list))
	at := make(map[string]int, len(r.list)) // path -> index in kept
	absorbed := make(map[string][]string)   // repo root -> IDs folded into it
	for i, p := range r.list {
		root, folding := roots[i]
		if !folding {
			if _, dup := at[p.Path]; dup {
				continue // defensive: add() dedupes by path, but never emit two entries for one path
			}
			at[p.Path] = len(kept)
			kept = append(kept, p)
			continue
		}
		absorbed[root] = append(absorbed[root], p.ID)
		if surviving[root] {
			continue // the repo has its own entry elsewhere in the list
		}
		if _, made := at[root]; made {
			continue // a sibling worktree of the same repo already stood one up
		}
		// Take the position of the first worktree that folded here, so the repo lands where the
		// user last saw one of its entries instead of jumping to the end of the sidebar.
		at[root] = len(kept)
		kept = append(kept, autoRepoAt(root))
	}

	for root, ids := range absorbed {
		i, ok := at[root]
		if !ok {
			continue
		}
		for _, id := range ids {
			if id != kept[i].ID && !containsString(kept[i].AbsorbedIDs, id) {
				kept[i].AbsorbedIDs = append(kept[i].AbsorbedIDs, id)
			}
		}
		sort.Strings(kept[i].AbsorbedIDs) // stable on disk: map iteration order must not churn the file
	}
	r.list = kept
	return true
}

// isLinkedWorktree reports whether dir is a linked worktree, by the one signal that needs no
// subprocess: a linked worktree's .git is a FILE (a gitfile naming the main repo's admin dir),
// while an ordinary checkout's is a directory. Gating the migration on this stat is what keeps it
// from spawning a git process per registered project at every single daemon start — after the
// first collapse every auto entry is a repo root and this returns false for all of them. It
// doubles as the check for a path that no longer exists: the stat simply fails.
func isLinkedWorktree(dir string) bool {
	fi, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil && fi.Mode().IsRegular()
}

// autoRepoAt builds the auto-discovered project entry for a resolved repo root. It re-reads the
// branch from git rather than inheriting one from a collapsed worktree, whose branch is the
// session's throwaway oculus/<name> and never the repo's default.
func autoRepoAt(root string) Project {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	isRepo, branch := gitInfo(ctx, root)
	return Project{
		ID:            "proj_" + shortHash(root),
		Name:          filepath.Base(root),
		Path:          root,
		IsGitRepo:     isRepo,
		DefaultBranch: branch,
		Source:        "auto",
	}
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// DirEntry is one browsable subdirectory returned by Browse (for the new-session folder picker).
type DirEntry struct {
	Name      string `json:"name"`
	Path      string `json:"path"` // absolute
	IsGitRepo bool   `json:"is_git_repo"`
}

// BrowseResult is the immediate subdirectories of a directory, for the folder picker.
type BrowseResult struct {
	Path    string     `json:"path"`             // the directory that was listed (absolute)
	Parent  string     `json:"parent,omitempty"` // its parent, for "up" navigation ("" at fs root)
	Entries []DirEntry `json:"entries"`
}

// Browse lists the immediate subdirectories of dir (defaulting to the user's home directory when
// empty), flagging which are git repos so a session can select several and worktree each. It is a
// read-only listing used by the new-session picker to browse INTO a folder and pick sub-folders —
// distinct from the session-scoped fs.tree. Hidden (dot-prefixed) directories are skipped.
func Browse(dir string) (BrowseResult, error) {
	if strings.TrimSpace(dir) == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return BrowseResult{}, err
		}
		dir = home
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return BrowseResult{}, err
	}
	abs = filepath.Clean(abs)
	if fi, err := os.Stat(abs); err != nil {
		return BrowseResult{}, err
	} else if !fi.IsDir() {
		return BrowseResult{}, fmt.Errorf("%s is not a directory", abs)
	}

	ents, err := os.ReadDir(abs)
	if err != nil {
		return BrowseResult{}, err
	}
	out := make([]DirEntry, 0, len(ents))
	for _, e := range ents {
		name := e.Name()
		if strings.HasPrefix(name, ".") { // skip hidden
			continue
		}
		p := filepath.Join(abs, name)
		if !e.IsDir() {
			// Include symlinks that resolve to a directory (common for project folders).
			if e.Type()&os.ModeSymlink == 0 {
				continue
			}
			if fi, err := os.Stat(p); err != nil || !fi.IsDir() {
				continue
			}
		}
		out = append(out, DirEntry{Name: name, Path: p, IsGitRepo: isGitRepo(p)})
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })

	parent := filepath.Dir(abs)
	if parent == abs { // at the filesystem root
		parent = ""
	}
	return BrowseResult{Path: abs, Parent: parent, Entries: out}, nil
}

// isGitRepo reports whether dir contains a .git entry (repo dir or gitfile) — a fast check for the
// picker; the exact branch is resolved later by Add.
func isGitRepo(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
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

// Get returns the project with id, if present. An id belonging to a worktree project that the
// load-time migration folded into its repo resolves to that repo, so a loop, approval rule or
// MCP server pinned to a now-collapsed worktree keeps working instead of failing with "unknown
// project" the first time it runs unattended after the upgrade. Exact IDs are matched in full
// before any alias, so a live project can never be shadowed by a stale reference to a dead one.
func (r *Registry) Get(id string) (Project, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, p := range r.list {
		if p.ID == id {
			return p, true
		}
	}
	for _, p := range r.list {
		if containsString(p.AbsorbedIDs, id) {
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
