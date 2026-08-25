// Package github lists and clones the user's repositories through the `gh` CLI.
//
// Why this exists: the New-session sheet listed every folder the daemon had ever seen an agent run
// in — worktrees, temp directories, three copies of the same repo — because auto-registration adds
// them and nothing ever removes them. On a work machine that list is unusable, and the thing a
// person actually knows is the REPOSITORY name, not which of nine paths on disk holds it.
//
// Through `gh` rather than the GitHub API directly, because gh already holds the user's credential
// and its keyring/SSO handling is not worth reimplementing. The cost is a dependency on a binary
// being installed and authenticated, so every entry point reports that clearly instead of failing
// with an opaque error.
package github

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// listTimeout bounds the repo listing. A cold `gh` call hits the network.
const listTimeout = 25 * time.Second

// cloneTimeout bounds a clone. Generous: a large repo over a slow link is not a failure.
const cloneTimeout = 10 * time.Minute

// maxRepos bounds one listing. The API page cap is 100 and that is plenty to search within; a person
// looking for a repo they touched a year ago will type its name rather than scroll to it.
const maxRepos = 100

// Repo is one repository the user can reach.
type Repo struct {
	Name          string `json:"name"`
	NameWithOwner string `json:"name_with_owner"`
	Description   string `json:"description,omitempty"`
	URL           string `json:"url,omitempty"`
	Private       bool   `json:"private,omitempty"`
	UpdatedAt     string `json:"updated_at,omitempty"`
	Language      string `json:"language,omitempty"`
	// LocalPath is where this repo already sits on the daemon host, "" if it is not checked out.
	//
	// Resolved server-side because the client cannot see the daemon's disk, and because the whole
	// point is to stop asking a person which of several paths is the right one.
	LocalPath string `json:"local_path,omitempty"`
}

// Status reports whether the gh path is usable, and says why not in terms someone can act on.
type Status struct {
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
	Account   string `json:"account,omitempty"`
}

// Check reports whether gh is installed and authenticated.
func Check(ctx context.Context) Status {
	path, err := exec.LookPath("gh")
	if err != nil {
		return Status{Reason: "The GitHub CLI (gh) isn't installed on this machine. Install it with `brew install gh`."}
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "auth", "status").CombinedOutput()
	if err != nil {
		return Status{Reason: "The GitHub CLI is installed but not signed in. Run `gh auth login` on this machine."}
	}
	return Status{Available: true, Account: accountFrom(string(out))}
}

// accountFrom pulls the logged-in handle out of `gh auth status` for display.
func accountFrom(s string) string {
	for _, line := range strings.Split(s, "\n") {
		// "  ✓ Logged in to github.com account jbeck018 (keyring)"
		if i := strings.Index(line, "account "); i >= 0 {
			rest := strings.TrimSpace(line[i+len("account "):])
			if j := strings.IndexAny(rest, " \t("); j > 0 {
				return rest[:j]
			}
			return rest
		}
	}
	return ""
}

// ghRepo is the subset of the REST shape we read.
type ghRepo struct {
	Name        string `json:"name"`
	FullName    string `json:"full_name"`
	Description string `json:"description"`
	HTMLURL     string `json:"html_url"`
	Private     bool   `json:"private"`
	UpdatedAt   string `json:"updated_at"`
	Language    string `json:"language"`
}

// List returns the repositories this account can reach, most recently updated first.
//
// Uses the REST endpoint rather than `gh repo list` because that command only returns repos the user
// OWNS. On a work machine the interesting ones belong to an organisation, so the affiliation filter
// below is what makes this useful at all rather than an empty list.
//
// searchRoots are directories to look in for an existing checkout; each repo that is already on disk
// comes back with LocalPath set, so the UI can offer to open it instead of cloning it again.
func List(ctx context.Context, searchRoots []string) ([]Repo, error) {
	if st := Check(ctx); !st.Available {
		return nil, fmt.Errorf("%s", st.Reason)
	}
	ctx, cancel := context.WithTimeout(ctx, listTimeout)
	defer cancel()

	out, err := exec.CommandContext(ctx, "gh", "api",
		fmt.Sprintf("user/repos?per_page=%d&sort=updated&affiliation=owner,collaborator,organization_member", maxRepos),
	).Output()
	if err != nil {
		return nil, fmt.Errorf("gh could not list your repositories: %w", err)
	}
	var raw []ghRepo
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("could not read gh's answer: %w", err)
	}

	repos := make([]Repo, 0, len(raw))
	for _, r := range raw {
		repos = append(repos, Repo{
			Name:          r.Name,
			NameWithOwner: r.FullName,
			Description:   r.Description,
			URL:           r.HTMLURL,
			Private:       r.Private,
			UpdatedAt:     r.UpdatedAt,
			Language:      r.Language,
			LocalPath:     findLocal(r.Name, searchRoots),
		})
	}
	// Already-cloned repos first: those are the ones a person is most likely to want, and burying
	// them under fifty they have never checked out is the problem this replaces.
	sort.SliceStable(repos, func(i, j int) bool {
		li, lj := repos[i].LocalPath != "", repos[j].LocalPath != ""
		if li != lj {
			return li
		}
		return repos[i].UpdatedAt > repos[j].UpdatedAt
	})
	return repos, nil
}

// findLocal looks for an existing checkout of `name` directly inside any search root.
//
// One level only, and it must contain a .git — a directory that merely shares the repo's name is not
// a checkout, and handing an agent the wrong folder is worse than saying nothing. Deliberately does
// NOT walk the filesystem: a recursive search across a work machine is slow, and the answer it finds
// is no more trustworthy than this one.
func findLocal(name string, roots []string) string {
	for _, root := range roots {
		if root == "" {
			continue
		}
		cand := filepath.Join(root, name)
		if fi, err := os.Stat(cand); err != nil || !fi.IsDir() {
			continue
		}
		if gi, err := os.Stat(filepath.Join(cand, ".git")); err == nil && (gi.IsDir() || gi.Mode().IsRegular()) {
			return cand
		}
	}
	return ""
}

// Clone checks out nameWithOwner into parent/<repo name> and returns the resulting path.
//
// Refuses rather than overwrites when the destination already exists: a clone into an occupied
// directory is either a no-op or a mess, and the caller already knows about existing checkouts
// because List reports them.
func Clone(ctx context.Context, nameWithOwner, parent string) (string, error) {
	// Argument checks BEFORE reaching for gh: rejecting "a/b/c" does not need the network, and a
	// validation path that shells out is one people stop covering because the tests are slow.
	if strings.TrimSpace(nameWithOwner) == "" {
		return "", fmt.Errorf("no repository given")
	}
	// Guard the shape before it becomes a path: "owner/name" is the only form gh clones, and
	// anything with a traversal segment must not be able to steer the destination out of the parent.
	parts := strings.Split(nameWithOwner, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" ||
		strings.Contains(nameWithOwner, "..") || strings.HasPrefix(nameWithOwner, "-") {
		return "", fmt.Errorf("%q is not a valid owner/repository", nameWithOwner)
	}
	parent = strings.TrimSpace(parent)
	if parent == "" || !filepath.IsAbs(parent) {
		return "", fmt.Errorf("the destination folder must be an absolute path")
	}
	dest := filepath.Join(parent, parts[1])
	if _, err := os.Stat(dest); err == nil {
		return "", fmt.Errorf("%s already exists — open it instead of cloning over it", dest)
	}
	if st := Check(ctx); !st.Available {
		return "", fmt.Errorf("%s", st.Reason)
	}
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return "", fmt.Errorf("could not create %s: %w", parent, err)
	}

	ctx, cancel := context.WithTimeout(ctx, cloneTimeout)
	defer cancel()
	// "--" terminates option parsing so a repository name can never be read as a flag.
	cmd := exec.CommandContext(ctx, "gh", "repo", "clone", "--", nameWithOwner, dest)
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("clone failed: %s", strings.TrimSpace(lastLines(string(out), 3)))
	}
	return dest, nil
}

// lastLines keeps an error message short enough to render on a card.
func lastLines(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) <= n {
		return strings.Join(lines, "; ")
	}
	return strings.Join(lines[len(lines)-n:], "; ")
}
