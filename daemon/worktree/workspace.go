package worktree

import (
	"fmt"
	"os"
	"path/filepath"
)

// A Workspace is a cross-repo isolation unit: one git worktree per member repo, all on the same
// oculus/<slug> branch, checked out side-by-side under a single layout directory
// (~/.oculus/workspaces/<slug>/<repo>). An agent runs in the layout dir and sees each repo as a
// subfolder, so a single task can span repos while every change stays isolated on its own branch
// for a coordinated multi-PR finish. This is the multi-repo analogue of Create().

// Member is one repo's worktree within a workspace.
type Member struct {
	Name       string `json:"name"`        // repo folder name (layout subdir)
	RepoRoot   string `json:"repo_root"`   // the original repo (for remove/prune/PR remote)
	Path       string `json:"path"`        // the worktree checkout path (layout/<name>)
	Branch     string `json:"branch"`      // oculus/<slug>
	BaseCommit string `json:"base_commit"` // repo HEAD when the worktree was created (stable diff base)
}

// WorkspacesBase is where cross-repo workspaces are laid out (sibling of the per-repo worktrees
// base so both live under ~/.oculus).
func WorkspacesBase() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "oculus-workspaces"
	}
	return filepath.Join(home, ".oculus", "workspaces")
}

// CreateWorkspace creates one worktree per repo in repoDirs under base/<slug>, each on branch
// oculus/<slug>. An empty base uses WorkspacesBase(). It returns the layout directory (the shared
// parent the agent runs in) and the members. On any error it rolls back every worktree it created
// so a partial workspace never leaks.
func CreateWorkspace(base, name string, repoDirs []string) (layout string, members []Member, err error) {
	slug := Slug(name)
	if slug == "" {
		return "", nil, fmt.Errorf("workspace: empty name")
	}
	if len(repoDirs) < 2 {
		return "", nil, fmt.Errorf("workspace: need at least 2 repos")
	}
	if base == "" {
		base = WorkspacesBase()
	}
	layout = filepath.Join(base, slug)
	if _, statErr := os.Stat(layout); statErr == nil {
		return "", nil, fmt.Errorf("workspace path already exists: %s", layout)
	}
	if mkErr := os.MkdirAll(layout, 0o755); mkErr != nil {
		return "", nil, mkErr
	}
	branch := "oculus/" + slug

	// Roll back everything created so far on any failure.
	rollback := func() {
		for _, m := range members {
			_ = Remove(m.RepoRoot, m.Path, true)
		}
		_ = os.RemoveAll(layout)
	}

	seen := map[string]bool{}
	for _, dir := range repoDirs {
		root, rErr := RepoRoot(dir)
		if rErr != nil {
			rollback()
			return "", nil, rErr
		}
		memberName := filepath.Base(root)
		if seen[memberName] {
			// Two selected repos share a basename; disambiguate so their checkouts don't collide.
			memberName = memberName + "-" + Slug(filepath.Base(filepath.Dir(root)))
		}
		seen[memberName] = true
		path := filepath.Join(layout, memberName)
		wt, cErr := createAt(root, path, branch)
		if cErr != nil {
			rollback()
			return "", nil, cErr
		}
		base, _ := HeadCommit(root)
		members = append(members, Member{
			Name: memberName, RepoRoot: root, Path: wt.Path, Branch: wt.Branch, BaseCommit: base,
		})
	}
	return layout, members, nil
}

// RemoveWorkspace removes every member worktree and the layout directory. force is needed for
// members with uncommitted changes. Best-effort: it removes what it can and returns the first
// error, so a partially-cleaned workspace still frees most of its worktrees.
func RemoveWorkspace(layout string, members []Member, force bool) error {
	var firstErr error
	for _, m := range members {
		if err := Remove(m.RepoRoot, m.Path, force); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if layout != "" {
		_ = os.RemoveAll(layout)
	}
	return firstErr
}
