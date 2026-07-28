package issues

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
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

	// Per-instance custom-field ids (sprint + story points), discovered once via /field and cached —
	// their ids vary per site, so they can't be hardcoded. Guarded by mu.
	fieldsDiscovered bool
	sprintFieldID    string
	pointsFieldID    string
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
	// NB: Jira's /search/jql 400s the ENTIRE request if `fields` names a field id that doesn't exist
	// on the instance — so we must NOT hardcode the sprint custom field here (its id varies per
	// instance; customfield_10020 is only the *default*). Sprint needs dynamic field-id discovery
	// (GET /rest/api/3/field → the gh-sprint field) before it can be requested safely; until then we
	// omit it so tickets always load. See Sprint parsing below (stays empty for now).
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
				Sprint      json.RawMessage `json:"customfield_10020"`
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
					ID, Key, Name string
				} `json:"project"`
			} `json:"fields"`
		} `json:"issues"`
	}
	if err := j.do(ctx, http.MethodGet, "/rest/api/3/search/jql?"+q.Encode(), nil, &data); err != nil {
		return nil, err
	}
	out := make([]Issue, 0, len(data.Issues))
	for _, is := range data.Issues {
		sprintName, sprintState := jiraSprint(is.Fields.Sprint)
		out = append(out, Issue{
			ID: is.Key, Key: is.Key, Title: is.Fields.Summary,
			Body:     adfToText(is.Fields.Description),
			Status:   is.Fields.Status.Name, Category: jiraCategory(is.Fields.Status.StatusCategory.Key),
			Assignee: is.Fields.Assignee.DisplayName, Priority: jiraPriority(is.Fields.Priority.Name),
			Provider: "jira", TeamID: is.Fields.Project.Key, TeamName: is.Fields.Project.Name,
			BranchName: branchNameFor(is.Key, is.Fields.Summary),
			URL:        j.base + "/browse/" + is.Key, UpdatedAt: is.Fields.Updated,
			SprintName: sprintName, SprintState: sprintState,
		})
	}
	return out, nil
}

// jiraSprint extracts the active (or last) sprint name/state from Jira's sprint custom field.
// The field id varies per instance and its shape drifts (an array of objects on modern Cloud, an
// array of GreenHopper strings on old instances), so parse defensively and never error the list.
func jiraSprint(raw json.RawMessage) (name, state string) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", ""
	}
	var sprints []struct {
		Name  string `json:"name"`
		State string `json:"state"`
	}
	if err := json.Unmarshal(raw, &sprints); err != nil || len(sprints) == 0 {
		return "", "" // old string form or unexpected shape — skip rather than fail
	}
	// Prefer an active sprint; otherwise fall back to the last one listed.
	for _, s := range sprints {
		if strings.EqualFold(s.State, "active") {
			return s.Name, s.State
		}
	}
	last := sprints[len(sprints)-1]
	return last.Name, last.State
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

// jiraPriorityName maps our numeric scale back to a Jira priority name for issue creation
// (inverse of jiraPriority). "" for 0/unset so the caller can omit the field.
func jiraPriorityName(n int) string {
	switch n {
	case 1:
		return "Highest"
	case 2:
		return "High"
	case 3:
		return "Medium"
	case 4:
		return "Low"
	case 5:
		return "Lowest"
	default:
		return ""
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

// ProjectStatuses returns a project's real workflow statuses as ordered board columns. Jira
// exposes statuses per issue type, so we flatten and dedupe (first occurrence wins) to present
// one column set for the whole project.
func (j *Jira) ProjectStatuses(ctx context.Context, projectKey string) ([]State, error) {
	var data []struct {
		Name     string `json:"name"` // issue type name
		Statuses []struct {
			ID             string `json:"id"`
			Name           string `json:"name"`
			StatusCategory struct {
				Key string `json:"key"`
			} `json:"statusCategory"`
		} `json:"statuses"`
	}
	if err := j.do(ctx, http.MethodGet, "/rest/api/3/project/"+projectKey+"/statuses", nil, &data); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	out := make([]State, 0)
	for _, it := range data {
		for _, s := range it.Statuses {
			if seen[s.ID] {
				continue
			}
			seen[s.ID] = true
			out = append(out, State{ID: s.ID, Name: s.Name, Category: jiraCategory(s.StatusCategory.Key), Position: float64(len(out))})
		}
	}
	return out, nil
}

// MoveToStatus drags an issue to a status column. Jira has no "set status" API — you POST a
// workflow transition — so we look up the transition that lands on statusID and execute it.
// Jira workflows don't always allow arbitrary moves, so an unreachable column is a clear error.
func (j *Jira) MoveToStatus(ctx context.Context, issueKey, statusID string) error {
	var data struct {
		Transitions []struct {
			ID string `json:"id"`
			To struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"to"`
		} `json:"transitions"`
	}
	if err := j.do(ctx, http.MethodGet, "/rest/api/3/issue/"+issueKey+"/transitions", nil, &data); err != nil {
		return err
	}
	for _, t := range data.Transitions {
		if t.To.ID == statusID {
			return j.Transition(ctx, issueKey, t.ID)
		}
	}
	return fmt.Errorf("no transition from the current status to that column")
}

// CreateIssue creates a ticket via the v3 issue API. The description must be ADF, so a
// non-empty body is wrapped in a single-paragraph doc.
func (j *Jira) CreateIssue(ctx context.Context, in CreateIssueInput) (Issue, error) {
	issueType := in.Type
	if issueType == "" {
		issueType = "Task"
	}
	fields := map[string]any{
		"project":   map[string]any{"key": in.Project},
		"summary":   in.Title,
		"issuetype": map[string]any{"name": issueType},
	}
	if in.Description != "" {
		fields["description"] = map[string]any{
			"type": "doc", "version": 1,
			"content": []any{map[string]any{"type": "paragraph",
				"content": []any{map[string]any{"type": "text", "text": in.Description}}}},
		}
	}
	if in.Priority > 0 {
		fields["priority"] = map[string]any{"name": jiraPriorityName(in.Priority)}
	}
	var resp struct {
		ID  string `json:"id"`
		Key string `json:"key"`
	}
	if err := j.do(ctx, http.MethodPost, "/rest/api/3/issue", map[string]any{"fields": fields}, &resp); err != nil {
		return Issue{}, err
	}
	// Build from the create response + inputs; the board refreshes the list afterwards, which
	// fills in the server-resolved status/priority names.
	return Issue{
		ID: resp.Key, Key: resp.Key, Title: in.Title, Body: in.Description,
		Priority: in.Priority, Provider: "jira", TeamID: in.Project,
		BranchName: branchNameFor(resp.Key, in.Title),
		URL:        j.base + "/browse/" + resp.Key,
	}, nil
}

// Projects lists the site's projects for the board picker (first page, maxResults=50).
func (j *Jira) Projects(ctx context.Context) ([]Project, error) {
	var data struct {
		Values []struct {
			Key  string `json:"key"`
			Name string `json:"name"`
		} `json:"values"`
	}
	if err := j.do(ctx, http.MethodGet, "/rest/api/3/project/search?maxResults=50", nil, &data); err != nil {
		return nil, err
	}
	out := make([]Project, 0, len(data.Values))
	for _, v := range data.Values {
		out = append(out, Project{ID: v.Key, Name: v.Name})
	}
	return out, nil
}

// Detail fetches one issue with its comments and attachments. The sprint field id varies per
// instance (customfield_10020 is the standard) and is parsed defensively so it never fails the read.
func (j *Jira) Detail(ctx context.Context, issueKey string) (Issue, []Comment, []Attachment, error) {
	// Discover the per-instance sprint + story-point field ids so we can request AND read them (best
	// effort — a discovery failure just means those two fields are omitted, never a failed read).
	_ = j.discoverFields(ctx)
	fieldList := "summary,description,status,project,updated,priority,assignee,labels,duedate,comment,attachment"
	if j.sprintFieldID != "" {
		fieldList += "," + j.sprintFieldID
	}
	if j.pointsFieldID != "" {
		fieldList += "," + j.pointsFieldID
	}
	// Decode the fields blob twice: once into the typed struct for known fields, and once into a raw
	// map so the per-instance sprint/points custom fields can be read by their discovered ids.
	var top struct {
		ID     string          `json:"id"`
		Key    string          `json:"key"`
		Fields json.RawMessage `json:"fields"`
	}
	if err := j.do(ctx, http.MethodGet, "/rest/api/3/issue/"+issueKey+"?fields="+fieldList, nil, &top); err != nil {
		return Issue{}, nil, nil, err
	}
	var f struct {
		Summary     string          `json:"summary"`
		Description json.RawMessage `json:"description"`
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
			AccountID   string `json:"accountId"`
			DisplayName string `json:"displayName"`
		} `json:"assignee"`
		Labels  []string `json:"labels"`
		DueDate string   `json:"duedate"`
		Project struct {
			ID, Key, Name string
		} `json:"project"`
		Comment struct {
			Comments []struct {
				ID     string `json:"id"`
				Author struct {
					DisplayName string `json:"displayName"`
				} `json:"author"`
				Body    json.RawMessage `json:"body"`
				Created string          `json:"created"`
			} `json:"comments"`
		} `json:"comment"`
		Attachment []struct {
			ID       string `json:"id"`
			Filename string `json:"filename"`
			Content  string `json:"content"`
			MimeType string `json:"mimeType"`
			Size     int    `json:"size"`
		} `json:"attachment"`
	}
	_ = json.Unmarshal(top.Fields, &f)
	var custom map[string]json.RawMessage
	_ = json.Unmarshal(top.Fields, &custom)

	sprintName, sprintState := jiraSprint(custom[j.sprintFieldID])
	labels := make([]Label, 0, len(f.Labels))
	for _, s := range f.Labels {
		labels = append(labels, Label{ID: s, Name: s})
	}
	issue := Issue{
		ID: top.Key, Key: top.Key, Title: f.Summary, Body: adfToText(f.Description),
		Status: f.Status.Name, Category: jiraCategory(f.Status.StatusCategory.Key),
		Assignee: f.Assignee.DisplayName, AssigneeID: f.Assignee.AccountID, Priority: jiraPriority(f.Priority.Name),
		Provider: "jira", TeamID: f.Project.Key, TeamName: f.Project.Name,
		BranchName: branchNameFor(top.Key, f.Summary),
		URL:        j.base + "/browse/" + top.Key, UpdatedAt: f.Updated,
		SprintName: sprintName, SprintState: sprintState,
		Labels: labels, DueDate: f.DueDate, Estimate: jiraNumber(custom[j.pointsFieldID]),
	}
	comments := make([]Comment, 0, len(f.Comment.Comments))
	for _, c := range f.Comment.Comments {
		// Pack the issue key so EditComment (which is issue-scoped in Jira) can recover it.
		comments = append(comments, Comment{ID: top.Key + "|" + c.ID, Author: c.Author.DisplayName, Body: adfToText(c.Body), CreatedAt: c.Created})
	}
	attachments := make([]Attachment, 0, len(f.Attachment))
	for _, a := range f.Attachment {
		attachments = append(attachments, Attachment{
			ID: a.ID, Filename: a.Filename, URL: a.Content, Mime: a.MimeType, Size: a.Size,
			IsImage: strings.HasPrefix(a.MimeType, "image/"),
		})
	}
	return issue, comments, attachments, nil
}

// Update applies the non-nil fields of f to a Jira issue. Status changes are a TRANSITION in Jira
// (not a field), so a StateID is routed through MoveToStatus; every other field is a PUT to the
// issue's fields. Sprint + story points use per-instance custom-field ids discovered on demand. The
// refreshed issue is returned via Detail so the UI reflects server-resolved values.
func (j *Jira) Update(ctx context.Context, issueKey string, f UpdateFields) (Issue, error) {
	// Status: a transition, handled separately from field edits.
	if f.StateID != nil {
		if err := j.MoveToStatus(ctx, issueKey, *f.StateID); err != nil {
			return Issue{}, err
		}
	}
	fields := map[string]any{}
	if f.Title != nil {
		fields["summary"] = *f.Title
	}
	if f.Description != nil {
		fields["description"] = adfDoc(*f.Description)
	}
	if f.Priority != nil {
		if *f.Priority > 0 {
			fields["priority"] = map[string]any{"name": jiraPriorityName(*f.Priority)}
		}
	}
	if f.AssigneeID != nil {
		if *f.AssigneeID == "" {
			fields["assignee"] = nil // unassign
		} else {
			fields["assignee"] = map[string]any{"accountId": *f.AssigneeID}
		}
	}
	if f.LabelIDs != nil {
		labels := *f.LabelIDs
		if labels == nil {
			labels = []string{}
		}
		fields["labels"] = labels // Jira labels are the label strings themselves
	}
	if f.DueDate != nil {
		fields["duedate"] = *f.DueDate // "" clears
	}
	if f.CycleID != nil || f.Estimate != nil {
		if err := j.discoverFields(ctx); err != nil {
			return Issue{}, err
		}
		if f.CycleID != nil && j.sprintFieldID != "" {
			if *f.CycleID == "" {
				fields[j.sprintFieldID] = nil
			} else if id, err := strconv.Atoi(*f.CycleID); err == nil {
				fields[j.sprintFieldID] = id
			}
		}
		if f.Estimate != nil && j.pointsFieldID != "" {
			if *f.Estimate == 0 {
				fields[j.pointsFieldID] = nil
			} else {
				fields[j.pointsFieldID] = *f.Estimate
			}
		}
	}
	if len(fields) > 0 {
		if err := j.do(ctx, http.MethodPut, "/rest/api/3/issue/"+issueKey, map[string]any{"fields": fields}, nil); err != nil {
			return Issue{}, err
		}
	}
	issue, _, _, err := j.Detail(ctx, issueKey)
	return issue, err
}

// discoverFields resolves + caches the per-instance sprint and story-point custom-field ids from
// GET /field. Runs once; failures are non-fatal for fields we can't find (they're simply skipped).
func (j *Jira) discoverFields(ctx context.Context) error {
	j.mu.Lock()
	done := j.fieldsDiscovered
	j.mu.Unlock()
	if done {
		return nil
	}
	var fields []struct {
		ID     string `json:"id"`
		Name   string `json:"name"`
		Schema struct {
			Custom string `json:"custom"`
			Type   string `json:"type"`
		} `json:"schema"`
	}
	if err := j.do(ctx, http.MethodGet, "/rest/api/3/field", nil, &fields); err != nil {
		return err
	}
	var sprintID, pointsID string
	for _, fld := range fields {
		switch {
		case fld.Schema.Custom == "com.pyxis.greenhopper.jira:gh-sprint":
			sprintID = fld.ID
		case fld.Schema.Custom == "com.pyxis.greenhopper.jira:jsw-story-points" ||
			fld.Name == "Story Points" || fld.Name == "Story point estimate":
			if pointsID == "" {
				pointsID = fld.ID
			}
		}
	}
	j.mu.Lock()
	j.sprintFieldID, j.pointsFieldID, j.fieldsDiscovered = sprintID, pointsID, true
	j.mu.Unlock()
	return nil
}

// Members lists people assignable to the issue (assignee picker). projectID is the issue key here,
// which scopes assignability to that issue's project + permissions.
func (j *Jira) Members(ctx context.Context, projectKey, issueKey string) ([]User, error) {
	var users []struct {
		AccountID   string `json:"accountId"`
		DisplayName string `json:"displayName"`
		Email       string `json:"emailAddress"`
		Active      bool   `json:"active"`
	}
	// Prefer issue-scoped assignability (respects the issue's project + security); fall back to the
	// project when no issue key is supplied.
	scope := "issueKey=" + url.QueryEscape(issueKey)
	if issueKey == "" {
		scope = "project=" + url.QueryEscape(projectKey)
	}
	path := "/rest/api/3/user/assignable/search?maxResults=100&" + scope
	if err := j.do(ctx, http.MethodGet, path, nil, &users); err != nil {
		return nil, err
	}
	out := make([]User, 0, len(users))
	for _, u := range users {
		if !u.Active {
			continue
		}
		out = append(out, User{ID: u.AccountID, Name: u.DisplayName, Email: u.Email})
	}
	return out, nil
}

// ProjectLabels lists the site's labels (label picker). Jira labels are free-form global strings, so
// each label's id is its own text.
func (j *Jira) ProjectLabels(ctx context.Context, projectID string) ([]Label, error) {
	var data struct {
		Values []string `json:"values"`
	}
	if err := j.do(ctx, http.MethodGet, "/rest/api/3/label?maxResults=500", nil, &data); err != nil {
		return nil, err
	}
	out := make([]Label, 0, len(data.Values))
	for _, s := range data.Values {
		out = append(out, Label{ID: s, Name: s})
	}
	return out, nil
}

// ProjectCycles lists the active + future sprints on the project's board (sprint picker). It resolves
// the project's first board, then its sprints. Projects without a board (kanban/none) return nil.
func (j *Jira) ProjectCycles(ctx context.Context, projectKey string) ([]Cycle, error) {
	var boards struct {
		Values []struct {
			ID int `json:"id"`
		} `json:"values"`
	}
	if err := j.do(ctx, http.MethodGet, "/rest/agile/1.0/board?maxResults=1&projectKeyOrId="+url.QueryEscape(projectKey), nil, &boards); err != nil {
		return nil, err
	}
	if len(boards.Values) == 0 {
		return nil, nil
	}
	var sprints struct {
		Values []struct {
			ID    int    `json:"id"`
			Name  string `json:"name"`
			State string `json:"state"`
		} `json:"values"`
	}
	path := fmt.Sprintf("/rest/agile/1.0/board/%d/sprint?state=active,future&maxResults=50", boards.Values[0].ID)
	if err := j.do(ctx, http.MethodGet, path, nil, &sprints); err != nil {
		return nil, nil // board may not be scrum; treat as "no sprints" rather than an error
	}
	out := make([]Cycle, 0, len(sprints.Values))
	for _, s := range sprints.Values {
		out = append(out, Cycle{ID: strconv.Itoa(s.ID), Name: s.Name, State: s.State})
	}
	return out, nil
}

// EditComment replaces a comment body. Jira's comment-edit endpoint is issue-scoped, so the issue key
// is packed into the comment id as "issueKey|commentId" by Detail and split back out here.
func (j *Jira) EditComment(ctx context.Context, commentID, body string) error {
	issueKey, id, ok := strings.Cut(commentID, "|")
	if !ok {
		return fmt.Errorf("jira: comment id missing issue key")
	}
	return j.do(ctx, http.MethodPut, "/rest/api/3/issue/"+issueKey+"/comment/"+id, adfCommentBody(body), nil)
}

// FetchImage GETs an auth-gated Jira attachment (same-origin as the site) and returns its bytes.
func (j *Jira) FetchImage(ctx context.Context, rawURL string) (string, []byte, error) {
	resp, err := j.send(ctx, http.MethodGet, strings.TrimPrefix(rawURL, j.base), nil)
	if err != nil {
		return "", nil, err
	}
	defer drainClose(resp.Body)
	if resp.StatusCode/100 != 2 {
		return "", nil, fmt.Errorf("jira image: HTTP %s", resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 25<<20))
	if err != nil {
		return "", nil, err
	}
	return resp.Header.Get("Content-Type"), data, nil
}

// adfDoc wraps plain text as a minimal ADF document (Jira descriptions/comments are ADF).
func adfDoc(text string) map[string]any {
	return map[string]any{
		"type": "doc", "version": 1,
		"content": []any{map[string]any{"type": "paragraph",
			"content": []any{map[string]any{"type": "text", "text": text}}}},
	}
}

// adfCommentBody is the body payload for a comment create/edit.
func adfCommentBody(text string) map[string]any { return map[string]any{"body": adfDoc(text)} }

// jiraNumber parses a numeric custom-field value (story points), returning 0 for null/absent/non-numeric.
func jiraNumber(raw json.RawMessage) float64 {
	if len(raw) == 0 {
		return 0
	}
	var n float64
	if err := json.Unmarshal(raw, &n); err != nil {
		return 0
	}
	return n
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
