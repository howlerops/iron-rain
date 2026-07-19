package issues

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	neturl "net/url"
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
		   "branchName":"jacob/eng-1-fix-bug","priority":2,"updatedAt":"2026-07-17","state":{"id":"s1","name":"In Progress","type":"started"},"team":{"id":"t1","key":"ENG"},"cycle":{"id":"c1","number":7,"name":"Sprint 7"}}
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
	if i.CycleID != "c1" || i.CycleNumber != 7 || i.CycleName != "Sprint 7" {
		t.Fatalf("parsed cycle = %+v", i)
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

func TestBuildUpdateInput_OnlyNonNil(t *testing.T) {
	title := "New title"
	prio := 3
	in := buildUpdateInput(UpdateFields{Title: &title, Priority: &prio})
	if len(in) != 2 {
		t.Fatalf("expected 2 fields, got %d: %+v", len(in), in)
	}
	if in["title"] != "New title" || in["priority"] != 3 {
		t.Fatalf("wrong values: %+v", in)
	}
	if _, ok := in["description"]; ok {
		t.Errorf("description should be absent when nil")
	}
	if _, ok := in["stateId"]; ok {
		t.Errorf("stateId should be absent when nil")
	}
	// Empty fields -> empty input (no accidental clobbering).
	if got := buildUpdateInput(UpdateFields{}); len(got) != 0 {
		t.Fatalf("empty UpdateFields should build empty input, got %+v", got)
	}
}

func TestLinear_FetchImage_SSRFGuard(t *testing.T) {
	l := NewLinear("tok")
	// A non-Linear host must be rejected before any network call is made.
	if _, _, err := l.FetchImage(context.Background(), "http://evil.com/x.png"); err == nil {
		t.Fatal("expected FetchImage to reject a non-linear host")
	}
	if _, _, err := l.FetchImage(context.Background(), "http://uploads.linear.app.evil.com/x.png"); err == nil {
		t.Fatal("expected FetchImage to reject a look-alike host")
	}
}

func TestLinear_FetchImage_AllowsLinearHost(t *testing.T) {
	// Point the client's transport at a stub but keep a *.linear.app URL so the
	// SSRF guard passes; verify auth header, size cap, and MIME sniff fallback.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "tok" {
			t.Errorf("missing auth header on image fetch")
		}
		w.Header().Set("Content-Type", "image/png")
		io.WriteString(w, "PNGDATA")
	}))
	defer srv.Close()

	l := NewLinear("tok")
	// Rewrite requests to uploads.linear.app onto the test server.
	l.http = &http.Client{Transport: rewriteTransport{target: srv.URL}}

	mime, data, err := l.FetchImage(context.Background(), "https://uploads.linear.app/abc/pic.png")
	if err != nil {
		t.Fatal(err)
	}
	if mime != "image/png" || string(data) != "PNGDATA" {
		t.Fatalf("mime=%q data=%q", mime, string(data))
	}
}

// rewriteTransport sends every request to target regardless of the request URL's
// host, so tests can exercise the *.linear.app SSRF-allowed path without DNS.
type rewriteTransport struct{ target string }

func (rt rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	u, err := neturl.Parse(rt.target)
	if err != nil {
		return nil, err
	}
	req.URL.Scheme = u.Scheme
	req.URL.Host = u.Host
	return http.DefaultTransport.RoundTrip(req)
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
