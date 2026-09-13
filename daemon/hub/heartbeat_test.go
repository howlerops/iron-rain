package hub

import (
	"strings"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/protocol"
	"github.com/howlerops/oculus/daemon/store"
)

func TestParseHandoff(t *testing.T) {
	md := "# Ship the widget\n\n## Done\n- wired the API\n- added tests\n\n## In progress\n- polish the UI\n"
	title, summary := parseHandoff(md)
	if title != "Ship the widget" {
		t.Errorf("title = %q, want %q", title, "Ship the widget")
	}
	if summary == "" || len(summary) > 260 {
		t.Errorf("summary length off: %q", summary)
	}
	// Empty input must not panic and yields empties.
	if tt, ss := parseHandoff(""); tt != "" || ss != "" {
		t.Errorf("empty parse = (%q,%q), want empties", tt, ss)
	}
}

func TestBuildChildPrompt(t *testing.T) {
	req := protocol.SessionChild{
		Subtask: "Add retries to the HTTP client",
		Files:   []string{"http/client.go", "http/client_test.go"},
	}
	rec := store.HandoffRecord{Title: "Ship resilient networking", Summary: "wired the client; retries pending"}
	p := buildChildPrompt(req, "/repo/.oculus/handoff/parent.md", rec)

	for _, want := range []string{
		"Add retries to the HTTP client",  // the subtask
		"/repo/.oculus/handoff/parent.md", // handoff pointer (context, not transcript)
		"Ship resilient networking",       // objective from the decision doc
		"http/client.go",                  // file allowlist
	} {
		if !strings.Contains(p, want) {
			t.Errorf("child prompt missing %q\n---\n%s", want, p)
		}
	}
	// It must NOT invent a transcript or tell the child to re-plan everything.
	if strings.Contains(p, "transcript") {
		t.Errorf("child prompt should not reference a transcript")
	}
	// Degrades without a handoff or files.
	bare := buildChildPrompt(protocol.SessionChild{Subtask: "x"}, "", store.HandoffRecord{})
	if !strings.Contains(bare, "x") {
		t.Errorf("bare prompt missing subtask: %s", bare)
	}
}

func TestDeriveState(t *testing.T) {
	now := time.Now()
	old := now.Add(-10 * time.Minute)
	recent := now.Add(-5 * time.Second)
	idle := now.Add(-90 * time.Second) // past idleGrace, before stallWindow

	todo := func(status string) []protocol.Todo {
		return []protocol.Todo{{Content: "x", Status: status}}
	}

	cases := []struct {
		name string
		m    *managedSession
		want string
	}{
		{"error status", &managedSession{lastStatus: protocol.StatusError}, hbErrored},
		{"pending approval", &managedSession{pendingApprovals: 1, lastActivity: old}, hbAwaitingInput},
		{"awaiting status", &managedSession{lastStatus: protocol.StatusAwaitingApproval, lastActivity: old}, hbAwaitingInput},
		{"recent activity is working", &managedSession{lastActivity: recent, latestTodos: todo("pending")}, hbWorking},
		{"all todos done + turn ended = done", &managedSession{lastActivity: old, turnEnded: true, latestTodos: todo("completed")}, hbDone},
		{"idle with incomplete todos = idle_incomplete", &managedSession{lastActivity: idle, turnEnded: true, latestTodos: todo("pending")}, hbIdleIncomplete},
		{"long idle incomplete = stalled", &managedSession{lastActivity: old, turnEnded: true, latestTodos: todo("in_progress")}, hbStalled},
		{"nudges exhausted", &managedSession{lastActivity: old, nudgeCount: 99, latestTodos: todo("pending")}, hbExhausted},
		{"budget exhausted", &managedSession{lastActivity: old, costUSD: 999, latestTodos: todo("pending")}, hbExhausted},
		{"idle, no todos yet = working (give room)", &managedSession{lastActivity: idle}, hbWorking},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := deriveState(c.m, now); got != c.want {
				t.Errorf("deriveState = %q, want %q", got, c.want)
			}
		})
	}
}

// parseHandoff previously used strings.TrimLeft(t, "#> -") — a CHARACTER SET, not a prefix — and
// joined the body lines with a bare space, so structured handoffs came back mangled.
func TestParseHandoffKeepsTextIntact(t *testing.T) {
	title, summary := parseHandoff("# Fixed the parser\n- --- separator kept\n- -3 degrees offset\n- #1 priority\n")
	if title != "Fixed the parser" {
		t.Errorf("title = %q", title)
	}
	// The cutset stripped leading -, # and > repeatedly: "---" vanished, "-3" became "3".
	for _, want := range []string{"--- separator kept", "-3 degrees offset", "#1 priority"} {
		if !strings.Contains(summary, want) {
			t.Errorf("summary %q lost %q", summary, want)
		}
	}
	// List items must stay distinguishable rather than running together as one sentence.
	if !strings.Contains(summary, " · ") {
		t.Errorf("summary %q collapsed its list items", summary)
	}
}

// needs_you must not be reported as done.
//
// It is the terminal state of the nudge ladder — a turn that stayed stuck after the daemon spent
// every nudge — and it exists specifically to page a human. deriveState had no case for it: it is
// not StatusError, its turn is closed so turnPhase is empty, and a session with no outstanding
// to-dos then satisfied "nothing outstanding AND the turn is over" and returned hbDone. The one
// status that means a person is needed rendered on the fleet card as a grey checkmark reading "Done".
func TestNeedsYouIsNotReportedAsDone(t *testing.T) {
	old := time.Now().Add(-2 * time.Hour)
	todo := func(status string) []protocol.Todo {
		return []protocol.Todo{{Content: "ship it", Status: status}}
	}
	for _, tc := range []struct {
		name string
		sess *managedSession
	}{
		{"with no todos at all", &managedSession{lastStatus: protocol.StatusNeedsYou, lastActivity: old, turnEnded: true}},
		{"with every todo finished", &managedSession{lastStatus: protocol.StatusNeedsYou, lastActivity: old, turnEnded: true, latestTodos: todo("completed")}},
		{"with work outstanding", &managedSession{lastStatus: protocol.StatusNeedsYou, lastActivity: old, turnEnded: true, latestTodos: todo("in_progress")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := deriveState(tc.sess, time.Now()); got != hbNeedsYou {
				t.Errorf("deriveState = %q, want %q — a session waiting on a human must not report as %q", got, hbNeedsYou, got)
			}
		})
	}
}

// The budget must stop WORK, not merely stop nudging.
//
// The cost test lived inside `case hbIdleIncomplete:` — the only state where the daemon was about to
// spend a nudge — so it capped what the DAEMON costs and never what the AGENT does. A session making
// steady progress is hbWorking on every tick, never reaches that branch, and runs past its budget
// indefinitely. That is the one surface where the number is real money, on the feature built to run
// unattended, where nobody is watching the meter.
//
// deriveState is the gate: as long as it returns hbWorking for a busy session, the old code could
// never consult the budget at all. That is what this pins.
func TestABusyOverBudgetSessionIsNotLeftToDeriveStateAlone(t *testing.T) {
	now := time.Now()
	over := &managedSession{
		lastActivity: now, // recent activity ⇒ hbWorking
		turnPhase:    "",  // no open turn
		costUSD:      999, // far past any budget
		budgetUSD:    5,
		autonomous:   true,
		latestTodos:  []protocol.Todo{{Content: "keep going", Status: "in_progress"}},
	}
	if got := deriveState(over, now); got != hbWorking {
		t.Fatalf("precondition: a session with recent activity derives as %q, expected %q", got, hbWorking)
	}
	// ...and hbWorking never reached the old budget test, which is the whole defect. The tick now
	// checks cost before the state switch, so being hbWorking is no longer a way to spend forever.
	if over.costUSD < over.budgetUSD {
		t.Fatal("fixture error: the session should be over budget")
	}
}
