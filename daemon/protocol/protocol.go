// Package protocol defines the Oculus app<->daemon wire messages: a typed JSON
// envelope {id?, type, payload} carried over the encrypted WebSocket channel.
//
// The wire format is the contract between the Go daemon and the Swift app; the
// golden vectors in ../../protocol/vectors/messages.json lock both sides in parity.
// See ../../skills/oculus-protocol.
package protocol

import (
	"encoding/json"
	"errors"
)

// Message types.
const (
	// requests (client -> daemon), carry an id echoed on the response
	TypeSessionList     = "session.list"
	TypeSessionGet      = "session.get"
	TypeSessionCreate   = "session.create"
	TypeSessionPrompt   = "session.prompt"
	TypeSessionStop      = "session.stop"
	TypeSessionAttach    = "session.attach"
	TypeSessionSubscribe = "session.subscribe" // observe an already-owned session (no dup subscription)
	TypeApprovalRespond  = "approval.respond"
	TypeDiscover        = "discover.list"
	TypeDeviceRegister  = "device.register"
	TypeProjectList     = "project.list"
	TypeProjectAdd      = "project.add"
	TypeProjectRemove   = "project.remove"
	TypeWorktreeDiff    = "worktree.diff"   // request the diff of a worktree session
	TypeWorktreeRemove  = "worktree.remove" // stop a worktree session + remove its worktree
	TypeWorktreePR      = "worktree.pr"     // commit + push + open a PR for a worktree session

	// events (daemon -> client), no id
	TypeSessionStatus   = "session.status"
	TypeSessionMessage  = "session.message" // a full (historical/replayed) turn
	TypeThinking         = "thinking.delta"  // streaming reasoning ("it's working")
	TypeOutputDelta      = "output.delta"
	TypeApprovalRequest  = "approval.request"
	TypeApprovalResolved = "approval.resolved" // broadcast: this approval was answered

	// responses
	TypeOK    = "ok"
	TypeError = "error"
)

// Session statuses.
const (
	StatusRunning          = "running"
	StatusIdle             = "idle"
	StatusAwaitingApproval = "awaiting_approval"
	StatusDone             = "done"
	StatusError            = "error"
)

// Approval decisions.
const (
	DecisionAllow  = "allow"
	DecisionDeny   = "deny"
	DecisionAlways = "always" // allow this + auto-allow the same tool for the session
)

// Envelope is the outer frame for every message.
type Envelope struct {
	ID      string          `json:"id,omitempty"`
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// Payload types.

type SessionCreate struct {
	Provider      string `json:"provider"`
	Cwd           string `json:"cwd,omitempty"`
	ProjectID     string `json:"project_id,omitempty"` // resolve cwd from this registered project
	Prompt        string `json:"prompt,omitempty"`
	Worktree      bool   `json:"worktree,omitempty"`       // run in a fresh git worktree (opt-in)
	WorkspaceName string `json:"workspace_name,omitempty"` // human name for the worktree branch
}

// Project is a registered folder sessions can be spawned in (mirrors project.Project).
type Project struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Path          string `json:"path"`
	IsGitRepo     bool   `json:"is_git_repo"`
	DefaultBranch string `json:"default_branch,omitempty"`
}

type ProjectAdd struct {
	Path string `json:"path"`
}

type ProjectRef struct {
	ProjectID string `json:"project_id"`
}

type ProjectList struct {
	Projects []Project `json:"projects"`
}

// Worktree finish-flow messages.

type WorktreeRemove struct {
	SessionID string `json:"session_id"`
	Force     bool   `json:"force,omitempty"` // remove even if the worktree has uncommitted changes
}

type WorktreeDiff struct {
	SessionID string `json:"session_id"`
	Diff      string `json:"diff,omitempty"` // populated on the response
}

type WorktreePR struct {
	SessionID string `json:"session_id"`
	Title     string `json:"title"`
	Body      string `json:"body,omitempty"`
}

type WorktreePRResult struct {
	SessionID string `json:"session_id"`
	Branch    string `json:"branch"`
	Pushed    bool   `json:"pushed"`
	URL       string `json:"url,omitempty"` // set when a PR was opened via gh
}

type SessionRef struct {
	SessionID string `json:"session_id"`
}

// SessionAttach attaches to an existing session discovered on the host.
type SessionAttach struct {
	Provider  string `json:"provider"`
	SessionID string `json:"session_id"`
	URL       string `json:"url,omitempty"` // opencode server URL the session lives on
}

// SessionMessage is a full (historical/replayed) conversation turn.
type SessionMessage struct {
	SessionID string `json:"session_id"`
	Role      string `json:"role"` // user | assistant | tool
	Text      string `json:"text"`
}

type SessionPrompt struct {
	SessionID string `json:"session_id"`
	Text      string `json:"text"`
}

type SessionStatus struct {
	SessionID string `json:"session_id"`
	Status    string `json:"status"`
	Detail    string `json:"detail,omitempty"` // current activity, e.g. "running bash"
}

// Thinking is a streaming chunk of the agent's reasoning (shown as "thinking…").
type Thinking struct {
	SessionID string `json:"session_id"`
	Text      string `json:"text"`
}

type OutputDelta struct {
	SessionID string `json:"session_id"`
	Text      string `json:"text"`
}

type ApprovalRequest struct {
	ApprovalID string          `json:"approval_id"`
	SessionID  string          `json:"session_id"`
	Tool       string          `json:"tool"`
	Detail     string          `json:"detail,omitempty"` // human-readable command/args (e.g. the bash command)
	Input      json.RawMessage `json:"input,omitempty"`
}

type ApprovalRespond struct {
	ApprovalID string `json:"approval_id"`
	Decision   string `json:"decision"`
}

// ApprovalResolved is broadcast to every client when an approval is answered, so a
// pending approval card clears on all devices (not just the one that responded).
type ApprovalResolved struct {
	ApprovalID string `json:"approval_id"`
	Decision   string `json:"decision"`
}

type Session struct {
	ID            string `json:"id"`
	Provider      string `json:"provider"`
	Status        string `json:"status"`
	Title         string `json:"title,omitempty"`
	ProjectID     string `json:"project_id,omitempty"`     // registered project this session runs in
	Cwd           string `json:"cwd,omitempty"`            // working directory (project path or worktree)
	WorkspaceName string `json:"workspace_name,omitempty"` // human name of a worktree workspace
	Branch        string `json:"branch,omitempty"`         // git branch (for worktree sessions)
	Port          int    `json:"port,omitempty"`           // port a setup hook assigned to this worktree
}

type SessionList struct {
	Sessions []Session `json:"sessions"`
}

// Discovered is one autodetected agent artifact on the host: a running opencode
// server, one of its live sessions, or a claude-code session transcript.
type Discovered struct {
	Provider  string `json:"provider"`             // "opencode" | "claude-code"
	Kind      string `json:"kind"`                 // "server" | "session"
	URL       string `json:"url,omitempty"`        // opencode server base URL
	SessionID string `json:"session_id,omitempty"` // live/transcript session id
	Title     string `json:"title,omitempty"`
	Cwd       string `json:"cwd,omitempty"`  // claude-code working dir (best-effort)
	Path      string `json:"path,omitempty"` // claude-code transcript path
	PID       int    `json:"pid,omitempty"`
}

// DiscoverList is the response to a discover.list request.
type DiscoverList struct {
	Items []Discovered `json:"items"`
}

// DeviceRegister registers an APNs device token to receive approval pushes.
type DeviceRegister struct {
	Token string `json:"token"`
}

// Discovery kinds.
const (
	KindServer  = "server"
	KindSession = "session"
)

type Error struct {
	Message string `json:"message"`
}

// Encode marshals an envelope. id may be empty (for events). payload may be nil.
func Encode(id, typ string, payload any) ([]byte, error) {
	env := Envelope{ID: id, Type: typ}
	if payload != nil {
		p, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		env.Payload = p
	}
	return json.Marshal(env)
}

// Decode parses an envelope.
func Decode(b []byte) (Envelope, error) {
	var env Envelope
	if err := json.Unmarshal(b, &env); err != nil {
		return Envelope{}, err
	}
	if env.Type == "" {
		return Envelope{}, errors.New("protocol: envelope missing type")
	}
	return env, nil
}

// Unmarshal decodes the envelope payload into v.
func (e Envelope) Unmarshal(v any) error {
	if len(e.Payload) == 0 {
		return errors.New("protocol: empty payload")
	}
	return json.Unmarshal(e.Payload, v)
}
