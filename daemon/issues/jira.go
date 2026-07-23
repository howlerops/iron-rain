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
	"sync"
	"time"
)

// Jira is a Provider backed by Jira Cloud REST v3. Two auth modes:
//   - Basic: an API token (email:token) against the site URL (id.atlassian.com → API tokens).
//   - OAuth: a Bearer access token against https://api.atlassian.com/ex/jira/{cloudid}; the
//     access token is auto-refreshed on a 401 using the stored refresh token.
//
// It fits behind the same interface as Linear.
type Jira struct {
	base string // site URL (basic) or https://api.atlassian.com/ex/jira/{cloudid} (oauth)
	http *http.Client

	mu   sync.Mutex // guards auth/tokens (an OAuth token can rotate under concurrent calls)
	auth string     // "Basic ..." or "Bearer ..."

	// OAuth refresh state (empty for basic-auth connections):
	oauth        bool
	clientID     string
	clientSecret string
	cloudID      string
	accessToken  string
	refreshToken string
	onRefresh    func(access, refresh string) // persist rotated tokens
}

// NewJira builds a Basic-auth Jira adapter. site is the Atlassian base URL, email + apiToken are
// the user's credentials (id.atlassian.com → API tokens).
func NewJira(site, email, apiToken string) *Jira {
	site = strings.TrimRight(site, "/")
	return &Jira{
		base: site,
		auth: "Basic " + base64.StdEncoding.EncodeToString([]byte(email+":"+apiToken)),
		http: &http.Client{Timeout: 30 * time.Second},
	}
}

// NewJiraOAuth builds an OAuth Jira adapter for a specific site (cloudid). It refreshes the access
// token on a 401 and calls onRefresh with the new (access, refresh) so the caller can persist them
// (Atlassian rotates refresh tokens).
func NewJiraOAuth(cloudID, accessToken, refreshToken, clientID, clientSecret string, onRefresh func(access, refresh string)) *Jira {
	return &Jira{
		base:         "https://api.atlassian.com/ex/jira/" + cloudID,
		auth:         "Bearer " + accessToken,
		http:         &http.Client{Timeout: 30 * time.Second},
		oauth:        true,
		clientID:     clientID,
		clientSecret: clientSecret,
		cloudID:      cloudID,
		accessToken:  accessToken,
		refreshToken: refreshToken,
		onRefresh:    onRefresh,
	}
}

func (j *Jira) Name() string { return "jira" }

func (j *Jira) do(ctx context.Context, method, path string, body any, out any) error {
	resp, err := j.send(ctx, method, path, body)
	if err != nil {
		return err
	}
	// OAuth access tokens expire (~1h) → a 401 means "refresh and retry once".
	if j.oauth && resp.StatusCode == http.StatusUnauthorized {
		drainClose(resp.Body)
		if rerr := j.refresh(ctx); rerr != nil {
			return rerr
		}
		if resp, err = j.send(ctx, method, path, body); err != nil {
			return err
		}
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

// send issues one request with the current auth header (re-marshals body so it can be retried).
func (j *Jira) send(ctx context.Context, method, path string, body any) (*http.Response, error) {
	var rdr *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, j.base+path, rdr)
	if err != nil {
		return nil, err
	}
	j.mu.Lock()
	auth := j.auth
	j.mu.Unlock()
	req.Header.Set("Authorization", auth)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	return j.http.Do(req)
}

// RefreshToken proactively refreshes the OAuth access token (keeps the connection alive and rotates
// the refresh token so it doesn't lapse), for the periodic token-refresh cron. No-op for basic auth.
func (j *Jira) RefreshToken(ctx context.Context) error {
	j.mu.Lock()
	isOAuth := j.oauth
	j.mu.Unlock()
	if !isOAuth {
		return nil
	}
	return j.refresh(ctx)
}

// refresh swaps the refresh token for a fresh access (+ rotated refresh) token and persists it.
func (j *Jira) refresh(ctx context.Context) error {
	j.mu.Lock()
	clientID, clientSecret, refreshTok := j.clientID, j.clientSecret, j.refreshToken
	j.mu.Unlock()
	tok, err := refreshJiraToken(ctx, clientID, clientSecret, refreshTok)
	if err != nil {
		return err
	}
	if tok.AccessToken == "" {
		return fmt.Errorf("jira: token refresh returned no access_token")
	}
	j.mu.Lock()
	j.accessToken = tok.AccessToken
	j.auth = "Bearer " + tok.AccessToken
	if tok.RefreshToken != "" {
		j.refreshToken = tok.RefreshToken // Atlassian rotates refresh tokens
	}
	onRefresh, newRefresh := j.onRefresh, j.refreshToken
	j.mu.Unlock()
	if onRefresh != nil {
		onRefresh(tok.AccessToken, newRefresh)
	}
	return nil
}

// adfNode is a node in Atlassian Document Format (Jira Cloud's rich-text JSON for descriptions).
type adfNode struct {
	Type    string    `json:"type"`
	Text    string    `json:"text"`
	Content []adfNode `json:"content"`
}

// adfToText flattens ADF to plain text — concatenating text nodes with newlines between blocks — so
// a Jira description imports as readable body text instead of being dropped (it's an object, not a
// string, so the old `Description string` silently parsed to empty). "" for null/empty/unparseable.
func adfToText(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var node adfNode
	if err := json.Unmarshal(raw, &node); err != nil {
		return "" // some tokens/instances return a plain string or nothing — don't error the whole list
	}
	var b strings.Builder
	adfWalk(&node, &b)
	return strings.TrimSpace(b.String())
}

func adfWalk(n *adfNode, b *strings.Builder) {
	switch n.Type {
	case "text":
		b.WriteString(n.Text)
	case "hardBreak":
		b.WriteByte('\n')
	}
	for i := range n.Content {
		adfWalk(&n.Content[i], b)
	}
	switch n.Type { // block nodes end with a newline so paragraphs/list items separate
	case "paragraph", "heading", "listItem", "blockquote", "codeBlock", "rule":
		b.WriteByte('\n')
	}
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
	q.Set("fields", "summary,status,priority,project,updated,assignee,issuetype,description")
	q.Set("maxResults", "50")
	var data struct {
		Issues []struct {
			ID     string `json:"id"`
			Key    string `json:"key"`
			Fields struct {
				Summary     string          `json:"summary"`
				Updated     string          `json:"updated"`
				Description json.RawMessage `json:"description"` // ADF object on Jira Cloud
				Status      struct {
					Name           string `json:"name"`
					StatusCategory struct {
						Key string `json:"key"`
					} `json:"statusCategory"`
				} `json:"status"`
				Priority struct {
					Name string `json:"name"`
				} `json:"priority"`
				Assignee struct {
					DisplayName string `json:"displayName"`
				} `json:"assignee"`
				IssueType struct {
					Name string `json:"name"`
				} `json:"issuetype"`
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
			Body:     adfToText(is.Fields.Description),
			Status:   is.Fields.Status.Name, Category: jiraCategory(is.Fields.Status.StatusCategory.Key),
			Assignee: is.Fields.Assignee.DisplayName, Priority: jiraPriority(is.Fields.Priority.Name),
			Provider: "jira", TeamID: is.Fields.Project.Key, BranchName: branchNameFor(is.Key, is.Fields.Summary),
			URL: j.base + "/browse/" + is.Key, UpdatedAt: is.Fields.Updated,
		})
	}
	return out, nil
}

// jiraPriority maps Jira's named priorities to our numeric scale (1 = highest/urgent → red dot),
// matching Linear's convention so the board renders both consistently. 0 = none/unset.
func jiraPriority(name string) int {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "highest", "urgent", "blocker", "critical":
		return 1
	case "high", "major":
		return 2
	case "medium", "normal":
		return 3
	case "low", "minor":
		return 4
	case "lowest", "trivial":
		return 5
	default:
		return 0
	}
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

// Detail fetches one issue (best-effort, no comments). The richer inspector is
// Linear-focused; Jira gets a minimal read so the interface is satisfied.
func (j *Jira) Detail(ctx context.Context, issueKey string) (Issue, []Comment, error) {
	var data struct {
		ID     string `json:"id"`
		Key    string `json:"key"`
		Fields struct {
			Summary     string          `json:"summary"`
			Description json.RawMessage `json:"description"` // ADF object
			Updated     string          `json:"updated"`
			Status      struct {
				Name           string `json:"name"`
				StatusCategory struct {
					Key string `json:"key"`
				} `json:"statusCategory"`
			} `json:"status"`
			Priority struct {
				Name string `json:"name"`
			} `json:"priority"`
			Assignee struct {
				DisplayName string `json:"displayName"`
			} `json:"assignee"`
			Project struct {
				ID, Key string
			} `json:"project"`
		} `json:"fields"`
	}
	if err := j.do(ctx, http.MethodGet, "/rest/api/3/issue/"+issueKey+"?fields=summary,description,status,project,updated,priority,assignee", nil, &data); err != nil {
		return Issue{}, nil, err
	}
	return Issue{
		ID: data.Key, Key: data.Key, Title: data.Fields.Summary, Body: adfToText(data.Fields.Description),
		Status: data.Fields.Status.Name, Category: jiraCategory(data.Fields.Status.StatusCategory.Key),
		Assignee: data.Fields.Assignee.DisplayName, Priority: jiraPriority(data.Fields.Priority.Name),
		Provider: "jira", TeamID: data.Fields.Project.Key,
		BranchName: branchNameFor(data.Key, data.Fields.Summary),
		URL:        j.base + "/browse/" + data.Key, UpdatedAt: data.Fields.Updated,
	}, nil, nil
}

// Update, EditComment, and FetchImage are not implemented for Jira yet; the daemon's
// rich inspector targets Linear. They keep the Provider interface satisfied.
func (j *Jira) Update(ctx context.Context, issueKey string, f UpdateFields) (Issue, error) {
	return Issue{}, fmt.Errorf("not supported for jira yet")
}

func (j *Jira) EditComment(ctx context.Context, commentID, body string) error {
	return fmt.Errorf("not supported for jira yet")
}

func (j *Jira) FetchImage(ctx context.Context, url string) (string, []byte, error) {
	return "", nil, fmt.Errorf("not supported for jira yet")
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
