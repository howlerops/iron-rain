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
	TypeSessionStop     = "session.stop"
	TypeApprovalRespond = "approval.respond"
	TypeDiscover        = "discover.list"

	// events (daemon -> client), no id
	TypeSessionStatus   = "session.status"
	TypeOutputDelta     = "output.delta"
	TypeApprovalRequest = "approval.request"

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
	DecisionAllow = "allow"
	DecisionDeny  = "deny"
)

// Envelope is the outer frame for every message.
type Envelope struct {
	ID      string          `json:"id,omitempty"`
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// Payload types.

type SessionCreate struct {
	Provider string `json:"provider"`
	Cwd      string `json:"cwd,omitempty"`
	Prompt   string `json:"prompt,omitempty"`
}

type SessionRef struct {
	SessionID string `json:"session_id"`
}

type SessionPrompt struct {
	SessionID string `json:"session_id"`
	Text      string `json:"text"`
}

type SessionStatus struct {
	SessionID string `json:"session_id"`
	Status    string `json:"status"`
}

type OutputDelta struct {
	SessionID string `json:"session_id"`
	Text      string `json:"text"`
}

type ApprovalRequest struct {
	ApprovalID string          `json:"approval_id"`
	SessionID  string          `json:"session_id"`
	Tool       string          `json:"tool"`
	Input      json.RawMessage `json:"input,omitempty"`
}

type ApprovalRespond struct {
	ApprovalID string `json:"approval_id"`
	Decision   string `json:"decision"`
}

type Session struct {
	ID       string `json:"id"`
	Provider string `json:"provider"`
	Status   string `json:"status"`
	Title    string `json:"title,omitempty"`
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
