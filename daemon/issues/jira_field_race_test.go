package issues

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// One Jira adapter is shared by several goroutines, and the discovered field ids were read without
// the lock that guards them.
//
// A single *Jira lives in Manager.providers and is driven concurrently by the 60-second tracker poll
// (ListAssigned), the token-refresh cron, and any issue.detail or issue.update the user triggers.
// discoverFields writes sprintFieldID and pointsFieldID under j.mu; every consumer read them bare.
//
// The damage is worse than a missing sprint chip. The id goes straight into the `fields=` query
// parameter, and Jira 400s the ENTIRE /search/jql request when `fields` names a field the instance
// does not have — which the surrounding code says in its own comment. So a torn or half-written read
// does not lose one column, it blanks the whole board.
//
// Run under -race; that is what makes this test mean anything.
func TestConcurrentJiraCallsDoNotRaceOnDiscoveredFields(t *testing.T) {
	const sprintField = "customfield_10777"

	// Discovery is deliberately slow, so the other callers are guaranteed to be reading while it
	// writes rather than after it has finished.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/rest/api/3/field":
			io.WriteString(w, `[{"id":"`+sprintField+`","name":"Sprint","schema":{"custom":"com.pyxis.greenhopper.jira:gh-sprint"}}]`)
		case strings.HasPrefix(r.URL.Path, "/rest/api/3/search/jql"):
			// Reject any custom field that is not the discovered one — exactly what a real instance
			// does, and the reason a torn read is fatal rather than cosmetic.
			for _, f := range strings.Split(r.URL.Query().Get("fields"), ",") {
				if strings.HasPrefix(f, "customfield_") && f != sprintField {
					w.WriteHeader(http.StatusBadRequest)
					io.WriteString(w, `{"errorMessages":["Field '`+f+`' does not exist"]}`)
					return
				}
			}
			io.WriteString(w, `{"issues":[{"id":"1","key":"ENG-1","fields":{"summary":"x","updated":"2026-07-17",
			  "status":{"name":"To Do","statusCategory":{"key":"new"}},"project":{"id":"10","key":"ENG"}}}]}`)
		case strings.HasPrefix(r.URL.Path, "/rest/api/3/issue/"):
			io.WriteString(w, `{"id":"1","key":"ENG-1","fields":{"summary":"x","updated":"2026-07-17",
			  "status":{"name":"To Do","statusCategory":{"key":"new"}},"project":{"id":"10","key":"ENG"}}}`)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	j := NewJira(srv.URL, "me@x.com", "tok")

	// The real concurrency: the poll and a user action, racing first-time discovery.
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var err error
			if i%2 == 0 {
				_, err = j.ListAssigned(context.Background())
			} else {
				_, _, _, err = j.Detail(context.Background(), "ENG-1")
			}
			if err != nil {
				errs <- err
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("a concurrent Jira call failed: %v\n\nIf this is a 400 naming a field that does not "+
			"exist, a half-written field id reached the query string and the board is empty.", err)
	}
}
