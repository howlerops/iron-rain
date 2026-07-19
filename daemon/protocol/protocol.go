// Package protocol defines the Oculus app<->daemon wire messages: a typed JSON
// envelope {id?, type, payload} carried over the encrypted WebSocket channel.
//
// The wire format is the contract between the Go daemon and the Swift app; the
// golden vectors in ../../protocol/vectors/messages.json lock both sides in parity.
// See ../../skills/oculus-protocol.
package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"sync"
)

// Message types.
const (
	// requests (client -> daemon), carry an id echoed on the response
	TypeSessionList        = "session.list"
	TypeSessionGet         = "session.get"
	TypeSessionCreate      = "session.create"
	TypeSessionPrompt      = "session.prompt"
	TypeSessionStop        = "session.stop"
	TypeSessionAttach      = "session.attach"
	TypeSessionSubscribe   = "session.subscribe" // observe an already-owned session (no dup subscription)
	TypeApprovalRespond    = "approval.respond"
	TypeDiscover           = "discover.list"
	TypeDeviceRegister     = "device.register"
	TypeProjectList        = "project.list"
	TypeProjectAdd         = "project.add"
	TypeProjectRemove      = "project.remove"
	TypeWorktreeDiff       = "worktree.diff"       // request the diff of a worktree session
	TypeWorktreeRemove     = "worktree.remove"     // stop a worktree session + remove its worktree
	TypeWorktreePR         = "worktree.pr"         // commit + push + open a PR for a worktree session
	TypeWorktreeConflicts  = "worktree.conflicts"  // files this worktree shares with other active worktrees
	TypeIntegrationConnect = "integration.connect" // connect a tracker (Linear/Jira) with a token
	TypeIntegrationStatus  = "integration.status"  // which trackers are connected
	TypeIntegrationOAuth   = "integration.oauth"   // begin an OAuth flow; returns an authorize URL
	TypeIssueList          = "issue.list"          // assigned issues (request + broadcast)
	TypeIssueStates        = "issue.states"        // workflow states (kanban columns) for a team
	TypeIssueLaunch        = "issue.launch"        // launch an agent on an issue (worktree)

	// Built-in editor file access — all paths validated against project roots + session cwds.
	TypeFSTree  = "fs.tree"  // list a directory (or the available roots when path is empty)
	TypeFSRead  = "fs.read"  // read a text file (content + sha for conflict detection)
	TypeFSWrite = "fs.write" // save a file if its base sha still matches on disk
	TypeFSDiff  = "fs.diff"  // unified git diff for a path or session (review)
	TypeFSWatch = "fs.watch" // subscribe to change events for open files

	// events (daemon -> client), no id
	TypeSessionStatus    = "session.status"
	TypeSessionMessage   = "session.message" // a full (historical/replayed) turn
	TypeThinking         = "thinking.delta"  // streaming reasoning ("it's working")
	TypeOutputDelta      = "output.delta"
	TypeApprovalRequest  = "approval.request"
	TypeApprovalResolved = "approval.resolved" // broadcast: this approval was answered
	TypeFSChange         = "fs.change"         // a watched file changed on disk

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
	Provider      string            `json:"provider"`
	Cwd           string            `json:"cwd,omitempty"`
	ProjectID     string            `json:"project_id,omitempty"`  // resolve cwd from this registered project
	ProjectIDs    []string          `json:"project_ids,omitempty"` // multi-root workspace: cwd = common ancestor
	Prompt        string            `json:"prompt,omitempty"`
	Images        []ImageAttachment `json:"images,omitempty"`         // images for the first prompt
	Worktree      bool              `json:"worktree,omitempty"`       // run in a fresh git worktree (opt-in)
	WorkspaceName string            `json:"workspace_name,omitempty"` // human name for the worktree branch
}

// Project is a registered folder sessions can be spawned in (mirrors project.Project).
type Project struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Path          string `json:"path"`
	IsGitRepo     bool   `json:"is_git_repo"`
	DefaultBranch string `json:"default_branch,omitempty"`
	Source        string `json:"source,omitempty"` // "manual" or "auto" (discovered from an active agent's cwd)
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

// WorktreeConflicts warns which files this worktree changed that OTHER active worktrees
// also changed (they'll collide on merge). Request carries just SessionID.
type WorktreeConflicts struct {
	SessionID string         `json:"session_id"`
	Files     []FileConflict `json:"files,omitempty"`
}

type FileConflict struct {
	Path     string   `json:"path"`
	Branches []string `json:"branches"` // other worktree branches that also touched Path
}

// Integrations / issues (mirrors daemon/issues).

type IntegrationConnect struct {
	Provider string `json:"provider"` // "linear" | "jira"
	Token    string `json:"token"`
}

type IntegrationStatus struct {
	Connected []string `json:"connected"` // provider names currently connected
}

type IntegrationOAuth struct {
	Provider string `json:"provider"`
	URL      string `json:"url,omitempty"` // authorize URL (on the response)
}

type Issue struct {
	ID         string `json:"id"`
	Key        string `json:"key"`
	Title      string `json:"title"`
	Body       string `json:"body,omitempty"`
	Status     string `json:"status"`
	Category   string `json:"category"` // todo | in_progress | done | other
	Assignee   string `json:"assignee,omitempty"`
	URL        string `json:"url,omitempty"`
	Provider   string `json:"provider"`
	BranchName string `json:"branch_name,omitempty"`
	TeamID     string `json:"team_id,omitempty"`
	Priority   int    `json:"priority,omitempty"`
	UpdatedAt  string `json:"updated_at,omitempty"`
}

type IssueList struct {
	Issues []Issue `json:"issues"`
}

type IssueState struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Category string  `json:"category"`
	Position float64 `json:"position"`
}

type IssueStatesReq struct {
	Provider string `json:"provider"`
	TeamID   string `json:"team_id"`
}

type IssueStateList struct {
	States []IssueState `json:"states"`
}

type IssueLaunch struct {
	IssueID       string `json:"issue_id"`
	Provider      string `json:"provider"`
	ProjectID     string `json:"project_id"`               // which registered repo to work in
	Worktree      bool   `json:"worktree,omitempty"`
	AgentProvider string `json:"agent_provider,omitempty"` // opencode|claude-code|pi (default opencode)
}

type SessionRef struct {
	SessionID string `json:"session_id"`
}

// --- Built-in editor file access (fs.*) ---

// FSTreeReq lists one directory. Empty Path returns the available roots (project roots +
// active session working dirs) so the client can choose where to browse.
type FSTreeReq struct {
	Path string `json:"path,omitempty"`
}

// FSNode is one directory entry (or a root).
type FSNode struct {
	Name string `json:"name"`
	Path string `json:"path"` // absolute
	Dir  bool   `json:"dir"`
	Size int64  `json:"size,omitempty"`
}

// FSTree is a directory listing. Roots is populated only for the empty-path request.
type FSTree struct {
	Path    string   `json:"path,omitempty"`
	Entries []FSNode `json:"entries,omitempty"`
	Roots   []FSNode `json:"roots,omitempty"`
}

// FSReadReq reads one file.
type FSReadReq struct {
	Path string `json:"path"`
}

// FSFile is a read file. Sha is over the returned content (the full file when not truncated)
// and is what fs.write checks for conflicts.
type FSFile struct {
	Path      string `json:"path"`
	Content   string `json:"content,omitempty"`
	Sha       string `json:"sha"`
	ModTime   int64  `json:"mtime,omitempty"`
	Size      int64  `json:"size,omitempty"`
	Binary    bool   `json:"binary,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

// FSWriteReq saves a file if BaseSha still matches the on-disk sha.
type FSWriteReq struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	BaseSha string `json:"base_sha,omitempty"`
}

// FSWriteResult reports the outcome. Conflict=true means the file changed since the client
// read it and nothing was written (prompt reload/overwrite).
type FSWriteResult struct {
	Path     string `json:"path"`
	Sha      string `json:"sha,omitempty"`
	ModTime  int64  `json:"mtime,omitempty"`
	Conflict bool   `json:"conflict,omitempty"`
}

// FSDiffReq requests a unified diff for a session's changes (SessionID) or a repo path.
type FSDiffReq struct {
	SessionID string `json:"session_id,omitempty"`
	Path      string `json:"path,omitempty"`
}

// FSDiff carries a unified diff.
type FSDiff struct {
	Path string `json:"path,omitempty"`
	Diff string `json:"diff"`
}

// FSChange is a broadcast event: a watched file changed on disk (e.g. the agent edited it).
type FSChange struct {
	Path string `json:"path"`
	Sha  string `json:"sha,omitempty"`
}

// SessionAttach attaches to an existing session discovered on the host.
type SessionAttach struct {
	Provider  string `json:"provider"`
	SessionID string `json:"session_id"`
	URL       string `json:"url,omitempty"` // opencode server URL the session lives on
	Cwd       string `json:"cwd,omitempty"` // original working dir (claude-code resume runs here)
}

// SessionMessage is a full (historical/replayed) conversation turn.
type SessionMessage struct {
	SessionID string `json:"session_id"`
	Role      string `json:"role"` // user | assistant | tool
	Text      string `json:"text"`
}

type SessionPrompt struct {
	SessionID string            `json:"session_id"`
	Text      string            `json:"text"`
	Images    []ImageAttachment `json:"images,omitempty"`
}

// ImageAttachment is a user-attached image passed to a multimodal agent. Data is
// base64-encoded (no data: prefix); Mime is e.g. "image/png".
type ImageAttachment struct {
	Mime string `json:"mime"`
	Data string `json:"data"`
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
	IssueKey      string `json:"issue_key,omitempty"`      // the ticket this session works (e.g. ENG-42)
	IssueID       string `json:"issue_id,omitempty"`
	UpdatedAt     int64  `json:"updated_at,omitempty"`     // unix seconds of last activity (0 = unknown)
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
	UpdatedAt int64  `json:"updated_at,omitempty"` // unix seconds of last activity (0 = unknown)
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

// streamEnvelope encodes an event envelope (no id) in a single marshal pass:
// the payload is serialized inline instead of round-tripping through a
// json.RawMessage. It mirrors Envelope's wire shape for the id-less case.
type streamEnvelope struct {
	Type    string `json:"type"`
	Payload any    `json:"payload,omitempty"`
}

// encodeBufPool reuses buffers for the streaming hot path so per-token deltas
// don't allocate a fresh outer buffer on every message.
var encodeBufPool = sync.Pool{
	New: func() any { return new(bytes.Buffer) },
}

// isStreamingDelta reports whether typ is a token-by-token streaming event
// (the daemon's hottest encode path).
func isStreamingDelta(typ string) bool {
	return typ == TypeOutputDelta || typ == TypeThinking
}

// Encode marshals an envelope. id may be empty (for events). payload may be nil.
func Encode(id, typ string, payload any) ([]byte, error) {
	// Fast path for token-by-token streaming events: they carry no id and fire
	// per-token, so encode the envelope in one marshal pass into a pooled buffer
	// instead of the RawMessage double-marshal below. Output is byte-identical.
	if id == "" && payload != nil && isStreamingDelta(typ) {
		return encodeStream(typ, payload)
	}
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

// encodeStream serializes an id-less {"type",...,"payload":...} frame in a
// single pass using a pooled buffer.
func encodeStream(typ string, payload any) ([]byte, error) {
	buf := encodeBufPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer encodeBufPool.Put(buf)

	enc := json.NewEncoder(buf)
	if err := enc.Encode(streamEnvelope{Type: typ, Payload: payload}); err != nil {
		return nil, err
	}
	// json.Encoder appends a trailing newline; drop it so the bytes match the
	// json.Marshal output of the slow path exactly.
	b := buf.Bytes()
	if n := len(b); n > 0 && b[n-1] == '\n' {
		b = b[:n-1]
	}
	// buf goes back to the pool, so return a copy the caller can retain.
	out := make([]byte, len(b))
	copy(out, b)
	return out, nil
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
