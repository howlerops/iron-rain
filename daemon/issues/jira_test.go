package issues

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestJiraCategory(t *testing.T) {
	for in, want := range map[string]string{"new": "todo", "indeterminate": "in_progress", "done": "done", "x": "other"} {
		if got := jiraCategory(in); got != want {
			t.Errorf("jiraCategory(%q)=%q want %q", in, got, want)
		}
	}
}

func TestBranchNameFor(t *testing.T) {
	if got := branchNameFor("ENG-7", "Fix the Login Bug!"); got != "eng-7-fix-the-login-bug" {
		t.Errorf("branchNameFor = %q", got)
	}
}

func TestJira_ListAssignedAndComment(t *testing.T) {
	var commentBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			t.Error("missing basic auth")
		}
		switch {
		case strings.HasPrefix(r.URL.Path, "/rest/api/3/search/jql"):
			io.WriteString(w, `{"issues":[{"id":"1001","key":"ENG-7","fields":{
			  "summary":"Fix login","updated":"2026-07-17","priority":{"name":"High"},
			  "status":{"name":"In Progress","statusCategory":{"key":"indeterminate"}},
			  "project":{"id":"10","key":"ENG"}}}]}`)
		case strings.Contains(r.URL.Path, "/comment"):
			b, _ := io.ReadAll(r.Body)
			commentBody = string(b)
			w.WriteHeader(http.StatusCreated)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	j := NewJira(srv.URL, "me@x.com", "tok")
	got, err := j.ListAssigned(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d issues", len(got))
	}
	i := got[0]
	if i.Key != "ENG-7" || i.Category != "in_progress" || i.Provider != "jira" ||
		i.BranchName != "eng-7-fix-login" || i.TeamID != "ENG" {
		t.Fatalf("issue = %+v", i)
	}

	if err := j.Comment(context.Background(), "ENG-7", "hello from Iron Rain"); err != nil {
		t.Fatal(err)
	}
	// v3 comment must be Atlassian Document Format.
	var doc map[string]any
	if err := json.Unmarshal([]byte(commentBody), &doc); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(commentBody, `"type":"doc"`) || !strings.Contains(commentBody, "hello from Iron Rain") {
		t.Errorf("comment not ADF: %s", commentBody)
	}
}
