package issues

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

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
    state { id name type } team { id key } }
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
					Team struct{ ID, Key string } `json:"team"`
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
			Provider: "linear", BranchName: n.BranchName, TeamID: n.Team.ID,
			Priority: n.Priority, UpdatedAt: n.UpdatedAt,
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
