package issues

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// maxImageBytes caps a proxied attachment so a hostile/huge upload can't exhaust
// daemon memory. 10 MB comfortably covers screenshots and pasted images.
const maxImageBytes = 10 << 20

// drainClose reads a response body to EOF and closes it, so net/http can return
// the connection to the idle pool for keep-alive reuse on the next request.
func drainClose(body io.ReadCloser) {
	_, _ = io.Copy(io.Discard, body)
	_ = body.Close()
}

// Linear is a Provider backed by Linear's GraphQL API. Auth is a token (OAuth access
// token or personal API key) sent in the Authorization header.
type Linear struct {
	token    string
	endpoint string
	http     *http.Client
}

const linearEndpoint = "https://api.linear.app/graphql"

func NewLinear(token string) *Linear {
	return &Linear{token: token, endpoint: linearEndpoint, http: &http.Client{Timeout: 30 * time.Second}}
}

func (l *Linear) Name() string { return "linear" }

// gql posts a GraphQL query and decodes data into out.
func (l *Linear) gql(ctx context.Context, query string, vars map[string]any, out any) error {
	body, _ := json.Marshal(map[string]any{"query": query, "variables": vars})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, l.endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", l.token)
	resp, err := l.http.Do(req)
	if err != nil {
		return err
	}
	defer drainClose(resp.Body)
	// Check the HTTP status before decoding: a non-2xx gateway/rate-limit
	// response often has a non-JSON body, and Decode would mask it with an
	// opaque parse error instead of the useful HTTP status.
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("linear: HTTP %s", resp.Status)
	}
	var env struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return err
	}
	if len(env.Errors) > 0 {
		return fmt.Errorf("linear: %s", env.Errors[0].Message)
	}
	if out != nil {
		return json.Unmarshal(env.Data, out)
	}
	return nil
}

const assignedIssuesQuery = `query { viewer { assignedIssues(first: 50, includeArchived: false) {
  nodes { id identifier title description url branchName priority updatedAt
    state { id name type } team { id key name } cycle { id number name } }
} } }`

func (l *Linear) ListAssigned(ctx context.Context) ([]Issue, error) {
	var data struct {
		Viewer struct {
			AssignedIssues struct {
				Nodes []struct {
					ID          string `json:"id"`
					Identifier  string `json:"identifier"`
					Title       string `json:"title"`
					Description string `json:"description"`
					URL         string `json:"url"`
					BranchName  string `json:"branchName"`
					Priority    int    `json:"priority"`
					UpdatedAt   string `json:"updatedAt"`
					State       struct {
						ID, Name, Type string
					} `json:"state"`
					Team  struct{ ID, Key, Name string } `json:"team"`
					Cycle struct {
						ID     string `json:"id"`
						Number int    `json:"number"`
						Name   string `json:"name"`
					} `json:"cycle"`
				} `json:"nodes"`
			} `json:"assignedIssues"`
		} `json:"viewer"`
	}
	if err := l.gql(ctx, assignedIssuesQuery, nil, &data); err != nil {
		return nil, err
	}
	out := make([]Issue, 0, len(data.Viewer.AssignedIssues.Nodes))
	for _, n := range data.Viewer.AssignedIssues.Nodes {
		out = append(out, Issue{
			ID: n.ID, Key: n.Identifier, Title: n.Title, Body: n.Description, URL: n.URL,
			Status: n.State.Name, Category: categoryFor(n.State.Type),
			Provider: "linear", BranchName: n.BranchName, TeamID: n.Team.ID, TeamName: n.Team.Name,
			Priority: n.Priority, UpdatedAt: n.UpdatedAt,
			CycleID: n.Cycle.ID, CycleNumber: n.Cycle.Number, CycleName: n.Cycle.Name,
		})
	}
	return out, nil
}

const workflowStatesQuery = `query($teamId: String!) { team(id: $teamId) {
  states { nodes { id name type position } } } }`

func (l *Linear) WorkflowStates(ctx context.Context, teamID string) ([]State, error) {
	var data struct {
		Team struct {
			States struct {
				Nodes []struct {
					ID, Name, Type string
					Position       float64
				} `json:"nodes"`
			} `json:"states"`
		} `json:"team"`
	}
	if err := l.gql(ctx, workflowStatesQuery, map[string]any{"teamId": teamID}, &data); err != nil {
		return nil, err
	}
	out := make([]State, 0, len(data.Team.States.Nodes))
	for _, s := range data.Team.States.Nodes {
		out = append(out, State{ID: s.ID, Name: s.Name, Category: categoryFor(s.Type), Position: s.Position})
	}
	return out, nil
}

const commentCreateMutation = `mutation($issueId: String!, $body: String!) {
  commentCreate(input: {issueId: $issueId, body: $body}) { success } }`

func (l *Linear) Comment(ctx context.Context, issueID, body string) error {
	return l.gql(ctx, commentCreateMutation, map[string]any{"issueId": issueID, "body": body}, nil)
}

const issueUpdateMutation = `mutation($id: String!, $stateId: String!) {
  issueUpdate(id: $id, input: {stateId: $stateId}) { success } }`

func (l *Linear) Transition(ctx context.Context, issueID, toStateID string) error {
	return l.gql(ctx, issueUpdateMutation, map[string]any{"id": issueID, "stateId": toStateID}, nil)
}

// ProjectStatuses returns a team's workflow states as board columns. For Linear a "project"
// column set is just the team's workflow states, so this delegates to WorkflowStates.
func (l *Linear) ProjectStatuses(ctx context.Context, teamID string) ([]State, error) {
	return l.WorkflowStates(ctx, teamID)
}

// MoveToStatus moves an issue to a status. Linear's stateId is the status id, so this is a
// plain issueUpdate — delegate to Transition.
func (l *Linear) MoveToStatus(ctx context.Context, issueID, statusID string) error {
	return l.Transition(ctx, issueID, statusID)
}

var issueCreateMutation = `mutation($input: IssueCreateInput!) {
  issueCreate(input: $input) { success issue { ` + issueFields + ` } } }`

// CreateIssue creates a ticket on a team (in.Project is the Linear team id). in.Type is ignored
// (Linear has no free-form issue type on create).
func (l *Linear) CreateIssue(ctx context.Context, in CreateIssueInput) (Issue, error) {
	input := map[string]any{"teamId": in.Project, "title": in.Title}
	if in.Description != "" {
		input["description"] = in.Description
	}
	if in.Priority > 0 {
		input["priority"] = in.Priority
	}
	var data struct {
		IssueCreate struct {
			Success bool            `json:"success"`
			Issue   linearIssueNode `json:"issue"`
		} `json:"issueCreate"`
	}
	if err := l.gql(ctx, issueCreateMutation, map[string]any{"input": input}, &data); err != nil {
		return Issue{}, err
	}
	return data.IssueCreate.Issue.toIssue(), nil
}

const teamsQuery = `query { teams { nodes { id name } } }`

// Projects lists the workspace's teams for the board picker.
func (l *Linear) Projects(ctx context.Context) ([]Project, error) {
	var data struct {
		Teams struct {
			Nodes []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"nodes"`
		} `json:"teams"`
	}
	if err := l.gql(ctx, teamsQuery, nil, &data); err != nil {
		return nil, err
	}
	out := make([]Project, 0, len(data.Teams.Nodes))
	for _, n := range data.Teams.Nodes {
		out = append(out, Project{ID: n.ID, Name: n.Name})
	}
	return out, nil
}

// issueFields is the GraphQL selection shared by Detail and Update so both return a
// fully-populated issue node.
const issueFields = `id identifier title description url branchName priority updatedAt estimate dueDate
  state { id name type } team { id key name } cycle { id number name }
  assignee { id name } labels { nodes { id name color } }`

// linearIssueNode mirrors issueFields for JSON decoding.
type linearIssueNode struct {
	ID          string  `json:"id"`
	Identifier  string  `json:"identifier"`
	Title       string  `json:"title"`
	Description string  `json:"description"`
	URL         string  `json:"url"`
	BranchName  string  `json:"branchName"`
	Priority    int     `json:"priority"`
	UpdatedAt   string  `json:"updatedAt"`
	Estimate    float64 `json:"estimate"`
	DueDate     string  `json:"dueDate"`
	State       struct {
		ID, Name, Type string
	} `json:"state"`
	Team     struct{ ID, Key, Name string } `json:"team"`
	Cycle    struct {
		ID     string `json:"id"`
		Number int    `json:"number"`
		Name   string `json:"name"`
	} `json:"cycle"`
	Assignee struct{ ID, Name string } `json:"assignee"`
	Labels   struct {
		Nodes []struct{ ID, Name, Color string } `json:"nodes"`
	} `json:"labels"`
}

func (n linearIssueNode) toIssue() Issue {
	labels := make([]Label, 0, len(n.Labels.Nodes))
	for _, l := range n.Labels.Nodes {
		labels = append(labels, Label{ID: l.ID, Name: l.Name, Color: l.Color})
	}
	return Issue{
		ID: n.ID, Key: n.Identifier, Title: n.Title, Body: n.Description, URL: n.URL,
		Status: n.State.Name, Category: categoryFor(n.State.Type),
		Provider: "linear", BranchName: n.BranchName, TeamID: n.Team.ID, TeamName: n.Team.Name,
		Priority: n.Priority, UpdatedAt: n.UpdatedAt,
		CycleID: n.Cycle.ID, CycleNumber: n.Cycle.Number, CycleName: n.Cycle.Name,
		AssigneeID: n.Assignee.ID, Assignee: n.Assignee.Name,
		Estimate: n.Estimate, DueDate: n.DueDate, Labels: labels,
	}
}

var issueDetailQuery = `query($id: String!) { issue(id: $id) { ` + issueFields + `
  comments { nodes { id body createdAt user { name } } }
  attachments { nodes { id title url } } } }`

func (l *Linear) Detail(ctx context.Context, issueID string) (Issue, []Comment, []Attachment, error) {
	var data struct {
		Issue struct {
			linearIssueNode
			Comments struct {
				Nodes []struct {
					ID        string `json:"id"`
					Body      string `json:"body"`
					CreatedAt string `json:"createdAt"`
					User      struct {
						Name string `json:"name"`
					} `json:"user"`
				} `json:"nodes"`
			} `json:"comments"`
			Attachments struct {
				Nodes []struct {
					ID    string `json:"id"`
					Title string `json:"title"`
					URL   string `json:"url"`
				} `json:"nodes"`
			} `json:"attachments"`
		} `json:"issue"`
	}
	if err := l.gql(ctx, issueDetailQuery, map[string]any{"id": issueID}, &data); err != nil {
		return Issue{}, nil, nil, err
	}
	comments := make([]Comment, 0, len(data.Issue.Comments.Nodes))
	for _, c := range data.Issue.Comments.Nodes {
		comments = append(comments, Comment{ID: c.ID, Author: c.User.Name, Body: c.Body, CreatedAt: c.CreatedAt})
	}
	// Linear attachments are integration links (not always files), so IsImage is best-effort
	// false — the app can still surface them as links.
	attachments := make([]Attachment, 0, len(data.Issue.Attachments.Nodes))
	for _, a := range data.Issue.Attachments.Nodes {
		attachments = append(attachments, Attachment{ID: a.ID, Filename: a.Title, URL: a.URL})
	}
	return data.Issue.linearIssueNode.toIssue(), comments, attachments, nil
}

// buildUpdateInput turns the non-nil fields of f into a Linear issueUpdate input map.
// Factored out so the field-selection logic is unit-testable without a live server.
func buildUpdateInput(f UpdateFields) map[string]any {
	input := map[string]any{}
	if f.Title != nil {
		input["title"] = *f.Title
	}
	if f.Description != nil {
		input["description"] = *f.Description
	}
	if f.StateID != nil {
		input["stateId"] = *f.StateID
	}
	if f.Priority != nil {
		input["priority"] = *f.Priority
	}
	if f.AssigneeID != nil {
		if *f.AssigneeID == "" {
			input["assigneeId"] = nil // unassign
		} else {
			input["assigneeId"] = *f.AssigneeID
		}
	}
	if f.LabelIDs != nil {
		input["labelIds"] = *f.LabelIDs // full replacement set ([] clears)
	}
	if f.CycleID != nil {
		if *f.CycleID == "" {
			input["cycleId"] = nil
		} else {
			input["cycleId"] = *f.CycleID
		}
	}
	if f.Estimate != nil {
		input["estimate"] = *f.Estimate
	}
	if f.DueDate != nil {
		if *f.DueDate == "" {
			input["dueDate"] = nil
		} else {
			input["dueDate"] = *f.DueDate
		}
	}
	return input
}

var issueUpdateFieldsMutation = `mutation($id: String!, $input: IssueUpdateInput!) {
  issueUpdate(id: $id, input: $input) { success issue { ` + issueFields + ` } } }`

func (l *Linear) Update(ctx context.Context, issueID string, f UpdateFields) (Issue, error) {
	var data struct {
		IssueUpdate struct {
			Success bool            `json:"success"`
			Issue   linearIssueNode `json:"issue"`
		} `json:"issueUpdate"`
	}
	vars := map[string]any{"id": issueID, "input": buildUpdateInput(f)}
	if err := l.gql(ctx, issueUpdateFieldsMutation, vars, &data); err != nil {
		return Issue{}, err
	}
	return data.IssueUpdate.Issue.toIssue(), nil
}

// Members lists a team's members (assignee picker). teamID is the Linear team id.
const membersQuery = `query($teamId: String!) { team(id: $teamId) {
  members(first: 100) { nodes { id name email active } } } }`

func (l *Linear) Members(ctx context.Context, teamID, _ string) ([]User, error) {
	var data struct {
		Team struct {
			Members struct {
				Nodes []struct {
					ID, Name, Email string
					Active          bool
				} `json:"nodes"`
			} `json:"members"`
		} `json:"team"`
	}
	if err := l.gql(ctx, membersQuery, map[string]any{"teamId": teamID}, &data); err != nil {
		return nil, err
	}
	out := make([]User, 0, len(data.Team.Members.Nodes))
	for _, m := range data.Team.Members.Nodes {
		if !m.Active {
			continue
		}
		out = append(out, User{ID: m.ID, Name: m.Name, Email: m.Email})
	}
	return out, nil
}

// ProjectLabels lists a team's labels (label picker).
const labelsQuery = `query($teamId: String!) { team(id: $teamId) {
  labels(first: 200) { nodes { id name color } } } }`

func (l *Linear) ProjectLabels(ctx context.Context, teamID string) ([]Label, error) {
	var data struct {
		Team struct {
			Labels struct {
				Nodes []struct{ ID, Name, Color string } `json:"nodes"`
			} `json:"labels"`
		} `json:"team"`
	}
	if err := l.gql(ctx, labelsQuery, map[string]any{"teamId": teamID}, &data); err != nil {
		return nil, err
	}
	out := make([]Label, 0, len(data.Team.Labels.Nodes))
	for _, lb := range data.Team.Labels.Nodes {
		out = append(out, Label{ID: lb.ID, Name: lb.Name, Color: lb.Color})
	}
	return out, nil
}

// ProjectCycles lists a team's cycles (sprint picker). Newest first; the active cycle is flagged.
const cyclesQuery = `query($teamId: String!) { team(id: $teamId) {
  cycles(first: 20) { nodes { id name number startsAt endsAt completedAt } }
  activeCycle { id } } }`

func (l *Linear) ProjectCycles(ctx context.Context, teamID string) ([]Cycle, error) {
	var data struct {
		Team struct {
			Cycles struct {
				Nodes []struct {
					ID, Name    string
					Number      int
					CompletedAt string `json:"completedAt"`
				} `json:"nodes"`
			} `json:"cycles"`
			ActiveCycle struct{ ID string } `json:"activeCycle"`
		} `json:"team"`
	}
	if err := l.gql(ctx, cyclesQuery, map[string]any{"teamId": teamID}, &data); err != nil {
		return nil, err
	}
	out := make([]Cycle, 0, len(data.Team.Cycles.Nodes))
	for _, c := range data.Team.Cycles.Nodes {
		state := "future"
		if c.CompletedAt != "" {
			state = "closed"
		} else if c.ID == data.Team.ActiveCycle.ID {
			state = "active"
		}
		name := c.Name
		if name == "" {
			name = fmt.Sprintf("Cycle %d", c.Number)
		}
		out = append(out, Cycle{ID: c.ID, Name: name, Number: c.Number, State: state})
	}
	return out, nil
}

const commentUpdateMutation = `mutation($id: String!, $body: String!) {
  commentUpdate(id: $id, input: {body: $body}) { success } }`

func (l *Linear) EditComment(ctx context.Context, commentID, body string) error {
	return l.gql(ctx, commentUpdateMutation, map[string]any{"id": commentID, "body": body}, nil)
}

// allowedImageHost reports whether host is a Linear uploads host we're willing to
// proxy. This is the SSRF guard: only Linear-owned hosts are fetchable, so a
// crafted attachment URL can't turn the daemon into a request proxy for internal
// services.
func allowedImageHost(host string) bool {
	host = strings.ToLower(host)
	return host == "uploads.linear.app" || strings.HasSuffix(host, ".linear.app")
}

func (l *Linear) FetchImage(ctx context.Context, rawURL string) (string, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", nil, err
	}
	if !allowedImageHost(req.URL.Hostname()) {
		return "", nil, fmt.Errorf("linear: refusing to fetch image from disallowed host %q", req.URL.Host)
	}
	req.Header.Set("Authorization", l.token)
	resp, err := l.http.Do(req)
	if err != nil {
		return "", nil, err
	}
	defer drainClose(resp.Body)
	if resp.StatusCode/100 != 2 {
		return "", nil, fmt.Errorf("linear: image HTTP %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxImageBytes))
	if err != nil {
		return "", nil, err
	}
	mime := resp.Header.Get("Content-Type")
	if mime == "" {
		mime = http.DetectContentType(data) // sniffs the first 512 bytes
	}
	return mime, data, nil
}
