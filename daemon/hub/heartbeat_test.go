package hub

import (
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/protocol"
)

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
