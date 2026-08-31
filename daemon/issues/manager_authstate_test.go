package issues

import (
	"context"
	"testing"
	"time"
)

// failingProvider always fails ListAssigned, like a tracker whose token has expired.
type failingProvider struct{ name string }

func (f *failingProvider) Name() string { return f.name }
func (f *failingProvider) ListAssigned(context.Context) ([]Issue, error) { return nil, errAuth{} }
func (f *failingProvider) WorkflowStates(context.Context, string) ([]State, error) { return nil, nil }
func (f *failingProvider) Comment(context.Context, string, string) error            { return nil }
func (f *failingProvider) Transition(context.Context, string, string) error         { return nil }
func (f *failingProvider) Detail(context.Context, string) (Issue, []Comment, []Attachment, error) {
	return Issue{}, nil, nil, nil
}
func (f *failingProvider) Update(context.Context, string, UpdateFields) (Issue, error) {
	return Issue{}, nil
}
func (f *failingProvider) EditComment(context.Context, string, string) error { return nil }
func (f *failingProvider) FetchImage(context.Context, string) (string, []byte, error) {
	return "", nil, nil
}
func (f *failingProvider) ProjectStatuses(context.Context, string) ([]State, error) { return nil, nil }
func (f *failingProvider) MoveToStatus(context.Context, string, string) error       { return nil }
func (f *failingProvider) CreateIssue(context.Context, CreateIssueInput) (Issue, error) {
	return Issue{}, nil
}
func (f *failingProvider) Projects(context.Context) ([]Project, error) { return nil, nil }
func (f *failingProvider) Members(context.Context, string, string) ([]User, error) {
	return nil, nil
}
func (f *failingProvider) ProjectLabels(context.Context, string) ([]Label, error) { return nil, nil }
func (f *failingProvider) ProjectCycles(context.Context, string) ([]Cycle, error) { return nil, nil }

type errAuth struct{}

func (errAuth) Error() string { return "linear: HTTP 401 Unauthorized" }

// A tracker that is failing must not read as healthy while it backs off.
//
// The poll loop skips a provider inside its backoff window, and the bookkeeping afterwards cleared
// the auth error of every provider that was not in this round's error map — which included every
// provider that never ran. So a broken tracker's error was deleted on the very next tick and the
// board said "connected and responded, but no issues are assigned to you" while the API was
// returning 401. Worse the longer it lasted: a failure classed permanent suspends polling for good,
// so the error was cleared and never came back.
func TestSkippedProviderKeepsItsAuthError(t *testing.T) {
	m := &Manager{
		providers:  map[string]Provider{},
		authErrors: map[string]string{},
		pollFail:   map[string]*pollState{},
	}
	m.providers["linear"] = &failingProvider{name: "linear"}

	// First poll: it runs, it fails, the error is recorded.
	_ = m.Refresh(t.Context())
	if m.authErrors["linear"] == "" {
		t.Fatal("a failing provider must record an auth error")
	}

	// Force it into its backoff window, exactly as notePollFailure does.
	m.mu.Lock()
	m.pollFail["linear"].skipUntil = time.Now().Add(10 * time.Minute)
	m.mu.Unlock()

	// Second poll: it is skipped. Skipping proves nothing, so the error must survive.
	_ = m.Refresh(t.Context())
	if m.authErrors["linear"] == "" {
		t.Fatal("a provider skipped by backoff had its auth error cleared — the board will claim the tracker is healthy while it is 401ing")
	}
}
