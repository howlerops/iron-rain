package issues

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Jira tickets never showed a sprint on the board.
//
// The board's list call requested a field set that did not include the sprint, then tried to decode
// it from a hardcoded `customfield_10020` — the DEFAULT id, which is only correct by coincidence.
// Both halves came with a comment explaining that dynamic field-id discovery would be needed before
// sprint could be requested safely. That discovery had already been built, and the ticket detail
// view sixty lines below was already using it for exactly this.
//
// The consequence was not a wrong sprint, it was no sprint anywhere on the board, permanently.
func TestTheBoardAsksForTheSprintFieldItDiscovered(t *testing.T) {
	const sprintField = "customfield_10777" // deliberately NOT the default id

	var askedFor string
	var discovered bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/rest/api/3/field":
			discovered = true
			io.WriteString(w, `[
			  {"id":"`+sprintField+`","name":"Sprint","schema":{"custom":"com.pyxis.greenhopper.jira:gh-sprint"}},
			  {"id":"customfield_10004","name":"Story Points","schema":{"custom":"com.pyxis.greenhopper.jira:jsw-story-points"}}
			]`)
		case strings.HasPrefix(r.URL.Path, "/rest/api/3/search/jql"):
			askedFor = r.URL.Query().Get("fields")
			// A real instance 400s the whole request when `fields` names an id it does not have.
			for _, f := range strings.Split(askedFor, ",") {
				if strings.HasPrefix(f, "customfield_") && f != sprintField {
					w.WriteHeader(http.StatusBadRequest)
					io.WriteString(w, `{"errorMessages":["Field '`+f+`' does not exist"]}`)
					return
				}
			}
			io.WriteString(w, `{"issues":[{"id":"1001","key":"ENG-7","fields":{
			  "summary":"Fix login","updated":"2026-07-17","priority":{"name":"High"},
			  "status":{"name":"In Progress","statusCategory":{"key":"indeterminate"}},
			  "project":{"id":"10","key":"ENG"},
			  "`+sprintField+`":[{"name":"Sprint 14","state":"active"}]}}]}`)
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
	if !discovered {
		t.Error("the board never ran field discovery, so it cannot know this instance's sprint field id")
	}
	if !strings.Contains(askedFor, sprintField) {
		t.Errorf("requested fields = %q, missing %s — Jira only returns fields you ask for, so the "+
			"sprint is absent from the response no matter how it is decoded", askedFor, sprintField)
	}
	if len(got) != 1 {
		t.Fatalf("got %d issues", len(got))
	}
	if got[0].SprintName != "Sprint 14" || got[0].SprintState != "active" {
		t.Errorf("sprint = %q/%q, want \"Sprint 14\"/\"active\" — every ticket on the board renders "+
			"with no sprint", got[0].SprintName, got[0].SprintState)
	}
	// The rest of the row must survive the decode change.
	if got[0].Key != "ENG-7" || got[0].Category != "in_progress" || got[0].TeamID != "ENG" ||
		got[0].BranchName != "eng-7-fix-login" || got[0].Title != "Fix login" {
		t.Errorf("issue = %+v", got[0])
	}
}

// An instance with no sprint field at all (Jira Core, or a discovery that failed) must still load
// the board. Requesting a field id that does not exist is a 400 on the ENTIRE search.
func TestTheBoardStillLoadsWhenThereIsNoSprintField(t *testing.T) {
	var askedFor string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/rest/api/3/field":
			io.WriteString(w, `[{"id":"customfield_10004","name":"Story Points","schema":{}}]`)
		case strings.HasPrefix(r.URL.Path, "/rest/api/3/search/jql"):
			askedFor = r.URL.Query().Get("fields")
			if strings.Contains(askedFor, "customfield_") {
				w.WriteHeader(http.StatusBadRequest)
				io.WriteString(w, `{"errorMessages":["Field does not exist"]}`)
				return
			}
			io.WriteString(w, `{"issues":[{"id":"1","key":"ENG-1","fields":{"summary":"x",
			  "status":{"name":"To Do","statusCategory":{"key":"new"}},"project":{"key":"ENG"}}}]}`)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	got, err := NewJira(srv.URL, "me@x.com", "tok").ListAssigned(context.Background())
	if err != nil {
		t.Fatalf("the board failed to load on an instance with no sprint field: %v", err)
	}
	if len(got) != 1 || got[0].SprintName != "" {
		t.Errorf("got %+v, want one issue with no sprint", got)
	}
}
