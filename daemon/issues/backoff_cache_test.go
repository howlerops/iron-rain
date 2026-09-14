package issues

import (
	"context"
	"path/filepath"
	"testing"
)

// A tracker that is backing off must not have its tickets erased from the board.
//
// Refresh skips any provider failing pollDue and then replaced the cache wholesale with `merged` —
// which only holds the providers that actually ran. So a skipped provider contributed nothing and
// every ticket it had supplied disappeared. One 401 sets a 2–15 minute skip window, and a permanent
// auth failure (an invalid_grant) suspends polling indefinitely, so "Jira hiccupped once" became
// "the Jira column is empty until a human reconnects".
//
// Stale-but-correct is the honest thing to show while the reconnect pill explains why. The tickets
// did not stop existing because a poll failed.
func TestABackingOffTrackerKeepsItsTickets(t *testing.T) {
	m := NewManager(filepath.Join(t.TempDir(), "issues.json"), nil)

	good := &flakyProvider{failingProvider: failingProvider{name: "linear"},
		issues: []Issue{{Key: "LIN-1", Provider: "linear", Title: "from linear"}}}
	flaky := &flakyProvider{failingProvider: failingProvider{name: "jira"},
		issues: []Issue{{Key: "ENG-1", Provider: "jira", Title: "from jira"}}}
	m.AddProvider("linear", good)
	m.AddProvider("jira", flaky)

	// A clean round: both boards load.
	m.Refresh(context.Background())
	if got := len(m.Issues()); got != 2 {
		t.Fatalf("precondition: %d issues after a clean poll, want 2", got)
	}

	// Jira now fails, which puts it into backoff.
	flaky.fail = true
	m.Refresh(context.Background())
	// …and the NEXT round skips it entirely, which is the case that used to blank it.
	flaky.fail = false
	m.Refresh(context.Background())

	var sawJira, sawLinear bool
	for _, iss := range m.Issues() {
		switch iss.Provider {
		case "jira":
			sawJira = true
		case "linear":
			sawLinear = true
		}
	}
	if !sawLinear {
		t.Error("the healthy tracker's tickets disappeared too")
	}
	if !sawJira {
		t.Error("every ticket from the backing-off tracker was erased from the board.\n\n" +
			"The user sees an empty column, not the stale-but-correct one they had a second ago — " +
			"and with a permanent auth failure it stays empty until somebody reconnects by hand.")
	}
}

// flakyProvider reuses failingProvider's stubs and adds a switchable ListAssigned.
type flakyProvider struct {
	failingProvider
	issues []Issue
	fail   bool
}

func (f *flakyProvider) ListAssigned(context.Context) ([]Issue, error) {
	if f.fail {
		return nil, errAuth{}
	}
	return f.issues, nil
}

// Disconnecting a tracker must clear its tickets — "gone" is not "quiet".
//
// The keep-what-it-last-told-us loop is right for a provider that failed or was skipped for backoff.
// But it keyed on success-this-round, and a DISCONNECTED provider can never satisfy that, so every
// ticket it had supplied was re-appended forever. Disconnect drops the adapter and then calls
// Refresh precisely to clear the board; this loop put them straight back, through every later poll,
// until a daemon restart. The integrations screen said "disconnected" while the column stayed full,
// and tapping one of those tickets failed with "jira not connected".
func TestDisconnectingATrackerClearsItsTickets(t *testing.T) {
	m := NewManager(filepath.Join(t.TempDir(), "issues.json"), nil)
	m.AddProvider("linear", &flakyProvider{failingProvider: failingProvider{name: "linear"},
		issues: []Issue{{Key: "LIN-1", Provider: "linear", Title: "from linear"}}})
	m.AddProvider("jira", &flakyProvider{failingProvider: failingProvider{name: "jira"},
		issues: []Issue{{Key: "ENG-1", Provider: "jira", Title: "from jira"}}})

	m.Refresh(context.Background())
	if got := len(m.Issues()); got != 2 {
		t.Fatalf("precondition: %d issues after a clean poll, want 2", got)
	}

	if err := m.Disconnect(context.Background(), "jira"); err != nil {
		t.Fatalf("disconnect: %v", err)
	}

	for _, iss := range m.Issues() {
		if iss.Provider == "jira" {
			t.Fatal("a disconnected tracker's tickets are still on the board. They stay through " +
				"every later poll until the daemon restarts, while the integrations screen says " +
				"disconnected and tapping one fails with \"jira not connected\".")
		}
	}
	var sawLinear bool
	for _, iss := range m.Issues() {
		if iss.Provider == "linear" {
			sawLinear = true
		}
	}
	if !sawLinear {
		t.Fatal("disconnecting one tracker cleared another's tickets")
	}
}
