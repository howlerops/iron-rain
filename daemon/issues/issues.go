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
	TeamID     string `json:"team_id,omitempty"`
	Priority   int    `json:"priority,omitempty"`
	UpdatedAt  string `json:"updated_at,omitempty"`
}

// State is a workflow state (a kanban column).
type State struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Category string  `json:"category"` // normalized (see categoryFor)
	Position float64 `json:"position"`
}

// Provider is one tracker backend.
type Provider interface {
	Name() string
	ListAssigned(ctx context.Context) ([]Issue, error)
	WorkflowStates(ctx context.Context, teamID string) ([]State, error)
	Comment(ctx context.Context, issueID, body string) error
	Transition(ctx context.Context, issueID, toStateID string) error
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
