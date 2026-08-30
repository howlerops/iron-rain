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
	TypeSessionList    = "session.list"
	TypeSessionGet     = "session.get"
	TypeSessionCreate  = "session.create"
	TypeSessionModeSet = "session.mode.set" // switch a live session between code/ask/architect
	TypeUsageReport    = "usage.report"     // spend + tokens over time, with the rolling window
	TypeTranscriptPage = "transcript.page"  // request older history for a session (client -> daemon)
	// The page's frames are bracketed by these so a client can tell replayed HISTORY from live
	// events and place it above what it already has, rather than appending it to the bottom.
	TypeTranscriptPageBegin   = "transcript.page.begin"
	TypeTranscriptPageEnd     = "transcript.page.end"
	TypeSessionPrompt         = "session.prompt"
	TypeCommandList           = "command.list"
	TypeLoopList              = "loop.list"
	TypeLoopUpsert            = "loop.upsert"
	TypeLoopDelete            = "loop.delete"
	TypeLoopSetEnabled        = "loop.enabled"
	TypeSessionStop           = "session.stop"
	TypeSessionInterrupt      = "session.interrupt" // stop the current turn, keep the session
	TypeSessionAutonomy       = "session.autonomy"  // toggle/re-arm heartbeat supervision
	TypeHandoffList           = "handoff.list"      // indexed agent-authored handoff files (request + event)
	TypeSessionChild          = "session.child"     // spawn a scoped sub-agent seeded from a parent's handoff
	TypeSessionRename         = "session.rename"
	TypeSessionAttach         = "session.attach"
	TypeSessionRestart        = "session.restart"   // re-create a stopped session (provider couldn't re-attach after a daemon restart)
	TypeSessionRecover        = "session.recover"   // re-attach an existing session, re-resolving its real directory (heals a broken session whose sends fail)
	TypeSessionSubscribe      = "session.subscribe" // observe an already-owned session (no dup subscription)
	TypeApprovalRespond       = "approval.respond"
	TypeDiscover              = "discover.list"
	TypeDeviceRegister        = "device.register"
	TypeClientIdentify        = "client.identify"   // this client's human name, used to attribute prompts
	TypeParticipants          = "participants"      // who is connected and what they may do
	TypeRoleGrant             = "role.grant"        // owner grants/revokes another participant's role
	TypeRolesEnable           = "roles.enable"      // turn multi-user enforcement on/off
	TypeInviteCreate          = "invite.create"     // mint a share credential with its own secret + role
	TypeInviteList            = "invite.list"       // outstanding invites
	TypeInviteRevoke          = "invite.revoke"     // drop one invite
	TypeProviderList          = "provider.list"     // agent providers registered on this daemon
	TypeProviderRefresh       = "provider.refresh"  // re-detect agent harnesses on PATH (rescan) + rebroadcast the list
	TypeAgentList             = "agent.list"        // full agent roster (native + detected + custom)
	TypeAgentUpsert           = "agent.upsert"      // add/edit a custom CLI agent (persisted, live)
	TypeAgentDelete           = "agent.delete"      // remove a custom CLI agent
	TypeAgentVisible          = "agent.visible"     // show/hide an agent in the session pickers
	TypeModelList             = "model.list"        // list a provider's available models
	TypeSessionSetModel       = "session.set_model" // switch a live session's model
	TypeProjectList           = "project.list"
	TypeProjectAdd            = "project.add"
	TypeProjectBrowse         = "project.browse"
	TypeProjectRemove         = "project.remove"
	TypeWorktreeDiff          = "worktree.diff"          // request the diff of a worktree session
	TypeWorktreeRemove        = "worktree.remove"        // stop a worktree session + remove its worktree
	TypeWorktreePR            = "worktree.pr"            // commit + push + open a PR for a worktree session
	TypeWorktreeCatchUp       = "worktree.catch_up"      // merge the repo's default branch into a worktree session's branch
	TypeWorktreeConflicts     = "worktree.conflicts"     // files this worktree shares with other active worktrees
	TypeDeviceList            = "device.list"            // enrolled clients that may reach this daemon
	TypeDeviceRevoke          = "device.revoke"          // lock out one device by its public key
	TypeDeviceLabel           = "device.label"           // give a device a human name
	TypeDeviceCredential      = "device.credential"      // daemon -> client: the credential this device keeps
	TypeDeviceCredentialAck   = "device.credential.ack"  // client -> daemon: stored it; safe to retire the old secret
	TypePairCode              = "pair.code"              // mint a fresh single-use pairing code + its URL
	TypePairStatus            = "pair.status"            // is the old permanent secret still live, and until when
	TypePairRetireLegacy      = "pair.retire_legacy"     // kill the old permanent secret now
	TypeWorktreeMerge         = "worktree.merge"         // land a worktree branch locally (repos with no remote)
	TypeWorktreeStatus        = "worktree.status"        // has the branch's PR landed yet?
	TypeWorkspaceDiff         = "workspace.diff"         // per-member diff for a cross-repo workspace session
	TypeWorkspacePR           = "workspace.pr"           // commit + push + open a PR for each workspace member
	TypeIntegrationConnect    = "integration.connect"    // connect a tracker (Linear/Jira) with a token
	TypeIntegrationDisconnect = "integration.disconnect" // remove a tracker's connection (clears its token)
	TypeIntegrationStatus     = "integration.status"     // which trackers are connected
	TypeTelemetrySet          = "telemetry.set"          // toggle anonymized diagnostics on/off
	TypeTelemetryStatus       = "telemetry.status"       // query whether anonymized diagnostics are on
	TypeJiraSites             = "jira.sites"             // list Atlassian sites the token can access (multi-site orgs)
	TypeJiraSetSite           = "jira.set_site"          // switch the active Jira site (cloud id)
	TypeIntegrationOAuth      = "integration.oauth"      // begin an OAuth flow; returns an authorize URL
	TypeIntegrationOAuthApp   = "integration.oauthapp"   // save a provider's OAuth app client_id/secret
	TypeIssueList             = "issue.list"             // assigned issues (request + broadcast)
	TypeIssueStates           = "issue.states"           // workflow states (kanban columns) for a team
	TypeIssueColumns          = "issue.columns"          // a project's ordered workflow statuses (real-status board columns)
	TypeIssueMove             = "issue.move"             // move an issue to a status (drag-drop) — resolves the transition
	TypeIssueCreate           = "issue.create"           // create a new ticket on a provider/project
	TypeIssueProjects         = "issue.projects"         // list the projects/teams the connected trackers expose (board picker)
	TypeIssueLaunch           = "issue.launch"           // launch an agent on an issue (worktree)
	TypeIssueDetail           = "issue.detail"           // full issue + comments
	TypeIssueUpdate           = "issue.update"           // edit issue fields (partial)
	TypeIssueComment          = "issue.comment"          // add a comment
	TypeIssueCommentEdit      = "issue.comment.edit"     // edit an existing comment
	TypeIssueMembers          = "issue.members"          // assignable users for a project/issue (assignee picker)
	TypeIssueLabels           = "issue.labels"           // a project's labels (label picker)
	TypeIssueCycles           = "issue.cycles"           // a project's sprints/cycles (sprint picker)
	TypeIssueImage            = "issue.image"            // proxy an auth-gated attachment image

	// Built-in editor file access — all paths validated against project roots + session cwds.
	TypeFSTree           = "fs.tree"            // list a directory (or the available roots when path is empty)
	TypeFSRead           = "fs.read"            // read a text file (content + sha for conflict detection)
	TypeFSReadBytes      = "fs.readbytes"       // read a file's raw bytes (images shown inline)
	TypeGitHubRepos      = "github.repos"       // list the repositories this account can reach
	TypeGitHubClone      = "github.clone"       // clone one of them onto the daemon host
	TypePreviewFetch     = "preview.fetch"      // fetch one resource from a session's own dev server
	TypePreviewDOMAsk    = "preview.dom.ask"    // daemon -> client: run a DOM op in the open preview
	TypePreviewDOMResult = "preview.dom.result" // client -> daemon: the answer
	TypeFSWrite          = "fs.write"           // save a file if its base sha still matches on disk
	TypeFSDiff           = "fs.diff"            // unified git diff for a path or session (review)
	TypeFSWatch          = "fs.watch"           // subscribe to change events for open files
	TypeFSSearch         = "fs.search"          // multi-file text search across the workspace
	TypeRunTest          = "run.test"           // run the project's tests/build in a session's workspace

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
	TypeSessionSubAgent  = "session.subagent"  // a sub-agent (opencode task, claude sidechain, ...) started/finished under a parent
	// What this provider CAN do, as data — see SessionCapabilities. Sent on attach/subscribe and
	// whenever it changes. The client renders affordances it is TOLD exist instead of hardcoding a
	// hint bar that is wrong for three providers out of four.
	TypeSessionCapabilities = "session.capabilities"
	// Live ambient state — see SessionFacts. This is the status bar: model, mode, effort, branch,
	// context budget, queued count.
	TypeSessionFacts = "session.facts"
	// Global defaults for NEW sessions (request/response). See SessionDefaults.
	TypeSessionDefaultsGet = "session.defaults.get"
	TypeSessionDefaultsSet = "session.defaults.set"
	// Conversation history operations — going back to an earlier point. Which of these a session
	// actually offers comes from SessionCapabilities.Thread; a client should not call one it was
	// not told about.
	TypeThreadTree       = "thread.tree"       // request → ThreadTreeResult
	TypeThreadFork       = "thread.fork"       // branch into a new session at a node
	TypeThreadRewind     = "thread.rewind"     // move THIS session back to a node
	TypeSessionTool      = "session.tool"      // a tool call with its command + output (rich inline card)
	TypeUIComponent      = "ui.component"      // event: a normalized generative-UI component (projected or fenced)
	TypeUIAction         = "ui.action"         // client → daemon: user activated a UI component's action
	TypeSessionHeartbeat = "session.heartbeat" // supervision state for a session (event)
	TypeRunOutput        = "run.output"        // streamed line from a test/build run (event)
	TypeRunResult        = "run.result"        // final pass/fail of a test/build run (event)
	TypeSessionProgress  = "session.progress"  // live step during session.create (drives the loading checklist)
	TypeLogSubscribe     = "log.subscribe"     // start streaming the daemon's log to this client (replays recent)
	TypeLogUnsubscribe   = "log.unsubscribe"   // stop streaming the daemon's log
	TypeLogLine          = "log.line"          // event: one daemon log line
	TypeActivityList     = "activity.list"     // request → recent cross-session activity events (the feed backbone)
	TypeActivityEvent    = "activity.event"    // event: one new activity item (finished/needs-you/error/loop)
	TypeActivityMarkRead = "activity.markread" // mark activity items read (clears the needs-you badge)
	TypeFanoutCreate     = "fanout.create"     // spawn N agents on the SAME prompt in isolated worktrees (compare + merge winner)
	TypeFanoutResolve    = "fanout.resolve"    // tear down a fan-out group (keep the winner, discard the rest + worktrees)
	// TypeFanoutSynthesize spawns an agent that reads the variants' diffs and writes the best
	// COMBINED implementation — as an additional variant in the same group, never as a replacement.
	// It is the alternative to diffing two branches and grafting one into the other by hand.
	TypeFanoutSynthesize = "fanout.synthesize"
	TypeTurnState        = "turn.state"       // daemon-authoritative turn lifecycle + heartbeat (the client renders, never infers)
	TypeNotifyPrefsGet   = "notify.prefs.get" // list toggleable push-notification types + their on/off state
	TypeNotifyPrefsSet   = "notify.prefs.set" // enable/disable one push-notification type

	TypeFanoutSummary        = "fanout.summary"         // per-variant results once every agent in a group finishes
	TypeMCPList              = "mcp.list"               // registered MCP servers + last probe status
	TypeMCPUpsert            = "mcp.upsert"             // add/replace an MCP server
	TypeMCPDelete            = "mcp.delete"             // remove an MCP server
	TypeMCPEnable            = "mcp.enable"             // enable/disable one server
	TypeMCPCheck             = "mcp.check"              // connect to a server and list its tools
	TypeMCPChanged           = "mcp.changed"            // broadcast: the server set or a status changed
	TypeMCPBrowse            = "mcp.browse"             // search the public MCP registry
	TypeMCPDiscover          = "mcp.discover"           // find servers a harness is already configured with
	TypeMCPImport            = "mcp.import"             // adopt discovered servers into the daemon registry
	TypeMCPExclusive         = "mcp.exclusive"          // let the daemon own MCP for its harnesses
	TypeApprovalRulesList    = "approval.rules.list"    // the persisted "always allow/deny" rules
	TypeApprovalRuleDelete   = "approval.rules.delete"  // drop one rule by index
	TypeApprovalRulesChanged = "approval.rules.changed" // broadcast: the rule set changed (any device)
	TypeCheckpointCreate     = "checkpoint.create"      // snapshot the session's worktree (a restore point on the timeline)
	TypeCheckpointList       = "checkpoint.list"        // list a session's checkpoints
	TypeCheckpointRestore    = "checkpoint.restore"     // roll the worktree back to a checkpoint
	TypeAccountList          = "account.list"           // list credential accounts + active selection + per-provider usage
	TypeAccountUpsert        = "account.upsert"         // add/update a credential account
	TypeAccountDelete        = "account.delete"         // remove a credential account
	TypeAccountActivate      = "account.activate"       // set the active account for a provider (hot-swap)
	TypeAccountQuota         = "account.quota"          // probe an account's remaining rate-limit/quota from the provider API
	TypeRemoteList           = "remote.list"            // list registered SSH remote hosts
	TypeRemoteUpsert         = "remote.upsert"          // add/update a remote host (probes it)
	TypeRemoteDelete         = "remote.delete"          // remove a remote host
	TypeRemoteStatus         = "remote.status"          // git status/diff of a remote worktree over SSH
	TypeRemoteRun            = "remote.run"             // start an agent SESSION on a remote host over SSH (streams output)

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

	// StatusStalled is an OPEN, still-recoverable turn the daemon believes is wedged: the provider
	// insists it is busy, but nothing has actually progressed. It is not an error and not terminal —
	// the daemon nudges a stalled turn to get it moving again (see StatusNeedsYou).
	StatusStalled = "stalled"
	// StatusNeedsYou is the terminal state for a turn that stayed stuck after the daemon spent its
	// nudges. It pages a human, but it is deliberately NOT an error: the agent didn't fail, it got
	// stuck, and "the agent errored" was the wrong story to tell about it. StatusError and
	// "abandoned" keep their old, narrower meanings — a provider that reported a failure, and a
	// provider proven unreachable.
	StatusNeedsYou = "needs_you"
	// StatusRecovering is an OPEN turn whose agent stopped answering and which the daemon is
	// actively repairing — reconnecting, re-resolving, retrying. It is not an error and needs
	// nothing from the user; it exists so a blip is something they watch heal rather than something
	// they get paged about. Only if recovery genuinely fails does the turn become abandoned.
	StatusRecovering = "recovering"
	// StatusAbandoned is a turn the daemon gave up on: the provider was proven unreachable, or the
	// stream died with the turn still open. Terminal, and the only state that renders as "no
	// response". It may be declared ONLY by the daemon (see hub/turn.go), never inferred client-side.
	//
	// Late addition: this was the one status of the eight carried as a bare string literal at every
	// use, so it alone got no compiler help against a typo and never appeared in a search for the
	// constant. Nothing depended on the literal — the wire value is unchanged.
	StatusAbandoned = "abandoned"
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
	Plan          bool              `json:"plan,omitempty"`           // DEPRECATED alias for Mode=="architect"; kept for older clients
	Mode          string            `json:"mode,omitempty"`           // code (default) | ask | architect — see the Mode* constants
	Autonomous    bool              `json:"autonomous,omitempty"`     // let the heartbeat nudge it to keep going
	MaxNudges     int               `json:"max_nudges,omitempty"`     // give-up bound for auto-nudging (0 = default)
	BudgetUSD     float64           `json:"budget_usd,omitempty"`     // cost ceiling for auto-nudging (0 = default)
	Model         string            `json:"model,omitempty"`          // model id to run with ("" = provider default)
	ModelProvider string            `json:"model_provider,omitempty"` // sub-provider/backend for the model (opencode needs it)
	Ephemeral     bool              `json:"ephemeral,omitempty"`      // a scratch "just chat" session: no project, NOT persisted (vanishes on restart)
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

// WorktreeCatchUp merges the repo's default branch into a worktree session's branch (request carries
// just SessionID; the rest is the response).
type WorktreeCatchUp struct {
	SessionID string   `json:"session_id"`
	Status    string   `json:"status,omitempty"`    // "updated" | "up_to_date" | "conflicts"
	Base      string   `json:"base,omitempty"`      // default branch merged in (e.g. "main")
	Message   string   `json:"message,omitempty"`   // human summary
	Conflicts []string `json:"conflicts,omitempty"` // conflicted paths (merge left in progress to resolve)
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

// LogHistory is the reply to log.subscribe: the recently-buffered daemon log lines.
type LogHistory struct {
	Lines []string `json:"lines"`
}

// ActivityEvent is one cross-session activity item (Activity feed / Needs-You inbox / ticker).
type ActivityEvent struct {
	ID        string `json:"id"`
	TS        int64  `json:"ts"`
	Kind      string `json:"kind"` // finished | needs_input | error | loop_run | loop_pr | started
	SessionID string `json:"session_id,omitempty"`
	Provider  string `json:"provider,omitempty"`
	Project   string `json:"project,omitempty"`
	Title     string `json:"title"`
	Detail    string `json:"detail,omitempty"`
	NeedsYou  bool   `json:"needs_you"`
	Read      bool   `json:"read"`
}

// ActivityList is the reply to activity.list: the recent feed (oldest first).
type ActivityList struct {
	Events []ActivityEvent `json:"events"`
}

// ActivityMarkRead marks items read; empty IDs = mark all read.
type ActivityMarkRead struct {
	IDs []string `json:"ids,omitempty"`
}

// FanoutCreate spawns N agents on the SAME prompt, each in its own git worktree/branch, as one
// group — so you can compare their approaches and merge the winner (Orca's core primitive). When
// Models is set, each variant uses the model at its index (cycling if fewer than Count); otherwise
// all variants use the provider default. Count is clamped to [2, 6].
type FanoutCreate struct {
	Provider   string   `json:"provider"`
	ProjectID  string   `json:"project_id,omitempty"`
	ProjectIDs []string `json:"project_ids,omitempty"`
	Prompt     string   `json:"prompt"`
	Plan       bool     `json:"plan,omitempty"`
	// Judge asks a fresh agent to recommend a winner once every variant finishes. Advisory only —
	// it answers with a tappable choice; the manual Keep buttons are unaffected.
	Judge bool `json:"judge,omitempty"`
	// Prompts turns a RACE into a DIVISION of labour: each agent gets its own subtask instead of all
	// of them attempting the same one. Count is ignored when this is set (the list defines the fan).
	// The variants still land in separate worktrees, so their work stays reviewable independently.
	Prompts []string `json:"prompts,omitempty"`
	Count   int      `json:"count"`
	Models  []string `json:"models,omitempty"`
}

// FanoutResult reports the spawned group.
type FanoutResult struct {
	Group      string   `json:"group"`
	SessionIDs []string `json:"session_ids"`
}

// FanoutResolve tears down a fan-out group: every variant EXCEPT Keep (a session id to preserve, "" =
// discard all) is stopped, deleted, and its worktree removed — so racing N approaches doesn't leave N
// orphaned worktrees + sessions accumulating after you've picked a winner.
type FanoutResolve struct {
	Group string `json:"group"`
	Keep  string `json:"keep,omitempty"`  // session id of the winner to preserve (optional)
	Force bool   `json:"force,omitempty"` // remove worktrees even with uncommitted changes
}

// FanoutResolved reports which variants were torn down (and which winner was kept).
type FanoutResolved struct {
	Group   string   `json:"group"`
	Kept    string   `json:"kept,omitempty"`
	Removed []string `json:"removed"`
	Failed  []string `json:"failed,omitempty"` // variants whose teardown errored (e.g. dirty worktree without force)
}

// TurnChild is one sub-agent's state within a parent turn.
//
// LastEventAt is the child's OWN liveness clock. The parent turn's clock is bumped by any event from
// any child, so a single chatty sub-agent used to mask nine stalled ones — a fan-out could sit with
// one worker alive and the rest dead and still look perfectly healthy. Stall detection reads this.
type TurnChild struct {
	ID          string `json:"id"`
	State       string `json:"state"` // running | done | error
	Title       string `json:"title,omitempty"`
	StartedAt   int64  `json:"started_at,omitempty"`    // unix seconds
	LastEventAt int64  `json:"last_event_at,omitempty"` // unix seconds of this child's last event
}

// TurnTool is one tool call still outstanding in a turn. Tool cards were previously fire-and-forget
// (a `running` event, then a `completed` one that might never arrive), so a tool whose completion was
// lost rendered as "running" forever — the single most-reported hang. The turn now knows what is
// outstanding, which makes two things possible: sealing them when the turn ends, and telling a
// wedged-vs-working turn apart (a turn where no tool has started or finished in minutes is stuck,
// even when the provider still swears it is busy).
type TurnTool struct {
	ID        string `json:"id"`
	Name      string `json:"name"`            // bash, read, glob, task, …
	Title     string `json:"title,omitempty"` // human summary of the invocation
	StartedAt int64  `json:"started_at,omitempty"`
}

// TurnState is the daemon-owned truth about a session's current turn: pushed on every transition and
// as a ~10s heartbeat while a turn is open. The client renders it verbatim — it runs NO liveness
// timers. "abandoned" is the ONLY state that may render as "no response", and only the daemon (via
// provider probes) can declare it.
type TurnState struct {
	SessionID   string      `json:"session_id"`
	TurnID      string      `json:"turn_id"`
	State       string      `json:"state"`                   // running | awaiting_approval | stalled | idle | error | needs_you | abandoned
	StartedAt   int64       `json:"started_at,omitempty"`    // unix seconds
	LastEventAt int64       `json:"last_event_at,omitempty"` // unix seconds of the last provider event
	Detail      string      `json:"detail,omitempty"`        // e.g. "running bash"
	Reason      string      `json:"reason,omitempty"`        // for stalled/error/needs_you/abandoned
	Children    []TurnChild `json:"children,omitempty"`      // sub-agents of this turn
	Tools       []TurnTool  `json:"tools,omitempty"`         // tool calls still outstanding
	Nudges      int         `json:"nudges,omitempty"`        // nudges spent on THIS turn (see StatusStalled)
}

// NotifyPref is one toggleable push-notification type. NotifyPrefs is the full labeled catalog with
// each type's current on/off state (notify.prefs.get); NotifyPrefSet flips one (notify.prefs.set).
type NotifyPref struct {
	Key     string `json:"key"`   // the APNs category, e.g. "AGENT_FINISHED"
	Label   string `json:"label"` // human name for the settings row
	Detail  string `json:"detail,omitempty"`
	Enabled bool   `json:"enabled"`
}
type NotifyPrefs struct {
	Prefs []NotifyPref `json:"prefs"`
}
type NotifyPrefSet struct {
	Key     string `json:"key"`
	Enabled bool   `json:"enabled"`
}

// Checkpoint is a restore point: a snapshot of a session's worktree at a point in time.
type Checkpoint struct {
	SHA   string `json:"sha"`
	Label string `json:"label"`
	TS    int64  `json:"ts"`
}

// CheckpointCreate snapshots a session's worktree with an optional label.
type CheckpointCreate struct {
	SessionID string `json:"session_id"`
	Label     string `json:"label,omitempty"`
}

// CheckpointRestore rolls a session's worktree back to the given checkpoint.
type CheckpointRestore struct {
	SessionID string `json:"session_id"`
	SHA       string `json:"sha"`
}

// CheckpointList is a session's checkpoints (newest first).
type CheckpointList struct {
	Checkpoints []Checkpoint `json:"checkpoints"`
}

// Account is one named credential set for a provider (env overrides = API keys / config dirs).
type Account struct {
	ID       string            `json:"id"`
	Provider string            `json:"provider"`
	Name     string            `json:"name"`
	Env      map[string]string `json:"env,omitempty"`
	Active   bool              `json:"active,omitempty"` // is this the active account for its provider
}

// ProviderUsage is rolled-up token/cost usage for one provider across its sessions (the usage meter).
type ProviderUsage struct {
	Provider     string  `json:"provider"`
	Sessions     int     `json:"sessions"`
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd"`
}

// AccountList is the reply to account.list: accounts (active-flagged) + per-provider usage.
type AccountList struct {
	Accounts []Account       `json:"accounts"`
	Usage    []ProviderUsage `json:"usage"`
}

// AccountActivate selects the active account for a provider.
type AccountActivate struct {
	Provider  string `json:"provider"`
	AccountID string `json:"account_id"`
}

// AccountQuota is the reply to account.quota: an account's remaining rate-limit/quota.
type AccountQuota struct {
	AccountID         string `json:"account_id"`
	Available         bool   `json:"available"`
	RequestsRemaining int    `json:"requests_remaining"`
	TokensRemaining   int    `json:"tokens_remaining"`
	ResetInSeconds    int    `json:"reset_in_seconds,omitempty"` // seconds until reset (0 = unknown)
	Note              string `json:"note,omitempty"`
}

// AccountRef identifies an account (delete).
type AccountRef struct {
	AccountID string `json:"account_id"`
}

// RemoteHost is a registered SSH remote where a worktree/agent can run.
type RemoteHost struct {
	ID         string        `json:"id,omitempty"`
	Name       string        `json:"name"`
	SSHTarget  string        `json:"ssh_target"`
	RemotePath string        `json:"remote_path"`
	Reachable  bool          `json:"reachable,omitempty"` // last probe result (remote.list)
	Forwards   []PortForward `json:"forwards,omitempty"`  // local↔remote port tunnels (e.g. dev server)
}

// PortForward tunnels a local port to a remote port over SSH (-L), so a remote dev server is
// reachable at http://localhost:<LocalPort>.
type PortForward struct {
	LocalPort  int `json:"local_port"`
	RemotePort int `json:"remote_port"`
}

// RemoteList is a set of remote hosts.
type RemoteList struct {
	Hosts []RemoteHost `json:"hosts"`
}

// RemoteRef identifies a remote host (delete / status).
type RemoteRef struct {
	ID string `json:"id"`
}

// RemoteStatus is the git status + diff of a remote worktree fetched over SSH.
type RemoteStatus struct {
	ID     string `json:"id"`
	Status string `json:"status"` // porcelain
	Diff   string `json:"diff"`
	Error  string `json:"error,omitempty"`
}

// RemoteRun starts an agent session ON a remote host over SSH: `ssh <target> "cd <path> && <cmd>"`.
// AgentCommand is the agent invocation to run remotely (e.g. "opencode run" or "claude -p"); the
// prompt is appended. The resulting session streams the remote agent's output like any other.
type RemoteRun struct {
	HostID       string `json:"host_id"`
	AgentCommand string `json:"agent_command"`
	Prompt       string `json:"prompt,omitempty"`
}

// LogLine is one streamed daemon log line (event).
type LogLine struct {
	Line string `json:"line"`
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
	// JiraSiteAmbiguous is set when the OAuth token reaches MORE THAN ONE Atlassian site.
	//
	// Atlassian's consent screen lets you choose a site, but the token it returns does not say which
	// one you chose — `accessible-resources` just lists every site the token can reach, in no
	// defined order. The daemon has to pick one to route API calls at, and picking the first is a
	// coin flip: get it wrong and the board loads someone else's project, with nothing on screen
	// explaining why.
	//
	// So the guess is still made (a connection has to route somewhere) but it is now DECLARED, and
	// the app prompts with `jira.sites` instead of leaving you to discover the wrong data and find
	// the switcher yourself.
	JiraSiteAmbiguous bool `json:"jira_site_ambiguous,omitempty"`
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
	TeamName    string `json:"team_name,omitempty"` // human name of the project/team (board picker labels)
	Priority    int    `json:"priority,omitempty"`
	UpdatedAt   string `json:"updated_at,omitempty"`
	CycleID     string `json:"cycle_id,omitempty"`
	CycleName   string `json:"cycle_name,omitempty"`
	CycleNumber int    `json:"cycle_number,omitempty"`
	SprintName  string `json:"sprint_name,omitempty"`  // Jira active sprint (Linear reuses cycle)
	SprintState string `json:"sprint_state,omitempty"` // "active" | "future" | "closed"
	// Editable-field detail (populated by issue.detail; drives full two-way editing).
	AssigneeID string       `json:"assignee_id,omitempty"`
	Labels     []IssueLabel `json:"labels,omitempty"`
	Estimate   float64      `json:"estimate,omitempty"`
	DueDate    string       `json:"due_date,omitempty"`
}

// IssueUser is an assignable person (assignee picker).
type IssueUser struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Email  string `json:"email,omitempty"`
	Avatar string `json:"avatar,omitempty"`
}

// IssueLabel is a tag/label (label picker + on-issue labels).
type IssueLabel struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Color string `json:"color,omitempty"`
}

// IssueCycle is a sprint (Jira) / cycle (Linear) (sprint picker).
type IssueCycle struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Number int    `json:"number,omitempty"`
	State  string `json:"state,omitempty"`
}

// Picker requests (client → daemon) + list replies for the ticket editor.
type IssueMembersReq struct {
	Provider string `json:"provider"`
	TeamID   string `json:"team_id"`
	IssueID  string `json:"issue_id,omitempty"`
}
type IssueMemberList struct {
	Members []IssueUser `json:"members"`
}
type IssueLabelsReq struct {
	Provider string `json:"provider"`
	TeamID   string `json:"team_id"`
}
type IssueLabelList struct {
	Labels []IssueLabel `json:"labels"`
}
type IssueCyclesReq struct {
	Provider string `json:"provider"`
	TeamID   string `json:"team_id"`
}
type IssueCycleList struct {
	Cycles []IssueCycle `json:"cycles"`
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

// IssueDetail is the full issue view: the issue, its comments, and its attachments.
type IssueDetail struct {
	Issue       Issue             `json:"issue"`
	Comments    []IssueComment    `json:"comments"`
	Attachments []IssueAttachment `json:"attachments,omitempty"`
}

// IssueAttachment is a file on an issue. IsImage lets the app render it inline vs. offer a download.
type IssueAttachment struct {
	ID       string `json:"id"`
	Filename string `json:"filename"`
	URL      string `json:"url"` // auth-gated content URL (fetch via issue.image)
	Mime     string `json:"mime,omitempty"`
	Size     int    `json:"size,omitempty"`
	IsImage  bool   `json:"is_image,omitempty"`
}

// IssueColumnsReq asks for a project/team's ordered workflow statuses (the real-status board columns).
type IssueColumnsReq struct {
	Provider string `json:"provider"`
	Project  string `json:"project"` // Jira project key / Linear team id
}

// IssueMove moves an issue to a status (drag-drop). The daemon resolves the transition (Jira) or
// sets the state (Linear). Reply is the updated Issue.
type IssueMove struct {
	Provider string `json:"provider"`
	IssueID  string `json:"issue_id"`
	StatusID string `json:"status_id"`
}

// IssueCreate creates a ticket. Reply is the created Issue.
type IssueCreate struct {
	Provider    string `json:"provider"`
	Project     string `json:"project"` // Jira project key / Linear team id
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Priority    int    `json:"priority,omitempty"`
	Type        string `json:"type,omitempty"` // Jira issue type name (e.g. "Task"); ignored by Linear
}

// IssueProjectsList lists the projects/teams the connected trackers expose (board picker).
type IssueProjectsList struct {
	Projects []IssueProject `json:"projects"`
}

type IssueProject struct {
	ID       string `json:"id"` // Jira project key / Linear team id
	Name     string `json:"name"`
	Provider string `json:"provider"`
}

// IssueUpdate is a partial edit of an issue; only non-nil fields are applied. The
// reply is the updated Issue.
type IssueUpdate struct {
	Provider    string    `json:"provider"`
	IssueID     string    `json:"issue_id"`
	Title       *string   `json:"title,omitempty"`
	Description *string   `json:"description,omitempty"`
	StateID     *string   `json:"state_id,omitempty"`
	Priority    *int      `json:"priority,omitempty"`
	AssigneeID  *string   `json:"assignee_id,omitempty"`
	LabelIDs    *[]string `json:"label_ids,omitempty"`
	CycleID     *string   `json:"cycle_id,omitempty"`
	Estimate    *float64  `json:"estimate,omitempty"`
	DueDate     *string   `json:"due_date,omitempty"`
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

// PreviewFetchReq asks the daemon to fetch ONE resource from a session's dev server on the client's
// behalf, so a web view running in the app can render a page that is only reachable from the daemon
// host.
//
// There is deliberately NO url field. The target is derived entirely from SessionID, server-side,
// by looking up that session's own registered preview. A caller-supplied URL would make this an
// open proxy running inside the user's LAN: it could reach 169.254.169.254, the daemon's own control
// port, another session's preview, or any host on the network. Taking only a path means the caller
// cannot name a destination at all, so those are not risks to be validated — they are unreachable.
type PreviewFetchReq struct {
	SessionID string `json:"session_id"`
	// Path is the resource within that dev server, e.g. "/" or "/assets/app.js?v=2".
	Path string `json:"path"`
	// Method defaults to GET. Present because a preview page will POST forms and issue API calls
	// against its own backend.
	Method string `json:"method,omitempty"`
	// Headers the web view asked for (Accept, Range and friends). Hop-by-hop and authorization
	// headers are dropped daemon-side; see previewRequestHeaders.
	Headers map[string]string `json:"headers,omitempty"`
	// Body is base64 (StdEncoding), for POST/PUT.
	Body string `json:"body,omitempty"`
}

// PreviewFetchResp is one HTTP response, carried whole.
//
// Whole rather than streamed because the envelope layer has no chunked-reply notion: every reply is
// a single payload. The frame ceiling is 8 MiB and base64 inflates by 4/3, so the daemon caps the
// body well under that and says so rather than truncating into a corrupt asset.
type PreviewFetchResp struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    string            `json:"body"` // base64 (StdEncoding)
}

// GitHubRepo is one repository the user can reach, as offered when starting a session.
type GitHubRepo struct {
	Name          string `json:"name"`
	NameWithOwner string `json:"name_with_owner"`
	Description   string `json:"description,omitempty"`
	URL           string `json:"url,omitempty"`
	Private       bool   `json:"private,omitempty"`
	UpdatedAt     string `json:"updated_at,omitempty"`
	Language      string `json:"language,omitempty"`
	// LocalPath is where this repo already sits on the daemon host, "" if it is not checked out.
	// Resolved daemon-side because the client cannot see that disk.
	LocalPath string `json:"local_path,omitempty"`
}

// GitHubReposReq asks for repositories. An empty Owner means "everything this account can reach";
// naming one asks that owner directly, which is the only way to reach an org the user is an outside
// collaborator on — those appear in neither user/repos nor user/orgs.
type GitHubReposReq struct {
	Owner string `json:"owner,omitempty"`
}

// GitHubRepos answers github.repos.
type GitHubRepos struct {
	// Available is false when gh is missing or signed out; Reason then says what to do about it.
	Available bool         `json:"available"`
	Reason    string       `json:"reason,omitempty"`
	Account   string       `json:"account,omitempty"`
	Repos     []GitHubRepo `json:"repos,omitempty"`
	// CloneRoots are directories the user already keeps checkouts in, most-used first, offered as
	// destinations so nobody has to type a path on a phone.
	CloneRoots []string `json:"clone_roots,omitempty"`
}

// GitHubClone asks the daemon to check a repository out under Parent.
type GitHubClone struct {
	NameWithOwner string `json:"name_with_owner"`
	Parent        string `json:"parent"`
}

// GitHubCloned reports where a clone landed.
type GitHubCloned struct {
	Path string `json:"path"`
}

// PreviewDOMAsk asks a connected app to perform one DOM operation inside the preview it already has
// open, and report back.
//
// The daemon cannot do this itself: it speaks HTTP, and a dev server's HTML is inert. Clicking needs
// a live DOM with the page's own JavaScript having run — for any client-rendered app the fetched
// markup is an empty shell. The app already has a real browser engine, so the work happens there.
//
// The cost of that choice, stated plainly because it shapes the tools' behaviour: this only works
// while someone has that session's preview open. An agent working with nobody watching gets a clear
// refusal rather than a wrong answer.
type PreviewDOMAsk struct {
	RequestID string `json:"request_id"`
	SessionID string `json:"session_id"`
	// Op is snapshot | click | fill.
	Op string `json:"op"`
	// Ref identifies an element from a previous snapshot. Not a CSS selector: a snapshot stamps each
	// element it lists, so a ref means "the thing you showed me", which survives a class name
	// changing under it and cannot be used to reach an element the agent was never shown.
	Ref string `json:"ref,omitempty"`
	// Value is the text to type, for fill.
	Value string `json:"value,omitempty"`
}

// PreviewDOMResult is one app's answer to a PreviewDOMAsk.
type PreviewDOMResult struct {
	RequestID string `json:"request_id"`
	OK        bool   `json:"ok"`
	// Result is the operation's payload — for a snapshot, the page outline.
	Result string `json:"result,omitempty"`
	Error  string `json:"error,omitempty"`
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
	// MsgID is the provider's stable message id when known (opencode). It lets the durable transcript
	// dedup a message a provider re-streams when its history is replayed on re-attach. Omitted for
	// providers/paths without a stable id (the transcript then keys such rows by sequence only).
	MsgID string `json:"msg_id,omitempty"`
	// Author is the human name of the device/person that sent a USER message (from client.identify).
	// Empty on assistant/tool messages and on anything sent before a client identified itself.
	Author string `json:"author,omitempty"`
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
	Input      json.RawMessage `json:"input,omitempty"`  // the tool's raw arguments, when the harness provides them
	// Patterns are the harness's OWN glob(s) for what this request covers (opencode sends these).
	// They make the best "always allow" suggestions because they come from the tool that will be
	// matching them, not from us re-parsing a command string.
	Patterns []string `json:"patterns,omitempty"`
	// Scopes the daemon suggests for an ALWAYS answer, narrowest first. The client renders these as
	// the "Always allow …" menu; it never invents its own, so scope semantics live in ONE place.
	SuggestedScopes []ApprovalScope `json:"suggested_scopes,omitempty"`
}

// ApprovalScope narrows what an ALWAYS decision applies to. Kind is "tool" (this tool anywhere),
// "pattern" (commands/URLs matching Value as a glob), "path" (anything under the Value subtree), or
// "project" (this tool, but only in the session's project).
type ApprovalScope struct {
	Kind  string `json:"kind"`
	Value string `json:"value,omitempty"`
	Label string `json:"label"` // ready-to-render menu text, e.g. `Always allow "git *"`
}

type ApprovalRespond struct {
	ApprovalID string `json:"approval_id"`
	Decision   string `json:"decision"`
	// Scope applies only to DecisionAlways. Omitted = the historical provider+tool rule.
	Scope *ApprovalScope `json:"scope,omitempty"`
}

// ApprovalResolved is broadcast to every client when an approval is answered, so a
// pending approval card clears on all devices (not just the one that responded).
type ApprovalResolved struct {
	ApprovalID string `json:"approval_id"`
	Decision   string `json:"decision"`
}

// Execution locations for Session.ExecKind — WHERE the agent process actually runs.
//
// Local is the EMPTY string rather than the word "local" on purpose. Every session a daemon built
// before this field existed reports nothing, and every one of those sessions IS local, so
// absent-means-local lets an app decode old and new daemons with a single rule ("host present →
// remote") instead of a three-way local/remote/unknown it would have to render some hedge for. It
// also keeps the field off the wire for the overwhelmingly common case, which matters on the relay
// path where the whole session list is re-sent on every change.
const (
	ExecKindLocal = ""    // this Mac — the daemon's own machine
	ExecKindSSH   = "ssh" // a remote host over ssh (hub.spawnRemote)
)

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
	// PreviewURL is the NAMED address of this session's dev server, e.g.
	// http://fix-login.localhost:7777. Several sessions run at once and each gets its own port, so
	// a bare number tells you nothing about whose it is. The raw port stays reachable and is not
	// replaced: anything that hardcodes localhost:PORT — OAuth redirect URIs, chiefly — cannot
	// follow a name.
	PreviewURL    string `json:"preview_url,omitempty"`
	IssueKey      string `json:"issue_key,omitempty"` // the ticket this session works (e.g. ENG-42)
	IssueID       string `json:"issue_id,omitempty"`
	Model         string `json:"model,omitempty"`          // active model id ("" = provider default)
	ModelProvider string `json:"model_provider,omitempty"` // sub-provider/backend for the model
	Mode          string `json:"mode,omitempty"`           // code | ask | architect ("" = code)
	Restartable   bool   `json:"restartable,omitempty"`    // a stopped session that can be re-created (session.restart)
	UpdatedAt     int64  `json:"updated_at,omitempty"`     // unix seconds of last activity (0 = unknown)
	// Cumulative token/cost usage for the session (surfaced as a meter; 0 = unknown).
	InputTokens  int     `json:"input_tokens,omitempty"`
	OutputTokens int     `json:"output_tokens,omitempty"`
	CostUSD      float64 `json:"cost_usd,omitempty"`
	// True when this worktree session's branch would conflict with the default branch (passive
	// badge, computed by a periodic sweep) — so parallel agents on one repo don't silently collide.
	Conflicted bool `json:"conflicted,omitempty"`
	// Fan-out grouping: when this session is one of N agents racing the same prompt, FanoutGroup is
	// the shared group id and FanoutVariant is its 0-based index (so the app groups + labels them).
	FanoutGroup   string `json:"fanout_group,omitempty"`
	FanoutVariant int    `json:"fanout_variant,omitempty"`
	// Ephemeral: a scratch "just chat" session (no project, not persisted) — the app can label/style it.
	Ephemeral bool `json:"ephemeral,omitempty"`
	// Execution location, deliberately NOT derived from Name. The remote host used to survive only
	// inside the default label ("remote: build-box"), which session.rename overwrites — so renaming a
	// remote session erased the only thing saying it wasn't running on this Mac, in the sidebar and in
	// its push notifications alike. ExecKind is one of the ExecKind* constants; ExecHost names the
	// remote host and is empty for a local session.
	ExecKind string `json:"exec_kind,omitempty"`
	ExecHost string `json:"exec_host,omitempty"`
}

// SessionUsage is a usage update for one session (event). InputTokens/OutputTokens/CostUSD are
// the delta for the just-completed turn; the hub accumulates them onto the Session.
type SessionUsage struct {
	SessionID    string  `json:"session_id"`
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd"`
}

// The two events below exist because the adapter layer had become a narrow waist sized to the
// INTERSECTION of the providers rather than the union of them. Every adapter emitted the same eight
// events, so the surface could only ever show what the poorest provider could do — which penalised
// the richest provider hardest, and left the app describing a session differently from that
// provider's own TUI sitting next to it.
//
// The fix is not "more event types per feature". It is to let a provider DECLARE what it supports
// and REPORT what is currently true, and let the client render from that. Adding a provider, or a
// capability to one, should not require a new event or a new branch in the UI.

// SessionMode is one behaviour/permission mode a provider offers — claude-code's permissionMode
// (default/plan/acceptEdits/bypassPermissions), opencode's Build vs Plan, and so on.
//
// Mode is not decoration: it decides what the agent will do WITHOUT asking. Not being able to see
// the current one is a correctness gap, which is why it is modelled rather than left to a label.
type SessionMode struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	// Unsafe marks a mode that bypasses approvals (e.g. bypassPermissions). The client is expected
	// to make entering one deliberate and visible while it is active.
	Unsafe bool `json:"unsafe,omitempty"`
}

// ThreadCaps declares which conversation-history operations a provider supports.
//
// Distinct from checkpoints, which snapshot the FILESYSTEM with git. These move the CONVERSATION:
// pi has them natively (/tree, and forking from any earlier user message), and the others have
// their own partial equivalents.
type ThreadCaps struct {
	Tree    bool `json:"tree,omitempty"`    // enumerate the tree's nodes
	Fork    bool `json:"fork,omitempty"`    // start a NEW session branching from an earlier point
	Rewind  bool `json:"rewind,omitempty"`  // move THIS session's position to another node
	Compact bool `json:"compact,omitempty"` // summarise history to reclaim context
	// Summarize: when moving away from a branch, this provider can summarise the entries being
	// left behind and carry that summary forward.
	//
	// Kept distinct from Compact, which reclaims context on the CURRENT line. This one is about not
	// losing what an abandoned attempt learned — the difference between "go back and try again" and
	// "go back and try again, knowing why the last attempt failed". pi offers it as a choice at the
	// moment of navigating (none / summarise / summarise with custom instructions).
	Summarize bool `json:"summarize,omitempty"`
	// Unrevert: a rewind on this provider can itself be undone (opencode's /unrevert). Nothing else
	// offers it, and it changes what a client should SAY before rewinding — "this cannot be undone"
	// is false on opencode and true everywhere else, and getting that backwards in either direction
	// is worse than saying nothing.
	Unrevert bool `json:"unrevert,omitempty"`
}

// SessionCapabilities is what a provider CAN do. Absent fields mean "not supported", so a client
// renders nothing for them rather than an affordance that fails when tapped.
type SessionCapabilities struct {
	SessionID string        `json:"session_id"`
	Provider  string        `json:"provider"`
	Modes     []SessionMode `json:"modes,omitempty"`
	Efforts   []string      `json:"efforts,omitempty"` // reasoning/thinking levels, low→high
	Commands  bool          `json:"commands,omitempty"`
	Agents    bool          `json:"agents,omitempty"` // can dispatch sub-agents
	Models    bool          `json:"models,omitempty"` // model can be chosen/switched
	Thread    ThreadCaps    `json:"thread"`
}

// SessionFacts is live ambient state — what is true right now. Every field is optional: a provider
// reports what it knows, and the client omits what it is not told rather than inventing a default.
//
// Emitted on change, not per turn, so it is cheap enough to keep a status bar honest.
type SessionFacts struct {
	SessionID string `json:"session_id"`
	Model     string `json:"model,omitempty"`
	Mode      string `json:"mode,omitempty"`   // matches a SessionMode.ID
	Effort    string `json:"effort,omitempty"` // matches an entry in Capabilities.Efforts
	Branch    string `json:"branch,omitempty"` // git branch the session is working on
	CWD       string `json:"cwd,omitempty"`
	// Context budget. Max of 0 means the provider does not report one, in which case the client
	// shows nothing rather than a meter reading zero.
	ContextUsed int `json:"context_used,omitempty"`
	ContextMax  int `json:"context_max,omitempty"`
	Queued      int `json:"queued,omitempty"` // prompts waiting behind the current turn
}

// ThreadNode is one point a conversation can be moved to.
//
// Measured against pi's real tree rather than guessed at: four turns produced NINETEEN nodes. Every
// tool call, file edit, shell run, model change and thinking-level change is its own node, and so is
// the branch summary itself. Two consequences the design has to take seriously:
//
//   - a picker that lists nodes unfiltered is a wall of tool calls with the user's messages lost in
//     it. pi's own selector ships type filters and a search box for exactly this reason, and any
//     client rendering this needs the same or it will be unusable by the second branch.
//   - Kind is not decoration. It is what lets a client filter, and what lets it render an edit or a
//     branch summary as something other than an empty preview line.
type ThreadNode struct {
	ID       string `json:"id"`
	ParentID string `json:"parent_id,omitempty"` // the tree is parent-linked; roots have none
	// Kind of node. Observed in pi: user, assistant, tool, edit, bash, branch_summary, model,
	// thinking, session. Left as a string rather than an enum because a provider may have kinds we
	// have not seen, and an unknown kind should render generically rather than be dropped.
	Kind    string `json:"kind,omitempty"`
	Preview string `json:"preview"`         // one line for the picker
	At      int64  `json:"at,omitempty"`    // unix seconds
	Depth   int    `json:"depth,omitempty"` // nesting for tree rendering; 0 = the main line
	// Current is the session's position — pi calls it the leaf. Exactly one node has it.
	Current bool `json:"current,omitempty"`
	// OnPath marks the nodes between the root and Current: the line the session is actually on.
	//
	// Distinct from Current, and both are needed. pi renders the active line bright and the
	// abandoned siblings dimmed, which is the whole reason a tree with two branches is readable at
	// all — without it a client can only highlight one row and every other branch looks equally live.
	OnPath bool `json:"on_path,omitempty"`
}

// ThreadRef names a node in a session's history (thread.fork / thread.rewind).
type ThreadRef struct {
	SessionID string `json:"session_id"`
	NodeID    string `json:"node_id"`
}

// ThreadTreeResult is a session's branchable history (thread.tree).
type ThreadTreeResult struct {
	SessionID string       `json:"session_id"`
	Nodes     []ThreadNode `json:"nodes"`
}

// ThreadForkResult reports where the fork went.
//
// SessionID is the session to open afterwards, and it is NOT always a new one: opencode's fork
// creates a separate session, while pi's rebinds the one you were in. A client that assumed either
// would send half its users to the wrong conversation, so the daemon answers with the id to use.
type ThreadForkResult struct {
	SessionID string `json:"session_id"`
	// New reports whether SessionID is a session that did not exist before, so the client knows
	// whether to add a row or stay where it is.
	New bool `json:"new,omitempty"`
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

// SessionTool is one tool call, surfaced as a rich inline card that separates the invocation
// (Name + Title, e.g. "bash · ls -la") from its Output, instead of hiding it behind a "running…"
// chip. Updated in place by ID: a running card gains its Output/Status when the tool completes.
type SessionTool struct {
	SessionID string `json:"session_id"`
	ID        string `json:"id"`
	Name      string `json:"name"`             // tool name (bash, read, edit, task, …)
	Title     string `json:"title,omitempty"`  // human summary of the invocation (opencode's tool title)
	Output    string `json:"output,omitempty"` // result text (or error), shown on expand
	Status    string `json:"status"`           // running | completed | error
	// Additions/Deletions are the line counts of an edit's diff, when the harness gave us one to
	// count. BOTH zero means "we couldn't tell" — not "nothing changed" — and the client renders no
	// badge at all rather than a confident "+0 −0". They're computed daemon-side because the diff
	// often arrives in provider metadata the client never sees.
	Additions int `json:"additions,omitempty"`
	Deletions int `json:"deletions,omitempty"`
}

// SubAgent announces the lifecycle of a sub-agent a session delegates to (e.g. opencode's `task`
// tool spawns a child session). The app renders an inline, collapsible card in the parent transcript
// keyed by ID; the child's own output/tools then stream in tagged with SessionID == ID.
type SubAgent struct {
	ParentID string `json:"parent_id"` // the delegating (parent) session
	ID       string `json:"id"`        // the sub-agent's session id (its events carry this SessionID)
	Title    string `json:"title,omitempty"`
	Status   string `json:"status"` // started | done | error
}

// UIComponent is a normalized generative-UI element the daemon parses out of a harness's assistant
// text — a fenced ```iron:ui``` block (or a bare one-line component JSON). Components are also PROJECTED from
// structured tool events (todos→checklist, see genui.ProjectTodos); the fence is the main source but
// no longer the only one. The model emits typed intent (a catalog component +
// inert props); the CLIENT owns the native view. Props is opaque at transport and validated
// daemon-side against the per-component schema; the client decodes it per (Component, SchemaV).
// FallbackText is mandatory markdown so an unknown component or a newer schema degrades visibly.
type UIComponent struct {
	SessionID    string          `json:"session_id"`
	MessageID    string          `json:"message_id,omitempty"` // RESERVED: no emitter populates this yet
	ID           string          `json:"id"`                   // stable per message — enables in-place update
	Component    string          `json:"component"`            // table | checklist | plan | callout | diff | choice | confirm
	SchemaV      int             `json:"schema_v"`             // per-component schema version (forward-compatible)
	Status       string          `json:"status"`               // running | ready | error
	Props        json.RawMessage `json:"props,omitempty"`      // inert, per-component; validated daemon-side
	Actions      []UIAction      `json:"actions,omitempty"`    // allow-listed interaction verbs (choice/confirm)
	FallbackText string          `json:"fallback_text"`        // mandatory markdown for unknown/older clients
}

// UIAction is an allow-listed interaction a UI component may offer. Kind is a whitelisted verb the
// CLIENT executes — never a command/RPC/URL: "prompt" (send a templated next user turn), "answer"
// (a typed reply to a request id), "permission" (resolve an approval via the native ApprovalSheet).
type UIAction struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"` // prompt | answer | permission
	Label  string `json:"label,omitempty"`
	Style  string `json:"style,omitempty"`  // default | destructive | cancel (confirm buttons)
	Prompt string `json:"prompt,omitempty"` // templated user-turn text sent for kind=="prompt"
}

// UIActionInvoke is sent when the user activates a UI component's action. The daemon maps it to the
// NEXT USER TURN (kind=prompt/answer) or resolves an approval (kind=permission) — a component can
// only ever start a bounded user turn, never execute a tool or a destructive op directly.
type UIActionInvoke struct {
	SessionID   string          `json:"session_id"`
	MessageID   string          `json:"message_id,omitempty"`
	ComponentID string          `json:"component_id"`
	ActionID    string          `json:"action_id"`
	Kind        string          `json:"kind"`
	Prompt      string          `json:"prompt,omitempty"` // resolved templated text for kind=="prompt"
	Values      json.RawMessage `json:"values,omitempty"` // selected option ids / form values
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
	Files           []string `json:"files,omitempty"`    // allowlist the child should stay within (advisory)
	Provider        string   `json:"provider,omitempty"` // defaults to the parent's provider
	// Model picks the model WITHIN the chosen provider, so a subtask can be routed to a cheaper or
	// stronger model than the parent is using. Provider alone was not enough: "delegate this to
	// Claude" and "delegate this to Claude but on a small model" are different asks, and only the
	// first was expressible.
	Model         string `json:"model,omitempty"`
	ModelProvider string `json:"model_provider,omitempty"`
	Autonomous    bool   `json:"autonomous,omitempty"` // enroll the child in heartbeat supervision
	// Worktree gives the child its own git worktree and branch instead of sharing the parent's
	// working directory.
	//
	// Sharing was safe only while delegation was strictly sequential. Two children in one directory
	// edit the same files underneath each other, and the damage is silent — each agent sees a tree
	// the other is mutating and neither is told. Now that the sheet can dispatch to several agents,
	// this is what makes concurrency truthful rather than merely permitted.
	Worktree bool `json:"worktree,omitempty"`
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
	Name       string            `json:"name"`
	Kind       string            `json:"kind"`
	Available  bool              `json:"available"`
	Editable   bool              `json:"editable"`
	Hidden     bool              `json:"hidden"` // user hid it from the session pickers (still runnable)
	Command    string            `json:"command,omitempty"`
	Args       []string          `json:"args,omitempty"`
	ResumeArgs []string          `json:"resume_args,omitempty"`
	Models     []string          `json:"models,omitempty"` // configured model names (custom CLI agents)
	Env        map[string]string `json:"env,omitempty"`    // env overrides (e.g. config-file pointers)
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

// ApprovalRuleInfo is one persisted approval rule as shown in the rules UI. Description is rendered
// daemon-side so every client words a rule identically. Index is the rule's position in the ordered
// list and is how a delete names it — stable for the lifetime of one list response.
type ApprovalRuleInfo struct {
	Index       int    `json:"index"`
	Action      string `json:"action"` // allow | deny
	Provider    string `json:"provider,omitempty"`
	Tool        string `json:"tool,omitempty"`
	Pattern     string `json:"pattern,omitempty"`
	PathPrefix  string `json:"path_prefix,omitempty"`
	ProjectID   string `json:"project_id,omitempty"`
	ProjectName string `json:"project_name,omitempty"` // resolved for display
	Description string `json:"description"`
}

// ApprovalRulesList is the full ordered rule set (approval.rules.list / approval.rules.changed).
type ApprovalRulesList struct {
	Rules []ApprovalRuleInfo `json:"rules"`
}

// ApprovalRuleDelete removes one rule. Index must come from the most recent list.
type ApprovalRuleDelete struct {
	Index int `json:"index"`
}

// Session modes. A mode is a PRESET over the approval rule engine, enforced daemon-side so it
// behaves the same on every harness — even ones with no native concept of a permission mode (a gap
// competitors have: Zed's tool permissions famously don't apply to external agents at all). Where a
// harness DOES have a native mode, the daemon also forwards it as a hint via agent.ModeSetter.
const (
	// ModeCode is normal operation: the persisted approval rules decide, anything else asks.
	ModeCode = "code"
	// ModeAsk is read-only. Mutating tools (edit/write/shell/patch) are auto-denied; reading,
	// searching and thinking are unaffected. For "explain this codebase" without any risk.
	ModeAsk = "ask"
	// ModeArchitect is plan-first: same denials as ask, plus the harness's native plan mode where it
	// has one, so the agent proposes a plan instead of editing.
	ModeArchitect = "architect"
	// ModeYolo auto-approves everything: no prompt for any tool, including shell and file writes.
	//
	// This is a real removal of the safety net, not a convenience setting, and it is modelled as a
	// mode precisely so it lives in the same place as the others — one vocabulary, one enforcement
	// point, one thing to display. On harnesses with a native equivalent (claude-code's
	// bypassPermissions) the daemon ALSO forwards it, which has a consequence worth stating plainly:
	// that harness then stops consulting the daemon's approval callback at all, so the approval rule
	// engine is out of the loop rather than merely quiet. A client must therefore show this mode as
	// continuously active, not confirm it once and forget.
	ModeYolo = "yolo"
)

// Modes is the canonical mode list, in the order a picker should show them — safest first, so the
// unsafe one is never the neighbouring tap of the safe one.
//
// Defined once here rather than per provider: modes are enforced daemon-side, so every harness
// supports all of them and any per-provider list would be the same list copied four times, with the
// usual outcome that they stop being the same.
func Modes() []SessionMode {
	return []SessionMode{
		{ID: ModeAsk, Label: "Read-only", Description: "Reads, searches and explains. Cannot edit, write or run commands."},
		{ID: ModeArchitect, Label: "Plan", Description: "Investigates and proposes a plan instead of editing."},
		{ID: ModeCode, Label: "Normal", Description: "Your approval rules decide; anything else asks first."},
		{ID: ModeYolo, Label: "YOLO", Description: "Approves everything automatically, including shell commands and file writes.", Unsafe: true},
	}
}

// SessionDefaults is the global starting configuration for new sessions.
//
// AllowYoloDefault is separate from Mode on purpose. Defaulting every future session to yolo turns
// approvals off for sessions nobody is present for — loops, fan-outs, scheduled work — so it takes a
// deliberate second acknowledgement rather than one tap, and the daemon re-checks the pair every
// time it starts a session rather than trusting that the file was written by this code.
type SessionDefaults struct {
	Mode             string `json:"mode,omitempty"`
	AllowYoloDefault bool   `json:"allow_yolo_default,omitempty"`
	// Modes is the catalog, so a settings screen renders the same list and copy as the session
	// picker without hardcoding either.
	Modes []SessionMode `json:"modes,omitempty"`
}

// SessionModeSet switches a live session's mode (session.mode.set).
type SessionModeSet struct {
	SessionID string `json:"session_id"`
	Mode      string `json:"mode"`
}

// FanoutVariantResult is one agent's outcome in a fan-out group. This is the aggregation half that
// the market is missing: every ADE can fan a prompt across N agents, but the human is then left to
// open N sessions and diff them by hand. Summary/Title come from the agent's OWN handoff record
// (what it says it did), so no extra model call is needed to produce them.
type FanoutVariantResult struct {
	SessionID    string `json:"session_id"`
	Variant      int    `json:"variant"`
	Model        string `json:"model,omitempty"`
	Status       string `json:"status"`            // idle | error | ...
	Title        string `json:"title,omitempty"`   // the agent's own summary title
	Summary      string `json:"summary,omitempty"` // the agent's own handoff summary
	FilesChanged int    `json:"files_changed"`     // vs the worktree's base commit
	Insertions   int    `json:"insertions"`
	Deletions    int    `json:"deletions"`
	DurationSec  int    `json:"duration_sec,omitempty"`
	Branch       string `json:"branch,omitempty"`
	Failed       bool   `json:"failed,omitempty"` // ended in error rather than completing
	// IsSynthesis marks the variant that READ the others and wrote a combined implementation, rather
	// than attempting the task independently. It is the single most decision-relevant fact about a
	// row — a diff written with knowledge of its competitors is not comparable to one written blind
	// — so the comparison must be able to say so rather than showing it as just another attempt.
	IsSynthesis bool `json:"is_synthesis,omitempty"`
	// SourceVariants are the 1-based variant numbers whose diffs it actually read. A synthesis built
	// from 2 of 6 attempts is a different object from one built from all of them, and the user
	// cannot tell them apart without this.
	SourceVariants []int `json:"source_variants,omitempty"`
}

// FanoutSummary is the comparison payload broadcast when every variant in a group has finished.
type FanoutSummary struct {
	Group   string                `json:"group"`
	Prompt  string                `json:"prompt,omitempty"`
	Results []FanoutVariantResult `json:"results"`
}

// MCPTool is one tool an MCP server advertises.
type MCPTool struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

// MCPServerInfo is one registered MCP server plus what we last learned by talking to it.
type MCPServerInfo struct {
	Name      string            `json:"name"`
	Transport string            `json:"transport"` // stdio | http
	Command   string            `json:"command,omitempty"`
	Args      []string          `json:"args,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	URL       string            `json:"url,omitempty"`
	Enabled   bool              `json:"enabled"`
	ProjectID string            `json:"project_id,omitempty"`
	// Live status from the last probe.
	OK              bool      `json:"ok"`
	Error           string    `json:"error,omitempty"`
	ProtocolVersion string    `json:"protocol_version,omitempty"`
	ServerVersion   string    `json:"server_version,omitempty"`
	Tools           []MCPTool `json:"tools,omitempty"`
	CheckedAt       int64     `json:"checked_at,omitempty"`
}

// MCPList is the full registry (mcp.list / mcp.changed).
type MCPList struct {
	Servers []MCPServerInfo `json:"servers"`
}

// MCPUpsert adds or replaces a server definition.
type MCPUpsert struct {
	Name      string            `json:"name"`
	Transport string            `json:"transport,omitempty"`
	Command   string            `json:"command,omitempty"`
	Args      []string          `json:"args,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
	URL       string            `json:"url,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
	Cwd       string            `json:"cwd,omitempty"`
	ProjectID string            `json:"project_id,omitempty"`
}

// MCPRef names one server.
type MCPRef struct {
	Name string `json:"name"`
}

// MCPEnable toggles one server.
type MCPEnable struct {
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
}

// ClientIdentify tells the daemon which device/person this connection is, so prompts, interrupts and
// approvals can be attributed. Purely descriptive today; it becomes the principal that per-user
// permissions hang off.
type ClientIdentify struct {
	Name string `json:"name"`
}

// Participant is one connected client and what it may do.
type Participant struct {
	Name string `json:"name"`
	Role string `json:"role"` // owner | steerer | observer
}

// ParticipantList is who is connected (participants / broadcast on change). Enabled reports whether
// role enforcement is on at all — with it off everyone is the owner, which is the solo default.
type ParticipantList struct {
	Enabled      bool          `json:"enabled"`
	Participants []Participant `json:"participants"`
}

// RoleGrant changes one participant's role. Only the owner may send it.
type RoleGrant struct {
	Name string `json:"name"`
	Role string `json:"role"`
}

// RolesEnable turns multi-user enforcement on or off.
type RolesEnable struct {
	Enabled bool `json:"enabled"`
}

// Invite is one outstanding share credential. The SECRET is returned only once, at creation — it is
// never listed afterwards, because a credential you can re-read is one that leaks from a screen.
type Invite struct {
	ID         string `json:"id"`
	Label      string `json:"label,omitempty"`
	Role       string `json:"role"`
	ExpiresAt  int64  `json:"expires_at"`
	Redeemed   int    `json:"redeemed"`
	MaxDevices int    `json:"max_devices,omitempty"`
}

// InviteList is the set of live invites.
type InviteList struct {
	Invites []Invite `json:"invites"`
}

// InviteCreate mints one. Role may be steerer or observer; an invite can never mint an owner.
type InviteCreate struct {
	Label    string `json:"label,omitempty"`
	Role     string `json:"role,omitempty"`
	TTLHours int    `json:"ttl_hours,omitempty"`
	// MaxDevices caps how many devices the link admits. Absent/0 means one — a share link is pasted
	// into chats, and chats forward.
	MaxDevices int `json:"max_devices,omitempty"`
}

// InviteCreated carries the one-time redeemable URL back to the creator.
type InviteCreated struct {
	Invite Invite `json:"invite"`
	URL    string `json:"url"`
}

// InviteRef names one invite.
type InviteRef struct {
	ID string `json:"id"`
}

// MCPDirectoryEntry is one server published in the public MCP registry, reduced to what an install
// form needs. Command/Args are a SUGGESTION the user confirms — nothing installs itself.
type MCPDirectoryEntry struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Version     string   `json:"version,omitempty"`
	Command     string   `json:"command,omitempty"`
	Args        []string `json:"args,omitempty"`
	URL         string   `json:"url,omitempty"`
	Transport   string   `json:"transport"`
	EnvKeys     []string `json:"env_keys,omitempty"`
	Unsupported string   `json:"unsupported,omitempty"`
}

// MCPBrowse searches the public registry.
type MCPBrowse struct {
	Query string `json:"query,omitempty"`
}

// MCPDirectory is a page of registry results.
type MCPDirectory struct {
	Entries []MCPDirectoryEntry `json:"entries"`
}

// MCPFound is one server discovered in a harness's OWN configuration, offered for import. Never
// adopted automatically: a server definition carries a command that runs with the user's
// credentials, so it gets confirmed rather than silently absorbed.
type MCPFound struct {
	Name      string   `json:"name"`
	Transport string   `json:"transport"`
	Command   string   `json:"command,omitempty"`
	Args      []string `json:"args,omitempty"`
	URL       string   `json:"url,omitempty"`
	EnvKeys   []string `json:"env_keys,omitempty"`
	Source    string   `json:"source"`
	Path      string   `json:"path,omitempty"`
}

// MCPDiscovered is what a scan of the harnesses turned up, plus whether exclusive mode is on.
type MCPDiscovered struct {
	Found     []MCPFound `json:"found"`
	Exclusive bool       `json:"exclusive"`
}

// MCPImport adopts the named discovered servers. Names must come from a recent discover.
type MCPImport struct {
	Names []string `json:"names"`
}

// MCPExclusiveSet turns exclusive mode on or off.
type MCPExclusiveSet struct {
	Enabled bool `json:"enabled"`
}

// TranscriptPage asks for the events immediately before the ones already held. Loaded is how many
// the client currently has, so the daemon needs no per-client cursor state.
type TranscriptPage struct {
	SessionID string `json:"session_id"`
	Loaded    int    `json:"loaded"`
	Limit     int    `json:"limit,omitempty"`
}

// TranscriptPageBegin marks the start of a page's frames.
type TranscriptPageBegin struct {
	SessionID string `json:"session_id"`
}

// TranscriptPageEnd closes a page and reports whether older history remains.
type TranscriptPageEnd struct {
	SessionID string `json:"session_id"`
	Count     int    `json:"count"`
	HasMore   bool   `json:"has_more"`
}

// UsageSlice is usage aggregated under one key (a provider, a model, a session).
type UsageSlice struct {
	Key          string  `json:"key"`
	Label        string  `json:"label,omitempty"`
	InputTokens  int     `json:"input_tokens"`
	OutputTokens int     `json:"output_tokens"`
	CostUSD      float64 `json:"cost_usd"`
}

// UsageWindow is the rolling limit window a subscription resets on.
//
// Claude's subscription plans meter on a rolling window that starts with your FIRST activity rather
// than on the clock, so the reset time is derived from when usage actually began, not from midnight.
type UsageWindow struct {
	StartedAt int64   `json:"started_at"`
	ResetsAt  int64   `json:"resets_at"`
	Hours     int     `json:"hours"`
	CostUSD   float64 `json:"cost_usd"`
	Tokens    int     `json:"tokens"`
	Active    bool    `json:"active"`
}

// UsageReport answers usage.report.
type UsageReport struct {
	Today     UsageSlice   `json:"today"`
	Week      UsageSlice   `json:"week"`
	Month     UsageSlice   `json:"month"`
	Window    UsageWindow  `json:"window"`
	Providers []UsageSlice `json:"providers"`
	Models    []UsageSlice `json:"models"`
	Sessions  []UsageSlice `json:"sessions"`
	// Subscription reports a NOTIONAL API-equivalent cost rather than money billed, because a
	// subscription-backed agent isn't charged per token. Clients must say so rather than implying
	// a bill.
	Subscription bool `json:"subscription"`
}

// WorktreeMerge lands a worktree's branch into the repo's default branch, for repos with no forge.
type WorktreeMerge struct {
	SessionID string `json:"session_id"`
	Message   string `json:"message,omitempty"`
}

// WorktreeStatus asks whether this worktree's work has landed — so the app can offer to clean up
// rather than leaving finished worktrees around forever.
type WorktreeStatus struct {
	SessionID string `json:"session_id"`
}

// PRChecks is a pull request's CI rollup flattened to what a review screen can act on: how many
// checks passed, failed or are still running, plus the names of the failing ones (capped). Absent
// when the PR has no checks at all — a repo without CI is not a failure.
type PRChecks struct {
	State   string   `json:"state,omitempty"` // SUCCESS | FAILURE | PENDING
	Passed  int      `json:"passed,omitempty"`
	Failed  int      `json:"failed,omitempty"`
	Pending int      `json:"pending,omitempty"`
	Failing []string `json:"failing,omitempty"`
}

// WorktreeStatusResult reports the branch's pull-request state. State is "" when there is no PR (or
// no gh), which is normal and not an error. Checks rides along because someone reviewing a worktree
// from their phone decides whether to merge off this one reply — "PR open" alone doesn't say whether
// it's safe to land.
type WorktreeStatusResult struct {
	SessionID string    `json:"session_id"`
	Branch    string    `json:"branch"`
	State     string    `json:"state,omitempty"` // OPEN | MERGED | CLOSED
	URL       string    `json:"url,omitempty"`
	HasRemote bool      `json:"has_remote"`
	Checks    *PRChecks `json:"checks,omitempty"`
}

// DeviceInfo is one client enrolled to reach this daemon, identified by the static public key it
// presents in the handshake.
type DeviceInfo struct {
	Pub       string `json:"pub"`
	Label     string `json:"label,omitempty"`
	FirstSeen int64  `json:"first_seen"`
	LastSeen  int64  `json:"last_seen"`
	This      bool   `json:"this,omitempty"`  // the device asking — so a UI can avoid revoking itself blind
	Guest     bool   `json:"guest,omitempty"` // came in through an invite; holds no credential of its own
}

// DeviceCredential is the per-device credential the daemon mints at enrollment and the client keeps
// (Keychain, ThisDeviceOnly). It is delivered as a normal protocol frame on the already-encrypted
// channel rather than in the handshake, so issuing it costs no wire-format change.
type DeviceCredential struct {
	Pub        string `json:"pub"`
	Credential string `json:"credential"`
	IssuedAt   int64  `json:"issued_at"`
}

// PairCode is a freshly minted single-use pairing code and the URL that carries it. ExpiresAt is
// shown to the owner, because a code with an invisible lifetime is one they'll screenshot.
type PairCode struct {
	Code      string `json:"code"`
	URL       string `json:"url,omitempty"`
	ExpiresAt int64  `json:"expires_at"`
}

// PairStatus reports whether the pre-upgrade permanent secret is still accepted, so the owner can
// see the migration finish (and end it early).
type PairStatus struct {
	LegacyLive     bool  `json:"legacy_live"`
	LegacyRetireAt int64 `json:"legacy_retire_at,omitempty"`
}

type DeviceList struct {
	Devices []DeviceInfo `json:"devices"`
}

type DeviceRef struct {
	Pub   string `json:"pub"`
	Label string `json:"label,omitempty"`
}
