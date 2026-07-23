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
	TypeSessionList         = "session.list"
	TypeSessionGet          = "session.get"
	TypeSessionCreate       = "session.create"
	TypeSessionPrompt       = "session.prompt"
	TypeCommandList         = "command.list"
	TypeLoopList            = "loop.list"
	TypeLoopUpsert          = "loop.upsert"
	TypeLoopDelete          = "loop.delete"
	TypeLoopSetEnabled      = "loop.enabled"
	TypeSessionStop         = "session.stop"
	TypeSessionInterrupt    = "session.interrupt" // stop the current turn, keep the session
	TypeSessionAutonomy     = "session.autonomy"  // toggle/re-arm heartbeat supervision
	TypeHandoffList         = "handoff.list"      // indexed agent-authored handoff files (request + event)
	TypeSessionChild        = "session.child"     // spawn a scoped sub-agent seeded from a parent's handoff
	TypeSessionRename       = "session.rename"
	TypeSessionAttach       = "session.attach"
	TypeSessionRestart      = "session.restart"  // re-create a stopped session (provider couldn't re-attach after a daemon restart)
	TypeSessionSubscribe    = "session.subscribe" // observe an already-owned session (no dup subscription)
	TypeApprovalRespond     = "approval.respond"
	TypeDiscover            = "discover.list"
	TypeDeviceRegister      = "device.register"
	TypeProviderList        = "provider.list" // agent providers registered on this daemon
	TypeAgentList           = "agent.list"    // full agent roster (native + detected + custom)
	TypeAgentUpsert         = "agent.upsert"  // add/edit a custom CLI agent (persisted, live)
	TypeAgentDelete         = "agent.delete"  // remove a custom CLI agent
	TypeAgentVisible        = "agent.visible"    // show/hide an agent in the session pickers
	TypeModelList           = "model.list"       // list a provider's available models
	TypeSessionSetModel     = "session.set_model" // switch a live session's model
	TypeProjectList         = "project.list"
	TypeProjectAdd          = "project.add"
	TypeProjectBrowse       = "project.browse"
	TypeProjectRemove       = "project.remove"
	TypeWorktreeDiff        = "worktree.diff"        // request the diff of a worktree session
	TypeWorktreeRemove      = "worktree.remove"      // stop a worktree session + remove its worktree
	TypeWorktreePR          = "worktree.pr"          // commit + push + open a PR for a worktree session
	TypeWorktreeConflicts   = "worktree.conflicts"   // files this worktree shares with other active worktrees
	TypeWorkspaceDiff       = "workspace.diff"       // per-member diff for a cross-repo workspace session
	TypeWorkspacePR         = "workspace.pr"         // commit + push + open a PR for each workspace member
	TypeIntegrationConnect  = "integration.connect"  // connect a tracker (Linear/Jira) with a token
	TypeIntegrationDisconnect = "integration.disconnect" // remove a tracker's connection (clears its token)
	TypeIntegrationStatus   = "integration.status"   // which trackers are connected
	TypeTelemetrySet        = "telemetry.set"        // toggle anonymized diagnostics on/off
	TypeTelemetryStatus     = "telemetry.status"     // query whether anonymized diagnostics are on
	TypeJiraSites           = "jira.sites"           // list Atlassian sites the token can access (multi-site orgs)
	TypeJiraSetSite         = "jira.set_site"        // switch the active Jira site (cloud id)
	TypeIntegrationOAuth    = "integration.oauth"    // begin an OAuth flow; returns an authorize URL
	TypeIntegrationOAuthApp = "integration.oauthapp" // save a provider's OAuth app client_id/secret
	TypeIssueList           = "issue.list"           // assigned issues (request + broadcast)
	TypeIssueStates         = "issue.states"         // workflow states (kanban columns) for a team
	TypeIssueLaunch         = "issue.launch"         // launch an agent on an issue (worktree)
	TypeIssueDetail         = "issue.detail"         // full issue + comments
	TypeIssueUpdate         = "issue.update"         // edit issue fields (partial)
	TypeIssueComment        = "issue.comment"        // add a comment
	TypeIssueCommentEdit    = "issue.comment.edit"   // edit an existing comment
	TypeIssueImage          = "issue.image"          // proxy an auth-gated attachment image

	// Built-in editor file access — all paths validated against project roots + session cwds.
	TypeFSTree      = "fs.tree"      // list a directory (or the available roots when path is empty)
	TypeFSRead      = "fs.read"      // read a text file (content + sha for conflict detection)
	TypeFSReadBytes = "fs.readbytes" // read a file's raw bytes (images shown inline)
	TypeFSWrite     = "fs.write"     // save a file if its base sha still matches on disk
	TypeFSDiff      = "fs.diff"      // unified git diff for a path or session (review)
	TypeFSWatch     = "fs.watch"     // subscribe to change events for open files
	TypeFSSearch    = "fs.search"    // multi-file text search across the workspace
	TypeRunTest     = "run.test"     // run the project's tests/build in a session's workspace

	// LSP (built-in editor: diagnostics/linting/types/definition)
	TypeLSPOpen        = "lsp.open"        // open a document in its language server
	TypeLSPChange      = "lsp.change"      // document edited (full-sync)
	TypeLSPClose       = "lsp.close"       // close a document
	TypeLSPHover       = "lsp.hover"       // type/doc info at a position
	TypeLSPDefinition  = "lsp.definition"  // go-to-definition at a position
	TypeLSPComplete    = "lsp.complete"    // completion items at a position (autocomplete)
	TypeLSPReferences  = "lsp.references"  // find all references to a symbol
	TypeLSPRename      = "lsp.rename"      // rename a symbol across files
	TypeLSPSymbols     = "lsp.symbols"     // document symbols (outline)
	TypeLSPFormat      = "lsp.format"      // format the whole document
	TypeLSPDiagnostics = "lsp.diagnostics" // event: diagnostics published for a file
	TypeLSPServerInfo  = "lsp.serverinfo"  // is a language server installed for this file?
	TypeLSPInstall     = "lsp.install"     // install the language server for this file

	// events (daemon -> client), no id
	TypeSessionStatus    = "session.status"
	TypeSessionMessage   = "session.message" // a full (historical/replayed) turn
	TypeThinking         = "thinking.delta"  // streaming reasoning ("it's working")
	TypeOutputDelta      = "output.delta"
	TypeApprovalRequest  = "approval.request"
	TypeApprovalResolved = "approval.resolved" // broadcast: this approval was answered
	TypeFSChange         = "fs.change"         // a watched file changed on disk
	TypeSessionUsage     = "session.usage"     // token/cost usage for a session (event)
	TypeSessionTodos     = "session.todos"     // the agent's live to-do list (event)
	TypeSessionHeartbeat = "session.heartbeat" // supervision state for a session (event)
	TypeRunOutput        = "run.output"        // streamed line from a test/build run (event)
	TypeRunResult        = "run.result"        // final pass/fail of a test/build run (event)
	TypeSessionProgress  = "session.progress"  // live step during session.create (drives the loading checklist)

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
	StatusStopped          = "stopped" // persisted but not live: the provider couldn't re-attach after a daemon restart; restartable
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
	Plan          bool              `json:"plan,omitempty"`           // start in plan mode (propose a plan to approve first)
	Autonomous    bool              `json:"autonomous,omitempty"`     // let the heartbeat nudge it to keep going
	MaxNudges     int               `json:"max_nudges,omitempty"`     // give-up bound for auto-nudging (0 = default)
	BudgetUSD     float64           `json:"budget_usd,omitempty"`     // cost ceiling for auto-nudging (0 = default)
	Model         string            `json:"model,omitempty"`          // model id to run with ("" = provider default)
	ModelProvider string            `json:"model_provider,omitempty"` // sub-provider/backend for the model (opencode needs it)
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

// Loop is a recurring autonomous workflow: watch a tracker for new tickets in a category and start
// an agent on each.
type Loop struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Enabled  bool   `json:"enabled"`
	Provider string `json:"provider"`
	Kind     string `json:"kind"` // "ticket" (default) | "task"

	ProjectID  string   `json:"project_id,omitempty"`  // legacy single repo
	ProjectIDs []string `json:"project_ids,omitempty"` // one or more repos

	// ticket kind:
	TriggerCategory string `json:"trigger_category,omitempty"`
	Tracker         string `json:"tracker,omitempty"`

	// task kind:
	Prompt          string `json:"prompt,omitempty"`
	IntervalMinutes int    `json:"interval_minutes,omitempty"`
	LastRun         int64  `json:"last_run,omitempty"`

	Worktree      bool    `json:"worktree"`
	Plan          bool    `json:"plan"`
	BudgetUSD     float64 `json:"budget_usd"`
	MaxConcurrent int     `json:"max_concurrent"`
}

// LoopRun is one execution of a loop (a ticket that got an agent).
type LoopRun struct {
	LoopID     string `json:"loop_id"`
	IssueKey   string `json:"issue_key"`
	IssueTitle string `json:"issue_title"`
	SessionID  string `json:"session_id"`
	Status     string `json:"status"`
	StartedAt  int64  `json:"started_at"`
}

// LoopList is the full loop config + run history.
type LoopList struct {
	Loops []Loop    `json:"loops"`
	Runs  []LoopRun `json:"runs"`
}

// LoopRef references a loop by id (delete).
type LoopRef struct {
	ID string `json:"id"`
}

// LoopSetEnabled toggles a loop.
type LoopSetEnabled struct {
	ID      string `json:"id"`
	Enabled bool   `json:"enabled"`
}

// CommandListReq asks for the slash commands available to a session's agent (for the "/" palette).
type CommandListReq struct {
	SessionID string `json:"session_id"`
}

// SlashCommand is one agent slash command offered in the composer palette.
type SlashCommand struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Source      string `json:"source,omitempty"` // "builtin" or "custom"
	Prefix      string `json:"prefix,omitempty"` // "/" (default) or "$" (codex skills)
}

// CommandList is the result of command.list.
type CommandList struct {
	Commands []SlashCommand `json:"commands"`
}

// ProjectBrowseReq lists the immediate subdirectories of Path (empty = the user's home directory)
// for the new-session folder picker — so you can browse INTO a folder and pick several sub-folders.
type ProjectBrowseReq struct {
	Path string `json:"path,omitempty"`
}

// ProjectDirEntry is one browsable subdirectory.
type ProjectDirEntry struct {
	Name      string `json:"name"`
	Path      string `json:"path"`
	IsGitRepo bool   `json:"is_git_repo"`
}

// ProjectBrowse is the result of project.browse: the listed directory, its parent (for "up"), and
// the subdirectories within it.
type ProjectBrowse struct {
	Path    string            `json:"path"`
	Parent  string            `json:"parent,omitempty"`
	Entries []ProjectDirEntry `json:"entries"`
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

// WorkspaceDiff is the per-member diff for a cross-repo workspace session (one entry per repo,
// each vs its own base commit). Request carries just SessionID.
type WorkspaceDiff struct {
	SessionID string                `json:"session_id"`
	Members   []WorkspaceMemberDiff `json:"members,omitempty"` // populated on the response
}

type WorkspaceMemberDiff struct {
	Name   string `json:"name"`
	Branch string `json:"branch"`
	Diff   string `json:"diff"`
}

// WorkspacePR commits, pushes, and opens a PR for every member repo of a workspace session — the
// coordinated multi-PR finish. Request carries SessionID + Title/Body (shared across members).
type WorkspacePR struct {
	SessionID string              `json:"session_id"`
	Title     string              `json:"title"`
	Body      string              `json:"body,omitempty"`
	Members   []WorkspaceMemberPR `json:"members,omitempty"` // populated on the response
}

type WorkspaceMemberPR struct {
	Name    string `json:"name"`
	Branch  string `json:"branch"`
	Pushed  bool   `json:"pushed"`
	URL     string `json:"url,omitempty"`     // set when a PR was opened via gh
	Skipped string `json:"skipped,omitempty"` // reason a member was skipped (no changes / no remote)
	Error   string `json:"error,omitempty"`   // per-member failure (others still proceed)
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

// JiraSite is one Atlassian site (cloud) the OAuth token can reach.
type JiraSite struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	URL  string `json:"url"`
}

// JiraSites is the list + the currently-selected cloud id (jira.sites response).
type JiraSites struct {
	Sites   []JiraSite `json:"sites"`
	Current string     `json:"current,omitempty"`
}

// JiraSetSite switches the active Jira site (jira.set_site request).
type JiraSetSite struct {
	CloudID string `json:"cloud_id"`
}

// Telemetry is the anonymized-diagnostics toggle state (set request + status response).
type Telemetry struct {
	Enabled bool `json:"enabled"`
}

// SessionProgress is a live step emitted while a session is being created, so the app can show a
// prescriptive checklist ("Creating worktree 2/3 · repo-b", "Starting opencode…") instead of a
// generic skeleton. Stage is a stable code; Detail is human text. Step/Total drive a "2/3" when >0.
type SessionProgress struct {
	Stage  string `json:"stage"`  // "workspace" | "worktree" | "bootstrap" | "provider" | "ready"
	Detail string `json:"detail"` // human-readable line
	Step   int    `json:"step,omitempty"`
	Total  int    `json:"total,omitempty"`
}

type IntegrationStatus struct {
	Connected  []string `json:"connected"`             // provider names currently connected
	OAuthApps  []string `json:"oauth_apps,omitempty"`  // providers with an OAuth app configured (client_id present)
	AuthErrors []string `json:"auth_errors,omitempty"` // connected providers whose fetch/refresh is failing (need reconnect)
	// AuthErrorDetails is provider -> the actual failure message (e.g. "jira: 401 Unauthorized"), so
	// the app can show WHY a connected tracker isn't loading, not just that it isn't.
	AuthErrorDetails map[string]string `json:"auth_error_details,omitempty"`
}

type IntegrationOAuth struct {
	Provider string `json:"provider"`
	URL      string `json:"url,omitempty"` // authorize URL (on the response)
}

// IntegrationOAuthApp stores a provider's OAuth app credentials so the OAuth flow can start
// from the app without hand-editing integrations.json.
type IntegrationOAuthApp struct {
	Provider     string `json:"provider"`
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
}

type Issue struct {
	ID          string `json:"id"`
	Key         string `json:"key"`
	Title       string `json:"title"`
	Body        string `json:"body,omitempty"`
	Status      string `json:"status"`
	Category    string `json:"category"` // todo | in_progress | done | other
	Assignee    string `json:"assignee,omitempty"`
	URL         string `json:"url,omitempty"`
	Provider    string `json:"provider"`
	BranchName  string `json:"branch_name,omitempty"`
	TeamID      string `json:"team_id,omitempty"`
	Priority    int    `json:"priority,omitempty"`
	UpdatedAt   string `json:"updated_at,omitempty"`
	CycleID     string `json:"cycle_id,omitempty"`
	CycleName   string `json:"cycle_name,omitempty"`
	CycleNumber int    `json:"cycle_number,omitempty"`
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

// IssueComment is a single comment on an issue.
type IssueComment struct {
	ID        string `json:"id"`
	Author    string `json:"author,omitempty"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at,omitempty"`
}

// IssueDetailReq requests one issue plus its comments.
type IssueDetailReq struct {
	Provider string `json:"provider"`
	IssueID  string `json:"issue_id"`
}

// IssueDetail is the full issue view: the issue and its comments.
type IssueDetail struct {
	Issue    Issue          `json:"issue"`
	Comments []IssueComment `json:"comments"`
}

// IssueUpdate is a partial edit of an issue; only non-nil fields are applied. The
// reply is the updated Issue.
type IssueUpdate struct {
	Provider    string  `json:"provider"`
	IssueID     string  `json:"issue_id"`
	Title       *string `json:"title,omitempty"`
	Description *string `json:"description,omitempty"`
	StateID     *string `json:"state_id,omitempty"`
	Priority    *int    `json:"priority,omitempty"`
}

// IssueCommentAdd adds a comment to an issue. The reply is the created IssueComment.
type IssueCommentAdd struct {
	Provider string `json:"provider"`
	IssueID  string `json:"issue_id"`
	Body     string `json:"body"`
}

// IssueCommentEdit replaces the body of an existing comment. The reply is nil.
type IssueCommentEdit struct {
	Provider  string `json:"provider"`
	CommentID string `json:"comment_id"`
	Body      string `json:"body"`
}

// IssueImageReq proxies an auth-gated attachment through the daemon.
type IssueImageReq struct {
	Provider string `json:"provider"`
	URL      string `json:"url"`
}

// IssueImage is a proxied image: MIME type and base64-encoded bytes.
type IssueImage struct {
	Mime string `json:"mime"`
	Data string `json:"data"` // base64 (StdEncoding), no data: prefix
}

type IssueLaunch struct {
	IssueID       string `json:"issue_id"`
	Provider      string `json:"provider"`
	ProjectID     string `json:"project_id"` // which registered repo to work in
	Worktree      bool   `json:"worktree,omitempty"`
	AgentProvider string `json:"agent_provider,omitempty"` // opencode|claude-code|pi (default opencode)
}

type SessionRef struct {
	SessionID string `json:"session_id"`
}

// SessionRename sets a user label on a managed session (empty clears it).
type SessionRename struct {
	SessionID string `json:"session_id"`
	Name      string `json:"name"`
}

// --- Built-in editor file access (fs.*) ---

// FSTreeReq lists one directory. Empty Path returns the available roots. With SessionID set,
// the roots are scoped to that session's workspace folder(s) (a per-session file tree); empty
// SessionID returns all roots (project roots + active session working dirs) for browsing.
type FSTreeReq struct {
	Path      string `json:"path,omitempty"`
	SessionID string `json:"session_id,omitempty"`
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

// FSReadBytesReq reads a file's raw bytes (an image to show inline).
type FSReadBytesReq struct {
	Path string `json:"path"`
}

// FSBytes is a file's raw bytes (base64) + MIME type.
type FSBytes struct {
	Path string `json:"path"`
	Mime string `json:"mime"`
	Data string `json:"data"` // base64 (StdEncoding)
}

// LSPDocReq addresses a document (open/change/close). Content is set for open/change.
type LSPDocReq struct {
	Path     string `json:"path"`
	Content  string `json:"content,omitempty"`
	Language string `json:"language,omitempty"` // optional hint; daemon infers from path
}

// LSPPosReq addresses a 0-based position in a document (hover/definition).
type LSPPosReq struct {
	Path      string `json:"path"`
	Line      int    `json:"line"`
	Character int    `json:"character"`
}

// LSPHover is hover text (type info / docs) for a position; empty if none.
type LSPHover struct {
	Contents string `json:"contents"`
}

// LSPDefinition is a go-to-definition target (0-based position). Found=false if none.
type LSPDefinition struct {
	Path      string `json:"path"`
	Line      int    `json:"line"`
	Character int    `json:"character"`
	Found     bool   `json:"found"`
}

// LSPDiagnostic mirrors one LSP diagnostic (0-based positions).
type LSPDiagnostic struct {
	StartLine int    `json:"start_line"`
	StartChar int    `json:"start_char"`
	EndLine   int    `json:"end_line"`
	EndChar   int    `json:"end_char"`
	Severity  int    `json:"severity"` // 1=error 2=warning 3=info 4=hint
	Message   string `json:"message"`
	Source    string `json:"source,omitempty"`
}

// LSPDiagnostics is a broadcast event: the current diagnostics for one file.
type LSPDiagnostics struct {
	Path        string          `json:"path"`
	Diagnostics []LSPDiagnostic `json:"diagnostics"`
}

// LSPServerInfo reports whether a language server is available for a file, and whether we can
// install one (a scripted recipe whose prerequisite tool is present).
type LSPServerInfo struct {
	Language     string `json:"language"` // "" if the file type is unsupported
	Installed    bool   `json:"installed"`
	Installable  bool   `json:"installable"`
	InstallLabel string `json:"install_label"` // e.g. "gopls" or "Xcode / Swift toolchain"
}

// LSPInstallResult reports the outcome of an install attempt.
type LSPInstallResult struct {
	OK        bool   `json:"ok"`
	Installed bool   `json:"installed"`
	Message   string `json:"message,omitempty"`
}

// LSPCompletionItem is one autocomplete suggestion.
type LSPCompletionItem struct {
	Label  string `json:"label"`            // shown in the list
	Insert string `json:"insert"`           // text to insert (may differ from Label)
	Detail string `json:"detail,omitempty"` // type/signature
	Kind   int    `json:"kind,omitempty"`   // LSP CompletionItemKind (1..25)
}

// LSPCompletion is the result of a completion request.
type LSPCompletion struct {
	Items []LSPCompletionItem `json:"items"`
}

// LSPFormatReq requests formatting of a document; Content is the current buffer.
type LSPFormatReq struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// LSPFormatResult carries the formatted text (Changed=false → already formatted).
type LSPFormatResult struct {
	Text    string `json:"text"`
	Changed bool   `json:"changed"`
}

// LSPLocation is a source location (0-based position).
type LSPLocation struct {
	Path      string `json:"path"`
	Line      int    `json:"line"`
	Character int    `json:"character"`
}

// LSPLocations is a list of locations (find-references result).
type LSPLocations struct {
	Locations []LSPLocation `json:"locations"`
}

// LSPRenameReq renames the symbol at a 0-based position to NewName across files.
type LSPRenameReq struct {
	Path      string `json:"path"`
	Line      int    `json:"line"`
	Character int    `json:"character"`
	NewName   string `json:"new_name"`
}

// LSPRenameResult reports which files were rewritten by a rename.
type LSPRenameResult struct {
	Files []string `json:"files"`
	Count int      `json:"count"`
}

// LSPSymbol is one document-symbol (outline node), possibly nested.
type LSPSymbol struct {
	Name      string      `json:"name"`
	Kind      int         `json:"kind"` // LSP SymbolKind (1..26)
	Detail    string      `json:"detail,omitempty"`
	Line      int         `json:"line"`
	Character int         `json:"character"`
	Children  []LSPSymbol `json:"children,omitempty"`
}

// LSPSymbols is the document-symbol (outline) result for a file.
type LSPSymbols struct {
	Symbols []LSPSymbol `json:"symbols"`
}

// --- Multi-file search ---

// FSSearchReq searches for Query across a session's workspace (SessionID) or all roots.
type FSSearchReq struct {
	Query     string `json:"query"`
	SessionID string `json:"session_id,omitempty"`
	Regex     bool   `json:"regex,omitempty"`
}

// FSSearchHit is one match (1-based line/column).
type FSSearchHit struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Col  int    `json:"col"`
	Text string `json:"text"`
}

// FSSearchResult is the capped list of matches.
type FSSearchResult struct {
	Results []FSSearchHit `json:"results"`
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
	Name          string `json:"name,omitempty"`           // user-set label (takes precedence over Title)
	ProjectID     string `json:"project_id,omitempty"`     // registered project this session runs in
	Cwd           string `json:"cwd,omitempty"`            // working directory (project path or worktree)
	WorkspaceName string `json:"workspace_name,omitempty"` // human name of a worktree workspace
	Branch        string `json:"branch,omitempty"`         // git branch (for worktree sessions)
	IsWorkspace   bool   `json:"is_workspace,omitempty"`   // cross-repo workspace (per-member worktrees)
	ParentID      string `json:"parent_id,omitempty"`      // parent session this was delegated from (child sessions)
	Subtask       string `json:"subtask,omitempty"`        // the subtask a child session owns
	Port          int    `json:"port,omitempty"`           // port a setup hook assigned to this worktree
	IssueKey      string `json:"issue_key,omitempty"`      // the ticket this session works (e.g. ENG-42)
	IssueID       string `json:"issue_id,omitempty"`
	Model         string `json:"model,omitempty"`          // active model id ("" = provider default)
	ModelProvider string `json:"model_provider,omitempty"` // sub-provider/backend for the model
	Restartable   bool   `json:"restartable,omitempty"`    // a stopped session that can be re-created (session.restart)
	UpdatedAt     int64  `json:"updated_at,omitempty"` // unix seconds of last activity (0 = unknown)
	// Cumulative token/cost usage for the session (surfaced as a meter; 0 = unknown).
	InputTokens  int     `json:"input_tokens,omitempty"`
	OutputTokens int     `json:"output_tokens,omitempty"`
	CostUSD      float64 `json:"cost_usd,omitempty"`
}

// SessionUsage is a usage update for one session (event). InputTokens/OutputTokens/CostUSD are
// the delta for the just-completed turn; the hub accumulates them onto the Session.
type SessionUsage struct {
	SessionID    string  `json:"session_id"`
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd"`
}

// Todo is one item in the agent's live to-do list.
type Todo struct {
	Content string `json:"content"`
	Status  string `json:"status"` // pending | in_progress | completed
}

// SessionTodos is the agent's current to-do list for a session (event; replaces the prior list).
type SessionTodos struct {
	SessionID string `json:"session_id"`
	Todos     []Todo `json:"todos"`
}

// SessionAutonomy toggles/re-arms heartbeat supervision for a session (client → daemon).
type SessionAutonomy struct {
	SessionID  string  `json:"session_id"`
	Autonomous bool    `json:"autonomous"`
	MaxNudges  int     `json:"max_nudges,omitempty"`
	BudgetUSD  float64 `json:"budget_usd,omitempty"`
}

// SessionHeartbeat is the heartbeat's derived supervision state for a session (event). State is
// working|awaiting_input|idle_incomplete|stalled|done|errored|exhausted.
type SessionHeartbeat struct {
	SessionID  string  `json:"session_id"`
	State      string  `json:"state"`
	NudgeCount int     `json:"nudge_count"`
	TodosDone  int     `json:"todos_done"`
	TodosTotal int     `json:"todos_total"`
	CostUSD    float64 `json:"cost_usd"`
	BudgetUSD  float64 `json:"budget_usd"`
}

// SessionChild spawns a scoped sub-agent for one subtask of a parent session. The child is
// seeded with a compact context — the subtask, a pointer to the parent's handoff file (the
// decision/state doc), and an optional file allowlist — NOT the parent transcript, so its context
// stays small. Response is the created Session.
type SessionChild struct {
	ParentSessionID string   `json:"parent_session_id"`
	Subtask         string   `json:"subtask"`
	Files           []string `json:"files,omitempty"`      // allowlist the child should stay within (advisory)
	Provider        string   `json:"provider,omitempty"`   // defaults to the parent's provider
	Autonomous      bool     `json:"autonomous,omitempty"` // enroll the child in heartbeat supervision
}

// HandoffEntry is one indexed agent-authored handoff file (progress externalized to disk so it
// survives context compaction and can seed scoped child sessions).
type HandoffEntry struct {
	SessionID string `json:"session_id"`
	Cwd       string `json:"cwd"`
	Path      string `json:"path"`
	Title     string `json:"title"`
	Summary   string `json:"summary"`
	UpdatedAt int64  `json:"updated_at"`
}

// HandoffList is the request (optionally filtered by cwd) and the event payload for handoffs.
type HandoffList struct {
	Cwd      string         `json:"cwd,omitempty"`
	Handoffs []HandoffEntry `json:"handoffs"`
}

// RunTest requests a test/build run in a session's workspace. Command is optional (the daemon
// auto-detects one from the project type when empty).
type RunTest struct {
	SessionID string `json:"session_id"`
	Command   string `json:"command,omitempty"`
}

// RunOutput is one streamed line of a test/build run (event).
type RunOutput struct {
	SessionID string `json:"session_id"`
	Line      string `json:"line"`
}

// RunResult is the final outcome of a test/build run (event).
type RunResult struct {
	SessionID string `json:"session_id"`
	Command   string `json:"command"`
	OK        bool   `json:"ok"`
	ExitCode  int    `json:"exit_code"`
}

type SessionList struct {
	Sessions []Session `json:"sessions"`
}

// ProviderList is the set of agent providers registered on this daemon (opencode, claude-code, pi,
// plus any generic CLI agents), so the app's new-session picker reflects what's actually available
// instead of a hardcoded list.
type ProviderList struct {
	Providers []string `json:"providers"`
}

// ModelInfo is one selectable model for a provider. Provider is the sub-provider/backend
// (e.g. "openai", "anthropic") that opencode needs alongside the model id; "" for providers that
// take a bare model string (claude-code).
type ModelInfo struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Provider string `json:"provider,omitempty"`
}

// ModelListReq asks for the models available to a provider (or a live session's provider).
type ModelListReq struct {
	Provider  string `json:"provider,omitempty"`
	SessionID string `json:"session_id,omitempty"`
}

// ModelList is a provider's selectable models. Current is the active model id (if known).
type ModelList struct {
	Models   []ModelInfo `json:"models"`
	Current  string      `json:"current,omitempty"`
	Editable bool        `json:"editable"` // whether the app can switch it (false = agent-managed)
}

// SessionSetModel switches a running session's model.
type SessionSetModel struct {
	SessionID string `json:"session_id"`
	Model     string `json:"model"`
	Provider  string `json:"provider,omitempty"`
}

// AgentInfo describes one agent in the roster. Kind is "native" (rich integrations: opencode/
// claude-code/pi — not editable), "detected" (a well-known CLI auto-found on PATH), or "custom"
// (user-defined in ~/.oculus/agents.json — editable/removable). Available means its command
// currently resolves on PATH.
type AgentInfo struct {
	Name       string   `json:"name"`
	Kind       string   `json:"kind"`
	Available  bool     `json:"available"`
	Editable   bool     `json:"editable"`
	Hidden     bool     `json:"hidden"` // user hid it from the session pickers (still runnable)
	Command    string   `json:"command,omitempty"`
	Args       []string `json:"args,omitempty"`
	ResumeArgs []string `json:"resume_args,omitempty"`
	Models     []string `json:"models,omitempty"` // configured model names (custom CLI agents)
}

// AgentList is the full agent roster returned by agent.list.
type AgentList struct {
	Agents []AgentInfo `json:"agents"`
}

// AgentUpsert adds or edits a custom CLI agent. Args templates may contain {prompt} and {cwd};
// ResumeArgs (optional) is used after the first turn for agents that support session continuity.
type AgentUpsert struct {
	Name       string            `json:"name"`
	Command    string            `json:"command"`
	Args       []string          `json:"args,omitempty"`
	ResumeArgs []string          `json:"resume_args,omitempty"`
	Env        map[string]string `json:"env,omitempty"`
	Models     []string          `json:"models,omitempty"` // model names for the picker (use {model} in Args)
}

// AgentRef references a custom agent by name (delete).
type AgentRef struct {
	Name string `json:"name"`
}

// AgentVisible shows or hides an agent in the session pickers (any kind). A hidden agent stays
// registered and runnable — it just doesn't clutter the default picker.
type AgentVisible struct {
	Name    string `json:"name"`
	Visible bool   `json:"visible"`
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
	Live      bool   `json:"live,omitempty"`       // currently running in a terminal (not just a transcript)
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
