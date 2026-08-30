package pi

import (
	"testing"

	"github.com/howlerops/oculus/daemon/protocol"
)

// The thread capabilities are a promise to the client: whatever is declared here gets a control in
// the UI, and a control that fails when tapped is worse than no control. They were got wrong once
// already — declared for prime-agent, which rides this same adapter but is a different product —
// so the shape is asserted rather than assumed.
func TestPiDeclaresTheThreadOperationsItActuallyHas(t *testing.T) {
	s := &session{id: "s1", p: NewNamed("pi", []string{"pi"}, "")}
	caps := s.Capabilities()

	if caps.Provider != "pi" {
		t.Fatalf("provider = %q, want pi", caps.Provider)
	}
	// /tree calls navigateTree, which moves THIS session's leaf — a rewind, not only a fork. Both
	// are real (fork also exists via --fork and the fork-from-a-message selector).
	if !caps.Thread.Rewind {
		t.Error("pi rewinds in place (navigateTree moves the leaf) — Rewind must be declared")
	}
	if !caps.Thread.Tree || !caps.Thread.Fork {
		t.Error("pi has both a tree and a fork")
	}
	// Summarising the branch you are leaving is offered at the moment of navigating, and is the
	// thing that makes /tree more than an undo.
	if !caps.Thread.Summarize {
		t.Error("pi can summarise the branch being left behind — Summarize must be declared")
	}
	if len(caps.Efforts) == 0 {
		t.Error("pi has thinking levels")
	}
}

// prime-agent (Prime Intellect) rides this adapter and DOES ship the same session-tree machinery —
// its install has navigateTree, the branch-summary collection, and the app.session.tree /
// app.session.fork actions. It was declared with no thread operations at first out of caution, which
// was the wrong error to make: it hid a feature the product has.
func TestPrimeAgentGetsTheThreadOperationsItShips(t *testing.T) {
	s := &session{id: "s1", p: NewNamed("prime-agent", []string{"prime-agent"}, "")}
	caps := s.Capabilities()

	if caps.Provider != "prime-agent" {
		t.Fatalf("provider = %q, want prime-agent", caps.Provider)
	}
	if !caps.Thread.Tree || !caps.Thread.Fork || !caps.Thread.Rewind {
		t.Errorf("prime-agent ships the tree machinery but declares %+v", caps.Thread)
	}
	if len(caps.Modes) == 0 {
		t.Error("every provider gets the daemon-enforced modes")
	}
}

// The allowlist is the point: NewNamed takes an arbitrary name, so a third product riding the same
// RPC protocol must NOT inherit tree controls it does not implement. Sharing a wire protocol is not
// evidence of sharing a feature.
func TestAnUnknownAgentOnThisAdapterGetsNoThreadOperations(t *testing.T) {
	s := &session{id: "s1", p: NewNamed("some-other-agent", []string{"x"}, "")}
	caps := s.Capabilities()
	if caps.Thread != (protocol.ThreadCaps{}) {
		t.Errorf("an unknown agent inherited thread caps: %+v", caps.Thread)
	}
}
