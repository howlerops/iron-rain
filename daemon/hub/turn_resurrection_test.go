package hub

import (
	"strings"
	"testing"
)

// A turn that had already finished could be dragged back into "working" and left there forever.
//
// handleUnreachable marks the turn `recovering` and then blocks for up to twenty seconds inside
// Revive. The agent can finish inside that window — a wifi handover or a slow opencode response is
// all it takes to get there — and the pump then delivers idle: closeTurnFrom clears turnPhase, seals
// the tools and releases the wake lock paired with openTurn's hold. When Revive returns, both of its
// exits wrote a phase back unconditionally, reopening a turn that was over.
//
// Nothing closes it a second time. The close already happened, so the composer stays locked, the
// heartbeat keeps reporting the session as working, the Mac is held awake, and the only way out is
// to restart the daemon. escalateStalled has the same shape one function down.
//
// Checked structurally: reproducing it needs a real Reviver blocking against a real pump at exactly
// the wrong moment, and a test that has to win a twenty-second race proves nothing when it passes.
// turnOnStatus already reads `open := m.turnPhase != ""` before it writes; this asserts the two
// functions that did not, do.
func TestATurnThatEndedIsNotResurrected(t *testing.T) {
	src, err := readFileString("turn.go")
	if err != nil {
		t.Fatal(err)
	}

	for _, fn := range []struct{ name, from, to string }{
		{"handleUnreachable", "func (m *managedSession) handleUnreachable(", "func (m *managedSession) escalateStalled("},
		{"escalateStalled", "func (m *managedSession) escalateStalled(", "\nfunc "},
	} {
		body := between(src, fn.from, fn.to)
		if body == "" {
			t.Fatalf("could not find %s", fn.name)
		}
		// Every write of turnPhase in these two must sit behind a check that the turn is still open.
		writes := strings.Count(body, "m.turnPhase = ")
		guards := strings.Count(body, `m.turnPhase == ""`)
		if writes == 0 {
			t.Errorf("%s no longer writes turnPhase — this test needs rewriting, not deleting", fn.name)
			continue
		}
		if guards == 0 {
			t.Errorf("%s writes turnPhase %d time(s) and never checks the turn is still open. It "+
				"blocks for up to 20s before those writes, and a turn that finished in the meantime "+
				"is reopened into a state nothing will close again: composer locked, heartbeat "+
				"reporting \"working\", the Mac held awake, until the daemon is restarted.",
				fn.name, writes)
		}
	}
}

// between returns the source between two markers, or "" if either is missing.
func between(src, from, to string) string {
	i := strings.Index(src, from)
	if i < 0 {
		return ""
	}
	rest := src[i+len(from):]
	j := strings.Index(rest, to)
	if j < 0 {
		return rest
	}
	return rest[:j]
}
