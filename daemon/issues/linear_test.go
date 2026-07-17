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

func TestCategoryFor(t *testing.T) {
	cases := map[string]string{
		"backlog": "todo", "unstarted": "todo", "triage": "todo",
		"started": "in_progress", "completed": "done", "canceled": "other", "weird": "other",
	}
	for in, want := range cases {
		if got := categoryFor(in); got != want {
			t.Errorf("categoryFor(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLinear_ListAssigned(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "tok" {
			t.Errorf("missing auth header")
		}
		var body struct{ Query string }
		_ = json.NewDecoder(r.Body).Decode(&body)
		if !strings.Contains(body.Query, "assignedIssues") {
			t.Fatalf("unexpected query: %s", body.Query)
		}
		io.WriteString(w, `{"data":{"viewer":{"assignedIssues":{"nodes":[
		  {"id":"u1","identifier":"ENG-1","title":"Fix bug","description":"desc","url":"https://linear.app/ENG-1",
		   "branchName":"jacob/eng-1-fix-bug","priority":2,"updatedAt":"2026-07-17","state":{"id":"s1","name":"In Progress","type":"started"},"team":{"id":"t1","key":"ENG"}}
		]}}}}`)
	}))
	defer srv.Close()

	l := NewLinear("tok")
	l.endpoint = srv.URL
	got, err := l.ListAssigned(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d issues", len(got))
	}
	i := got[0]
	if i.Key != "ENG-1" || i.Title != "Fix bug" || i.Category != "in_progress" ||
		i.BranchName != "jacob/eng-1-fix-bug" || i.TeamID != "t1" || i.Provider != "linear" {
		t.Fatalf("parsed issue = %+v", i)
	}
}

func TestNewLinear_HasRequestTimeout(t *testing.T) {
	// A zero timeout lets a hung request wedge the single poll goroutine forever.
	if l := NewLinear("tok"); l.http.Timeout == 0 {
		t.Fatal("NewLinear http client has no timeout")
	}
}

func TestLinear_NonJSONErrorStatus(t *testing.T) {
	// A rate limiter / gateway returns a non-2xx with an HTML body. gql must
	// report the HTTP status, not an opaque JSON parse error.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		io.WriteString(w, "<html>rate limited</html>")
	}))
	defer srv.Close()

	l := NewLinear("tok")
	l.endpoint = srv.URL
	_, err := l.ListAssigned(context.Background())
	if err == nil {
		t.Fatal("expected an error on HTTP 429")
	}
	if !strings.Contains(err.Error(), "429") {
		t.Fatalf("error %q does not mention HTTP status 429", err.Error())
	}
}

func TestLinear_CommentAndTransition(t *testing.T) {
	var seen []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Query     string
			Variables map[string]any
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		seen = append(seen, map[string]any{"q": body.Query, "v": body.Variables})
		io.WriteString(w, `{"data":{"ok":{"success":true}}}`)
	}))
	defer srv.Close()

	l := NewLinear("tok")
	l.endpoint = srv.URL
	if err := l.Comment(context.Background(), "u1", "PR opened: http://pr"); err != nil {
		t.Fatal(err)
	}
	if err := l.Transition(context.Background(), "u1", "s2"); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(seen))
	}
	if !strings.Contains(seen[0]["q"].(string), "commentCreate") || seen[0]["v"].(map[string]any)["body"] != "PR opened: http://pr" {
		t.Errorf("comment call wrong: %+v", seen[0])
	}
	if !strings.Contains(seen[1]["q"].(string), "issueUpdate") || seen[1]["v"].(map[string]any)["stateId"] != "s2" {
		t.Errorf("transition call wrong: %+v", seen[1])
	}
}
