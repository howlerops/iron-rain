package issues

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Jira is a Provider backed by Jira Cloud REST v3, authenticated with an API token
// (Basic email:token). It fits behind the same interface as Linear.
type Jira struct {
	base string // https://your-site.atlassian.net
	auth string // "Basic <base64(email:token)>"
	http *http.Client
}

// NewJira builds a Jira adapter. site is the Atlassian base URL, email + apiToken are the
// user's credentials (id.atlassian.com → API tokens).
func NewJira(site, email, apiToken string) *Jira {
	site = strings.TrimRight(site, "/")
	return &Jira{
		base: site,
		auth: "Basic " + base64.StdEncoding.EncodeToString([]byte(email+":"+apiToken)),
		http: &http.Client{Timeout: 30 * time.Second},
	}
}

func (j *Jira) Name() string { return "jira" }

func (j *Jira) do(ctx context.Context, method, path string, body any, out any) error {
	var rdr *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, j.base+path, rdr)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", j.auth)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := j.http.Do(req)
	if err != nil {
		return err
	}
	defer drainClose(resp.Body)
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("jira %s %s: HTTP %s", method, path, resp.Status)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

// jiraCategory maps Jira's statusCategory key to our normalized category.
func jiraCategory(key string) string {
	switch key {
	case "new":
		return "todo"
	case "indeterminate":
		return "in_progress"
	case "done":
		return "done"
	default:
		return "other"
	}
}

func (j *Jira) ListAssigned(ctx context.Context) ([]Issue, error) {
	q := url.Values{}
	q.Set("jql", "assignee = currentUser() AND resolution = Unresolved ORDER BY updated DESC")
	q.Set("fields", "summary,status,priority,project,updated")
	q.Set("maxResults", "50")
	var data struct {
		Issues []struct {
			ID     string `json:"id"`
			Key    string `json:"key"`
			Fields struct {
				Summary string `json:"summary"`
				Updated string `json:"updated"`
				Status  struct {
					Name           string `json:"name"`
					StatusCategory struct {
						Key string `json:"key"`
					} `json:"statusCategory"`
				} `json:"status"`
				Priority struct {
					Name string `json:"name"`
				} `json:"priority"`
				Project struct {
					ID, Key string
				} `json:"project"`
			} `json:"fields"`
		} `json:"issues"`
	}
	if err := j.do(ctx, http.MethodGet, "/rest/api/3/search/jql?"+q.Encode(), nil, &data); err != nil {
		return nil, err
	}
	out := make([]Issue, 0, len(data.Issues))
	for _, is := range data.Issues {
		out = append(out, Issue{
			ID: is.Key, Key: is.Key, Title: is.Fields.Summary,
			Status: is.Fields.Status.Name, Category: jiraCategory(is.Fields.Status.StatusCategory.Key),
			Provider: "jira", TeamID: is.Fields.Project.Key, BranchName: branchNameFor(is.Key, is.Fields.Summary),
			URL: j.base + "/browse/" + is.Key, UpdatedAt: is.Fields.Updated,
		})
	}
	return out, nil
}

// WorkflowStates returns the issue's available transitions as columns (Jira transitions
// are per-issue; teamID here is the issue key).
func (j *Jira) WorkflowStates(ctx context.Context, issueKey string) ([]State, error) {
	var data struct {
		Transitions []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
			To   struct {
				StatusCategory struct {
					Key string `json:"key"`
				} `json:"statusCategory"`
			} `json:"to"`
		} `json:"transitions"`
	}
	if err := j.do(ctx, http.MethodGet, "/rest/api/3/issue/"+issueKey+"/transitions", nil, &data); err != nil {
		return nil, err
	}
	out := make([]State, 0, len(data.Transitions))
	for i, t := range data.Transitions {
		out = append(out, State{ID: t.ID, Name: t.Name, Category: jiraCategory(t.To.StatusCategory.Key), Position: float64(i)})
	}
	return out, nil
}

func (j *Jira) Comment(ctx context.Context, issueKey, body string) error {
	// v3 comments use Atlassian Document Format.
	doc := map[string]any{"body": map[string]any{
		"type": "doc", "version": 1,
		"content": []any{map[string]any{"type": "paragraph",
			"content": []any{map[string]any{"type": "text", "text": body}}}},
	}}
	return j.do(ctx, http.MethodPost, "/rest/api/3/issue/"+issueKey+"/comment", doc, nil)
}

func (j *Jira) Transition(ctx context.Context, issueKey, transitionID string) error {
	body := map[string]any{"transition": map[string]any{"id": transitionID}}
	return j.do(ctx, http.MethodPost, "/rest/api/3/issue/"+issueKey+"/transitions", body, nil)
}

// branchNameFor synthesizes a git branch name for a Jira issue (Linear provides its own).
func branchNameFor(key, summary string) string {
	k, s := branchSlug(key), branchSlug(summary)
	if s == "" {
		return k
	}
	return k + "-" + s
}

func branchSlug(x string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(x) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		default:
			if b.Len() > 0 && !prevDash {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}
