// Package issues is a provider-agnostic ticket layer: connect a tracker (Linear first,
// Jira next), list the user's assigned issues, and write back (comment, transition). The
// daemon holds the auth and serves issues to every paired device over the protocol.
package issues

import "context"

// Issue is a normalized ticket across providers.
type Issue struct {
	ID          string `json:"id"`  // provider id (Linear UUID / Jira key)
	Key         string `json:"key"` // human identifier ("ENG-42")
	Title       string `json:"title"`
	Body        string `json:"body,omitempty"`
	Status      string `json:"status"`   // provider status name
	Category    string `json:"category"` // normalized: todo | in_progress | done | other
	Assignee    string `json:"assignee,omitempty"`
	URL         string `json:"url,omitempty"`
	Provider    string `json:"provider"`              // "linear" | "jira"
	BranchName  string `json:"branch_name,omitempty"` // git branch for a worktree
	TeamID      string `json:"team_id,omitempty"`
	TeamName    string `json:"team_name,omitempty"` // human name of the project/team
	Priority    int    `json:"priority,omitempty"`
	UpdatedAt   string `json:"updated_at,omitempty"`
	CycleID     string `json:"cycle_id,omitempty"`
	CycleName   string `json:"cycle_name,omitempty"`
	CycleNumber int    `json:"cycle_number,omitempty"`
	SprintName  string `json:"sprint_name,omitempty"`  // Jira active sprint (Linear reuses cycle)
	SprintState string `json:"sprint_state,omitempty"` // "active" | "future" | "closed"
	// Editable-field detail (set by Detail; enables full two-way editing).
	AssigneeID string  `json:"assignee_id,omitempty"` // provider id of the assignee (for writes)
	Labels     []Label `json:"labels,omitempty"`      // labels/tags currently on the issue
	Estimate   float64 `json:"estimate,omitempty"`    // story points / estimate (0 = none)
	DueDate    string  `json:"due_date,omitempty"`    // ISO date (YYYY-MM-DD), empty = none
}

// User is an assignable person on a project (assignee picker).
type User struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Email  string `json:"email,omitempty"`
	Avatar string `json:"avatar,omitempty"`
}

// Label is a tag/label on a project (label picker). Color is a hex string when the provider gives one.
type Label struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Color string `json:"color,omitempty"`
}

// Cycle is a sprint (Jira) / cycle (Linear) on a project (sprint picker).
type Cycle struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Number int    `json:"number,omitempty"`
	State  string `json:"state,omitempty"` // active | future | closed
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

// UpdateFields is a partial issue edit: only non-nil fields are applied. A pointer to a zero value is
// meaningful (e.g. Estimate=0 clears points, DueDate="" clears the date, LabelIDs=[] clears labels).
type UpdateFields struct {
	Title       *string
	Description *string
	StateID     *string
	Priority    *int
	AssigneeID  *string   // "" unassigns
	LabelIDs    *[]string // full replacement set ([] clears)
	CycleID     *string   // sprint (Jira) / cycle (Linear) id; "" removes from sprint/cycle
	Estimate    *float64  // 0 clears
	DueDate     *string   // "YYYY-MM-DD"; "" clears
}

// CreateIssueInput is a new-ticket request. Project is a Jira project key / Linear team id;
// Type is a Jira issue type name (e.g. "Task"), ignored by Linear.
type CreateIssueInput struct {
	Project     string
	Title       string
	Description string
	Priority    int
	Type        string
}

// Project is a tracker project/team offered in the board picker.
type Project struct {
	ID   string
	Name string
}

// Attachment is a file on an issue. IsImage lets the app render it inline vs. offer a download.
type Attachment struct {
	ID       string
	Filename string
	URL      string // auth-gated content URL (fetch via FetchImage)
	Mime     string
	Size     int
	IsImage  bool
}

// Provider is one tracker backend.
type Provider interface {
	Name() string
	ListAssigned(ctx context.Context) ([]Issue, error)
	WorkflowStates(ctx context.Context, teamID string) ([]State, error)
	Comment(ctx context.Context, issueID, body string) error
	Transition(ctx context.Context, issueID, toStateID string) error

	// Detail fetches a single issue plus its comments and attachments.
	Detail(ctx context.Context, issueID string) (Issue, []Comment, []Attachment, error)
	// Update applies the non-nil fields of f and returns the updated issue.
	Update(ctx context.Context, issueID string, f UpdateFields) (Issue, error)
	// EditComment replaces the body of an existing comment.
	EditComment(ctx context.Context, commentID, body string) error
	// FetchImage GETs an auth-gated attachment, returning its MIME type and bytes.
	FetchImage(ctx context.Context, url string) (mime string, data []byte, err error)

	// ProjectStatuses returns a project/team's ordered workflow statuses (the board columns).
	ProjectStatuses(ctx context.Context, projectID string) ([]State, error)
	// MoveToStatus moves an issue to a status (resolves a Jira transition; sets a Linear state).
	MoveToStatus(ctx context.Context, issueID, statusID string) error
	// CreateIssue creates a ticket and returns it.
	CreateIssue(ctx context.Context, in CreateIssueInput) (Issue, error)
	// Projects lists the projects/teams this provider exposes (board picker).
	Projects(ctx context.Context) ([]Project, error)

	// Members lists people who can be assigned (assignee picker). projectID is the team/project key;
	// issueID is the issue key — Jira scopes assignability by issue, Linear by team.
	Members(ctx context.Context, projectID, issueID string) ([]User, error)
	// ProjectLabels lists the labels/tags available on a project/team (label picker).
	ProjectLabels(ctx context.Context, projectID string) ([]Label, error)
	// ProjectCycles lists a project/team's sprints (Jira) / cycles (Linear) (sprint picker).
	ProjectCycles(ctx context.Context, projectID string) ([]Cycle, error)
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
