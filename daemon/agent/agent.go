// Package agent abstracts a coding-agent provider (opencode, claude-code, ...)
// behind a uniform session model the daemon can drive over the protocol.
package agent

import (
	"context"

	"github.com/howlerops/oculus/daemon/protocol"
)

// Event is something the daemon should forward to the client as a protocol event.
// Type is a protocol.Type* constant; Payload is the matching protocol payload
// struct (e.g. protocol.OutputDelta, protocol.ApprovalRequest, protocol.SessionStatus).
type Event struct {
	Type    string
	Payload any
}

// Encode marshals the event into a protocol envelope (events carry no id).
func (e Event) Encode() ([]byte, error) {
	return protocol.Encode("", e.Type, e.Payload)
}

// Session is a running agent session.
type Session interface {
	ID() string
	Provider() string
	// Events streams translated protocol events until the session is closed.
	Events() <-chan Event
	// Prompt sends a follow-up instruction.
	Prompt(ctx context.Context, text string) error
	// Respond resolves a pending approval (decision is protocol.DecisionAllow/Deny).
	Respond(ctx context.Context, approvalID, decision string) error
	// Stop interrupts the agent.
	Stop(ctx context.Context) error
	// Close releases resources and ends the Events stream.
	Close() error
}

// Prober is an optional Session capability: authoritatively report whether the underlying agent is
// still WORKING a turn (independent of the event stream). The hub's reconciler probes when the
// stream goes quiet: busy → keep waiting forever (no false timeout); not busy → the completion event
// was lost, recover + close the turn. Network-backed providers (opencode) implement this; subprocess
// providers don't need to (their stream-end IS authoritative).
type Prober interface {
	Probe(ctx context.Context) (busy bool, err error)
}

// Recoverer is an optional Session capability: re-fetch and re-emit a turn's final output over
// Events() when its completion was missed (a lost stream event). Best-effort.
type Recoverer interface {
	Recover(ctx context.Context)
}

// Provider creates and lists sessions for one agent backend.
type Provider interface {
	Name() string
	Create(ctx context.Context, cwd, prompt string) (Session, error)
	List(ctx context.Context) ([]protocol.Session, error)
}

// Attacher is an optional Provider capability: attach to an existing session that
// was discovered on the host, replaying its history and then streaming it live.
type Attacher interface {
	// Attach resumes/observes an existing session. cwd is the session's original working
	// directory (used by providers like claude-code that resume as a fresh process; opencode
	// ignores it because the server already knows the session's directory).
	Attach(ctx context.Context, sessionID, cwd string) (Session, error)
}

// Deleter is an optional Session capability: permanently delete the session from the provider's
// server (e.g. opencode's DELETE /session/:id), so a user-initiated delete truly removes it and it
// can't be re-attached or re-discovered later. Providers without server-side state don't implement it.
type Deleter interface {
	Delete(ctx context.Context) error
}

// DirReporter is an optional Session capability: report the session's real working directory. Used
// to heal a stale persisted cwd — e.g. opencode resolves a session's authoritative directory on
// attach, and the hub writes it back so subsequent sends (partitioned by directory) hit the right one.
type DirReporter interface {
	Dir() string
}

// PlanCreator is an optional Provider capability: start a session in "plan mode", where the
// agent proposes a plan (and requests approval to proceed) before making changes. Providers
// that don't support it simply aren't asserted to this interface (the hub falls back to Create).
type PlanCreator interface {
	CreatePlan(ctx context.Context, cwd, prompt string) (Session, error)
}

// ImagePrompter is an optional Session capability: send a prompt with attached images
// to a multimodal agent. Sessions that don't implement it fall back to text-only Prompt.
type ImagePrompter interface {
	PromptImages(ctx context.Context, text string, images []protocol.ImageAttachment) error
}

// ModelLister is an optional Provider capability: report the models the user can pick from.
// Providers that don't implement it are treated as agent-managed (no picker).
type ModelLister interface {
	Models(ctx context.Context) ([]protocol.ModelInfo, error)
}

// ModelSetter is an optional Session capability: switch the model used for subsequent turns.
// provider is the sub-provider/backend (e.g. "openai"); "" when the model id stands alone.
type ModelSetter interface {
	SetModel(provider, model string) error
}
