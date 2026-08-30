package agent

import (
	"context"

	"github.com/howlerops/oculus/daemon/protocol"
)

// Declaring what a provider can do, rather than discovering it by trying.
//
// The adapters had converged on a common nine events, which made every session look the same
// regardless of what was actually driving it. That is the right instinct applied at the wrong
// layer: uniformity belongs in the SHAPE of what we report, not in the SET of things we are willing
// to report. Reducing at the adapter boundary throws away information the client could have used,
// and the client cannot ask for it back.
//
// So a provider declares its capabilities as data and reports its live state as data. Adding a
// capability to one provider does not require a protocol change or a new branch in the UI, and a
// provider that lacks something simply says nothing about it — the client renders no affordance
// rather than one that fails when used.
//
// These are OPTIONAL interfaces, matching Prober / ResumeChecker / Replayer: a session that does
// not implement them is not broken, it is just quieter.

// Capable is an optional Session capability: declare what this provider supports.
//
// Called once when the session is attached to and again whenever the answer changes (e.g. a
// provider that learns its command list asynchronously).
type Capable interface {
	Capabilities() protocol.SessionCapabilities
}

// Factual is an optional Session capability: report live ambient state — model, mode, effort,
// branch, context budget, queue depth.
//
// Pull rather than push, so the hub can ask on attach and get a complete picture instead of
// reconstructing one from whatever events happened to have been seen. Adapters that learn a fact
// mid-stream should also emit it as an event; this is the "what is true now" answer for a client
// that just connected.
type Factual interface {
	Facts(ctx context.Context) protocol.SessionFacts
}

// ThreadOps is an optional Session capability: move the CONVERSATION, as opposed to the checkpoint
// machinery which moves the filesystem with git.
//
// pi models this natively — /tree lists the branch points and you can fork from any earlier user
// message. The others have partial equivalents. A provider implements only what it has and declares
// the rest false in ThreadCaps, so the client shows exactly the operations that will work.
type ThreadOps interface {
	// ThreadTree enumerates the points this conversation can be forked or rewound to, newest last.
	ThreadTree(ctx context.Context) ([]protocol.ThreadNode, error)
	// ThreadFork starts a NEW session branching from nodeID, leaving this one untouched. Returns the
	// new session's provider-side id.
	ThreadFork(ctx context.Context, nodeID string) (string, error)
	// ThreadRewind moves THIS session back to nodeID, discarding what came after.
	ThreadRewind(ctx context.Context, nodeID string) error
}

// CapabilitiesOf returns what a session declares, or a zero manifest for one that declares nothing.
// Callers get a usable value either way, so no adapter has to implement Capable just to be handled.
func CapabilitiesOf(s Session) protocol.SessionCapabilities {
	if c, ok := s.(Capable); ok {
		caps := c.Capabilities()
		caps.SessionID = s.ID()
		if caps.Provider == "" {
			caps.Provider = s.Provider()
		}
		return caps
	}
	return protocol.SessionCapabilities{SessionID: s.ID(), Provider: s.Provider()}
}

// FactsOf returns a session's live state, or just its id for one that reports nothing.
func FactsOf(ctx context.Context, s Session) protocol.SessionFacts {
	if f, ok := s.(Factual); ok {
		facts := f.Facts(ctx)
		facts.SessionID = s.ID()
		return facts
	}
	return protocol.SessionFacts{SessionID: s.ID()}
}
