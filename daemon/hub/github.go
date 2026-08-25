package hub

import (
	"context"
	"log"
	"path/filepath"
	"sort"
	"strings"

	"github.com/howlerops/oculus/daemon/github"
	"github.com/howlerops/oculus/daemon/protocol"
)

// Starting a session from a REPOSITORY rather than from a path.
//
// The folder list this replaces was auto-registered from wherever agents had run, so it accumulated
// worktrees, three checkouts of the same repo, and temp directories, with no search and no way to
// forget any of it. The name a person actually knows is the repository's, and the machine is better
// placed than they are to work out which of nine directories holds it.

// cloneRoots returns the directories the user already keeps checkouts in, most-used first.
//
// Derived from the projects the daemon knows rather than configured, because the answer is already
// present in the data: someone with ~/totango/a, ~/totango/b and ~/work/c means "~/totango" without
// having been asked. Offering a real destination matters most on a phone, where typing an absolute
// path is the difference between a feature and a nuisance.
//
// Worktrees are excluded from the tally. They are a per-task artefact and their parent is a
// directory nobody clones into — counting them would nominate exactly the wrong folder.
func (h *Hub) cloneRoots() []string {
	reg := h.projectRegistry()
	if reg == nil {
		return nil
	}
	count := map[string]int{}
	for _, p := range reg.List() {
		dir := filepath.Dir(p.Path)
		if dir == "" || dir == "." || dir == string(filepath.Separator) {
			continue
		}
		if isWorktreeish(p.Path) || isWorktreeish(dir) {
			continue
		}
		count[dir]++
	}
	roots := make([]string, 0, len(count))
	for dir := range count {
		roots = append(roots, dir)
	}
	sort.SliceStable(roots, func(i, j int) bool {
		if count[roots[i]] != count[roots[j]] {
			return count[roots[i]] > count[roots[j]]
		}
		return roots[i] < roots[j] // stable output so the UI's default doesn't move between calls
	})
	if len(roots) > 5 {
		roots = roots[:5]
	}
	return roots
}

// isWorktreeish reports whether a path looks like scratch space rather than somewhere a person keeps
// repositories. All three of these appear in the real list this feature replaces.
//
// Matched on specific markers rather than "anywhere under the system temp directory", which was the
// first attempt. That blanket rule was both too broad — it would quietly discard a legitimate if
// unusual setup — and impossible to test, since a Go test's own TempDir lives there and every
// fixture was classified as scratch. The narrow markers describe what was actually observed.
func isWorktreeish(path string) bool {
	for _, marker := range []string{"/pr-worktrees/", "/.oculus/worktrees/", "/T/opencode/"} {
		if strings.Contains(path, marker) {
			return true
		}
	}
	return false
}

// handleGitHubRepos answers github.repos.
func (h *Hub) handleGitHubRepos(ctx context.Context) protocol.GitHubRepos {
	roots := h.cloneRoots()
	st := github.Check(ctx)
	if !st.Available {
		return protocol.GitHubRepos{Available: false, Reason: st.Reason, CloneRoots: roots}
	}
	repos, err := github.List(ctx, roots)
	if err != nil {
		return protocol.GitHubRepos{Available: false, Reason: err.Error(), Account: st.Account, CloneRoots: roots}
	}
	out := make([]protocol.GitHubRepo, 0, len(repos))
	for _, r := range repos {
		out = append(out, protocol.GitHubRepo{
			Name: r.Name, NameWithOwner: r.NameWithOwner, Description: r.Description,
			URL: r.URL, Private: r.Private, UpdatedAt: r.UpdatedAt,
			Language: r.Language, LocalPath: r.LocalPath,
		})
	}
	return protocol.GitHubRepos{Available: true, Account: st.Account, Repos: out, CloneRoots: roots}
}

// handleGitHubClone checks a repository out and registers it as a project, so the caller can start a
// session in it without a second round trip.
func (h *Hub) handleGitHubClone(ctx context.Context, req protocol.GitHubClone) (protocol.GitHubCloned, error) {
	parent := req.Parent
	if parent == "" {
		// Fall back to where they already keep things rather than refusing: a phone that could not
		// offer a root would otherwise be unable to clone at all.
		if roots := h.cloneRoots(); len(roots) > 0 {
			parent = roots[0]
		}
	}
	path, err := github.Clone(ctx, req.NameWithOwner, parent)
	if err != nil {
		return protocol.GitHubCloned{}, err
	}
	log.Printf("github: cloned %s to %s", req.NameWithOwner, path)
	// Register it immediately: the caller's next move is to start a session here, and making them
	// add the folder by hand after a successful clone is the kind of seam this feature exists to
	// remove. A failure to register is not a failure to clone, so it is logged, not returned.
	if reg := h.projectRegistry(); reg != nil {
		if _, err := reg.Add(path); err != nil {
			log.Printf("github: cloned %s but could not register it: %v", path, err)
		}
	}
	return protocol.GitHubCloned{Path: path}, nil
}
