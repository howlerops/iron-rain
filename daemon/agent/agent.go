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

// ImagePrompter is an optional Session capability: send a prompt with attached images
// to a multimodal agent. Sessions that don't implement it fall back to text-only Prompt.
type ImagePrompter interface {
	PromptImages(ctx context.Context, text string, images []protocol.ImageAttachment) error
}
