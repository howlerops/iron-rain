// Package hub is the daemon core: it registers providers, and for each client
// connection routes protocol requests to sessions and forwards session events back
// over the encrypted transport.
package hub

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/howlerops/oculus/daemon/agent"
	"github.com/howlerops/oculus/daemon/fsaccess"
	"github.com/howlerops/oculus/daemon/issues"
	"github.com/howlerops/oculus/daemon/lsp"
	"github.com/howlerops/oculus/daemon/project"
	"github.com/howlerops/oculus/daemon/protocol"
	"github.com/howlerops/oculus/daemon/push"
	"github.com/howlerops/oculus/daemon/slack"
	"github.com/howlerops/oculus/daemon/store"
	"github.com/howlerops/oculus/daemon/transport"
	"github.com/howlerops/oculus/daemon/worktree"
)

// DiscoverFunc autodetects active agent artifacts on the host (see daemon/discovery).
type DiscoverFunc func(context.Context) ([]protocol.Discovered, error)

// Hub owns providers and live sessions.
type Hub struct {
	mu        sync.Mutex
	providers map[string]agent.Provider
	sessions  map[string]*managedSession // sessionID -> hub-owned shared session
	approvals map[string]*managedSession // approvalID -> owning session
	discover  DiscoverFunc

	notifier      push.Notifier // optional: push actionable approvals to a device
	slack         *slack.Client // optional: mirror agent events to a Slack channel
	pushTokens    []string      // registered device tokens
	attach        AttacherFactory
	clients       map[*transport.Conn]*hubClient // all connected clients (for global broadcasts)
	projects      *project.Registry              // optional: registered folders sessions spawn in
	autoProjects  bool                           // auto-register projects from active agents' cwds
	issues        *issues.Manager                // optional: connected trackers (Linear/Jira)
	oauthAddr     string                         // loopback host:port for tracker OAuth callbacks (per-provider path)
	worktreeBase  string                         // base dir for worktrees ("" = worktree.DefaultBase)
	reservedPorts map[int]bool                   // ports handed to worktree setup hooks (collision-free)
	db            *store.Store                   // optional: durable local state (session names + records)
	lsp           *lsp.Manager                   // language servers for the built-in editor (diagnostics/types)
	runningTests  map[string]bool                // session ids with a test/build run in progress

	pushTimeout     time.Duration // per-Notify deadline for the approval push fan-out
	pushConcurrency int           // cap on concurrent in-flight pushes
}

// reservePort allocates a free port in [lo,hi] not already handed to another worktree.
func (h *Hub) reservePort(lo, hi int) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.reservedPorts == nil {
		h.reservedPorts = map[int]bool{}
	}
	p, _ := worktree.AllocPort(lo, hi, h.reservedPorts) // marks p reserved in the map
	return p
}

// releasePort returns a port previously handed out by reservePort to the free pool, so
// worktree ports are not permanently burned when a session/worktree ends or fails.
func (h *Hub) releasePort(p int) {
	if p == 0 {
		return
	}
	h.mu.Lock()
	delete(h.reservedPorts, p)
	h.mu.Unlock()
}

// SetWorktreeBase overrides where session worktrees are created (default: ~/.oculus/worktrees).
func (h *Hub) SetWorktreeBase(dir string) {
	h.mu.Lock()
	h.worktreeBase = dir
	h.mu.Unlock()
}

// SetStore attaches the durable local database (session names + records). Set once at
// startup before serving; nil disables persistence (state then lives only for the
// daemon's lifetime).
func (h *Hub) SetStore(s *store.Store) {
	h.mu.Lock()
	h.db = s
	h.mu.Unlock()
}

// startSession creates a managed session per req — resolving the project cwd, optionally
// creating + bootstrapping a git worktree, and merging extra metadata (e.g. an issue
// link). It does NOT subscribe a client or start the run loop; the caller does that.
func (h *Hub) startSession(ctx context.Context, req protocol.SessionCreate, meta sessionMeta) (*managedSession, error) {
	h.mu.Lock()
	p := h.providers[req.Provider]
	h.mu.Unlock()
	if p == nil {
		return nil, fmt.Errorf("unknown provider: %s", req.Provider)
	}
	cwd := req.Cwd
	// isolate is the user's worktree/isolation intent, captured before the multi-repo branch
	// clears req.Worktree (a shared multi-repo session can't use a single worktree).
	isolate := req.Worktree
	// Multi-root workspace: two paths. Isolated (isolate=true) → one git worktree per repo under a
	// shared layout dir, so a task spans repos while each change stays on its own branch for a
	// coordinated multi-PR finish. Shared (isolate=false) → run in the common ancestor in place.
	// One selection falls through to the normal single-project path.
	multiRepo := false
	if len(req.ProjectIDs) == 1 && req.ProjectID == "" {
		req.ProjectID = req.ProjectIDs[0]
	} else if len(req.ProjectIDs) > 1 {
		reg := h.projectRegistry()
		if reg == nil {
			return nil, fmt.Errorf("projects not enabled")
		}
		var paths []string
		for _, id := range req.ProjectIDs {
			if proj, ok := reg.Get(id); ok {
				paths = append(paths, proj.Path)
			}
		}
		if len(paths) < 2 {
			return nil, fmt.Errorf("multi-repo needs at least 2 valid projects")
		}
		req.Worktree = false // handled here, not by the single-repo worktree block below
		req.ProjectID = ""
		multiRepo = true
		if isolate {
			name := req.WorkspaceName
			if name == "" {
				name = "workspace-" + randToken()
			}
			layout, members, err := worktree.CreateWorkspace("", name, paths)
			if err != nil {
				return nil, fmt.Errorf("workspace setup failed: %w", err)
			}
			cwd = layout
			meta.members = members
			meta.workspaceName = name
		} else {
			anc := commonAncestor(paths)
			if anc == "" || anc == string(filepath.Separator) {
				return nil, fmt.Errorf("selected repos have no shared parent directory")
			}
			cwd = anc
			meta.workspaceName = fmt.Sprintf("%d repos", len(paths))
		}
	}
	if req.ProjectID != "" {
		reg := h.projectRegistry()
		if reg == nil {
			return nil, fmt.Errorf("projects not enabled")
		}
		proj, ok := reg.Get(req.ProjectID)
		if !ok {
			return nil, fmt.Errorf("unknown project: %s", req.ProjectID)
		}
		cwd = proj.Path
	}
	if req.ProjectID == "" && !multiRepo {
		h.autoRegisterCwd(cwd)
	}
	meta.projectID = req.ProjectID
	meta.cwd = cwd
	if req.Worktree {
		name := req.WorkspaceName
		if name == "" {
			name = "session-" + randToken()
		}
		h.mu.Lock()
		base := h.worktreeBase
		h.mu.Unlock()
		repoRoot, _ := worktree.RepoRoot(cwd)
		wt, err := worktree.Create(base, cwd, name)
		if err != nil {
			return nil, err
		}
		cwd = wt.Path
		meta.cwd = wt.Path
		meta.branch = wt.Branch
		meta.workspaceName = name
		meta.worktreePath = wt.Path
		meta.repoRoot = repoRoot
		meta.baseCommit, _ = worktree.HeadCommit(repoRoot)
		if repoRoot != "" {
			if cfg, ok, _ := worktree.LoadConfig(repoRoot); ok {
				port := 0
				if len(cfg.PortRange) >= 2 {
					port = h.reservePort(cfg.PortRange[0], cfg.PortRange[1])
				}
				res, berr := worktree.Bootstrap(ctx, repoRoot, wt.Path, cfg, port)
				if berr != nil {
					_ = worktree.Remove(repoRoot, wt.Path, true)
					h.releasePort(port) // don't leak the reserved port on a failed bootstrap
					return nil, fmt.Errorf("worktree setup failed: %w", berr)
				}
				meta.port = res.Port
			}
		}
	}
	createPrompt := req.Prompt
	if len(req.Images) > 0 {
		createPrompt = ""
	}
	var sess agent.Session
	var err error
	if req.Plan {
		if pc, ok := p.(agent.PlanCreator); ok {
			sess, err = pc.CreatePlan(ctx, cwd, createPrompt)
		} else {
			sess, err = p.Create(ctx, cwd, createPrompt) // provider has no plan mode; run normally
		}
	} else {
		sess, err = p.Create(ctx, cwd, createPrompt)
	}
	if err != nil {
		return nil, err
	}
	if len(req.Images) > 0 {
		_ = promptSession(ctx, sess, req.Prompt, req.Images)
	}
	return h.addSession(sess, meta), nil
}

// handleIssueLaunch launches an agent on a ticket: a worktree session on the issue's
// branch, prompted with the issue, linked back to the ticket, and (write-back) moves the
// ticket to "in progress" with a comment.
func (h *Hub) handleIssueLaunch(ctx context.Context, conn *transport.Conn, env protocol.Envelope) {
	var req protocol.IssueLaunch
	if err := env.Unmarshal(&req); err != nil {
		h.sendErr(conn, env.ID, "bad issue.launch")
		return
	}
	m := h.issuesMgr()
	if m == nil {
		h.sendErr(conn, env.ID, "integrations not enabled")
		return
	}
	var issue *issues.Issue
	for _, i := range m.Issues() {
		if i.ID == req.IssueID {
			cp := i
			issue = &cp
			break
		}
	}
	if issue == nil {
		h.sendErr(conn, env.ID, "issue not found")
		return
	}
	if req.ProjectID == "" {
		h.sendErr(conn, env.ID, "choose a project for this ticket")
		return
	}
	agentProvider := req.AgentProvider
	if agentProvider == "" {
		agentProvider = "opencode"
	}
	branch := issue.BranchName
	if branch == "" {
		branch = issue.Key
	}
	create := protocol.SessionCreate{
		Provider:      agentProvider,
		ProjectID:     req.ProjectID,
		Prompt:        fmt.Sprintf("Work on %s — %s\n\n%s\n\n%s", issue.Key, issue.Title, issue.Body, issue.URL),
		Worktree:      true,
		WorkspaceName: branch,
	}
	ms, err := h.startSession(ctx, create, sessionMeta{issueID: issue.ID, issueKey: issue.Key, issueProvider: issue.Provider})
	if err != nil {
		h.sendErr(conn, env.ID, err.Error())
		return
	}
	go h.writeBackStarted(issue.Provider, issue.ID, issue.TeamID) // move ticket → in progress
	h.sendOK(conn, env.ID, ms.info())
	ms.subscribe(conn)
	go ms.run()
}

// writeBackStarted moves a ticket to its "in progress" state + comments (best-effort).
func (h *Hub) writeBackStarted(provider, issueID, teamID string) {
	m := h.issuesMgr()
	if m == nil {
		return
	}
	p := m.Provider(provider)
	if p == nil {
		return
	}
	ctx := context.Background()
	states, err := p.WorkflowStates(ctx, teamID)
	if err != nil {
		return
	}
	for _, s := range states {
		if s.Category == "in_progress" {
			_ = p.Transition(ctx, issueID, s.ID)
			break
		}
	}
	_ = p.Comment(ctx, issueID, "🤖 Iron Rain started an agent on this issue.")
}

// writeBackPR closes the issue→worktree→PR loop: when an issue-linked session opens a PR, comment
// the PR URL back on the ticket (and move it to a review state if the tracker has one). Best-effort.
func (h *Hub) writeBackPR(provider, issueID, prURL string) {
	if provider == "" || issueID == "" || prURL == "" {
		return
	}
	m := h.issuesMgr()
	if m == nil {
		return
	}
	p := m.Provider(provider)
	if p == nil {
		return
	}
	_ = p.Comment(context.Background(), issueID, "🤖 Iron Rain opened a PR for this issue: "+prURL)
}

// promptSession sends text (+ optional images) to a session, using the multimodal path
// when images are present and the session supports it, else falling back to text.
func promptSession(ctx context.Context, sess agent.Session, text string, images []protocol.ImageAttachment) error {
	if len(images) > 0 {
		if ip, ok := sess.(agent.ImagePrompter); ok {
			return ip.PromptImages(ctx, text, images)
		}
	}
	return sess.Prompt(ctx, text)
}

func randToken() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

// SetProjects attaches a project registry so clients can list/add/remove projects and
// spawn sessions scoped to one.
func (h *Hub) SetProjects(r *project.Registry) {
	h.mu.Lock()
	h.projects = r
	h.mu.Unlock()
}

// SetAutoProjects toggles auto-registering projects from active agents' working dirs.
func (h *Hub) SetAutoProjects(on bool) {
	h.mu.Lock()
	h.autoProjects = on
	h.mu.Unlock()
}

// autoRegisterProjects registers a project for every discovered agent's cwd (git root).
func (h *Hub) autoRegisterProjects(items []protocol.Discovered) {
	for _, it := range items {
		h.autoRegisterCwd(it.Cwd)
	}
}

// autoRegisterCwd resolves cwd to its git root and adds it to the registry (deduped),
// when auto-projects is enabled and a registry is attached.
func (h *Hub) autoRegisterCwd(cwd string) {
	if cwd == "" {
		return
	}
	h.mu.Lock()
	on, reg := h.autoProjects, h.projects
	h.mu.Unlock()
	if !on || reg == nil {
		return
	}
	root := cwd
	if r, err := worktree.RepoRoot(cwd); err == nil {
		root = r
	}
	_, _ = reg.AddAuto(root)
}

func (h *Hub) projectRegistry() *project.Registry {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.projects
}

// SetIssues attaches a tracker manager (Linear/Jira) and broadcasts issue updates.
func (h *Hub) SetIssues(m *issues.Manager) {
	h.mu.Lock()
	h.issues = m
	h.mu.Unlock()
}

// BroadcastIssues pushes the current assigned issues to every device (the Manager's poll
// callback). Exported so main.go can wire it as the Manager's onUpdate.
func (h *Hub) BroadcastIssues(in []issues.Issue) {
	h.broadcast(protocol.TypeIssueList, protocol.IssueList{Issues: toProtoIssues(in)})
	if m := h.issuesMgr(); m != nil {
		h.broadcast(protocol.TypeIntegrationStatus, protocol.IntegrationStatus{Connected: m.Connected()})
	}
}

// SetOAuthAddr sets the loopback host:port used to build per-provider tracker OAuth callback URLs.
func (h *Hub) SetOAuthAddr(addrPort string) {
	h.mu.Lock()
	h.oauthAddr = addrPort
	h.mu.Unlock()
}

func (h *Hub) issuesMgr() *issues.Manager {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.issues
}

func toProtoIssue(i issues.Issue) protocol.Issue {
	return protocol.Issue{
		ID: i.ID, Key: i.Key, Title: i.Title, Body: i.Body, Status: i.Status,
		Category: i.Category, Assignee: i.Assignee, URL: i.URL, Provider: i.Provider,
		BranchName: i.BranchName, TeamID: i.TeamID, Priority: i.Priority, UpdatedAt: i.UpdatedAt,
		CycleID: i.CycleID, CycleName: i.CycleName, CycleNumber: i.CycleNumber,
	}
}

func toProtoIssues(in []issues.Issue) []protocol.Issue {
	out := make([]protocol.Issue, len(in))
	for i, v := range in {
		out[i] = toProtoIssue(v)
	}
	return out
}

func toProtoComments(in []issues.Comment) []protocol.IssueComment {
	out := make([]protocol.IssueComment, len(in))
	for i, c := range in {
		out[i] = protocol.IssueComment{ID: c.ID, Author: c.Author, Body: c.Body, CreatedAt: c.CreatedAt}
	}
	return out
}

func toProtoProject(p project.Project) protocol.Project {
	return protocol.Project{ID: p.ID, Name: p.Name, Path: p.Path, IsGitRepo: p.IsGitRepo, DefaultBranch: p.DefaultBranch, Source: p.Source}
}

func toProtoProjects(ps []project.Project) []protocol.Project {
	out := make([]protocol.Project, len(ps))
	for i, p := range ps {
		out[i] = toProtoProject(p)
	}
	return out
}

// AttacherFactory returns an Attacher for a discovered session (by provider + URL),
// or nil if that provider/URL can't be attached.
type AttacherFactory func(provider, url string) agent.Attacher

// New returns an empty Hub.
func New() *Hub {
	h := &Hub{
		providers:       map[string]agent.Provider{},
		sessions:        map[string]*managedSession{},
		approvals:       map[string]*managedSession{},
		clients:         map[*transport.Conn]*hubClient{},
		autoProjects:    true, // on by default; disable with --auto-projects=false
		pushTimeout:     defaultPushTimeout,
		pushConcurrency: defaultPushConcurrency,
	}
	// Language servers push diagnostics asynchronously; fan them out to every client.
	h.lsp = lsp.NewManager(func(path string, diags []lsp.Diagnostic) {
		h.broadcast(protocol.TypeLSPDiagnostics, protocol.LSPDiagnostics{Path: path, Diagnostics: toProtoDiags(diags)})
	})
	return h
}

// Shutdown stops background subsystems (language servers). Call on daemon exit.
func (h *Hub) Shutdown() {
	if h.lsp != nil {
		h.lsp.Shutdown()
	}
}

func toProtoDiags(diags []lsp.Diagnostic) []protocol.LSPDiagnostic {
	out := make([]protocol.LSPDiagnostic, len(diags))
	for i, d := range diags {
		out[i] = protocol.LSPDiagnostic{
			StartLine: d.StartLine, StartChar: d.StartChar,
			EndLine: d.EndLine, EndChar: d.EndChar,
			Severity: d.Severity, Message: d.Message, Source: d.Source,
		}
	}
	return out
}

// addSession creates and stores a managed (shared) session for a provider session.
// A persisted user-set name (from a prior rename) is restored here so it survives a
// daemon restart, unless the caller already supplied an explicit label.
func (h *Hub) addSession(sess agent.Session, meta sessionMeta) *managedSession {
	if meta.label == "" && h.db != nil {
		if n, ok := h.db.Name(sess.ID()); ok {
			meta.label = n
		}
	}
	m := newManagedSession(h, sess, meta)
	h.mu.Lock()
	h.sessions[sess.ID()] = m
	h.mu.Unlock()
	h.persistSession(m) // durable record so it survives a daemon restart
	return m
}

func (h *Hub) managed(id string) *managedSession {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.sessions[id]
}

func (h *Hub) removeSession(id string) {
	h.mu.Lock()
	delete(h.sessions, id)
	db := h.db
	h.mu.Unlock()
	// The session's event stream ended for good (explicit stop or the provider closed
	// it); drop its durable record so it isn't re-attached on the next start.
	if db != nil {
		_ = db.DeleteSession(id)
		_ = db.DeleteHandoff(id)
	}
}

// spawnChild creates a scoped sub-agent for one subtask of a parent session. The child is seeded
// with a compact prompt (subtask + a pointer to the parent's handoff/decision doc + an optional
// file allowlist) instead of the parent transcript, so it starts with minimal context. It runs in
// the parent's working directory and is linked back to the parent for grouping.
func (h *Hub) spawnChild(ctx context.Context, req protocol.SessionChild) (*managedSession, error) {
	parent := h.managed(req.ParentSessionID)
	if parent == nil {
		return nil, fmt.Errorf("unknown parent session: %s", req.ParentSessionID)
	}
	if strings.TrimSpace(req.Subtask) == "" {
		return nil, fmt.Errorf("child session needs a subtask")
	}
	parent.mu.Lock()
	cwd := parent.meta.cwd
	projectID := parent.meta.projectID
	parent.mu.Unlock()

	provider := req.Provider
	if provider == "" {
		provider = parent.sess.Provider()
	}
	h.mu.Lock()
	p := h.providers[provider]
	db := h.db
	h.mu.Unlock()
	if p == nil {
		return nil, fmt.Errorf("unknown provider: %s", provider)
	}

	var handoff store.HandoffRecord
	if db != nil {
		handoff, _ = db.Handoff(req.ParentSessionID)
	}
	prompt := buildChildPrompt(req, handoffPath(cwd, req.ParentSessionID), handoff)

	sess, err := p.Create(ctx, cwd, prompt)
	if err != nil {
		return nil, err
	}
	meta := sessionMeta{
		projectID: projectID, cwd: cwd, workspaceName: "subtask",
		parentID: req.ParentSessionID, subtask: req.Subtask,
	}
	m := h.addSession(sess, meta)
	if req.Autonomous {
		m.mu.Lock()
		m.autonomous = true
		m.mu.Unlock()
	}
	return m, nil
}

// buildChildPrompt composes the scoped seed prompt for a delegated sub-agent: the subtask, a
// pointer to the shared handoff (decision/state doc) to read for context, and the file allowlist.
// It deliberately omits the parent transcript to keep the child's context small.
func buildChildPrompt(req protocol.SessionChild, handoff string, rec store.HandoffRecord) string {
	b := &strings.Builder{}
	b.WriteString("You are a sub-agent handling ONE subtask of a larger effort. Do that subtask only — don't re-plan the whole project.\n\n")
	b.WriteString("## Subtask\n")
	b.WriteString(strings.TrimSpace(req.Subtask))
	b.WriteString("\n\n## Shared context (read first; don't duplicate its work)\n")
	if handoff != "" {
		b.WriteString("The current progress + decisions are in " + handoff + " — read it before you start.\n")
	}
	if rec.Title != "" {
		b.WriteString("Objective: " + rec.Title + "\n")
	}
	if rec.Summary != "" {
		b.WriteString("Recent state: " + rec.Summary + "\n")
	}
	if len(req.Files) > 0 {
		b.WriteString("\n## Files you may change\n")
		b.WriteString(strings.Join(req.Files, "\n"))
		b.WriteString("\nStay within these unless the subtask clearly requires more.\n")
	}
	b.WriteString("\nWhen you finish, briefly note what you changed (so the parent can integrate it) and stop.")
	return b.String()
}

// finishWorkspaceMember commits, pushes, and opens a PR for one workspace member. It never
// aborts the whole finish: a clean repo or one without an origin remote is reported as skipped,
// and a per-member failure is captured in Error so the other members still proceed.
func (h *Hub) finishWorkspaceMember(ctx context.Context, mem worktree.Member, title, body string) protocol.WorkspaceMemberPR {
	res := protocol.WorkspaceMemberPR{Name: mem.Name, Branch: mem.Branch}
	changed, err := worktree.ChangedFiles(mem.Path, mem.BaseCommit)
	if err != nil {
		res.Error = err.Error()
		return res
	}
	if len(changed) == 0 {
		res.Skipped = "no changes"
		return res
	}
	if _, err := worktree.CommitAll(ctx, mem.Path, title); err != nil {
		res.Error = err.Error()
		return res
	}
	if !worktree.HasRemote(mem.Path) {
		res.Skipped = "no origin remote — commit is local"
		return res
	}
	if err := worktree.Push(ctx, mem.Path, mem.Branch); err != nil {
		res.Error = err.Error()
		return res
	}
	res.Pushed = true
	res.URL, _ = worktree.CreatePR(ctx, mem.Path, mem.Branch, title, body) // gh optional; branch is pushed regardless
	return res
}

func (h *Hub) recordApproval(approvalID string, m *managedSession) {
	h.mu.Lock()
	h.approvals[approvalID] = m
	h.mu.Unlock()
}

// Register adds a provider (keyed by Name()).
func (h *Hub) Register(p agent.Provider) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.providers[p.Name()] = p
}

// providerNames returns the registered provider names, sorted, for the app's session picker.
func (h *Hub) providerNames() []string {
	h.mu.Lock()
	names := make([]string, 0, len(h.providers))
	for name := range h.providers {
		names = append(names, name)
	}
	h.mu.Unlock()
	sort.Strings(names)
	return names
}

// SetDiscoverer installs the host-scan used to answer discover.list requests.
func (h *Hub) SetDiscoverer(f DiscoverFunc) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.discover = f
}

// SetNotifier installs a push Notifier; approval requests are then pushed to every
// registered device token (see RegisterDevice). A nil Notifier disables push.
func (h *Hub) SetNotifier(n push.Notifier) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.notifier = n
}

// SetSlack enables mirroring agent-event notifications to a Slack channel (nil disables it).
func (h *Hub) SetSlack(c *slack.Client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.slack = c
}

// SetAttacherFactory installs the factory used to attach to discovered sessions.
func (h *Hub) SetAttacherFactory(f AttacherFactory) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.attach = f
}

// RegisterDevice adds a device token to receive approval pushes.
func (h *Hub) RegisterDevice(token string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, t := range h.pushTokens {
		if t == token {
			return
		}
	}
	h.pushTokens = append(h.pushTokens, token)
}

// pushApproval delivers an approval to every registered device (best-effort, async).
func (h *Hub) pushApproval(ar protocol.ApprovalRequest) {
	h.pushNotify(push.Notification{
		Title:    "Approve " + ar.Tool,
		Body:     "Tap to review in Oculus",
		Category: "APPROVAL",
		ThreadID: ar.SessionID,
		Custom:   map[string]any{"approval_id": ar.ApprovalID, "session_id": ar.SessionID},
	})
}

// pushAgentFinished notifies that an agent turn completed while you're away.
func (h *Hub) pushAgentFinished(sessionID, label string) {
	title := "Agent finished"
	if label != "" {
		title = label + " finished"
	}
	h.pushNotify(push.Notification{
		Title: title, Body: "The agent is done — tap to review", Category: "AGENT_FINISHED",
		ThreadID: sessionID, Custom: map[string]any{"session_id": sessionID},
	})
}

// pushAgentError notifies that a session hit an error.
func (h *Hub) pushAgentError(sessionID, label, detail string) {
	body := detail
	if body == "" {
		body = "The agent hit an error — tap to review"
	}
	title := "Agent error"
	if label != "" {
		title = label + " error"
	}
	h.pushNotify(push.Notification{
		Title: title, Body: body, Category: "AGENT_ERROR",
		ThreadID: sessionID, Custom: map[string]any{"session_id": sessionID},
	})
}

// pushAgentStalled notifies that a supervised session needs attention (stalled, out of nudge
// budget, or waiting on you).
func (h *Hub) pushAgentStalled(sessionID, label, reason string) {
	title := "Agent needs you"
	if label != "" {
		title = label + " needs you"
	}
	body := reason
	if body == "" {
		body = "The agent stopped making progress — tap to review"
	}
	h.pushNotify(push.Notification{
		Title: title, Body: body, Category: "AGENT_STALLED",
		ThreadID: sessionID, Custom: map[string]any{"session_id": sessionID},
	})
}

// pushTestsFailed notifies that a test/build run failed in a session.
func (h *Hub) pushTestsFailed(sessionID, label, cmd string) {
	title := "Tests failed"
	if label != "" {
		title = label + ": tests failed"
	}
	h.pushNotify(push.Notification{
		Title: title, Body: cmd + " — tap to review", Category: "TESTS_FAILED",
		ThreadID: sessionID, Custom: map[string]any{"session_id": sessionID},
	})
}

// pushNotify fans a notification out to every registered device without blocking the caller.
func (h *Hub) pushNotify(notif push.Notification) {
	h.mu.Lock()
	n := h.notifier
	sc := h.slack
	tokens := append([]string(nil), h.pushTokens...)
	h.mu.Unlock()
	// Mirror to Slack (if configured) independently of APNs — a team may want Slack visibility
	// without any paired phones. Fire-and-forget with a bounded context.
	if sc != nil {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := sc.Post(ctx, slack.Format(notif.Title, notif.Body, notif.Category)); err != nil {
				log.Printf("hub: slack post (%s) failed: %v", notif.Category, err)
			}
		}()
	}
	if n == nil || len(tokens) == 0 {
		return
	}
	log.Printf("hub: pushing %s to %d device(s)", notif.Category, len(tokens))
	// Fan out on a dispatcher goroutine so the caller (the session event pump) never
	// blocks. Each Notify gets a bounded context so a hung APNs call can't leak a
	// goroutine forever, and a semaphore caps concurrent in-flight pushes.
	sem := make(chan struct{}, h.pushConcurrency)
	timeout := h.pushTimeout
	go func() {
		var wg sync.WaitGroup
		for _, t := range tokens {
			sem <- struct{}{}
			wg.Add(1)
			go func(token string) {
				defer wg.Done()
				defer func() { <-sem }()
				ctx, cancel := context.WithTimeout(context.Background(), timeout)
				defer cancel()
				if err := n.Notify(ctx, token, notif); err != nil {
					log.Printf("hub: push to %s… failed: %v", safePrefix(token), err)
				} else {
					log.Printf("hub: push to %s… delivered to APNs", safePrefix(token))
				}
			}(t)
		}
		wg.Wait()
	}()
}

// Push fan-out bounds: cap in-flight Notify calls and give each a deadline so a hung
// push provider can neither leak goroutines nor spawn them without limit. Defaults for
// the per-Hub fields (set in New; tests can shrink them per-instance).
const (
	defaultPushTimeout     = 15 * time.Second
	defaultPushConcurrency = 8
)

func safePrefix(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

// Serve handles one client connection until it closes or errors.
func (h *Hub) Serve(ctx context.Context, conn *transport.Conn) error {
	c := &hubClient{conn: conn, ch: make(chan []byte, hubOutboundBuffer), done: make(chan struct{})}
	h.mu.Lock()
	h.clients[conn] = c
	h.mu.Unlock()
	go h.writeClientLoop(c)
	defer func() {
		h.dropClient(conn)
		h.mu.Lock()
		sessions := make([]*managedSession, 0, len(h.sessions))
		for _, m := range h.sessions {
			sessions = append(sessions, m)
		}
		h.mu.Unlock()
		// Detach this client from every session it was observing. Sessions persist
		// (work runs on the host) — they end only when the provider stream closes.
		for _, m := range sessions {
			m.unsubscribe(conn)
		}
	}()
	for {
		raw, err := conn.Recv()
		if err != nil {
			return err
		}
		env, err := protocol.Decode(raw)
		if err != nil {
			h.sendErr(conn, "", "bad message")
			continue
		}
		// Long-running handlers (git/worktree/provider/tracker I/O) run off the read
		// loop so a slow operation can't block this connection from processing further
		// messages — e.g. an approval.respond the client sends while a worktree is still
		// bootstrapping. They already reply asynchronously via conn.Send. Cheap, ordered
		// operations stay inline.
		if asyncDispatch(env.Type) {
			go h.dispatch(ctx, conn, env)
		} else {
			h.dispatch(ctx, conn, env)
		}
	}
}

// asyncDispatch reports whether a request type performs blocking I/O and must therefore
// be dispatched off the connection read loop.
func asyncDispatch(typ string) bool {
	switch typ {
	case protocol.TypeSessionCreate, // worktree.Create + Bootstrap (setup hooks) + provider Create
		protocol.TypeIssueLaunch,         // same startSession path as create
		protocol.TypeWorktreeDiff,        // git diff
		protocol.TypeWorktreeRemove,      // provider Stop/Close + git remove/prune
		protocol.TypeWorktreePR,          // git CommitAll/Push/CreatePR
		protocol.TypeWorktreeConflicts,   // git per-worktree ChangedFiles
		protocol.TypeWorkspaceDiff,       // git diff per workspace member
		protocol.TypeWorkspacePR,         // git commit/push/PR per workspace member
		protocol.TypeSessionChild,        // provider Create for a scoped sub-agent
		protocol.TypeIntegrationConnect,  // tracker HTTP
		protocol.TypeIntegrationOAuthApp, // writes integrations.json
		protocol.TypeIntegrationOAuth,    // tracker HTTP
		protocol.TypeIssueStates,         // tracker HTTP
		protocol.TypeIssueDetail,         // tracker HTTP
		protocol.TypeIssueUpdate,         // tracker HTTP
		protocol.TypeIssueComment,        // tracker HTTP
		protocol.TypeIssueCommentEdit,    // tracker HTTP
		protocol.TypeIssueImage,          // tracker HTTP (image fetch)
		protocol.TypeSessionPrompt,       // provider prompt (may be network)
		protocol.TypeApprovalRespond,     // provider Respond (may be network)
		protocol.TypeSessionAttach,       // provider Attach
		protocol.TypeSessionStop,         // provider Stop
		protocol.TypeSessionInterrupt,    // provider Stop (interrupt only)
		protocol.TypeFSTree,              // disk: dir listing
		protocol.TypeFSRead,              // disk: file read
		protocol.TypeFSReadBytes,         // disk: raw bytes (images)
		protocol.TypeFSWrite,             // disk: file write
		protocol.TypeFSDiff,              // git diff
		protocol.TypeFSSearch,            // disk: multi-file search
		protocol.TypeRunTest,             // run tests/build (subprocess)
		protocol.TypeHandoffList,         // disk-backed store read
		protocol.TypeLSPReferences,       // language server: references
		protocol.TypeLSPRename,           // language server: rename
		protocol.TypeLSPSymbols,          // language server: document symbols
		protocol.TypeLSPOpen,             // language server: didOpen
		protocol.TypeLSPChange,           // language server: didChange
		protocol.TypeLSPClose,            // language server: didClose
		protocol.TypeLSPHover,            // language server: hover
		protocol.TypeLSPDefinition,       // language server: definition
		protocol.TypeLSPComplete,         // language server: completion
		protocol.TypeLSPFormat,           // language server: format document
		protocol.TypeLSPServerInfo,       // language server: install status
		protocol.TypeLSPInstall,          // language server: install (runs a package manager)
		protocol.TypeDiscover:            // host scan
		return true
	}
	return false
}

// broadcast sends an event to every connected client (used for cross-device sync).
func (h *Hub) broadcast(typ string, payload any) {
	raw, err := protocol.Encode("", typ, payload)
	if err != nil {
		return
	}
	h.mu.Lock()
	clients := make([]*hubClient, 0, len(h.clients))
	for _, c := range h.clients {
		clients = append(clients, c)
	}
	h.mu.Unlock()
	// Enqueue to each client's bounded outbound queue without blocking: a client whose
	// queue is full (a wedged socket) is dropped rather than allowed to apply head-of-line
	// blocking or accumulate unbounded sender goroutines. Its writer goroutine performs the
	// actual (blocking) encrypted Send. Mirrors the per-session subscriber fan-out.
	for _, c := range clients {
		select {
		case c.ch <- raw:
		default:
			h.dropClient(c.conn)
		}
	}
}

// hubClient is one connected client's bounded outbound queue for hub-level (cross-device)
// broadcasts, drained by a dedicated writer goroutine. broadcast() enqueues without blocking;
// a client whose queue overflows is dropped. Point-to-point replies (sendOK/sendErr) still write
// to conn directly — Conn.Send serializes frames, so queued and direct writes can't interleave.
type hubClient struct {
	conn *transport.Conn
	ch   chan []byte
	done chan struct{}
	once sync.Once
}

func (c *hubClient) close() { c.once.Do(func() { close(c.done) }) }

// writeClientLoop delivers queued broadcasts to one client until it's dropped or its socket
// errors. It is the only goroutine that writes broadcast frames to this conn.
func (h *Hub) writeClientLoop(c *hubClient) {
	for {
		select {
		case raw := <-c.ch:
			if c.conn.Send(raw) != nil {
				h.dropClient(c.conn)
				return
			}
		case <-c.done:
			return
		}
	}
}

// dropClient removes a client from the broadcast set and stops its writer goroutine. Idempotent
// (safe to call from the writer, broadcast overflow, and Serve teardown). It does not close the
// conn — the Serve read loop owns the connection's lifecycle.
func (h *Hub) dropClient(conn *transport.Conn) {
	h.mu.Lock()
	c := h.clients[conn]
	delete(h.clients, conn)
	h.mu.Unlock()
	if c != nil {
		c.close()
	}
}

const hubOutboundBuffer = 256 // queued broadcasts per client before it is dropped

func (h *Hub) dispatch(ctx context.Context, conn *transport.Conn, env protocol.Envelope) {
	switch env.Type {
	case protocol.TypeSessionCreate:
		var req protocol.SessionCreate
		if err := env.Unmarshal(&req); err != nil {
			h.sendErr(conn, env.ID, "bad session.create")
			return
		}
		m, err := h.startSession(ctx, req, sessionMeta{})
		if err != nil {
			h.sendErr(conn, env.ID, err.Error())
			return
		}
		if req.Autonomous { // opt into heartbeat supervision at create time
			m.mu.Lock()
			m.autonomous = true
			m.maxNudges = req.MaxNudges
			m.budgetUSD = req.BudgetUSD
			m.mu.Unlock()
		}
		h.sendOK(conn, env.ID, m.info())
		m.subscribe(conn) // the creator observes its own session
		go m.run()

	case protocol.TypeSessionAutonomy:
		var req protocol.SessionAutonomy
		_ = env.Unmarshal(&req)
		if m := h.managed(req.SessionID); m != nil {
			m.mu.Lock()
			m.autonomous = req.Autonomous
			if req.MaxNudges > 0 {
				m.maxNudges = req.MaxNudges
			}
			if req.BudgetUSD > 0 {
				m.budgetUSD = req.BudgetUSD
			}
			if req.Autonomous { // re-arming clears the give-up state
				m.nudgeCount = 0
			}
			m.mu.Unlock()
		}
		h.sendOK(conn, env.ID, nil)

	case protocol.TypeHandoffList:
		var req protocol.HandoffList
		_ = env.Unmarshal(&req)
		if h.db == nil {
			h.sendOK(conn, env.ID, protocol.HandoffList{Handoffs: []protocol.HandoffEntry{}})
			return
		}
		list, err := h.db.Handoffs(req.Cwd)
		if err != nil {
			h.sendErr(conn, env.ID, err.Error())
			return
		}
		h.sendOK(conn, env.ID, protocol.HandoffList{Cwd: req.Cwd, Handoffs: toHandoffEntries(list)})

	case protocol.TypeSessionChild:
		var req protocol.SessionChild
		if err := env.Unmarshal(&req); err != nil {
			h.sendErr(conn, env.ID, "bad session.child")
			return
		}
		child, err := h.spawnChild(ctx, req)
		if err != nil {
			h.sendErr(conn, env.ID, err.Error())
			return
		}
		h.sendOK(conn, env.ID, child.info())
		child.subscribe(conn)
		go child.run()

	case protocol.TypeSessionList:
		h.mu.Lock()
		list := make([]protocol.Session, 0, len(h.sessions))
		for _, m := range h.sessions {
			list = append(list, m.info())
		}
		h.mu.Unlock()
		h.sendOK(conn, env.ID, protocol.SessionList{Sessions: list})

	case protocol.TypeProviderList:
		h.sendOK(conn, env.ID, protocol.ProviderList{Providers: h.providerNames()})

	case protocol.TypeProjectList:
		reg := h.projectRegistry()
		if reg == nil {
			h.sendErr(conn, env.ID, "projects not enabled")
			return
		}
		h.sendOK(conn, env.ID, protocol.ProjectList{Projects: toProtoProjects(reg.List())})

	case protocol.TypeProjectAdd:
		var req protocol.ProjectAdd
		if err := env.Unmarshal(&req); err != nil {
			h.sendErr(conn, env.ID, "bad project.add")
			return
		}
		reg := h.projectRegistry()
		if reg == nil {
			h.sendErr(conn, env.ID, "projects not enabled")
			return
		}
		p, err := reg.Add(req.Path)
		if err != nil {
			h.sendErr(conn, env.ID, err.Error())
			return
		}
		h.sendOK(conn, env.ID, toProtoProject(p))

	case protocol.TypeProjectRemove:
		var req protocol.ProjectRef
		if err := env.Unmarshal(&req); err != nil {
			h.sendErr(conn, env.ID, "bad project.remove")
			return
		}
		reg := h.projectRegistry()
		if reg == nil {
			h.sendErr(conn, env.ID, "projects not enabled")
			return
		}
		if err := reg.Remove(req.ProjectID); err != nil {
			h.sendErr(conn, env.ID, err.Error())
			return
		}
		h.sendOK(conn, env.ID, protocol.ProjectList{Projects: toProtoProjects(reg.List())})

	case protocol.TypeWorktreeDiff:
		var req protocol.WorktreeDiff
		_ = env.Unmarshal(&req)
		m := h.managed(req.SessionID)
		if m == nil || m.meta.worktreePath == "" {
			h.sendErr(conn, env.ID, "not a worktree session")
			return
		}
		diff, err := worktree.Diff(ctx, m.meta.worktreePath, m.meta.baseCommit)
		if err != nil {
			h.sendErr(conn, env.ID, err.Error())
			return
		}
		h.sendOK(conn, env.ID, protocol.WorktreeDiff{SessionID: req.SessionID, Diff: diff})

	case protocol.TypeWorkspaceDiff:
		var req protocol.WorkspaceDiff
		_ = env.Unmarshal(&req)
		m := h.managed(req.SessionID)
		if m == nil || len(m.meta.members) == 0 {
			h.sendErr(conn, env.ID, "not a workspace session")
			return
		}
		members := m.meta.members
		out := make([]protocol.WorkspaceMemberDiff, 0, len(members))
		for _, mem := range members {
			diff, err := worktree.Diff(ctx, mem.Path, mem.BaseCommit)
			if err != nil {
				h.sendErr(conn, env.ID, err.Error())
				return
			}
			out = append(out, protocol.WorkspaceMemberDiff{Name: mem.Name, Branch: mem.Branch, Diff: diff})
		}
		h.sendOK(conn, env.ID, protocol.WorkspaceDiff{SessionID: req.SessionID, Members: out})

	case protocol.TypeWorkspacePR:
		var req protocol.WorkspacePR
		_ = env.Unmarshal(&req)
		m := h.managed(req.SessionID)
		if m == nil || len(m.meta.members) == 0 {
			h.sendErr(conn, env.ID, "not a workspace session")
			return
		}
		title := req.Title
		if title == "" {
			title = m.meta.workspaceName
		}
		results := make([]protocol.WorkspaceMemberPR, 0, len(m.meta.members))
		for _, mem := range m.meta.members {
			results = append(results, h.finishWorkspaceMember(ctx, mem, title, req.Body))
		}
		h.sendOK(conn, env.ID, protocol.WorkspacePR{SessionID: req.SessionID, Title: title, Body: req.Body, Members: results})

	case protocol.TypeWorktreeRemove:
		var req protocol.WorktreeRemove
		_ = env.Unmarshal(&req)
		m := h.managed(req.SessionID)
		if m == nil || (m.meta.worktreePath == "" && len(m.meta.members) == 0) {
			h.sendErr(conn, env.ID, "not a worktree session")
			return
		}
		_ = m.sess.Stop(ctx)
		_ = m.sess.Close()
		if len(m.meta.members) > 0 {
			// Cross-repo workspace: tear down every member worktree + the layout dir.
			if err := worktree.RemoveWorkspace(m.meta.cwd, m.meta.members, req.Force); err != nil {
				h.sendErr(conn, env.ID, err.Error())
				return
			}
			for _, mem := range m.meta.members {
				_ = worktree.Prune(mem.RepoRoot)
			}
		} else {
			if err := worktree.Remove(m.meta.repoRoot, m.meta.worktreePath, req.Force); err != nil {
				h.sendErr(conn, env.ID, err.Error())
				return
			}
			_ = worktree.Prune(m.meta.repoRoot)
		}
		h.releasePort(m.meta.port) // return the worktree's reserved port to the pool
		h.removeSession(req.SessionID)
		h.sendOK(conn, env.ID, protocol.SessionRef{SessionID: req.SessionID})

	case protocol.TypeWorktreePR:
		var req protocol.WorktreePR
		_ = env.Unmarshal(&req)
		m := h.managed(req.SessionID)
		if m == nil || m.meta.worktreePath == "" {
			h.sendErr(conn, env.ID, "not a worktree session")
			return
		}
		wtPath, branch := m.meta.worktreePath, m.meta.branch
		title := req.Title
		if title == "" {
			title = branch
		}
		if _, err := worktree.CommitAll(ctx, wtPath, title); err != nil {
			h.sendErr(conn, env.ID, err.Error())
			return
		}
		if !worktree.HasRemote(wtPath) {
			h.sendErr(conn, env.ID, "no 'origin' remote — ask the agent to push and open the PR")
			return
		}
		if err := worktree.Push(ctx, wtPath, branch); err != nil {
			h.sendErr(conn, env.ID, err.Error())
			return
		}
		url, _ := worktree.CreatePR(ctx, wtPath, branch, title, req.Body) // gh optional; branch is pushed regardless
		if url != "" && m.meta.issueID != "" {
			go h.writeBackPR(m.meta.issueProvider, m.meta.issueID, url) // close the loop on the linked ticket
		}
		h.sendOK(conn, env.ID, protocol.WorktreePRResult{SessionID: req.SessionID, Branch: branch, Pushed: true, URL: url})

	case protocol.TypeWorktreeConflicts:
		var req protocol.WorktreeConflicts
		_ = env.Unmarshal(&req)
		m := h.managed(req.SessionID)
		if m == nil || m.meta.worktreePath == "" {
			h.sendErr(conn, env.ID, "not a worktree session")
			return
		}
		// Collect the files each active worktree changed vs its base, keyed by branch.
		h.mu.Lock()
		others := make([]*managedSession, 0, len(h.sessions))
		for _, ms := range h.sessions {
			others = append(others, ms)
		}
		h.mu.Unlock()
		changed := map[string][]string{}
		for _, ms := range others {
			if ms.meta.worktreePath == "" || ms.meta.branch == "" {
				continue
			}
			if files, err := worktree.ChangedFiles(ms.meta.worktreePath, ms.meta.baseCommit); err == nil {
				changed[ms.meta.branch] = files
			}
		}
		overlaps := worktree.Overlaps(m.meta.branch, changed)
		files := make([]protocol.FileConflict, 0, len(overlaps))
		for path, branches := range overlaps {
			files = append(files, protocol.FileConflict{Path: path, Branches: branches})
		}
		h.sendOK(conn, env.ID, protocol.WorktreeConflicts{SessionID: req.SessionID, Files: files})

	case protocol.TypeIntegrationConnect:
		var req protocol.IntegrationConnect
		if err := env.Unmarshal(&req); err != nil {
			h.sendErr(conn, env.ID, "bad integration.connect")
			return
		}
		m := h.issuesMgr()
		if m == nil {
			h.sendErr(conn, env.ID, "integrations not enabled")
			return
		}
		if err := m.Connect(ctx, req.Provider, req.Token); err != nil {
			h.sendErr(conn, env.ID, err.Error())
			return
		}
		h.sendOK(conn, env.ID, protocol.IntegrationStatus{Connected: m.Connected()})

	case protocol.TypeIntegrationStatus:
		var connected, oauthApps []string
		if m := h.issuesMgr(); m != nil {
			connected = m.Connected()
			oauthApps = m.OAuthApps()
		}
		h.sendOK(conn, env.ID, protocol.IntegrationStatus{Connected: connected, OAuthApps: oauthApps})

	case protocol.TypeIntegrationOAuthApp:
		var req protocol.IntegrationOAuthApp
		if err := env.Unmarshal(&req); err != nil {
			h.sendErr(conn, env.ID, "bad integration.oauthapp")
			return
		}
		m := h.issuesMgr()
		if m == nil {
			h.sendErr(conn, env.ID, "integrations not enabled")
			return
		}
		if err := m.SetOAuthApp(req.Provider, req.ClientID, req.ClientSecret); err != nil {
			h.sendErr(conn, env.ID, err.Error())
			return
		}
		h.sendOK(conn, env.ID, protocol.IntegrationStatus{Connected: m.Connected(), OAuthApps: m.OAuthApps()})

	case protocol.TypeIntegrationOAuth:
		var req protocol.IntegrationOAuth
		_ = env.Unmarshal(&req)
		m := h.issuesMgr()
		h.mu.Lock()
		addr := h.oauthAddr
		h.mu.Unlock()
		if m == nil || addr == "" {
			h.sendErr(conn, env.ID, "integrations/oauth not enabled")
			return
		}
		redirect := issues.OAuthRedirectURI(addr, req.Provider)
		url, err := m.OAuthStart(req.Provider, redirect)
		if err != nil {
			h.sendErr(conn, env.ID, err.Error())
			return
		}
		h.sendOK(conn, env.ID, protocol.IntegrationOAuth{Provider: req.Provider, URL: url})

	case protocol.TypeIssueList:
		m := h.issuesMgr()
		if m == nil {
			h.sendErr(conn, env.ID, "integrations not enabled")
			return
		}
		h.sendOK(conn, env.ID, protocol.IssueList{Issues: toProtoIssues(m.Issues())})

	case protocol.TypeIssueStates:
		var req protocol.IssueStatesReq
		_ = env.Unmarshal(&req)
		m := h.issuesMgr()
		if m == nil {
			h.sendErr(conn, env.ID, "integrations not enabled")
			return
		}
		states, err := m.States(ctx, req.Provider, req.TeamID)
		if err != nil {
			h.sendErr(conn, env.ID, err.Error())
			return
		}
		out := make([]protocol.IssueState, len(states))
		for i, s := range states {
			out[i] = protocol.IssueState{ID: s.ID, Name: s.Name, Category: s.Category, Position: s.Position}
		}
		h.sendOK(conn, env.ID, protocol.IssueStateList{States: out})

	case protocol.TypeIssueLaunch:
		h.handleIssueLaunch(ctx, conn, env)

	case protocol.TypeIssueDetail:
		var req protocol.IssueDetailReq
		_ = env.Unmarshal(&req)
		m := h.issuesMgr()
		if m == nil {
			h.sendErr(conn, env.ID, "integrations not enabled")
			return
		}
		issue, comments, err := m.Detail(ctx, req.Provider, req.IssueID)
		if err != nil {
			h.sendErr(conn, env.ID, err.Error())
			return
		}
		h.sendOK(conn, env.ID, protocol.IssueDetail{
			Issue:    toProtoIssue(issue),
			Comments: toProtoComments(comments),
		})

	case protocol.TypeIssueUpdate:
		var req protocol.IssueUpdate
		_ = env.Unmarshal(&req)
		m := h.issuesMgr()
		if m == nil {
			h.sendErr(conn, env.ID, "integrations not enabled")
			return
		}
		updated, err := m.Update(ctx, req.Provider, req.IssueID, issues.UpdateFields{
			Title:       req.Title,
			Description: req.Description,
			StateID:     req.StateID,
			Priority:    req.Priority,
		})
		if err != nil {
			h.sendErr(conn, env.ID, err.Error())
			return
		}
		h.sendOK(conn, env.ID, toProtoIssue(updated))
		// Refresh the merged cache off the reply path so every device's board
		// reflects the edit (fire-and-forget; the reply already went out).
		go func() { _ = m.Refresh(context.Background()) }()

	case protocol.TypeIssueComment:
		var req protocol.IssueCommentAdd
		_ = env.Unmarshal(&req)
		m := h.issuesMgr()
		if m == nil {
			h.sendErr(conn, env.ID, "integrations not enabled")
			return
		}
		if err := m.AddComment(ctx, req.Provider, req.IssueID, req.Body); err != nil {
			h.sendErr(conn, env.ID, err.Error())
			return
		}
		// The provider's add-comment mutation returns only success, so synthesize the
		// created comment from the request body (the client already has it optimistically).
		h.sendOK(conn, env.ID, protocol.IssueComment{Body: req.Body})

	case protocol.TypeIssueCommentEdit:
		var req protocol.IssueCommentEdit
		_ = env.Unmarshal(&req)
		m := h.issuesMgr()
		if m == nil {
			h.sendErr(conn, env.ID, "integrations not enabled")
			return
		}
		if err := m.EditComment(ctx, req.Provider, req.CommentID, req.Body); err != nil {
			h.sendErr(conn, env.ID, err.Error())
			return
		}
		h.sendOK(conn, env.ID, nil)

	case protocol.TypeIssueImage:
		var req protocol.IssueImageReq
		_ = env.Unmarshal(&req)
		m := h.issuesMgr()
		if m == nil {
			h.sendErr(conn, env.ID, "integrations not enabled")
			return
		}
		mime, data, err := m.FetchImage(ctx, req.Provider, req.URL)
		if err != nil {
			h.sendErr(conn, env.ID, err.Error())
			return
		}
		h.sendOK(conn, env.ID, protocol.IssueImage{
			Mime: mime,
			Data: base64.StdEncoding.EncodeToString(data),
		})

	case protocol.TypeSessionSubscribe:
		var req protocol.SessionRef
		_ = env.Unmarshal(&req)
		m := h.managed(req.SessionID)
		if m == nil {
			h.sendErr(conn, env.ID, "no such session")
			return
		}
		h.sendOK(conn, env.ID, m.info())
		m.subscribe(conn) // replays the transcript, then live events flow

	case protocol.TypeSessionPrompt:
		var req protocol.SessionPrompt
		_ = env.Unmarshal(&req)
		m := h.managed(req.SessionID)
		if m == nil {
			h.sendErr(conn, env.ID, "no such session")
			return
		}
		if err := promptSession(ctx, m.sess, req.Text, req.Images); err != nil {
			h.sendErr(conn, env.ID, err.Error())
			return
		}
		h.sendOK(conn, env.ID, nil)

	case protocol.TypeApprovalRespond:
		var req protocol.ApprovalRespond
		_ = env.Unmarshal(&req)
		h.mu.Lock()
		m := h.approvals[req.ApprovalID]
		delete(h.approvals, req.ApprovalID)
		h.mu.Unlock()
		if m == nil {
			h.sendErr(conn, env.ID, "no such approval")
			return
		}
		m.mu.Lock()
		if m.pendingApprovals > 0 {
			m.pendingApprovals--
		}
		m.mu.Unlock()
		if err := m.sess.Respond(ctx, req.ApprovalID, req.Decision); err != nil {
			h.sendErr(conn, env.ID, err.Error())
			return
		}
		h.sendOK(conn, env.ID, nil)
		// Tell every client this approval was answered, so its card clears everywhere.
		h.broadcast(protocol.TypeApprovalResolved, protocol.ApprovalResolved{ApprovalID: req.ApprovalID, Decision: req.Decision})

	case protocol.TypeSessionAttach:
		var req protocol.SessionAttach
		if err := env.Unmarshal(&req); err != nil {
			h.sendErr(conn, env.ID, "bad session.attach")
			return
		}
		// If the daemon already owns this session, just subscribe — no duplicate
		// provider subscription. This is the crux of the single-session-broadcast model.
		if m := h.managed(req.SessionID); m != nil {
			h.sendOK(conn, env.ID, m.info())
			m.subscribe(conn)
			return
		}
		h.mu.Lock()
		factory := h.attach
		registered := h.providers[req.Provider]
		h.mu.Unlock()
		var att agent.Attacher
		if factory != nil {
			att = factory(req.Provider, req.URL)
		}
		if att == nil {
			att, _ = registered.(agent.Attacher)
		}
		if att == nil {
			h.sendErr(conn, env.ID, "provider cannot attach: "+req.Provider)
			return
		}
		sess, err := att.Attach(ctx, req.SessionID, req.Cwd)
		if err != nil {
			h.sendErr(conn, env.ID, err.Error())
			return
		}
		m := h.addSession(sess, sessionMeta{})
		h.sendOK(conn, env.ID, m.info())
		m.subscribe(conn)
		go m.run()

	case protocol.TypeDiscover:
		h.mu.Lock()
		f := h.discover
		h.mu.Unlock()
		items := []protocol.Discovered{}
		if f != nil {
			got, err := f(ctx)
			if err != nil {
				h.sendErr(conn, env.ID, err.Error())
				return
			}
			items = got
		}
		h.autoRegisterProjects(items) // auto-create projects from active agents' cwds
		h.sendOK(conn, env.ID, protocol.DiscoverList{Items: items})

	case protocol.TypeDeviceRegister:
		var req protocol.DeviceRegister
		if err := env.Unmarshal(&req); err != nil || req.Token == "" {
			h.sendErr(conn, env.ID, "bad device.register")
			return
		}
		h.RegisterDevice(req.Token)
		log.Printf("hub: device registered for push (token %s…, %d chars)", safePrefix(req.Token), len(req.Token))
		h.sendOK(conn, env.ID, nil)

	case protocol.TypeSessionInterrupt:
		var req protocol.SessionRef
		_ = env.Unmarshal(&req)
		if m := h.managed(req.SessionID); m != nil {
			_ = m.sess.Stop(ctx) // interrupt the current turn; the session stays open for a redirect
		}
		h.sendOK(conn, env.ID, nil)

	case protocol.TypeSessionStop:
		var req protocol.SessionRef
		_ = env.Unmarshal(&req)
		if m := h.managed(req.SessionID); m != nil {
			_ = m.sess.Stop(ctx) // interrupt any running turn
			_ = m.sess.Close()   // end the event stream -> run() -> removeSession (drops the record)
			h.removeSession(req.SessionID)
			if h.db != nil {
				_ = h.db.SetName(req.SessionID, "") // clear the orphaned rename
			}
		}
		h.sendOK(conn, env.ID, nil)
		// Delete is permanent: tell every client to drop the row.
		h.broadcastSessionList()

	case protocol.TypeSessionRename:
		var req protocol.SessionRename
		_ = env.Unmarshal(&req)
		m := h.managed(req.SessionID)
		if m == nil {
			h.sendErr(conn, env.ID, "unknown session")
			return
		}
		name := strings.TrimSpace(req.Name)
		m.mu.Lock()
		m.meta.label = name
		m.mu.Unlock()
		// Persist so the name survives a daemon restart (best-effort; log on failure).
		if h.db != nil {
			if err := h.db.SetName(req.SessionID, name); err != nil {
				log.Printf("session.rename: persist %s: %v", req.SessionID, err)
			}
		}
		h.sendOK(conn, env.ID, m.info())
		// Broadcast the updated list so every client reflects the new name.
		h.broadcastSessionList()

	case protocol.TypeFSTree:
		var req protocol.FSTreeReq
		_ = env.Unmarshal(&req)
		guard := h.fsGuard()
		if req.Path == "" {
			// Per-session file tree: scope the roots to the session's workspace folder(s).
			// Empty session → all roots (browse mode).
			var paths []string
			if req.SessionID != "" {
				paths = h.sessionRoots(req.SessionID)
			} else {
				paths = guard.Roots()
			}
			roots := make([]protocol.FSNode, 0, len(paths))
			for _, r := range paths {
				roots = append(roots, protocol.FSNode{Name: filepath.Base(r), Path: r, Dir: true})
			}
			h.sendOK(conn, env.ID, protocol.FSTree{Roots: roots})
			return
		}
		nodes, err := guard.Tree(req.Path)
		if err != nil {
			h.sendErr(conn, env.ID, err.Error())
			return
		}
		entries := make([]protocol.FSNode, 0, len(nodes))
		for _, n := range nodes {
			entries = append(entries, protocol.FSNode{Name: n.Name, Path: n.Path, Dir: n.Dir, Size: n.Size})
		}
		h.sendOK(conn, env.ID, protocol.FSTree{Path: req.Path, Entries: entries})

	case protocol.TypeFSRead:
		var req protocol.FSReadReq
		_ = env.Unmarshal(&req)
		f, err := h.fsGuard().Read(req.Path)
		if err != nil {
			h.sendErr(conn, env.ID, err.Error())
			return
		}
		h.sendOK(conn, env.ID, protocol.FSFile{
			Path: req.Path, Content: f.Content, Sha: f.Sha, ModTime: f.ModTime,
			Size: f.Size, Binary: f.Binary, Truncated: f.Truncated,
		})

	case protocol.TypeFSReadBytes:
		var req protocol.FSReadBytesReq
		_ = env.Unmarshal(&req)
		mime, data, err := h.fsGuard().ReadBytes(req.Path)
		if err != nil {
			h.sendErr(conn, env.ID, err.Error())
			return
		}
		h.sendOK(conn, env.ID, protocol.FSBytes{
			Path: req.Path, Mime: mime, Data: base64.StdEncoding.EncodeToString(data),
		})

	case protocol.TypeLSPOpen:
		var req protocol.LSPDocReq
		_ = env.Unmarshal(&req)
		// Only open a language server for a file inside an allowed root.
		if _, err := h.fsGuard().Resolve(req.Path); err != nil {
			h.sendErr(conn, env.ID, err.Error())
			return
		}
		if err := h.lsp.Open(ctx, req.Path, req.Content); err != nil {
			h.sendErr(conn, env.ID, err.Error())
			return
		}
		h.sendOK(conn, env.ID, nil)

	case protocol.TypeLSPChange:
		var req protocol.LSPDocReq
		_ = env.Unmarshal(&req)
		_ = h.lsp.Change(ctx, req.Path, req.Content)
		h.sendOK(conn, env.ID, nil)

	case protocol.TypeLSPClose:
		var req protocol.LSPDocReq
		_ = env.Unmarshal(&req)
		h.lsp.Close(req.Path)
		h.sendOK(conn, env.ID, nil)

	case protocol.TypeLSPHover:
		var req protocol.LSPPosReq
		_ = env.Unmarshal(&req)
		txt, err := h.lsp.Hover(ctx, req.Path, req.Line, req.Character)
		if err != nil {
			h.sendErr(conn, env.ID, err.Error())
			return
		}
		h.sendOK(conn, env.ID, protocol.LSPHover{Contents: txt})

	case protocol.TypeLSPDefinition:
		var req protocol.LSPPosReq
		_ = env.Unmarshal(&req)
		loc, err := h.lsp.Definition(ctx, req.Path, req.Line, req.Character)
		if err != nil {
			h.sendErr(conn, env.ID, err.Error())
			return
		}
		h.sendOK(conn, env.ID, protocol.LSPDefinition{
			Path: loc.Path, Line: loc.StartLine, Character: loc.StartChar, Found: loc.Path != "",
		})

	case protocol.TypeLSPComplete:
		var req protocol.LSPPosReq
		_ = env.Unmarshal(&req)
		items, err := h.lsp.Completion(ctx, req.Path, req.Line, req.Character)
		if err != nil {
			h.sendErr(conn, env.ID, err.Error())
			return
		}
		out := make([]protocol.LSPCompletionItem, len(items))
		for i, it := range items {
			out[i] = protocol.LSPCompletionItem{Label: it.Label, Insert: it.Insert, Detail: it.Detail, Kind: it.Kind}
		}
		h.sendOK(conn, env.ID, protocol.LSPCompletion{Items: out})

	case protocol.TypeLSPFormat:
		var req protocol.LSPFormatReq
		_ = env.Unmarshal(&req)
		text, changed, err := h.lsp.Format(ctx, req.Path, req.Content)
		if err != nil {
			h.sendErr(conn, env.ID, err.Error())
			return
		}
		h.sendOK(conn, env.ID, protocol.LSPFormatResult{Text: text, Changed: changed})

	case protocol.TypeLSPReferences:
		var req protocol.LSPPosReq
		_ = env.Unmarshal(&req)
		locs, err := h.lsp.References(ctx, req.Path, req.Line, req.Character)
		if err != nil {
			h.sendErr(conn, env.ID, err.Error())
			return
		}
		out := make([]protocol.LSPLocation, len(locs))
		for i, l := range locs {
			out[i] = protocol.LSPLocation{Path: l.Path, Line: l.StartLine, Character: l.StartChar}
		}
		h.sendOK(conn, env.ID, protocol.LSPLocations{Locations: out})

	case protocol.TypeLSPSymbols:
		var req protocol.LSPDocReq
		_ = env.Unmarshal(&req)
		syms, err := h.lsp.DocumentSymbols(ctx, req.Path)
		if err != nil {
			h.sendErr(conn, env.ID, err.Error())
			return
		}
		h.sendOK(conn, env.ID, protocol.LSPSymbols{Symbols: toProtoSymbols(syms)})

	case protocol.TypeLSPRename:
		var req protocol.LSPRenameReq
		_ = env.Unmarshal(&req)
		files, err := h.applyRename(ctx, req)
		if err != nil {
			h.sendErr(conn, env.ID, err.Error())
			return
		}
		h.sendOK(conn, env.ID, protocol.LSPRenameResult{Files: files, Count: len(files)})

	case protocol.TypeRunTest:
		var req protocol.RunTest
		_ = env.Unmarshal(&req)
		h.sendOK(conn, env.ID, nil) // ack; results stream as run.output / run.result events
		go h.runTest(req.SessionID, req.Command)

	case protocol.TypeFSSearch:
		var req protocol.FSSearchReq
		_ = env.Unmarshal(&req)
		var roots []string
		if req.SessionID != "" {
			roots = h.sessionRoots(req.SessionID)
		} else {
			roots = h.fsGuard().Roots()
		}
		hits, err := fsaccess.Search(req.Query, roots, req.Regex, 500)
		if err != nil {
			h.sendErr(conn, env.ID, err.Error())
			return
		}
		out := make([]protocol.FSSearchHit, len(hits))
		for i, hh := range hits {
			out[i] = protocol.FSSearchHit{Path: hh.Path, Line: hh.Line, Col: hh.Col, Text: hh.Text}
		}
		h.sendOK(conn, env.ID, protocol.FSSearchResult{Results: out})

	case protocol.TypeLSPServerInfo:
		var req protocol.LSPDocReq
		_ = env.Unmarshal(&req)
		info := lsp.InfoForPath(req.Path)
		h.sendOK(conn, env.ID, protocol.LSPServerInfo{
			Language: info.Language, Installed: info.Installed,
			Installable: info.Installable, InstallLabel: info.InstallLabel,
		})

	case protocol.TypeLSPInstall:
		var req protocol.LSPDocReq
		_ = env.Unmarshal(&req)
		// Runs a package manager (go install / npm -g / rustup / brew); may take minutes.
		// It's on the network-dispatch path, so it doesn't block the receive loop.
		msg, err := lsp.Install(ctx, req.Path)
		installed := lsp.InfoForPath(req.Path).Installed
		if err != nil {
			h.sendOK(conn, env.ID, protocol.LSPInstallResult{OK: false, Installed: installed, Message: err.Error()})
			return
		}
		if msg == "" {
			msg = "Installed."
		}
		h.sendOK(conn, env.ID, protocol.LSPInstallResult{OK: true, Installed: installed, Message: msg})

	case protocol.TypeFSWrite:
		var req protocol.FSWriteReq
		_ = env.Unmarshal(&req)
		f, conflict, err := h.fsGuard().Write(req.Path, req.Content, req.BaseSha)
		if err != nil {
			h.sendErr(conn, env.ID, err.Error())
			return
		}
		h.sendOK(conn, env.ID, protocol.FSWriteResult{Path: req.Path, Sha: f.Sha, ModTime: f.ModTime, Conflict: conflict})

	case protocol.TypeFSDiff:
		var req protocol.FSDiffReq
		_ = env.Unmarshal(&req)
		diff, err := h.fsDiff(ctx, req)
		if err != nil {
			h.sendErr(conn, env.ID, err.Error())
			return
		}
		h.sendOK(conn, env.ID, protocol.FSDiff{Path: req.Path, Diff: diff})

	default:
		h.sendErr(conn, env.ID, "unknown type: "+env.Type)
	}
}

// commonAncestor returns the deepest directory containing all the given absolute paths, or the
// filesystem root ("/") if they share only that.
func commonAncestor(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	parts := strings.Split(filepath.Clean(paths[0]), string(filepath.Separator))
	for _, p := range paths[1:] {
		pp := strings.Split(filepath.Clean(p), string(filepath.Separator))
		n := len(parts)
		if len(pp) < n {
			n = len(pp)
		}
		i := 0
		for i < n && parts[i] == pp[i] {
			i++
		}
		parts = parts[:i]
	}
	anc := strings.Join(parts, string(filepath.Separator))
	if anc == "" {
		return string(filepath.Separator)
	}
	return anc
}

// fsGuard builds a file-access guard scoped to the registered project roots plus every active
// session's working dir and repo root — the only places the built-in editor may touch.
// sessionRoots returns the workspace folder(s) for a session — its working directory (the
// worktree or project dir the agent edits). Empty if the session is unknown. Used to scope
// the built-in editor's file tree to the active session.
func (h *Hub) sessionRoots(sessionID string) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	m := h.sessions[sessionID]
	if m == nil || m.meta.cwd == "" {
		return nil
	}
	if len(m.meta.members) > 0 {
		// A cross-repo workspace: the file tree spans each member repo's worktree (the layout dir
		// itself is just a container). Return them so the editor scopes to the actual checkouts.
		roots := make([]string, 0, len(m.meta.members))
		for _, mem := range m.meta.members {
			roots = append(roots, mem.Path)
		}
		return roots
	}
	return []string{m.meta.cwd}
}

func (h *Hub) fsGuard() *fsaccess.Guard {
	var roots []string
	// Read h.projects and the session set under the same lock that guards them (List() has
	// its own lock, so call it after releasing h.mu).
	h.mu.Lock()
	reg := h.projects
	for _, m := range h.sessions {
		if m.meta.cwd != "" {
			roots = append(roots, m.meta.cwd)
		}
		if m.meta.repoRoot != "" {
			roots = append(roots, m.meta.repoRoot)
		}
		for _, mem := range m.meta.members { // cross-repo workspace member checkouts
			roots = append(roots, mem.Path)
		}
	}
	h.mu.Unlock()
	if reg != nil {
		for _, p := range reg.List() {
			roots = append(roots, p.Path)
		}
	}
	return fsaccess.New(roots)
}

func toProtoSymbols(syms []lsp.Symbol) []protocol.LSPSymbol {
	out := make([]protocol.LSPSymbol, len(syms))
	for i, s := range syms {
		out[i] = protocol.LSPSymbol{
			Name: s.Name, Kind: s.Kind, Detail: s.Detail,
			Line: s.Line, Character: s.Char, Children: toProtoSymbols(s.Children),
		}
	}
	return out
}

// applyRename renames a symbol across files via the language server, writing each changed file
// (validated against the fs sandbox) and broadcasting fs.change so open buffers reload.
func (h *Hub) applyRename(ctx context.Context, req protocol.LSPRenameReq) ([]string, error) {
	if strings.TrimSpace(req.NewName) == "" {
		return nil, fmt.Errorf("empty new name")
	}
	contents, err := h.lsp.RenameApply(ctx, req.Path, req.Line, req.Character, req.NewName)
	if err != nil {
		return nil, err
	}
	guard := h.fsGuard()
	var files []string
	for p, content := range contents {
		abs, err := guard.Resolve(p) // refuse anything outside allowed roots
		if err != nil {
			continue
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			continue
		}
		files = append(files, abs)
		h.broadcast(protocol.TypeFSChange, protocol.FSChange{Path: abs}) // open buffers reload
	}
	sort.Strings(files)
	return files, nil
}

// fsDiff returns the unified diff for a session's worktree, or `git diff` at a path's repo.
func (h *Hub) fsDiff(ctx context.Context, req protocol.FSDiffReq) (string, error) {
	if req.SessionID != "" {
		m := h.managed(req.SessionID)
		if m == nil {
			return "", fmt.Errorf("unknown session")
		}
		if m.meta.worktreePath != "" {
			return worktree.Diff(ctx, m.meta.worktreePath, m.meta.baseCommit)
		}
		if m.meta.cwd != "" {
			return worktree.Diff(ctx, m.meta.cwd, "")
		}
		return "", fmt.Errorf("session has no working dir")
	}
	if req.Path == "" {
		return "", fmt.Errorf("diff needs a session_id or path")
	}
	abs, err := h.fsGuard().Resolve(req.Path)
	if err != nil {
		return "", err
	}
	return worktree.Diff(ctx, abs, "")
}

func (h *Hub) sendOK(conn *transport.Conn, id string, payload any) {
	if raw, err := protocol.Encode(id, protocol.TypeOK, payload); err == nil {
		_ = conn.Send(raw)
	}
}

func (h *Hub) sendErr(conn *transport.Conn, id, msg string) {
	if raw, err := protocol.Encode(id, protocol.TypeError, protocol.Error{Message: msg}); err == nil {
		_ = conn.Send(raw)
	}
}
