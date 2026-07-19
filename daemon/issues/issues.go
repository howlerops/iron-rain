// Package issues is a provider-agnostic ticket layer: connect a tracker (Linear first,
// Jira next), list the user's assigned issues, and write back (comment, transition). The
// daemon holds the auth and serves issues to every paired device over the protocol.
package issues

import "context"

// Issue is a normalized ticket across providers.
type Issue struct {
	ID         string `json:"id"`  // provider id (Linear UUID / Jira key)
	Key        string `json:"key"` // human identifier ("ENG-42")
	Title      string `json:"title"`
	Body       string `json:"body,omitempty"`
	Status     string `json:"status"`   // provider status name
	Category   string `json:"category"` // normalized: todo | in_progress | done | other
	Assignee   string `json:"assignee,omitempty"`
	URL        string `json:"url,omitempty"`
	Provider   string `json:"provider"`              // "linear" | "jira"
	BranchName string `json:"branch_name,omitempty"` // git branch for a worktree
	TeamID      string `json:"team_id,omitempty"`
	Priority    int    `json:"priority,omitempty"`
	UpdatedAt   string `json:"updated_at,omitempty"`
	CycleID     string `json:"cycle_id,omitempty"`
	CycleName   string `json:"cycle_name,omitempty"`
	CycleNumber int    `json:"cycle_number,omitempty"`
}

// State is a workflow state (a kanban column).
type State struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Category string  `json:"category"` // normalized (see categoryFor)
	Position float64 `json:"position"`
}

// Comment is a single comment on an issue. (Distinct from the Provider.Comment
// method, which adds a comment — Go permits a type and a method to share a name.)
type Comment struct {
	ID        string `json:"id"`
	Author    string `json:"author,omitempty"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at,omitempty"`
}

// UpdateFields is a partial issue edit: only non-nil fields are applied.
type UpdateFields struct {
	Title       *string
	Description *string
	StateID     *string
	Priority    *int
}

// Provider is one tracker backend.
type Provider interface {
	Name() string
	ListAssigned(ctx context.Context) ([]Issue, error)
	WorkflowStates(ctx context.Context, teamID string) ([]State, error)
	Comment(ctx context.Context, issueID, body string) error
	Transition(ctx context.Context, issueID, toStateID string) error

	// Detail fetches a single issue plus its comments.
	Detail(ctx context.Context, issueID string) (Issue, []Comment, error)
	// Update applies the non-nil fields of f and returns the updated issue.
	Update(ctx context.Context, issueID string, f UpdateFields) (Issue, error)
	// EditComment replaces the body of an existing comment.
	EditComment(ctx context.Context, commentID, body string) error
	// FetchImage GETs an auth-gated attachment, returning its MIME type and bytes.
	FetchImage(ctx context.Context, url string) (mime string, data []byte, err error)
}

// categoryFor normalizes a provider status type into our four buckets.
func categoryFor(providerType string) string {
	switch providerType {
	case "backlog", "unstarted", "triage":
		return "todo"
	case "started":
		return "in_progress"
	case "completed":
		return "done"
	default: // canceled, unknown
		return "other"
	}
}
