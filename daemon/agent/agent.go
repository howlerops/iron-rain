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

// ResumeChecker is an optional Provider capability: report whether an existing session id can
// actually be RESUMED with its history (vs. an attach that would start a fresh, empty impostor).
// Used at restore to drop unrecoverable husks instead of reviving them as blank sessions.
type ResumeChecker interface {
	CanResume(id string) bool
}

// Replayer is a marker for Sessions whose provider RE-STREAMS the conversation history itself on
// attach (opencode's replayHistory, claude-code's JSONL transcript replay). For these, the provider
// is the single source of replay truth — the hub must NOT also replay its durable transcript, or
// every message shows twice after a daemon restart (the "duplicated prompt" bug). pi/cli have no
// self-replay, so the durable transcript remains their only history source.
type Replayer interface {
	SelfReplaying() bool
}

// Recoverer is an optional Session capability: re-fetch and re-emit a turn's final output over
// Events() when its completion was missed (a lost stream event). Best-effort.
type Recoverer interface {
	Recover(ctx context.Context)
}

// Nudger is an optional Session capability: deliver a message into a turn that is ALREADY RUNNING,
// without killing it.
//
// This exists because Prompt is not safe for the job. For opencode, Prompt aborts an unfinished
// prior turn before sending (a session runs serially there, so a queued prompt would never run) —
// which means the obvious implementation of "nudge a stuck agent to keep going" destroys the very
// work it was trying to rescue. The two operations look identical at the call site and have opposite
// effects, so they get different names.
//
// The contract is narrow and deliberately hard to get wrong:
//   - Nudge MUST NOT abort, cancel, or restart the in-flight turn.
//   - Nudge MAY be a no-op that returns an error if the provider cannot deliver mid-turn.
//   - Nudge is best-effort: the caller treats failure as "this provider can't be nudged" and
//     escalates to a human instead of retrying.
//
// A provider that cannot honor that contract MUST NOT implement Nudger. Falling back to Prompt is
// never correct here — the caller escalates to a human, which is strictly better than silently
// killing a running agent.
type Nudger interface {
	Nudge(ctx context.Context, text string) error
}

// Unsticker is an optional Session capability: send a message into a turn the caller has PROVEN is
// wedged, killing that turn first so the message actually runs.
//
// This is the destructive counterpart to Nudger, split out so the destruction is never accidental.
// Only a caller holding real evidence of a wedge (the hub's turn engine: a provider probe plus a
// tool-progress clock) may use it — never a bare "the last turn hasn't reported idle yet", which is
// equally true of an agent that is simply still working.
type Unsticker interface {
	PromptUnsticking(ctx context.Context, text string) error
}

// Reviver is an optional Session capability: repair this session's own connection to its agent,
// in place, keeping the session id and its history.
//
// It exists so an unreachable agent is something the daemon FIXES rather than something it reports.
// Most "unreachable" is transient — a laptop slept, wifi dropped, the opencode server was restarted
// out from under us — and the old behaviour (four failed probes, then abandon the turn and push
// "the agent stopped responding") turned a blip the daemon could have healed silently into an
// incident the user had to come back and clean up by hand.
//
// The contract:
//   - Revive returns nil ONLY when the session is genuinely usable again — the caller immediately
//     re-probes, so an optimistic nil costs a round-trip, not correctness.
//   - It must preserve the session's identity and history. A Revive that silently starts a FRESH
//     conversation under the same id is worse than failing: the turn would resume against an agent
//     with no memory of it, and nothing downstream could tell.
//   - It must be safe to call repeatedly, and safe to call on a session that turns out to be fine.
//   - Returning an error means "not repaired"; the caller retries with backoff and eventually gives
//     up honestly rather than pretending.
//
// A provider whose session cannot outlive its transport (the generic CLI adapter, where a turn IS
// the process) must NOT implement this — for those, unreachable really is unrecoverable, and saying
// so immediately is the useful answer.
type Reviver interface {
	Revive(ctx context.Context) error
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

// ModeSetter is an optional Session capability: switch the agent's working mode mid-session, without
// restarting it. Sessions that don't implement it still obey the hub's own mode enforcement (which
// gates tools at the approval layer), they just don't get the harness-native behavior change.
//
// opencode implements this cheaply because its agent/mode is sent per message; claude-code forwards
// it to the sidecar, which applies it to the next turn.
type ModeSetter interface {
	SetMode(ctx context.Context, mode string) error
}
