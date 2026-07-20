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
