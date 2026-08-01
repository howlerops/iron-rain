// Package hub is the daemon core: it registers providers, and for each client
// connection routes protocol requests to sessions and forwards session events back
// over the encrypted transport.
package hub

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/howlerops/oculus/daemon/accounts"
	"github.com/howlerops/oculus/daemon/activity"
	"github.com/howlerops/oculus/daemon/agent"
	"github.com/howlerops/oculus/daemon/agent/cli"
	"github.com/howlerops/oculus/daemon/commands"
	"github.com/howlerops/oculus/daemon/fsaccess"
	"github.com/howlerops/oculus/daemon/genui"
	"github.com/howlerops/oculus/daemon/issues"
	"github.com/howlerops/oculus/daemon/loghub"
	"github.com/howlerops/oculus/daemon/loops"
	"github.com/howlerops/oculus/daemon/lsp"
	"github.com/howlerops/oculus/daemon/mcp"
	"github.com/howlerops/oculus/daemon/project"
	"github.com/howlerops/oculus/daemon/protocol"
	"github.com/howlerops/oculus/daemon/push"
	"github.com/howlerops/oculus/daemon/quota"
	"github.com/howlerops/oculus/daemon/slack"
	"github.com/howlerops/oculus/daemon/sshremote"
	"github.com/howlerops/oculus/daemon/store"
	"github.com/howlerops/oculus/daemon/telemetry"
	"github.com/howlerops/oculus/daemon/transcript"
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
	devices   *deviceRegistry            // enrolled client keys (per-device revocation)
	approvals map[string]*managedSession // approvalID -> owning session
	discover  DiscoverFunc

	notifier          push.Notifier // optional: push actionable approvals to a device
	slack             *slack.Client // optional: mirror agent events to a Slack channel
	pushTokens        []string      // registered device tokens
	attach            AttacherFactory
	clients           map[*transport.Conn]*hubClient // all connected clients (for global broadcasts)
	projects          *project.Registry              // optional: registered folders sessions spawn in
	autoProjects      bool                           // auto-register projects from active agents' cwds
	issues            *issues.Manager                // optional: connected trackers (Linear/Jira)
	telemetry         *telemetry.Client              // optional: anonymized diagnostics shipping
	logHub            *loghub.Hub                    // optional: live daemon-log stream (Developer log panel)
	logSubs           map[*transport.Conn]bool       // clients subscribed to the log stream
	transcripts       *transcript.Store              // optional: durable append-only per-session transcript (never-lose-work)
	activity          *activity.Store                // optional: cross-session activity feed (Activity destination backbone)
	accounts          *accounts.Registry             // optional: multi-account credentials + active selection per provider
	remotes           *sshremote.Registry            // optional: registered SSH remote hosts (remote worktrees)
	sshRunner         *sshremote.Runner              // optional: executes git/agent ops on remotes over SSH
	redetect          func()                         // optional: re-run agent-harness detection (provider.refresh)
	loopEngine        *loops.Engine                  // optional: recurring autonomous ticket workflows
	agentsPath        string                         // path to ~/.oculus/agents.json (custom CLI agents)
	agentHidePath     string                         // path to ~/.oculus/agent-visibility.json (hidden names)
	agentHidden       map[string]bool                // agent names hidden from the session pickers
	oauthAddr         string                         // loopback host:port for tracker OAuth callbacks (per-provider path)
	worktreeBase      string                         // base dir for worktrees ("" = worktree.DefaultBase)
	reservedPorts     map[int]bool                   // ports handed to worktree setup hooks (collision-free)
	db                *store.Store                   // optional: durable local state (session names + records)
	lsp               *lsp.Manager                   // language servers for the built-in editor (diagnostics/types)
	runningTests      map[string]bool                // session ids with a test/build run in progress
	approvalRulesPath string                         // path to ~/.oculus/approval-rules.json (persistent rules)
	approvalRules     []ApprovalRule                 // ordered scoped rules; deny beats allow (see approval_rules.go)
	approvalReqs      map[string]pendingApproval     // approvalID -> the request + its scope, for a scoped ALWAYS
	notifyPrefsPath   string                         // path to ~/.oculus/notify.json (per-category push toggles)
	notifyOff         map[string]bool                // push categories the user turned OFF (absent = enabled)
	fanoutNotified    map[string]bool                // fan-out groups already notified as "all done" (fire once)
	fanoutJudge       map[string]fanoutJudgeSpec     // groups that opted into an advisory judge
	fanoutPrompt      map[string]string              // the task each group is racing (for the comparison header)

	mcp     *mcp.Registry   // daemon-owned MCP server registry (nil = MCP not enabled)
	roles   *roleRegistry   // who may steer vs. watch (see roles.go); disabled = everyone is the owner
	invites *inviteRegistry // outstanding share credentials (see invites.go)
	// pairURL builds a redeemable pairing URL for an invite secret. Set by main once the reachable
	// address is known; nil = invites can be created but not rendered as a link.
	pairURL func(secret string) string

	mcpGateway     *mcp.Gateway // local HTTP front door for supervised MCP servers (nil = not enabled)
	mcpGatewayBase string       // reachable base URL of the gateway ("" until the listener is up)
	mcpToken       string       // machine-wide bearer token for the gateway
	mcpTokens      *mcpSessionTokens
	mcpApprovals   map[string]chan string // approvalID -> waiter, for MCP calls blocked on a human
	mcpFound       map[string]mcp.Found   // last discovery, so an import can only adopt what was offered
	mcpExclusive   bool                   // daemon owns MCP: harnesses ignore their own config

	// agentsFileMu serializes the load→mutate→save cycle on ~/.oculus/agents.json. agent.upsert and
	// agent.delete are both in the async-dispatch allowlist, so without this two concurrent edits
	// each read the same pre-state and the later write silently discards the earlier one. It is a
	// SEPARATE lock from h.mu on purpose: it is held across disk I/O, which h.mu never is.
	agentsFileMu sync.Mutex

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
// progressFn reports a live create step (nil-safe via the local emit helper). Drives the app's
// prescriptive loading checklist.
type progressFn func(stage, detail string, step, total int)

func (h *Hub) startSession(ctx context.Context, req protocol.SessionCreate, meta sessionMeta, progress progressFn) (*managedSession, error) {
	t0 := time.Now()
	emit := func(stage, detail string, step, total int) {
		if progress != nil {
			progress(stage, detail, step, total)
		}
	}
	log.Printf("session.create: provider=%s project=%s projects=%v worktree=%v plan=%v prompt=%dB",
		req.Provider, req.ProjectID, req.ProjectIDs, req.Worktree, req.Plan, len(req.Prompt))
	h.mu.Lock()
	p := h.providers[req.Provider]
	h.mu.Unlock()
	if p == nil {
		log.Printf("session.create: FAILED — unknown provider %q", req.Provider)
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
	var multiRepoNote string // seeded into the first prompt so the agent knows the workspace's repos
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
			log.Printf("session.create: building %d-repo isolated workspace %q (a git worktree per repo)…", len(paths), name)
			emit("workspace", fmt.Sprintf("Preparing %d-repo workspace…", len(paths)), 0, 0)
			wt0 := time.Now()
			layout, members, err := worktree.CreateWorkspace("", name, paths, func(step, total int, repo string) {
				emit("worktree", "Creating worktree · "+repo, step, total)
			})
			if err != nil {
				log.Printf("session.create: FAILED — workspace setup: %v", err)
				return nil, fmt.Errorf("workspace setup failed: %w", err)
			}
			log.Printf("session.create: workspace %q ready at %s (%d members) in %s", name, layout, len(members), time.Since(wt0).Round(time.Millisecond))
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
			// The agent runs in the common ancestor, but the code view must show ONLY the picked
			// folders — not their siblings under that ancestor.
			meta.roots = append([]string(nil), paths...)
		}
		// Tell the agent, on its first turn, exactly which repos this workspace spans and where they
		// are — otherwise (especially a shared common-ancestor cwd) it can't tell the target repos
		// from their siblings and reports it "can't find the repos".
		var lines []string
		if isolate {
			for _, mem := range meta.members {
				lines = append(lines, "- "+filepath.Base(mem.Path)+"  ("+mem.Path+")")
			}
		} else {
			for _, pth := range paths {
				lines = append(lines, "- "+filepath.Base(pth)+"  ("+pth+")")
			}
		}
		if len(lines) > 0 {
			multiRepoNote = fmt.Sprintf("[Workspace] This session spans %d repositories. Your working directory is %s. The repositories are:\n%s\nWork across them as needed — refer to each by the absolute path shown.\n\n",
				len(lines), cwd, strings.Join(lines, "\n"))
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
	meta.ephemeral = req.Ephemeral // scratch "just chat" session: not persisted
	if meta.ephemeral && meta.label == "" {
		meta.label = "Chat"
	}
	if req.Worktree {
		name := req.WorkspaceName
		if name == "" {
			name = "session-" + randToken()
		}
		emit("worktree", "Creating an isolated worktree…", 0, 0)
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
	// One-shot prefix folded into the session's FIRST user turn only: the generative-UI guide (teaches
	// every harness the ```iron:ui``` grammar — see genui.Preamble; the app hides it) plus any
	// workspace note. If the session is created WITH a first prompt (issue launch, loops, etc.) it
	// rides that; otherwise it rides the first user send below (via pendingContext).
	firstTurnPrefix := genui.Preamble() + multiRepoNote
	if firstTurnPrefix != "" && createPrompt != "" {
		createPrompt = firstTurnPrefix + createPrompt
		firstTurnPrefix = ""
	}
	// Mode subsumes the old Plan bool: architect keeps the harness's native plan mode, ask is
	// read-only, code is normal. Enforcement is daemon-side (modes.go), so a harness with no native
	// mode still obeys it.
	mode := normalizeMode(req.Mode, req.Plan)
	planStart := mode == protocol.ModeArchitect
	log.Printf("session.create: starting %s in %s (mode=%s)…", req.Provider, cwd, mode)
	emit("provider", "Starting "+req.Provider+"…", 0, 0)
	pc0 := time.Now()
	var sess agent.Session
	var err error
	// Attach the daemon's MCP servers for this project so every harness spawns with the same tools.
	// It rides the context because providers are global while MCP scoping is per-session.
	// Mint this session's own gateway token BEFORE spawning, so its MCP calls are attributable and
	// can be held to the session's mode and approval rules. It's bound to the real session id a
	// moment later, once the provider assigns one.
	mcpToken := ""
	if g := h.mcpGatewayHandle(); g != nil {
		mcpToken = h.mcpTokens.mint()
		g.AddSessionToken(mcpToken)
	}
	if servers := h.mcpServersForSession(req.ProjectID, mcpToken); len(servers) > 0 {
		ctx = mcp.WithConfig(ctx, mcp.Config{Servers: servers, Exclusive: h.mcpExclusiveEnabled()})
	}
	if planStart {
		if pc, ok := p.(agent.PlanCreator); ok {
			sess, err = pc.CreatePlan(ctx, cwd, createPrompt)
		} else {
			sess, err = p.Create(ctx, cwd, createPrompt) // provider has no plan mode; daemon-side rules still apply
		}
	} else {
		sess, err = p.Create(ctx, cwd, createPrompt)
	}
	if err != nil {
		// The token was minted for a session that will never exist — a live, unattributable gateway
		// credential nobody could ever revoke. Take it back before reporting the failure.
		h.discardMCPToken(mcpToken)
		log.Printf("session.create: FAILED — provider %s start: %v", req.Provider, err)
		return nil, err
	}
	log.Printf("session.create: %s session %s started in %s", req.Provider, sess.ID(), time.Since(pc0).Round(time.Millisecond))
	if len(req.Images) > 0 {
		text := req.Prompt
		if firstTurnPrefix != "" {
			text = firstTurnPrefix + text
			firstTurnPrefix = ""
		}
		_ = promptSession(ctx, sess, text, req.Images)
	}
	if req.Model != "" {
		if setter, ok := sess.(agent.ModelSetter); ok {
			_ = setter.SetModel(req.ModelProvider, req.Model)
		}
	}
	ms := h.addSession(sess, meta)
	if strings.TrimSpace(req.Prompt) != "" {
		ms.openTurn("") // created WITH a prompt → a turn is already in flight
	}
	h.mcpTokens.bind(mcpToken, sess.ID()) // the token now identifies this session to the gateway
	ms.mu.Lock()
	if req.Model != "" {
		ms.model, ms.modelProvider = req.Model, req.ModelProvider
	}
	ms.mode = mode
	ms.pendingContext = firstTurnPrefix
	ms.mu.Unlock()
	emit("ready", "Session ready", 0, 0)
	log.Printf("session.create: DONE — session %s ready in %s", sess.ID(), time.Since(t0).Round(time.Millisecond))
	return ms, nil
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
	ms, err := h.startSession(ctx, create, sessionMeta{issueID: issue.ID, issueKey: issue.Key, issueProvider: issue.Provider}, nil)
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

// reverseCheckpoints returns the checkpoints newest-first (they're stored oldest-first).
func reverseCheckpoints(cps []protocol.Checkpoint) []protocol.Checkpoint {
	out := make([]protocol.Checkpoint, len(cps))
	for i, cp := range cps {
		out[len(cps)-1-i] = cp
	}
	return out
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

// SetTelemetry attaches the anonymized diagnostics client.
func (h *Hub) SetTelemetry(t *telemetry.Client) {
	h.mu.Lock()
	h.telemetry = t
	h.mu.Unlock()
}

// tel returns the telemetry client (nil if not attached) without holding the lock at call sites.
func (h *Hub) tel() *telemetry.Client {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.telemetry
}

// SetTranscripts attaches the durable per-session transcript store (never-lose-work backstop).
func (h *Hub) SetTranscripts(t *transcript.Store) {
	h.mu.Lock()
	h.transcripts = t
	h.mu.Unlock()
}

// SetActivity attaches the cross-session activity store and fans each new event out to every client
// as an activity.event broadcast (feeds the Activity destination, Needs-You inbox, and ticker).
func (h *Hub) SetActivity(a *activity.Store) {
	h.mu.Lock()
	h.activity = a
	h.mu.Unlock()
	if a != nil {
		a.SetListener(func(e activity.Event) {
			h.broadcast(protocol.TypeActivityEvent, toProtoActivity(e))
		})
	}
}

// SetAccounts attaches the credentials registry and wires every provider that supports per-account
// env (the CLI agents) to resolve the ACTIVE account's env at each session spawn — so hot-swapping
// an account in the app changes what new sessions run with. Call after providers are registered.
func (h *Hub) SetAccounts(r *accounts.Registry) {
	h.mu.Lock()
	h.accounts = r
	provs := make(map[string]agent.Provider, len(h.providers))
	for name, p := range h.providers {
		provs[name] = p
	}
	h.mu.Unlock()
	for name, p := range provs {
		if s, ok := p.(interface {
			SetAccountEnv(func() map[string]string)
		}); ok {
			n := name
			s.SetAccountEnv(func() map[string]string { return r.EnvFor(n) })
		}
	}
}

// SetRedetect installs the callback that re-runs agent-harness detection (opencode/claude-code/pi +
// CLI agents on PATH), so a client can trigger a rescan without restarting the daemon.
func (h *Hub) SetRedetect(f func()) {
	h.mu.Lock()
	h.redetect = f
	h.mu.Unlock()
}

// SetRemotes attaches the SSH remote-host registry + runner (remote worktrees).
func (h *Hub) SetRemotes(reg *sshremote.Registry, runner *sshremote.Runner) {
	h.mu.Lock()
	h.remotes = reg
	h.sshRunner = runner
	h.mu.Unlock()
}

// remoteList builds the remote.list reply, probing reachability best-effort with a short bound.
func (h *Hub) remoteList(ctx context.Context) protocol.RemoteList {
	h.mu.Lock()
	reg := h.remotes
	runner := h.sshRunner
	h.mu.Unlock()
	out := protocol.RemoteList{Hosts: []protocol.RemoteHost{}}
	if reg == nil {
		return out
	}
	for _, hst := range reg.List() {
		reachable := false
		if runner != nil {
			pctx, cancel := context.WithTimeout(ctx, 8*time.Second)
			reachable = runner.Probe(pctx, hst) == nil
			cancel()
		}
		var fwds []protocol.PortForward
		for _, f := range hst.Forwards {
			fwds = append(fwds, protocol.PortForward{LocalPort: f.LocalPort, RemotePort: f.RemotePort})
		}
		out.Hosts = append(out.Hosts, protocol.RemoteHost{
			ID: hst.ID, Name: hst.Name, SSHTarget: hst.SSHTarget, RemotePath: hst.RemotePath,
			Reachable: reachable, Forwards: fwds,
		})
	}
	return out
}

// accountList builds the account.list reply: accounts flagged with the active selection + per-
// provider usage rolled up from live sessions (the usage meter).
func (h *Hub) accountList() protocol.AccountList {
	h.mu.Lock()
	reg := h.accounts
	usage := map[string]*protocol.ProviderUsage{}
	for _, m := range h.sessions {
		m.mu.Lock()
		prov := m.sess.Provider()
		u := usage[prov]
		if u == nil {
			u = &protocol.ProviderUsage{Provider: prov}
			usage[prov] = u
		}
		u.Sessions++
		u.InputTokens += m.inTok
		u.OutputTokens += m.outTok
		u.CostUSD += m.costUSD
		m.mu.Unlock()
	}
	h.mu.Unlock()

	out := protocol.AccountList{Accounts: []protocol.Account{}, Usage: []protocol.ProviderUsage{}}
	if reg != nil {
		for _, a := range reg.List() {
			out.Accounts = append(out.Accounts, protocol.Account{
				ID: a.ID, Provider: a.Provider, Name: a.Name, Env: a.Env,
				Active: reg.ActiveID(a.Provider) == a.ID,
			})
		}
	}
	for _, u := range usage {
		out.Usage = append(out.Usage, *u)
	}
	return out
}

// recordActivity records one cross-session event (no-op if the store isn't attached). Called from
// the event pump (turn end/error), approvals (needs-you), and the loops engine.
func (h *Hub) recordActivity(e activity.Event) {
	h.mu.Lock()
	a := h.activity
	h.mu.Unlock()
	if a != nil {
		a.Record(e)
	}
}

func toProtoActivity(e activity.Event) protocol.ActivityEvent {
	return protocol.ActivityEvent{
		ID: e.ID, TS: e.TS, Kind: e.Kind, SessionID: e.SessionID, Provider: e.Provider,
		Project: e.Project, Title: e.Title, Detail: e.Detail, NeedsYou: e.NeedsYou, Read: e.Read,
	}
}

// tr returns the transcript store (nil if not attached) without holding the lock at call sites.
func (h *Hub) tr() *transcript.Store {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.transcripts
}

// SetLogHub attaches the daemon-log stream and wires each new line to the subscribed clients, so a
// Developer log panel can tail the daemon (local OR remote) live.
func (h *Hub) SetLogHub(lh *loghub.Hub) {
	h.mu.Lock()
	h.logHub = lh
	h.mu.Unlock()
	if lh != nil {
		lh.SetListener(func(line string) { h.broadcastLogLine(line) })
	}
}

// broadcastLogLine fans a single log line out to every log-subscribed client.
func (h *Hub) broadcastLogLine(line string) {
	h.mu.Lock()
	subs := make([]*transport.Conn, 0, len(h.logSubs))
	for c := range h.logSubs {
		subs = append(subs, c)
	}
	h.mu.Unlock()
	if len(subs) == 0 {
		return
	}
	payload := protocol.LogLine{Line: line}
	for _, c := range subs {
		h.sendEvent(c, protocol.TypeLogLine, payload)
	}
}

// BroadcastIssues pushes the current assigned issues to every device (the Manager's poll
// callback). Exported so main.go can wire it as the Manager's onUpdate.
func (h *Hub) BroadcastIssues(in []issues.Issue) {
	h.broadcast(protocol.TypeIssueList, protocol.IssueList{Issues: toProtoIssues(in)})
	if m := h.issuesMgr(); m != nil {
		h.broadcast(protocol.TypeIntegrationStatus, protocol.IntegrationStatus{Connected: m.Connected(), OAuthApps: m.OAuthApps(), AuthErrors: m.AuthErrors(), AuthErrorDetails: m.AuthErrorDetails()})
	}
	// Feed the loop engine so it can start agents on newly-appearing tickets.
	h.mu.Lock()
	eng := h.loopEngine
	h.mu.Unlock()
	if eng != nil {
		conv := make([]loops.Issue, 0, len(in))
		for _, i := range in {
			conv = append(conv, loops.Issue{Key: i.Key, Title: i.Title, Category: i.Category, Provider: i.Provider})
		}
		eng.OnIssues(conv)
	}
}

// EnableLoops turns on recurring autonomous ticket workflows, persisted at path. Each new matching
// ticket gets an autonomous agent session (worktree + optional plan mode), like a hands-free
// issue.launch. Call once at startup.
func (h *Hub) EnableLoops(path string) {
	eng := loops.New(path, h.spawnLoopRun, h.broadcastLoops)
	h.mu.Lock()
	h.loopEngine = eng
	h.mu.Unlock()
	// Task-kind loops fire on a schedule; ticket-kind loops fire from the issue poll (OnIssues).
	eng.StartScheduler(context.Background(), 60*time.Second)
}

func (h *Hub) loops() *loops.Engine {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.loopEngine
}

// spawnLoopRun starts an autonomous agent for a loop (the engine's injected spawn). iss is nil for a
// task-kind loop (a scheduled custom job that leans on the agent's MCP tools); non-nil for a ticket
// loop (one new tracker ticket → one session). Both cover one or more repos via the loop's Repos().
func (h *Hub) spawnLoopRun(lp loops.Loop, iss *loops.Issue) (string, error) {
	// Resolve the loop's target repos into a single ProjectID or a multi-root workspace.
	repos := lp.Repos()
	var projectID string
	var projectIDs []string
	switch {
	case len(repos) == 1:
		projectID = repos[0]
	case len(repos) > 1:
		projectIDs = repos
	}

	// Task loop: no ticket — run the recurring custom prompt. The agent uses whatever tools it has
	// (including MCP servers — GitHub, trackers, code search) to do the job (find bugs → file issues →
	// fix, review PRs, etc.).
	if iss == nil {
		branch := "loop/" + sanitizeBranch(lp.Name)
		prompt := fmt.Sprintf("You are running autonomously as a recurring loop named %q.\n\nYour standing job:\n%s\n\nUse every tool available to you — including any MCP tools (GitHub, issue trackers, code search, package/security scanners) — to carry this out end to end. When you make concrete code changes, open a PR. When you discover work worth tracking, file issues in the connected tracker. Report a short summary of what you did.", lp.Name, lp.Prompt)
		create := protocol.SessionCreate{
			Provider: lp.Provider, ProjectID: projectID, ProjectIDs: projectIDs, Prompt: prompt,
			Worktree: lp.Worktree, Plan: lp.Plan, Autonomous: true, BudgetUSD: lp.BudgetUSD, WorkspaceName: branch,
		}
		ms, err := h.startSession(context.Background(), create, sessionMeta{loopName: lp.Name}, nil)
		if err != nil {
			return "", err
		}
		ms.mu.Lock()
		ms.autonomous = true
		ms.budgetUSD = lp.BudgetUSD
		ms.mu.Unlock()
		go ms.run()
		h.recordActivity(activity.Event{
			Kind: activity.KindLoopRun, SessionID: ms.sess.ID(), Provider: lp.Provider,
			Title: "Loop “" + lp.Name + "” started a run",
		})
		return ms.sess.ID(), nil
	}

	// Ticket loop: work a single new tracker ticket, like a hands-free issue.launch.
	var full *issues.Issue
	if m := h.issuesMgr(); m != nil {
		for _, i := range m.Issues() {
			if i.Key == iss.Key {
				cp := i
				full = &cp
				break
			}
		}
	}
	branch := "loop/" + iss.Key
	prompt := fmt.Sprintf("You are running autonomously as part of a loop. Work on ticket %s — %s.\nPlan the approach first, then implement it end to end and open a PR when done.", iss.Key, iss.Title)
	meta := sessionMeta{issueKey: iss.Key, loopName: lp.Name}
	if full != nil {
		prompt = fmt.Sprintf("You are running autonomously as part of a loop. Work on %s — %s\n\n%s\n\n%s\n\nPlan the approach first, then implement it end to end and open a PR when done.", full.Key, full.Title, full.Body, full.URL)
		meta = sessionMeta{issueID: full.ID, issueKey: full.Key, issueProvider: full.Provider}
		if full.BranchName != "" {
			branch = full.BranchName
		}
	}
	create := protocol.SessionCreate{
		Provider: lp.Provider, ProjectID: projectID, ProjectIDs: projectIDs, Prompt: prompt,
		Worktree: lp.Worktree, Plan: lp.Plan, Autonomous: true, BudgetUSD: lp.BudgetUSD, WorkspaceName: branch,
	}
	ms, err := h.startSession(context.Background(), create, meta, nil)
	if err != nil {
		return "", err
	}
	ms.mu.Lock()
	ms.autonomous = true
	ms.budgetUSD = lp.BudgetUSD
	ms.mu.Unlock()
	if full != nil {
		go h.writeBackStarted(full.Provider, full.ID, full.TeamID)
	}
	go ms.run()
	h.recordActivity(activity.Event{
		Kind: activity.KindLoopRun, SessionID: ms.sess.ID(), Provider: lp.Provider,
		Title: "Loop “" + lp.Name + "” picked up " + iss.Key, Detail: iss.Title,
	})
	return ms.sess.ID(), nil
}

// sanitizeBranch turns a loop name into a git-branch-safe slug.
func sanitizeBranch(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			out = append(out, r)
		case r >= 'A' && r <= 'Z':
			out = append(out, r+('a'-'A'))
		case r == ' ' || r == '-' || r == '_' || r == '/':
			out = append(out, '-')
		}
	}
	s = string(out)
	for len(s) > 0 && s[0] == '-' {
		s = s[1:]
	}
	for len(s) > 0 && s[len(s)-1] == '-' {
		s = s[:len(s)-1]
	}
	if s == "" {
		s = "task"
	}
	return s
}

func (h *Hub) broadcastLoops() {
	eng := h.loops()
	if eng == nil {
		return
	}
	h.broadcast(protocol.TypeLoopList, protocol.LoopList{Loops: toProtoLoops(eng.List()), Runs: toProtoRuns(eng.Runs())})
}

func toProtoLoops(in []loops.Loop) []protocol.Loop {
	out := make([]protocol.Loop, 0, len(in))
	for _, l := range in {
		out = append(out, protocol.Loop{
			ID: l.ID, Name: l.Name, Enabled: l.Enabled, Provider: l.Provider, Kind: l.Kind,
			ProjectID: l.ProjectID, ProjectIDs: l.ProjectIDs,
			TriggerCategory: l.TriggerCategory, Tracker: l.Tracker,
			Prompt: l.Prompt, IntervalMinutes: l.IntervalMinutes, LastRun: l.LastRun,
			Worktree: l.Worktree, Plan: l.Plan,
			BudgetUSD: l.BudgetUSD, MaxConcurrent: l.MaxConcurrent,
		})
	}
	return out
}

func toProtoRuns(in []loops.Run) []protocol.LoopRun {
	out := make([]protocol.LoopRun, 0, len(in))
	for _, r := range in {
		out = append(out, protocol.LoopRun{
			LoopID: r.LoopID, IssueKey: r.IssueKey, IssueTitle: r.IssueTitle,
			SessionID: r.SessionID, Status: r.Status, StartedAt: r.StartedAt,
		})
	}
	return out
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
	labels := make([]protocol.IssueLabel, len(i.Labels))
	for k, l := range i.Labels {
		labels[k] = protocol.IssueLabel{ID: l.ID, Name: l.Name, Color: l.Color}
	}
	return protocol.Issue{
		ID: i.ID, Key: i.Key, Title: i.Title, Body: i.Body, Status: i.Status,
		Category: i.Category, Assignee: i.Assignee, URL: i.URL, Provider: i.Provider,
		BranchName: i.BranchName, TeamID: i.TeamID, TeamName: i.TeamName, Priority: i.Priority, UpdatedAt: i.UpdatedAt,
		CycleID: i.CycleID, CycleName: i.CycleName, CycleNumber: i.CycleNumber,
		SprintName: i.SprintName, SprintState: i.SprintState,
		AssigneeID: i.AssigneeID, Labels: labels, Estimate: i.Estimate, DueDate: i.DueDate,
	}
}

func toProtoAttachments(in []issues.Attachment) []protocol.IssueAttachment {
	out := make([]protocol.IssueAttachment, len(in))
	for i, a := range in {
		out[i] = protocol.IssueAttachment{ID: a.ID, Filename: a.Filename, URL: a.URL, Mime: a.Mime, Size: a.Size, IsImage: a.IsImage}
	}
	return out
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
		roles:           newRoleRegistry(),
		invites:         newInviteRegistry(),
		mcpTokens:       newMCPSessionTokens(),
		mcpApprovals:    map[string]chan string{},
		fanoutNotified:  map[string]bool{},
		fanoutJudge:     map[string]fanoutJudgeSpec{},
		fanoutPrompt:    map[string]string{},
		clients:         map[*transport.Conn]*hubClient{},
		logSubs:         map[*transport.Conn]bool{},
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
	// Heal a stale/wrong cwd from the provider's authoritative session directory. A wrong cwd
	// silently breaks sends (opencode partitions message writes by ?directory=), so trust the
	// resolved directory over whatever was persisted. No-op for freshly created sessions (the
	// reported dir already equals the chosen cwd).
	if dr, ok := sess.(agent.DirReporter); ok {
		if real := dr.Dir(); real != "" && real != meta.cwd {
			log.Printf("session %s: healed directory %q → %q", sess.ID(), meta.cwd, real)
			meta.cwd = real
		}
	}
	m := newManagedSession(h, sess, meta)
	h.mu.Lock()
	h.sessions[sess.ID()] = m
	h.mu.Unlock()
	if !meta.ephemeral {
		h.persistSession(m) // durable record so it survives a daemon restart (ephemeral chats aren't kept)
	}
	return m
}

func (h *Hub) managed(id string) *managedSession {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.sessions[id]
}

// removeSession drops a session from the live map + its durable record. `owner` guards against a
// use-after-recover race: recoverSession closes the OLD binding (whose run() goroutine then calls this)
// AFTER re-registering a FRESH managedSession under the same id — so we must only evict when the id
// still points at THIS session. Pass nil to force removal (e.g. deleting a stopped/record-only session).
func (h *Hub) removeSession(id string, owner *managedSession) {
	h.mu.Lock()
	if owner != nil && h.sessions[id] != owner {
		h.mu.Unlock()
		return // superseded by a recovered/restarted binding — don't clobber it
	}
	group := ""
	if m := h.sessions[id]; m != nil {
		group = m.meta.fanoutGroup
	}
	delete(h.sessions, id)
	db := h.db
	h.mu.Unlock()
	// The session's event stream ended for good (explicit stop or the provider closed
	// it); drop its durable record so it isn't re-attached on the next start.
	if db != nil {
		_ = db.DeleteSession(id)
		_ = db.DeleteHandoff(id)
		_ = db.DeleteTranscript(id) // drop the durable conversation too
	}
	h.forgetFanoutIfEmpty(group) // last variant gone → drop the group's notify marker
	h.revokeMCPToken(id)
	h.sweepSessionApprovals(id)
}

// sweepSessionApprovals clears every pending approval owned by a session that no longer exists.
//
// Without this, a session dying with questions outstanding leaked on three levels: the hub maps kept
// their entries (holding the dead *managedSession un-collectable) forever, every client kept showing
// an approval card whose Answer could only error with "no such approval", and an MCP tool call
// blocked on askForMCPApproval sat out its full 10-minute ceiling waiting on a session that could
// never answer. Yesterday's wedged fan-out multiplied all three by the number of stuck children.
//
// Resolved as DENY: the tool never ran, and clients render unknown decision strings as approved,
// which would be a lie.
func (h *Hub) sweepSessionApprovals(sessionID string) {
	h.mu.Lock()
	var ids []string
	for id, m := range h.approvals {
		if m != nil && m.sess.ID() == sessionID {
			ids = append(ids, id)
		}
	}
	for _, id := range ids {
		delete(h.approvals, id)
		delete(h.approvalReqs, id)
	}
	h.mu.Unlock()
	for _, id := range ids {
		// Unblock a held MCP call first (it's waiting on a channel, not on the broadcast).
		h.resolveMCPApproval(id, protocol.DecisionDeny)
		h.broadcast(protocol.TypeApprovalResolved, protocol.ApprovalResolved{ApprovalID: id, Decision: protocol.DecisionDeny})
	}
	if len(ids) > 0 {
		log.Printf("approvals: swept %d pending approval(s) of dead session %s", len(ids), sessionID)
	}
}

// detachSession removes a session from the LIVE map when its provider stream ended UNEXPECTEDLY (a
// crashed claude-code sidecar / exited CLI), but PRESERVES the durable record + handoff so it
// resurfaces as a stopped/restartable session instead of vanishing from every device. Contrast
// removeSession, which is the user-initiated permanent delete.
func (h *Hub) detachSession(id string, owner *managedSession) {
	h.mu.Lock()
	if owner != nil && h.sessions[id] != owner {
		h.mu.Unlock()
		return // a recovered/restarted binding already took this id — leave it live
	}
	_, existed := h.sessions[id]
	delete(h.sessions, id)
	h.mu.Unlock()
	if existed {
		// The binding is gone even though the record survives: its gateway token dies with it (a
		// restart mints a fresh one) and its unanswered approvals can never be answered.
		h.revokeMCPToken(id)
		h.sweepSessionApprovals(id)
		log.Printf("session %s: provider stream ended unexpectedly — kept as stopped/restartable (record preserved)", id)
		h.broadcastSessionList()
	}
}

// spawnChild creates a scoped sub-agent for one subtask of a parent session. The child is seeded
// with a compact prompt (subtask + a pointer to the parent's handoff/decision doc + an optional
// file allowlist) instead of the parent transcript, so it starts with minimal context. It runs in
// the parent's working directory and is linked back to the parent for grouping.
// spawnFanout starts N agents on the SAME prompt, each in its own git worktree/branch, tagged with
// one shared group id — the fan-out primitive: race several approaches, then compare and merge the
// winner. Each variant is an ordinary worktree session (so it already streams status, diffs, and
// finishes/PRs via the existing paths); the only new thing is the group tag. Returns the group id +
// the spawned session ids. Partial success is allowed (some variants may fail to start).
func (h *Hub) spawnFanout(ctx context.Context, req protocol.FanoutCreate) (protocol.FanoutResult, error) {
	// Two shapes share this path. A RACE (Prompt + Count) runs the same task N ways to compare
	// approaches; a DIVISION (Prompts) gives each agent a different subtask. Division is the more
	// useful shape for large work, and it reuses every piece of the race machinery — isolation,
	// completion tracking, aggregation — so it is a shorter path than a separate engine.
	prompts := make([]string, 0, len(req.Prompts))
	for _, p := range req.Prompts {
		if strings.TrimSpace(p) != "" {
			prompts = append(prompts, p)
		}
	}
	divided := len(prompts) > 0
	count := req.Count
	if divided {
		count = len(prompts)
	} else {
		if count < 2 {
			count = 2
		}
		if count > 6 {
			count = 6
		}
	}
	if count > maxFanoutVariants {
		return protocol.FanoutResult{}, fmt.Errorf("fan-out is capped at %d agents (asked for %d)", maxFanoutVariants, count)
	}
	if !divided && strings.TrimSpace(req.Prompt) == "" {
		return protocol.FanoutResult{}, fmt.Errorf("fan-out needs a prompt")
	}
	group := randToken()
	res := protocol.FanoutResult{Group: group}
	var firstErr error
	for i := 0; i < count; i++ {
		prompt := req.Prompt
		if divided {
			prompt = prompts[i]
		}
		create := protocol.SessionCreate{
			Provider:      req.Provider,
			ProjectID:     req.ProjectID,
			ProjectIDs:    req.ProjectIDs,
			Prompt:        prompt,
			Plan:          req.Plan,
			Worktree:      true, // each variant is isolated on its own branch
			WorkspaceName: fmt.Sprintf("fanout-%s-%d", group, i+1),
		}
		if i < len(req.Models) && req.Models[i] != "" {
			create.Model = req.Models[i]
		} else if len(req.Models) > 0 {
			create.Model = req.Models[i%len(req.Models)] // cycle if fewer models than variants
		}
		meta := sessionMeta{fanoutGroup: group, fanoutVariant: i}
		ms, err := h.startSession(ctx, create, meta, nil)
		if err != nil {
			log.Printf("fanout %s: variant %d FAILED to start: %v", group, i+1, err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		go ms.run()
		res.SessionIDs = append(res.SessionIDs, ms.sess.ID())
	}
	if len(res.SessionIDs) == 0 {
		return res, fmt.Errorf("fan-out: no variants started: %v", firstErr)
	}
	// Remember the prompt + judge intent for when the last variant settles.
	h.mu.Lock()
	if divided {
		h.fanoutPrompt[group] = fmt.Sprintf("%d subtasks divided across %d agents", len(prompts), len(res.SessionIDs))
	} else {
		h.fanoutPrompt[group] = req.Prompt
	}
	if req.Judge {
		h.fanoutJudge[group] = fanoutJudgeSpec{provider: req.Provider, projectID: req.ProjectID}
	}
	h.mu.Unlock()
	log.Printf("fanout %s: started %d/%d variants on prompt (%dB)", group, len(res.SessionIDs), count, len(req.Prompt))
	title := fmt.Sprintf("Fan-out: %d agents racing the same task", len(res.SessionIDs))
	if divided {
		title = fmt.Sprintf("Fan-out: %d subtasks running in parallel", len(res.SessionIDs))
	}
	h.recordActivity(activity.Event{Kind: activity.KindFanoutRun, Title: title})
	h.broadcastSessionList()
	return res, nil
}

// resolveFanout ends a fan-out: it tears down every variant in the group EXCEPT the kept winner
// (stop + server-side delete + worktree removal), so a decided race doesn't leave N orphaned
// worktrees/sessions accumulating. Best-effort per variant — a dirty worktree without Force lands in
// Failed rather than aborting the whole resolve.
func (h *Hub) resolveFanout(ctx context.Context, req protocol.FanoutResolve) protocol.FanoutResolved {
	out := protocol.FanoutResolved{Group: req.Group, Kept: req.Keep, Removed: []string{}}
	// Snapshot the group's variants under lock (teardown mutates h.sessions, so don't iterate it live).
	h.mu.Lock()
	var variants []*managedSession
	for _, m := range h.sessions {
		if m.meta.fanoutGroup == req.Group && m.sess.ID() != req.Keep {
			variants = append(variants, m)
		}
	}
	h.mu.Unlock()
	for _, m := range variants {
		if err := h.teardownWorktreeSession(ctx, m, req.Force); err != nil {
			log.Printf("fanout.resolve %s: variant %s teardown failed: %v", req.Group, m.sess.ID(), err)
			out.Failed = append(out.Failed, m.sess.ID())
			continue
		}
		out.Removed = append(out.Removed, m.sess.ID())
	}
	// The winner is no longer part of a race — drop its group tag so it reads as an ordinary session.
	if req.Keep != "" {
		if m := h.managed(req.Keep); m != nil {
			m.mu.Lock()
			m.meta.fanoutGroup = ""
			m.mu.Unlock()
			h.persistSession(m)
		}
	}
	// The group is over — drop its once-only notify marker so the map can't grow without bound across
	// a long-lived daemon's many fan-outs.
	h.mu.Lock()
	delete(h.fanoutNotified, req.Group)
	delete(h.fanoutJudge, req.Group)
	delete(h.fanoutPrompt, req.Group)
	h.mu.Unlock()
	log.Printf("fanout.resolve %s: removed %d, kept %q, failed %d", req.Group, len(out.Removed), req.Keep, len(out.Failed))
	return out
}

// forgetFanoutIfEmpty drops a group's notify marker once no session carries the tag any more — the
// path for groups that end by deleting every variant individually rather than via fanout.resolve.
// Caller must NOT hold h.mu.
func (h *Hub) forgetFanoutIfEmpty(group string) {
	if group == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, m := range h.sessions {
		if m.meta.fanoutGroup == group {
			return
		}
	}
	delete(h.fanoutNotified, group)
	delete(h.fanoutJudge, group)
	delete(h.fanoutPrompt, group)
}

// checkFanoutDone fires the "fan-out finished" push once, when EVERY variant in the group has reached
// idle/done. Called when any grouped session goes idle.
func (h *Hub) checkFanoutDone(group string) {
	if group == "" {
		return
	}
	h.mu.Lock()
	if h.fanoutNotified[group] {
		h.mu.Unlock()
		return
	}
	var members []*managedSession
	for _, m := range h.sessions {
		if m.meta.fanoutGroup == group {
			members = append(members, m)
		}
	}
	allIdle := len(members) > 0
	for _, m := range members {
		m.mu.Lock()
		st := m.lastStatus
		m.mu.Unlock()
		if st != protocol.StatusIdle && st != protocol.StatusDone {
			allIdle = false
			break
		}
	}
	if allIdle {
		h.fanoutNotified[group] = true
	}
	count := len(members)
	h.mu.Unlock()
	if allIdle {
		h.pushFanoutDone(group, count)
		// Aggregate: assemble the per-variant comparison so the user opens ONE screen instead of N
		// sessions. Off the hub goroutine because it shells out to git per worktree.
		go h.broadcastFanoutSummary(group)
	}
}

// teardownWorktreeSession stops a worktree session, deletes it server-side, and removes its worktree —
// the shared teardown behind fanout.resolve. Returns the worktree-removal error (e.g. a dirty tree
// without force) so the caller can report a partial failure; everything else is best-effort.
func (h *Hub) teardownWorktreeSession(ctx context.Context, m *managedSession, force bool) error {
	m.markUserStopped() // intentional delete → run() drops the durable record (not a crash to preserve)
	_ = m.sess.Stop(ctx)
	if d, ok := m.sess.(agent.Deleter); ok {
		_ = d.Delete(ctx) // server-side delete so the variant can't be re-attached/re-discovered
	}
	_ = m.sess.Close()
	if len(m.meta.members) > 0 {
		if err := worktree.RemoveWorkspace(m.meta.cwd, m.meta.members, force); err != nil {
			return err
		}
		for _, mem := range m.meta.members {
			_ = worktree.Prune(mem.RepoRoot)
		}
	} else if m.meta.worktreePath != "" {
		if err := worktree.Remove(m.meta.repoRoot, m.meta.worktreePath, force); err != nil {
			return err
		}
		_ = worktree.Prune(m.meta.repoRoot)
	}
	h.releasePort(m.meta.port)
	h.removeSession(m.sess.ID(), m)
	return nil
}

// spawnRemote starts an agent session ON a remote host over SSH. It reuses the generic CLI provider
// with Command="ssh": the remote invocation is `cd <path> && <agentCommand> {prompt}`, so the remote
// agent's stdout streams back through the normal session machinery (OSC status, etc.). The remote
// box must have the agent installed and key-based ssh set up (BatchMode).
func (h *Hub) spawnRemote(ctx context.Context, req protocol.RemoteRun) (*managedSession, error) {
	h.mu.Lock()
	reg := h.remotes
	runner := h.sshRunner
	h.mu.Unlock()
	if reg == nil || runner == nil {
		return nil, fmt.Errorf("remotes unavailable")
	}
	hst, ok := reg.Get(req.HostID)
	if !ok {
		return nil, fmt.Errorf("no such remote host")
	}
	if strings.TrimSpace(req.AgentCommand) == "" {
		return nil, fmt.Errorf("remote run needs an agent command")
	}
	// Build: ssh <opts> <forwards> <target> "cd '<path>' && <agentCommand> {prompt}". The CLI
	// provider substitutes {prompt} into the final arg per turn. Port forwards (from the host) tunnel
	// a remote dev server to localhost so Design Mode / a browser can reach it during the run.
	remoteCmd := req.AgentCommand + " {prompt}"
	if hst.RemotePath != "" {
		remoteCmd = "cd '" + strings.ReplaceAll(hst.RemotePath, "'", `'\''`) + "' && " + remoteCmd
	}
	args := runner.SSHArgv(hst, remoteCmd)
	prov := cli.NewProvider(cli.Config{Name: "ssh:" + hst.Name, Command: "ssh", Args: args})

	sess, err := prov.Create(ctx, "", req.Prompt)
	if err != nil {
		return nil, fmt.Errorf("remote run: %v", err)
	}
	m := h.addSession(sess, sessionMeta{label: "remote: " + hst.Name, cwd: hst.RemotePath})
	log.Printf("remote.run: started agent %q on %s (%s)", req.AgentCommand, hst.Name, hst.SSHTarget)
	h.recordActivity(activity.Event{Kind: activity.KindStarted, SessionID: sess.ID(),
		Title: "Remote agent started on " + hst.Name})
	return m, nil
}

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

// pendingApproval is everything a later ALWAYS answer needs to build a SCOPED rule. Keeping only the
// tool name (the original behavior) meant "Always" could never mean anything narrower than
// "every bash command forever" — the whole request has to survive until the user answers.
type pendingApproval struct {
	req       protocol.ApprovalRequest
	provider  string
	projectID string
}

func (h *Hub) recordApproval(ar protocol.ApprovalRequest, m *managedSession) {
	m.mu.Lock()
	projectID := m.meta.projectID
	m.mu.Unlock()
	h.mu.Lock()
	h.approvals[ar.ApprovalID] = m
	if h.approvalReqs == nil {
		h.approvalReqs = map[string]pendingApproval{}
	}
	h.approvalReqs[ar.ApprovalID] = pendingApproval{req: ar, provider: m.sess.Provider(), projectID: projectID}
	h.mu.Unlock()
}

// Register adds a provider (keyed by Name()).
func (h *Hub) Register(p agent.Provider) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.providers[p.Name()] = p
}

// Unregister removes a provider by name (used when a custom agent is deleted).
func (h *Hub) Unregister(name string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.providers, name)
}

// SetAgentsPath records where custom CLI agents (~/.oculus/agents.json) and picker-visibility
// prefs (~/.oculus/agent-visibility.json) are persisted, enabling the agent.* management endpoints.
func (h *Hub) SetAgentsPath(agents, visibility string) {
	hidden := loadHiddenSet(visibility)
	h.mu.Lock()
	h.agentsPath = agents
	h.agentHidePath = visibility
	h.agentHidden = hidden
	h.mu.Unlock()
}

func loadHiddenSet(path string) map[string]bool {
	out := map[string]bool{}
	if path == "" {
		return out
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	var names []string
	if json.Unmarshal(data, &names) == nil {
		for _, n := range names {
			out[n] = true
		}
	}
	return out
}

// saveHiddenSet writes the current hidden-name set (caller holds no lock). 0600.
func (h *Hub) saveHiddenSet() {
	h.mu.Lock()
	path := h.agentHidePath
	names := make([]string, 0, len(h.agentHidden))
	for n, on := range h.agentHidden {
		if on {
			names = append(names, n)
		}
	}
	h.mu.Unlock()
	if path == "" {
		return
	}
	sort.Strings(names)
	if data, err := json.MarshalIndent(names, "", "  "); err == nil {
		_ = os.WriteFile(path, data, 0o600)
	}
}

// nativeAgents are the rich first-class integrations — not editable/removable as "custom" agents.
var nativeAgents = map[string]bool{"opencode": true, "claude-code": true, "pi": true}

// agentList builds the full agent roster: every registered provider plus any user-defined agent
// whose command isn't currently on PATH (so it's still visible/editable), classified by kind.
func (h *Hub) agentList() protocol.AgentList {
	h.mu.Lock()
	registered := make(map[string]bool, len(h.providers))
	for n := range h.providers {
		registered[n] = true
	}
	path := h.agentsPath
	h.mu.Unlock()

	userCfgs, _ := cli.Load(path)
	userByName := make(map[string]cli.Config, len(userCfgs))
	for _, c := range userCfgs {
		userByName[c.Name] = c
	}
	builtinByName := map[string]cli.Config{}
	for _, c := range cli.Builtins() {
		builtinByName[c.Name] = c
	}

	names := map[string]bool{}
	for n := range registered {
		names[n] = true
	}
	for n := range userByName {
		names[n] = true // include user agents even if their command isn't installed
	}

	h.mu.Lock()
	hidden := make(map[string]bool, len(h.agentHidden))
	for n, on := range h.agentHidden {
		hidden[n] = on
	}
	h.mu.Unlock()

	out := make([]protocol.AgentInfo, 0, len(names))
	for name := range names {
		info := protocol.AgentInfo{Name: name, Available: registered[name], Hidden: hidden[name]}
		switch {
		case nativeAgents[name]:
			info.Kind = "native"
		case userByName[name].Name != "":
			c := userByName[name]
			info.Kind = "custom"
			info.Editable = true
			info.Command, info.Args, info.ResumeArgs, info.Models = c.Command, c.Args, c.ResumeArgs, c.Models
			info.Env = c.Env
			if !info.Available {
				info.Available = cli.Available(c.Command)
			}
		default:
			info.Kind = "detected"
			if c, ok := builtinByName[name]; ok {
				info.Command, info.Args = c.Command, c.Args
			}
		}
		out = append(out, info)
	}
	// Stable order: native, detected, custom; then by name.
	rank := map[string]int{"native": 0, "detected": 1, "custom": 2}
	sort.Slice(out, func(i, j int) bool {
		if rank[out[i].Kind] != rank[out[j].Kind] {
			return rank[out[i].Kind] < rank[out[j].Kind]
		}
		return out[i].Name < out[j].Name
	})
	return protocol.AgentList{Agents: out}
}

// broadcastProviders pushes the updated provider set to every client (after an agent add/remove).
func (h *Hub) broadcastProviders() {
	h.broadcast(protocol.TypeProviderList, protocol.ProviderList{Providers: h.providerNames()})
}

// providerNames returns the VISIBLE registered provider names, sorted, for the app's session picker.
// Agents the user hid are omitted here (they stay in h.providers and remain runnable) so the picker
// stays short; the full roster is available via agent.list.
func (h *Hub) providerNames() []string {
	h.mu.Lock()
	names := make([]string, 0, len(h.providers))
	for name := range h.providers {
		if h.agentHidden[name] {
			continue
		}
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
	// An approval request is the canonical "needs you" signal — surface it in the Activity inbox.
	title := "Agent needs approval"
	if m := h.managed(ar.SessionID); m != nil {
		title = m.activityTitle() + " needs approval"
	}
	h.recordActivity(activity.Event{
		Kind: activity.KindNeedsInput, SessionID: ar.SessionID, Title: title, Detail: ar.Tool, NeedsYou: true,
	})
}

// pushAgentFinished notifies that an agent turn completed, with a compact summary of the run
// (duration · to-do progress · spend) instead of a bare "done".
func (h *Hub) pushAgentFinished(sessionID, label string, dur time.Duration, todosDone, todosTotal int, cost float64) {
	title := "Agent finished"
	if label != "" {
		title = label + " finished"
	}
	// Body: "5/5 tasks · 3m12s · $0.42" — only the parts we actually have.
	var parts []string
	if todosTotal > 0 {
		parts = append(parts, fmt.Sprintf("%d/%d tasks", todosDone, todosTotal))
	}
	if dur >= time.Second {
		parts = append(parts, dur.Round(time.Second).String())
	}
	if cost > 0 {
		parts = append(parts, fmt.Sprintf("$%.2f", cost))
	}
	body := "Tap to review"
	if len(parts) > 0 {
		body = strings.Join(parts, " · ") + " — tap to review"
	}
	h.pushNotify(push.Notification{
		Title: title, Body: body, Category: "AGENT_FINISHED", Wake: true,
		ThreadID: sessionID, Custom: map[string]any{"session_id": sessionID},
	})
}

// pushPRFinished notifies that a session opened a PR / finished its worktree branch — a real
// end-of-task milestone, not just a turn ending.
func (h *Hub) pushPRFinished(sessionID, label, prURL string) {
	title := "PR ready"
	if label != "" {
		title = label + ": PR ready"
	}
	body := "The agent opened a pull request — tap to review"
	if prURL != "" {
		body = prURL
	}
	h.pushNotify(push.Notification{
		Title: title, Body: body, Category: "PR_FINISHED",
		ThreadID: sessionID, Custom: map[string]any{"session_id": sessionID, "url": prURL},
	})
}

// pushFanoutDone notifies that every agent in a fan-out group has completed.
func (h *Hub) pushFanoutDone(group string, count int) {
	h.pushNotify(push.Notification{
		Title:    "Fan-out finished",
		Body:     fmt.Sprintf("All %d agents are done — tap to compare and merge the winner", count),
		Category: "FANOUT_DONE",
		ThreadID: "fanout-" + group, Custom: map[string]any{"fanout_group": group},
	})
}

// pushLoopDone notifies that an autonomous loop run completed.
func (h *Hub) pushLoopDone(sessionID, loopName string) {
	title := "Loop run finished"
	if loopName != "" {
		title = loopName + ": run finished"
	}
	h.pushNotify(push.Notification{
		Title: title, Body: "An autonomous loop run completed — tap to review", Category: "LOOP_DONE",
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
	off := h.notifyOff[notif.Category] // user turned this category off
	h.mu.Unlock()
	if off {
		return // this notification type is disabled in the user's Notifications settings
	}
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
	// A guest who came in through an invite starts in that invite's role; the owner's own devices
	// start as owner. Resolved from the handshake public key, so it can't be spoofed by a client
	// simply asserting a role.
	h.roles.setRole(conn, h.roleForConn(conn.PeerPublicKey()))
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
		protocol.TypeFanoutCreate,          // N× worktree.Create + provider Create (fan-out)
		protocol.TypeFanoutResolve,         // N× provider Stop/Delete + git worktree remove/prune
		protocol.TypeCheckpointCreate,      // git snapshot (blocking)
		protocol.TypeCheckpointRestore,     // git checkout (blocking)
		protocol.TypeRemoteList,            // ssh probe per host (network)
		protocol.TypeRemoteUpsert,          // ssh probe (network)
		protocol.TypeRemoteStatus,          // ssh git status/diff (network)
		protocol.TypeRemoteRun,             // ssh agent session start (network)
		protocol.TypeAccountQuota,          // provider API quota probe (network)
		protocol.TypeProviderRefresh,       // re-detect harnesses on PATH (may start opencode)
		protocol.TypeIssueLaunch,           // same startSession path as create
		protocol.TypeWorktreeDiff,          // git diff
		protocol.TypeWorktreeRemove,        // provider Stop/Close + git remove/prune
		protocol.TypeWorktreePR,            // git CommitAll/Push/CreatePR
		protocol.TypeWorktreeCatchUp,       // git fetch + merge default branch
		protocol.TypeWorktreeConflicts,     // git per-worktree ChangedFiles
		protocol.TypeWorkspaceDiff,         // git diff per workspace member
		protocol.TypeWorkspacePR,           // git commit/push/PR per workspace member
		protocol.TypeSessionChild,          // provider Create for a scoped sub-agent
		protocol.TypeIntegrationConnect,    // tracker HTTP
		protocol.TypeIntegrationDisconnect, // writes integrations.json + refresh
		protocol.TypeIntegrationOAuthApp,   // writes integrations.json
		protocol.TypeIntegrationOAuth,      // tracker HTTP
		protocol.TypeJiraSites,             // tracker HTTP (accessible-resources)
		protocol.TypeJiraSetSite,           // tracker HTTP (switch site + refresh)
		protocol.TypeIssueStates,           // tracker HTTP
		protocol.TypeIssueColumns,          // tracker HTTP
		protocol.TypeIssueMove,             // tracker HTTP (resolve + transition + re-fetch)
		protocol.TypeIssueCreate,           // tracker HTTP
		protocol.TypeIssueProjects,         // tracker HTTP (per connected provider)
		protocol.TypeIssueDetail,           // tracker HTTP
		protocol.TypeIssueUpdate,           // tracker HTTP
		protocol.TypeIssueMembers,          // tracker HTTP (assignee picker)
		protocol.TypeIssueLabels,           // tracker HTTP (label picker)
		protocol.TypeIssueCycles,           // tracker HTTP (sprint picker)
		protocol.TypeIssueComment,          // tracker HTTP
		protocol.TypeIssueCommentEdit,      // tracker HTTP
		protocol.TypeIssueImage,            // tracker HTTP (image fetch)
		protocol.TypeSessionPrompt,         // provider prompt (may be network)
		protocol.TypeApprovalRespond,       // provider Respond (may be network)
		protocol.TypeSessionAttach,         // provider Attach
		protocol.TypeSessionRestart,        // provider Create (re-create a stopped session)
		protocol.TypeSessionRecover,        // provider Attach (re-attach + heal a broken session's directory)
		protocol.TypeSessionStop,           // provider Stop
		protocol.TypeSessionInterrupt,      // provider Stop (interrupt only)
		protocol.TypeProjectBrowse,         // disk: dir listing for the folder picker
		protocol.TypeCommandList,           // disk: scans .claude/commands for the slash palette
		protocol.TypeLoopUpsert,            // disk: persists loop config (+ may spawn a session)
		protocol.TypeLoopDelete,            // disk: persists loop config
		protocol.TypeLoopSetEnabled,        // disk: persists loop config
		protocol.TypeAgentList,             // disk: reads ~/.oculus/agents.json
		protocol.TypeAgentUpsert,           // disk: persists a custom agent
		protocol.TypeAgentDelete,           // disk: persists a custom agent
		protocol.TypeAgentVisible,          // disk: persists picker visibility
		protocol.TypeApprovalRuleDelete,    // disk: persists ~/.oculus/approval-rules.json
		protocol.TypeModelList,             // network: queries the provider for models
		protocol.TypeSessionSetModel,       // network: switches a session's model
		protocol.TypeFSTree,                // disk: dir listing
		protocol.TypeFSRead,                // disk: file read
		protocol.TypeFSReadBytes,           // disk: raw bytes (images)
		protocol.TypeFSWrite,               // disk: file write
		protocol.TypeFSDiff,                // git diff
		protocol.TypeFSSearch,              // disk: multi-file search
		protocol.TypeRunTest,               // run tests/build (subprocess)
		protocol.TypeHandoffList,           // disk-backed store read
		protocol.TypeLSPReferences,         // language server: references
		protocol.TypeLSPRename,             // language server: rename
		protocol.TypeLSPSymbols,            // language server: document symbols
		protocol.TypeLSPOpen,               // language server: didOpen
		protocol.TypeLSPChange,             // language server: didChange
		protocol.TypeLSPClose,              // language server: didClose
		protocol.TypeLSPHover,              // language server: hover
		protocol.TypeLSPDefinition,         // language server: definition
		protocol.TypeLSPComplete,           // language server: completion
		protocol.TypeLSPFormat,             // language server: format document
		protocol.TypeLSPServerInfo,         // language server: install status
		protocol.TypeLSPInstall,            // language server: install (runs a package manager)
		protocol.TypeDiscover:              // host scan
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

	// nameMu guards name, which a client sets once via client.identify. It is the ATTRIBUTION for
	// everything this connection does — whose phone or Mac sent a prompt. With several devices (or,
	// later, several people) on one session, an unattributed transcript is genuinely confusing:
	// you cannot tell your own message from someone else's.
	nameMu sync.Mutex
	name   string
}

func (c *hubClient) setName(n string) {
	c.nameMu.Lock()
	c.name = n
	c.nameMu.Unlock()
}

func (c *hubClient) displayName() string {
	c.nameMu.Lock()
	defer c.nameMu.Unlock()
	return c.name
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
	delete(h.logSubs, conn)
	h.mu.Unlock()
	h.roles.forget(conn) // a role belongs to a live connection, not a name
	if c != nil {
		c.close()
		h.broadcastParticipants() // presence: everyone sees who left
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
		// Bound the whole create so a hung worktree/provider start surfaces as an error the app can
		// show, instead of leaving it forever on the "starting session" spinner.
		cctx, ccancel := context.WithTimeout(ctx, 3*time.Minute)
		tc0 := time.Now()
		// Stream create steps to THIS client so its loading screen shows a prescriptive checklist.
		prog := func(stage, detail string, step, total int) {
			h.sendEvent(conn, protocol.TypeSessionProgress, protocol.SessionProgress{Stage: stage, Detail: detail, Step: step, Total: total})
		}
		m, err := h.startSession(cctx, req, sessionMeta{}, prog)
		ccancel()
		if t := h.tel(); t != nil {
			ev := "session.create"
			if len(req.ProjectIDs) > 1 {
				ev = "session.create.multirepo" // the case that hung — track it distinctly
			}
			t.Record(ev, req.Provider, time.Since(tc0), err)
		}
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

	case protocol.TypeFanoutCreate:
		var req protocol.FanoutCreate
		if err := env.Unmarshal(&req); err != nil {
			h.sendErr(conn, env.ID, "bad fanout.create")
			return
		}
		res, err := h.spawnFanout(ctx, req) // runs each variant internally
		if err != nil {
			h.sendErr(conn, env.ID, err.Error())
			return
		}
		h.sendOK(conn, env.ID, res)

	case protocol.TypeFanoutResolve:
		var req protocol.FanoutResolve
		if err := env.Unmarshal(&req); err != nil || req.Group == "" {
			h.sendErr(conn, env.ID, "bad fanout.resolve")
			return
		}
		h.sendOK(conn, env.ID, h.resolveFanout(ctx, req))
		h.broadcastSessionList() // the discarded variants are gone → refresh every client's list

	case protocol.TypeNotifyPrefsGet:
		h.sendOK(conn, env.ID, h.notifyPrefs())

	case protocol.TypeNotifyPrefsSet:
		var req protocol.NotifyPrefSet
		if err := env.Unmarshal(&req); err != nil || req.Key == "" {
			h.sendErr(conn, env.ID, "bad notify.prefs.set")
			return
		}
		h.setNotifyPref(req.Key, req.Enabled)
		h.sendOK(conn, env.ID, h.notifyPrefs())

	case protocol.TypeUsageReport:
		h.sendOK(conn, env.ID, h.usageReport())

	case protocol.TypeTranscriptPage:
		var req protocol.TranscriptPage
		if err := env.Unmarshal(&req); err != nil {
			h.sendErr(conn, env.ID, "bad transcript.page")
			return
		}
		m := h.managed(req.SessionID)
		if m == nil {
			h.sendErr(conn, env.ID, "no such session")
			return
		}
		m.sendHistoryPage(conn, req.Loaded, req.Limit)
		h.sendOK(conn, env.ID, nil)

	case protocol.TypeSessionModeSet:
		var req protocol.SessionModeSet
		if err := env.Unmarshal(&req); err != nil {
			h.sendErr(conn, env.ID, "bad session.mode.set")
			return
		}
		m := h.managed(req.SessionID)
		if m == nil {
			h.sendErr(conn, env.ID, "no such session")
			return
		}
		h.setSessionMode(ctx, m, req.Mode)
		h.sendOK(conn, env.ID, nil)
		h.broadcastSessionList() // the mode chip is part of the session row

	case protocol.TypeMCPList, protocol.TypeMCPUpsert, protocol.TypeMCPDelete,
		protocol.TypeMCPEnable, protocol.TypeMCPCheck, protocol.TypeMCPBrowse,
		protocol.TypeMCPDiscover, protocol.TypeMCPImport, protocol.TypeMCPExclusive:
		h.handleMCP(ctx, conn, env)

	case protocol.TypeApprovalRulesList:
		h.sendOK(conn, env.ID, h.approvalRulesList())

	case protocol.TypeApprovalRuleDelete:
		var req protocol.ApprovalRuleDelete
		if err := env.Unmarshal(&req); err != nil {
			h.sendErr(conn, env.ID, "bad approval.rules.delete")
			return
		}
		if !h.deleteApprovalRule(req.Index) {
			h.sendErr(conn, env.ID, "no such rule")
			return
		}
		list := h.approvalRulesList()
		h.sendOK(conn, env.ID, list)
		// Broadcast so a second device's rules screen updates live instead of going stale — the
		// mistake agent.list and notify.prefs.set both make.
		h.broadcast(protocol.TypeApprovalRulesChanged, list)

	case protocol.TypeCheckpointCreate:
		var req protocol.CheckpointCreate
		_ = env.Unmarshal(&req)
		m := h.managed(req.SessionID)
		if m == nil || m.meta.worktreePath == "" {
			h.sendErr(conn, env.ID, "checkpoints need a worktree session")
			return
		}
		sha, err := worktree.Snapshot(ctx, m.meta.worktreePath)
		if err != nil {
			h.sendErr(conn, env.ID, err.Error())
			return
		}
		cp := protocol.Checkpoint{SHA: sha, Label: req.Label, TS: time.Now().Unix()}
		m.mu.Lock()
		m.checkpoints = append(m.checkpoints, cp)
		list := append([]protocol.Checkpoint(nil), m.checkpoints...)
		m.mu.Unlock()
		log.Printf("session %s: checkpoint saved (%s)", req.SessionID, sha[:min(len(sha), 8)])
		h.sendOK(conn, env.ID, protocol.CheckpointList{Checkpoints: reverseCheckpoints(list)})

	case protocol.TypeCheckpointList:
		var req protocol.SessionRef
		_ = env.Unmarshal(&req)
		m := h.managed(req.SessionID)
		if m == nil {
			h.sendErr(conn, env.ID, "no such session")
			return
		}
		m.mu.Lock()
		list := append([]protocol.Checkpoint(nil), m.checkpoints...)
		m.mu.Unlock()
		h.sendOK(conn, env.ID, protocol.CheckpointList{Checkpoints: reverseCheckpoints(list)})

	case protocol.TypeCheckpointRestore:
		var req protocol.CheckpointRestore
		_ = env.Unmarshal(&req)
		m := h.managed(req.SessionID)
		if m == nil || m.meta.worktreePath == "" {
			h.sendErr(conn, env.ID, "checkpoints need a worktree session")
			return
		}
		if err := worktree.RestoreSnapshot(ctx, m.meta.worktreePath, req.SHA); err != nil {
			h.sendErr(conn, env.ID, err.Error())
			return
		}
		log.Printf("session %s: rolled back to checkpoint %s", req.SessionID, req.SHA[:min(len(req.SHA), 8)])
		h.sendOK(conn, env.ID, nil)

	case protocol.TypeAccountList:
		h.sendOK(conn, env.ID, h.accountList())

	case protocol.TypeAccountUpsert:
		var a protocol.Account
		_ = env.Unmarshal(&a)
		h.mu.Lock()
		reg := h.accounts
		h.mu.Unlock()
		if reg == nil {
			h.sendErr(conn, env.ID, "accounts unavailable")
			return
		}
		reg.Upsert(accounts.Account{ID: a.ID, Provider: a.Provider, Name: a.Name, Env: a.Env})
		h.sendOK(conn, env.ID, h.accountList())

	case protocol.TypeAccountDelete:
		var req protocol.AccountRef
		_ = env.Unmarshal(&req)
		h.mu.Lock()
		reg := h.accounts
		h.mu.Unlock()
		if reg != nil {
			reg.Delete(req.AccountID)
		}
		h.sendOK(conn, env.ID, h.accountList())

	case protocol.TypeAccountActivate:
		var req protocol.AccountActivate
		_ = env.Unmarshal(&req)
		h.mu.Lock()
		reg := h.accounts
		h.mu.Unlock()
		if reg == nil || !reg.SetActive(req.Provider, req.AccountID) {
			h.sendErr(conn, env.ID, "no such account for that provider")
			return
		}
		log.Printf("account: %s active account switched to %s", req.Provider, req.AccountID)
		h.sendOK(conn, env.ID, h.accountList())

	case protocol.TypeAccountQuota:
		var req protocol.AccountRef
		_ = env.Unmarshal(&req)
		h.mu.Lock()
		reg := h.accounts
		h.mu.Unlock()
		out := protocol.AccountQuota{AccountID: req.AccountID}
		if reg == nil {
			out.Note = "accounts unavailable"
			h.sendOK(conn, env.ID, out)
			return
		}
		a, ok := reg.Get(req.AccountID)
		if !ok {
			h.sendErr(conn, env.ID, "no such account")
			return
		}
		q, err := quota.New().Probe(ctx, a.Provider, a.Env)
		out.Available = q.Available
		out.RequestsRemaining = q.RequestsRemaining
		out.TokensRemaining = q.TokensRemaining
		out.Note = q.Note
		if !q.ResetAt.IsZero() {
			if secs := int(time.Until(q.ResetAt).Seconds()); secs > 0 {
				out.ResetInSeconds = secs
			}
		}
		if err != nil && out.Note == "" {
			out.Note = err.Error()
		}
		h.sendOK(conn, env.ID, out)

	case protocol.TypeRemoteList:
		h.sendOK(conn, env.ID, h.remoteList(ctx))

	case protocol.TypeRemoteUpsert:
		var hst protocol.RemoteHost
		_ = env.Unmarshal(&hst)
		h.mu.Lock()
		reg := h.remotes
		h.mu.Unlock()
		if reg == nil {
			h.sendErr(conn, env.ID, "remotes unavailable")
			return
		}
		var fwds []sshremote.PortForward
		for _, f := range hst.Forwards {
			fwds = append(fwds, sshremote.PortForward{LocalPort: f.LocalPort, RemotePort: f.RemotePort})
		}
		reg.Upsert(sshremote.Host{ID: hst.ID, Name: hst.Name, SSHTarget: hst.SSHTarget, RemotePath: hst.RemotePath, Forwards: fwds})
		h.sendOK(conn, env.ID, h.remoteList(ctx))

	case protocol.TypeRemoteDelete:
		var req protocol.RemoteRef
		_ = env.Unmarshal(&req)
		h.mu.Lock()
		reg := h.remotes
		h.mu.Unlock()
		if reg != nil {
			reg.Delete(req.ID)
		}
		h.sendOK(conn, env.ID, h.remoteList(ctx))

	case protocol.TypeRemoteStatus:
		var req protocol.RemoteRef
		_ = env.Unmarshal(&req)
		h.mu.Lock()
		reg := h.remotes
		runner := h.sshRunner
		h.mu.Unlock()
		if reg == nil || runner == nil {
			h.sendErr(conn, env.ID, "remotes unavailable")
			return
		}
		hst, ok := reg.Get(req.ID)
		if !ok {
			h.sendErr(conn, env.ID, "no such remote")
			return
		}
		res := protocol.RemoteStatus{ID: req.ID}
		if st, err := runner.GitStatus(ctx, hst); err != nil {
			res.Error = err.Error()
		} else {
			res.Status = st
			res.Diff, _ = runner.GitDiff(ctx, hst)
		}
		h.sendOK(conn, env.ID, res)

	case protocol.TypeRemoteRun:
		var req protocol.RemoteRun
		_ = env.Unmarshal(&req)
		m, err := h.spawnRemote(ctx, req)
		if err != nil {
			h.sendErr(conn, env.ID, err.Error())
			return
		}
		h.sendOK(conn, env.ID, m.info())
		m.subscribe(conn)
		go m.run()

	case protocol.TypeSessionList:
		h.mu.Lock()
		list := make([]protocol.Session, 0, len(h.sessions))
		for _, m := range h.sessions {
			list = append(list, m.info())
		}
		h.mu.Unlock()
		// Also surface persisted sessions the daemon couldn't re-attach after a restart, as
		// "stopped" + restartable — so they don't silently vanish from the sidebar.
		list = append(list, h.stoppedSessions()...)
		h.sendOK(conn, env.ID, protocol.SessionList{Sessions: list})

	case protocol.TypeProviderList:
		h.sendOK(conn, env.ID, protocol.ProviderList{Providers: h.providerNames()})

	case protocol.TypeProviderRefresh:
		h.mu.Lock()
		redetect := h.redetect
		h.mu.Unlock()
		if redetect != nil {
			log.Printf("provider.refresh: re-detecting agent harnesses on PATH")
			redetect() // re-runs detection; Register overwrites by name (idempotent)
		}
		h.broadcastProviders() // push the (possibly new) set to every client
		h.sendOK(conn, env.ID, protocol.ProviderList{Providers: h.providerNames()})

	case protocol.TypeModelList:
		var req protocol.ModelListReq
		_ = env.Unmarshal(&req)
		providerName := req.Provider
		current := ""
		if req.SessionID != "" {
			if m := h.managed(req.SessionID); m != nil {
				providerName = m.sess.Provider()
				m.mu.Lock()
				current = m.model
				m.mu.Unlock()
			}
		}
		h.mu.Lock()
		p := h.providers[providerName]
		h.mu.Unlock()
		lister, ok := p.(agent.ModelLister)
		if !ok {
			// Provider doesn't expose models — agent-managed. Empty + not editable.
			h.sendOK(conn, env.ID, protocol.ModelList{Editable: false})
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		models, err := lister.Models(ctx)
		cancel()
		if err != nil {
			h.sendErr(conn, env.ID, "list models: "+err.Error())
			return
		}
		h.sendOK(conn, env.ID, protocol.ModelList{Models: models, Current: current, Editable: true})

	case protocol.TypeSessionSetModel:
		var req protocol.SessionSetModel
		if err := env.Unmarshal(&req); err != nil || req.SessionID == "" {
			h.sendErr(conn, env.ID, "bad set-model")
			return
		}
		m := h.managed(req.SessionID)
		if m == nil {
			h.sendErr(conn, env.ID, "session not found")
			return
		}
		setter, ok := m.sess.(agent.ModelSetter)
		if !ok {
			h.sendErr(conn, env.ID, "this agent's model can't be switched here")
			return
		}
		if err := setter.SetModel(req.Provider, req.Model); err != nil {
			h.sendErr(conn, env.ID, "set model: "+err.Error())
			return
		}
		m.mu.Lock()
		m.model, m.modelProvider = req.Model, req.Provider
		m.mu.Unlock()
		h.broadcastSessionList() // reflect the new model on the session everywhere
		h.sendOK(conn, env.ID, protocol.SessionSetModel{SessionID: req.SessionID, Model: req.Model, Provider: req.Provider})

	case protocol.TypeAgentList:
		h.sendOK(conn, env.ID, h.agentList())

	case protocol.TypeAgentUpsert:
		var in protocol.AgentUpsert
		if err := env.Unmarshal(&in); err != nil {
			h.sendErr(conn, env.ID, "bad agent")
			return
		}
		in.Name = strings.TrimSpace(in.Name)
		in.Command = strings.TrimSpace(in.Command)
		if in.Name == "" || in.Command == "" {
			h.sendErr(conn, env.ID, "agent needs a name and a command")
			return
		}
		if nativeAgents[in.Name] {
			h.sendErr(conn, env.ID, "'"+in.Name+"' is a built-in agent and can't be overridden")
			return
		}
		h.mu.Lock()
		path := h.agentsPath
		h.mu.Unlock()
		if path == "" {
			h.sendErr(conn, env.ID, "custom agents not enabled")
			return
		}
		cfg := cli.Config{Name: in.Name, Command: in.Command, Args: in.Args, ResumeArgs: in.ResumeArgs, Env: in.Env, Models: in.Models}
		if len(cfg.Models) > 0 && cfg.Model == "" {
			cfg.Model = cfg.Models[0] // default selection
		}
		if len(cfg.Args) == 0 {
			cfg.Args = []string{"{prompt}"} // sane default: pass the prompt as the sole arg
		}
		h.agentsFileMu.Lock()
		existing, _ := cli.Load(path)
		replaced := false
		for i := range existing {
			if existing[i].Name == cfg.Name {
				existing[i] = cfg
				replaced = true
				break
			}
		}
		if !replaced {
			existing = append(existing, cfg)
		}
		err := cli.Save(path, existing)
		h.agentsFileMu.Unlock()
		if err != nil {
			h.sendErr(conn, env.ID, "save agents: "+err.Error())
			return
		}
		h.Register(cli.NewProvider(cfg)) // live: shows up in provider.list immediately
		h.broadcastProviders()
		h.sendOK(conn, env.ID, h.agentList())

	case protocol.TypeAgentDelete:
		var ref protocol.AgentRef
		if err := env.Unmarshal(&ref); err != nil || strings.TrimSpace(ref.Name) == "" {
			h.sendErr(conn, env.ID, "bad agent ref")
			return
		}
		if nativeAgents[ref.Name] {
			h.sendErr(conn, env.ID, "can't remove a built-in agent")
			return
		}
		h.mu.Lock()
		path := h.agentsPath
		h.mu.Unlock()
		h.agentsFileMu.Lock()
		existing, _ := cli.Load(path)
		kept := existing[:0]
		for _, c := range existing {
			if c.Name != ref.Name {
				kept = append(kept, c)
			}
		}
		err := cli.Save(path, kept)
		h.agentsFileMu.Unlock()
		if err != nil {
			h.sendErr(conn, env.ID, "save agents: "+err.Error())
			return
		}
		h.Unregister(ref.Name)
		h.mu.Lock()
		delete(h.agentHidden, ref.Name)
		h.mu.Unlock()
		h.saveHiddenSet()
		// If the removed name shadowed an auto-detected built-in that's still installed, restore it.
		for _, b := range cli.Builtins() {
			if b.Name == ref.Name && cli.Available(b.Command) {
				h.Register(cli.NewProvider(b))
			}
		}
		h.broadcastProviders()
		h.sendOK(conn, env.ID, h.agentList())

	case protocol.TypeAgentVisible:
		var v protocol.AgentVisible
		if err := env.Unmarshal(&v); err != nil || strings.TrimSpace(v.Name) == "" {
			h.sendErr(conn, env.ID, "bad visibility request")
			return
		}
		h.mu.Lock()
		if h.agentHidden == nil {
			h.agentHidden = map[string]bool{}
		}
		if v.Visible {
			delete(h.agentHidden, v.Name)
		} else {
			h.agentHidden[v.Name] = true
		}
		h.mu.Unlock()
		h.saveHiddenSet()
		h.broadcastProviders() // pickers refresh to the new visible set
		h.sendOK(conn, env.ID, h.agentList())

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

	case protocol.TypeProjectBrowse:
		var req protocol.ProjectBrowseReq
		_ = env.Unmarshal(&req)
		res, err := project.Browse(req.Path)
		if err != nil {
			h.sendErr(conn, env.ID, err.Error())
			return
		}
		entries := make([]protocol.ProjectDirEntry, 0, len(res.Entries))
		for _, e := range res.Entries {
			entries = append(entries, protocol.ProjectDirEntry{Name: e.Name, Path: e.Path, IsGitRepo: e.IsGitRepo})
		}
		h.sendOK(conn, env.ID, protocol.ProjectBrowse{Path: res.Path, Parent: res.Parent, Entries: entries})

	case protocol.TypeCommandList:
		var req protocol.CommandListReq
		_ = env.Unmarshal(&req)
		provider, cwd := "", ""
		if m := h.managed(req.SessionID); m != nil {
			provider = m.sess.Provider()
			cwd = m.meta.cwd
		}
		cmds := commands.List(provider, cwd)
		out := make([]protocol.SlashCommand, 0, len(cmds))
		for _, c := range cmds {
			out = append(out, protocol.SlashCommand{Name: c.Name, Description: c.Description, Source: c.Source, Prefix: c.Prefix})
		}
		h.sendOK(conn, env.ID, protocol.CommandList{Commands: out})

	case protocol.TypeLoopList:
		eng := h.loops()
		if eng == nil {
			h.sendErr(conn, env.ID, "loops not enabled")
			return
		}
		h.sendOK(conn, env.ID, protocol.LoopList{Loops: toProtoLoops(eng.List()), Runs: toProtoRuns(eng.Runs())})

	case protocol.TypeLoopUpsert:
		eng := h.loops()
		if eng == nil {
			h.sendErr(conn, env.ID, "loops not enabled")
			return
		}
		var l protocol.Loop
		if err := env.Unmarshal(&l); err != nil {
			h.sendErr(conn, env.ID, "bad loop")
			return
		}
		if l.ID == "" {
			l.ID = "loop_" + randToken()
		}
		saved := eng.Upsert(loops.Loop{
			ID: l.ID, Name: l.Name, Enabled: l.Enabled, Provider: l.Provider, Kind: l.Kind,
			ProjectID: l.ProjectID, ProjectIDs: l.ProjectIDs,
			TriggerCategory: l.TriggerCategory, Tracker: l.Tracker,
			Prompt: l.Prompt, IntervalMinutes: l.IntervalMinutes,
			Worktree: l.Worktree, Plan: l.Plan,
			BudgetUSD: l.BudgetUSD, MaxConcurrent: l.MaxConcurrent,
		})
		h.sendOK(conn, env.ID, toProtoLoops([]loops.Loop{saved})[0])

	case protocol.TypeLoopDelete:
		eng := h.loops()
		if eng == nil {
			h.sendErr(conn, env.ID, "loops not enabled")
			return
		}
		var req protocol.LoopRef
		_ = env.Unmarshal(&req)
		eng.Delete(req.ID)
		h.sendOK(conn, env.ID, protocol.LoopList{Loops: toProtoLoops(eng.List()), Runs: toProtoRuns(eng.Runs())})

	case protocol.TypeLoopSetEnabled:
		eng := h.loops()
		if eng == nil {
			h.sendErr(conn, env.ID, "loops not enabled")
			return
		}
		var req protocol.LoopSetEnabled
		_ = env.Unmarshal(&req)
		eng.SetEnabled(req.ID, req.Enabled)
		h.sendOK(conn, env.ID, protocol.LoopList{Loops: toProtoLoops(eng.List()), Runs: toProtoRuns(eng.Runs())})

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

	case protocol.TypeWorktreeCatchUp:
		var req protocol.WorktreeCatchUp
		_ = env.Unmarshal(&req)
		m := h.managed(req.SessionID)
		if m == nil {
			h.sendErr(conn, env.ID, "no such session")
			return
		}
		m.mu.Lock()
		wtPath := m.meta.worktreePath
		members := append([]worktree.Member(nil), m.meta.members...)
		m.mu.Unlock()
		switch {
		case wtPath != "":
			res, err := worktree.CatchUpToMain(ctx, wtPath)
			if err != nil {
				h.sendErr(conn, env.ID, err.Error())
				return
			}
			h.broadcast(protocol.TypeFSChange, protocol.FSChange{Path: wtPath}) // files moved — reload open buffers
			h.sendOK(conn, env.ID, protocol.WorktreeCatchUp{SessionID: req.SessionID, Status: res.Status, Base: res.Base, Message: res.Message, Conflicts: res.Conflicts})
		case len(members) > 0:
			// Workspace: catch every member repo up to its own default branch; aggregate the outcome.
			status, base := "up_to_date", ""
			var msgs, conflicts []string
			for _, mem := range members {
				res, err := worktree.CatchUpToMain(ctx, mem.Path)
				if err != nil {
					h.sendErr(conn, env.ID, mem.Name+": "+err.Error())
					return
				}
				base = res.Base
				msgs = append(msgs, mem.Name+": "+res.Message)
				if res.Status == "conflicts" {
					status = "conflicts"
					for _, f := range res.Conflicts {
						conflicts = append(conflicts, mem.Name+"/"+f)
					}
				} else if res.Status == "updated" && status != "conflicts" {
					status = "updated"
				}
			}
			h.broadcast(protocol.TypeFSChange, protocol.FSChange{Path: ""})
			h.sendOK(conn, env.ID, protocol.WorktreeCatchUp{SessionID: req.SessionID, Status: status, Base: base, Message: strings.Join(msgs, "\n"), Conflicts: conflicts})
		default:
			h.sendErr(conn, env.ID, "not a worktree session")
		}

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
		h.pushPRFinished(req.SessionID, m.activityTitle(), "") // notify: workspace PRs opened
		h.sendOK(conn, env.ID, protocol.WorkspacePR{SessionID: req.SessionID, Title: title, Body: req.Body, Members: results})

	case protocol.TypeWorktreeRemove:
		var req protocol.WorktreeRemove
		_ = env.Unmarshal(&req)
		m := h.managed(req.SessionID)
		if m == nil || (m.meta.worktreePath == "" && len(m.meta.members) == 0) {
			h.sendErr(conn, env.ID, "not a worktree session")
			return
		}
		m.markUserStopped() // worktree teardown is an intentional delete, not a crash to preserve
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
		h.removeSession(req.SessionID, m)
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
		h.pushPRFinished(req.SessionID, m.activityTitle(), url) // notify: end-of-task milestone reached
		h.sendOK(conn, env.ID, protocol.WorktreePRResult{SessionID: req.SessionID, Branch: branch, Pushed: true, URL: url})

	case protocol.TypeDeviceList:
		h.sendOK(conn, env.ID, h.deviceList(conn))

	case protocol.TypeDeviceRevoke:
		var req protocol.DeviceRef
		if err := env.Unmarshal(&req); err != nil || req.Pub == "" {
			h.sendErr(conn, env.ID, "bad device.revoke")
			return
		}
		if err := h.RevokeDevice(req.Pub); err != nil {
			h.sendErr(conn, env.ID, err.Error())
			return
		}
		h.sendOK(conn, env.ID, h.deviceList(conn))
		h.broadcast(protocol.TypeDeviceList, h.deviceList(nil))

	case protocol.TypeDeviceLabel:
		var req protocol.DeviceRef
		if err := env.Unmarshal(&req); err != nil || req.Pub == "" {
			h.sendErr(conn, env.ID, "bad device.label")
			return
		}
		if err := h.LabelDevice(req.Pub, req.Label); err != nil {
			h.sendErr(conn, env.ID, err.Error())
			return
		}
		h.sendOK(conn, env.ID, h.deviceList(conn))

	case protocol.TypeWorktreeMerge:
		var req protocol.WorktreeMerge
		_ = env.Unmarshal(&req)
		m := h.managed(req.SessionID)
		if m == nil || m.meta.worktreePath == "" {
			h.sendErr(conn, env.ID, "not a worktree session")
			return
		}
		wtPath, branch, root := m.meta.worktreePath, m.meta.branch, m.meta.repoRoot
		msg := req.Message
		if msg == "" {
			msg = branch
		}
		// Commit whatever the agent left uncommitted first — otherwise "finish" silently drops the
		// last edits, which is the worst possible failure for a button called Finish.
		if _, err := worktree.CommitAll(ctx, wtPath, msg); err != nil {
			h.sendErr(conn, env.ID, err.Error())
			return
		}
		if err := worktree.MergeIntoDefault(ctx, root, branch); err != nil {
			h.sendErr(conn, env.ID, err.Error())
			return
		}
		h.sendOK(conn, env.ID, protocol.WorktreePRResult{SessionID: req.SessionID, Branch: branch, Pushed: false})

	case protocol.TypeWorktreeStatus:
		var req protocol.WorktreeStatus
		_ = env.Unmarshal(&req)
		m := h.managed(req.SessionID)
		if m == nil || m.meta.worktreePath == "" {
			h.sendErr(conn, env.ID, "not a worktree session")
			return
		}
		state, url, _ := worktree.PRState(ctx, m.meta.worktreePath, m.meta.branch)
		h.sendOK(conn, env.ID, protocol.WorktreeStatusResult{
			SessionID: req.SessionID, Branch: m.meta.branch, State: state, URL: url,
			HasRemote: worktree.HasRemote(m.meta.worktreePath),
		})

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
		h.sendOK(conn, env.ID, protocol.IntegrationStatus{Connected: m.Connected(), OAuthApps: m.OAuthApps(), AuthErrors: m.AuthErrors(), AuthErrorDetails: m.AuthErrorDetails()})

	case protocol.TypeIntegrationDisconnect:
		var req protocol.IntegrationConnect // reuses {provider, token}; only provider is read
		if err := env.Unmarshal(&req); err != nil {
			h.sendErr(conn, env.ID, "bad integration.disconnect")
			return
		}
		m := h.issuesMgr()
		if m == nil {
			h.sendErr(conn, env.ID, "integrations not enabled")
			return
		}
		if err := m.Disconnect(ctx, req.Provider); err != nil {
			h.sendErr(conn, env.ID, err.Error())
			return
		}
		st := protocol.IntegrationStatus{Connected: m.Connected(), OAuthApps: m.OAuthApps(), AuthErrors: m.AuthErrors(), AuthErrorDetails: m.AuthErrorDetails()}
		h.sendOK(conn, env.ID, st)
		h.broadcast(protocol.TypeIntegrationStatus, st) // every device converges on the disconnect

	case protocol.TypeIntegrationStatus:
		var connected, oauthApps, authErrors []string
		var details map[string]string
		if m := h.issuesMgr(); m != nil {
			connected = m.Connected()
			oauthApps = m.OAuthApps()
			authErrors = m.AuthErrors()
			details = m.AuthErrorDetails()
		}
		h.sendOK(conn, env.ID, protocol.IntegrationStatus{Connected: connected, OAuthApps: oauthApps, AuthErrors: authErrors, AuthErrorDetails: details})

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
		h.sendOK(conn, env.ID, protocol.IntegrationStatus{Connected: m.Connected(), OAuthApps: m.OAuthApps(), AuthErrors: m.AuthErrors(), AuthErrorDetails: m.AuthErrorDetails()})

	case protocol.TypeTelemetryStatus:
		on := false
		if t := h.tel(); t != nil {
			on = t.Enabled()
		}
		h.sendOK(conn, env.ID, protocol.Telemetry{Enabled: on})

	case protocol.TypeTelemetrySet:
		var req protocol.Telemetry
		_ = env.Unmarshal(&req)
		if t := h.tel(); t != nil {
			t.SetEnabled(req.Enabled)
		}
		st := protocol.Telemetry{Enabled: req.Enabled}
		h.sendOK(conn, env.ID, st)
		h.broadcast(protocol.TypeTelemetryStatus, st) // converge every device on the toggle

	case protocol.TypeJiraSites:
		m := h.issuesMgr()
		if m == nil {
			h.sendErr(conn, env.ID, "integrations not enabled")
			return
		}
		sites, current, err := m.JiraSites(ctx)
		if err != nil {
			h.sendErr(conn, env.ID, err.Error())
			return
		}
		out := make([]protocol.JiraSite, 0, len(sites))
		for _, s := range sites {
			out = append(out, protocol.JiraSite{ID: s.ID, Name: s.Name, URL: s.URL})
		}
		h.sendOK(conn, env.ID, protocol.JiraSites{Sites: out, Current: current})

	case protocol.TypeJiraSetSite:
		var req protocol.JiraSetSite
		_ = env.Unmarshal(&req)
		m := h.issuesMgr()
		if m == nil {
			h.sendErr(conn, env.ID, "integrations not enabled")
			return
		}
		if err := m.SetJiraSite(ctx, req.CloudID); err != nil {
			h.sendErr(conn, env.ID, err.Error())
			return
		}
		h.sendOK(conn, env.ID, protocol.IntegrationStatus{Connected: m.Connected(), OAuthApps: m.OAuthApps(), AuthErrors: m.AuthErrors(), AuthErrorDetails: m.AuthErrorDetails()})

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

	case protocol.TypeIssueColumns:
		var req protocol.IssueColumnsReq
		_ = env.Unmarshal(&req)
		m := h.issuesMgr()
		if m == nil {
			h.sendErr(conn, env.ID, "integrations not enabled")
			return
		}
		p := m.Provider(req.Provider)
		if p == nil {
			h.sendErr(conn, env.ID, req.Provider+" not connected")
			return
		}
		states, err := p.ProjectStatuses(ctx, req.Project)
		if err != nil {
			h.sendErr(conn, env.ID, err.Error())
			return
		}
		out := make([]protocol.IssueState, len(states))
		for i, s := range states {
			out[i] = protocol.IssueState{ID: s.ID, Name: s.Name, Category: s.Category, Position: s.Position}
		}
		h.sendOK(conn, env.ID, protocol.IssueStateList{States: out})

	case protocol.TypeIssueMove:
		var req protocol.IssueMove
		_ = env.Unmarshal(&req)
		m := h.issuesMgr()
		if m == nil {
			h.sendErr(conn, env.ID, "integrations not enabled")
			return
		}
		p := m.Provider(req.Provider)
		if p == nil {
			h.sendErr(conn, env.ID, req.Provider+" not connected")
			return
		}
		if err := p.MoveToStatus(ctx, req.IssueID, req.StatusID); err != nil {
			h.sendErr(conn, env.ID, err.Error())
			return
		}
		// Re-fetch so the reply carries the updated status/category for the board; fall back to a
		// minimal issue if the re-fetch fails (the move already succeeded).
		updated, _, _, err := p.Detail(ctx, req.IssueID)
		if err != nil {
			updated = issues.Issue{ID: req.IssueID, Provider: req.Provider}
		}
		h.sendOK(conn, env.ID, toProtoIssue(updated))
		// Refresh the merged cache off the reply path so every device's board reflects the move.
		go func() { _ = m.Refresh(context.Background()) }()

	case protocol.TypeIssueCreate:
		var req protocol.IssueCreate
		_ = env.Unmarshal(&req)
		m := h.issuesMgr()
		if m == nil {
			h.sendErr(conn, env.ID, "integrations not enabled")
			return
		}
		p := m.Provider(req.Provider)
		if p == nil {
			h.sendErr(conn, env.ID, req.Provider+" not connected")
			return
		}
		created, err := p.CreateIssue(ctx, issues.CreateIssueInput{
			Project: req.Project, Title: req.Title, Description: req.Description,
			Priority: req.Priority, Type: req.Type,
		})
		if err != nil {
			h.sendErr(conn, env.ID, err.Error())
			return
		}
		h.sendOK(conn, env.ID, toProtoIssue(created))
		// Refresh so the new ticket appears on every device's board.
		go func() { _ = m.Refresh(context.Background()) }()

	case protocol.TypeIssueProjects:
		m := h.issuesMgr()
		if m == nil {
			h.sendErr(conn, env.ID, "integrations not enabled")
			return
		}
		var projects []protocol.IssueProject
		for _, name := range m.Connected() {
			p := m.Provider(name)
			if p == nil {
				continue
			}
			ps, err := p.Projects(ctx)
			if err != nil {
				continue // one tracker failing shouldn't blank the whole picker
			}
			for _, pr := range ps {
				projects = append(projects, protocol.IssueProject{ID: pr.ID, Name: pr.Name, Provider: name})
			}
		}
		h.sendOK(conn, env.ID, protocol.IssueProjectsList{Projects: projects})

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
		issue, comments, attachments, err := m.Detail(ctx, req.Provider, req.IssueID)
		if err != nil {
			h.sendErr(conn, env.ID, err.Error())
			return
		}
		h.sendOK(conn, env.ID, protocol.IssueDetail{
			Issue:       toProtoIssue(issue),
			Comments:    toProtoComments(comments),
			Attachments: toProtoAttachments(attachments),
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
			AssigneeID:  req.AssigneeID,
			LabelIDs:    req.LabelIDs,
			CycleID:     req.CycleID,
			Estimate:    req.Estimate,
			DueDate:     req.DueDate,
		})
		if err != nil {
			h.sendErr(conn, env.ID, err.Error())
			return
		}
		h.sendOK(conn, env.ID, toProtoIssue(updated))
		// Refresh the merged cache off the reply path so every device's board
		// reflects the edit (fire-and-forget; the reply already went out).
		go func() { _ = m.Refresh(context.Background()) }()

	case protocol.TypeIssueMembers:
		var req protocol.IssueMembersReq
		_ = env.Unmarshal(&req)
		m := h.issuesMgr()
		if m == nil {
			h.sendErr(conn, env.ID, "integrations not enabled")
			return
		}
		users, err := m.Members(ctx, req.Provider, req.TeamID, req.IssueID)
		if err != nil {
			h.sendErr(conn, env.ID, err.Error())
			return
		}
		out := make([]protocol.IssueUser, len(users))
		for i, u := range users {
			out[i] = protocol.IssueUser{ID: u.ID, Name: u.Name, Email: u.Email, Avatar: u.Avatar}
		}
		h.sendOK(conn, env.ID, protocol.IssueMemberList{Members: out})

	case protocol.TypeIssueLabels:
		var req protocol.IssueLabelsReq
		_ = env.Unmarshal(&req)
		m := h.issuesMgr()
		if m == nil {
			h.sendErr(conn, env.ID, "integrations not enabled")
			return
		}
		labels, err := m.ProjectLabels(ctx, req.Provider, req.TeamID)
		if err != nil {
			h.sendErr(conn, env.ID, err.Error())
			return
		}
		out := make([]protocol.IssueLabel, len(labels))
		for i, l := range labels {
			out[i] = protocol.IssueLabel{ID: l.ID, Name: l.Name, Color: l.Color}
		}
		h.sendOK(conn, env.ID, protocol.IssueLabelList{Labels: out})

	case protocol.TypeIssueCycles:
		var req protocol.IssueCyclesReq
		_ = env.Unmarshal(&req)
		m := h.issuesMgr()
		if m == nil {
			h.sendErr(conn, env.ID, "integrations not enabled")
			return
		}
		cycles, err := m.ProjectCycles(ctx, req.Provider, req.TeamID)
		if err != nil {
			h.sendErr(conn, env.ID, err.Error())
			return
		}
		out := make([]protocol.IssueCycle, len(cycles))
		for i, c := range cycles {
			out[i] = protocol.IssueCycle{ID: c.ID, Name: c.Name, Number: c.Number, State: c.State}
		}
		h.sendOK(conn, env.ID, protocol.IssueCycleList{Cycles: out})

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
		if !h.requireCapability(conn, env.ID, capSteer, "send a prompt") {
			return
		}
		var req protocol.SessionPrompt
		_ = env.Unmarshal(&req)
		m := h.managed(req.SessionID)
		if m == nil {
			h.sendErr(conn, env.ID, "no such session")
			return
		}
		text := req.Text
		// One-shot: prepend the workspace note (which repos + where) to the FIRST user turn of a
		// multi-repo session, so the agent knows where the repos are.
		m.mu.Lock()
		if m.pendingContext != "" {
			text = m.pendingContext + text
			m.pendingContext = ""
		}
		m.mu.Unlock()
		// WRITE-AHEAD: durably record the user's prompt BEFORE sending it. If the send silently
		// fails (wrong directory → opencode 2xx-no-op, provider outage), the text is already on disk
		// and recoverable — it can never vaporize the way it did in the 6-hour-loss incident.
		author := h.clientName(conn)
		_ = h.tr().Append(req.SessionID, transcript.Entry{Kind: "user", Text: text, Author: author})
		m.openTurn("") // Turn Engine: a turn is now in flight (heartbeats + reconciler start)
		// Echo the prompt back to EVERY subscriber attributed to its sender. Without this a second
		// device shows the message with no indication of who sent it.
		if author != "" {
			m.broadcastUserEcho(text, author)
		}
		log.Printf("session %s (%s): prompt received (%d chars) from %s", req.SessionID, m.sess.Provider(), len(text), authorOrLocal(author))
		if err := promptSession(ctx, m.sess, text, req.Images); err != nil {
			log.Printf("session %s: prompt send FAILED: %v", req.SessionID, err)
			if t := h.tel(); t != nil {
				t.Record("session.prompt", m.sess.Provider(), 0, err)
			}
			h.sendErr(conn, env.ID, err.Error())
			return
		}
		// Arm the no-response watchdog: opencode's async POST returns nil before delivery, so a
		// wrong-directory send is accepted (2xx) yet produces ZERO events. If nothing comes back
		// soon, surface it in ~20s instead of the user staring at a spinner for 30 minutes.
		m.armResponseWatchdog()
		h.sendOK(conn, env.ID, nil)

	case protocol.TypeUIAction:
		// A user activated a generative-UI component's action. Its ONLY effect is to start the next
		// user turn (a component can never execute a tool or a destructive op directly). kind=prompt/
		// answer deliver the templated text as a normal prompt.
		//
		// Values carries a form's collected answers and is appended to the prompt (see formValuesText).
		// kind "permission" remains DECLARED BUT UNIMPLEMENTED: it would need an approval id that
		// fenced components don't carry. A client sending it gets the no-op fallthrough below, not an
		// error — don't read that as working-but-untested.
		var req protocol.UIActionInvoke
		_ = env.Unmarshal(&req)
		m := h.managed(req.SessionID)
		if m == nil {
			h.sendErr(conn, env.ID, "no such session")
			return
		}
		if req.Kind == "prompt" || req.Kind == "answer" {
			text := req.Prompt
			// A form submits its collected values alongside the action's prompt. Rendering them
			// daemon-side keeps one canonical phrasing rather than each client inventing its own.
			if values := formValuesText(req.Values); values != "" {
				if text == "" {
					text = values
				} else {
					text = text + "\n\n" + values
				}
			}
			if text == "" {
				h.sendErr(conn, env.ID, "ui action has no prompt")
				return
			}
			_ = h.tr().Append(req.SessionID, transcript.Entry{Kind: "user", Text: text})
			m.openTurn("")
			log.Printf("session %s (%s): ui.action %q -> prompt (%d chars)", req.SessionID, m.sess.Provider(), req.ActionID, len(text))
			if err := promptSession(ctx, m.sess, text, nil); err != nil {
				h.sendErr(conn, env.ID, err.Error())
				return
			}
			m.armResponseWatchdog()
		}
		h.sendOK(conn, env.ID, nil)

	case protocol.TypeApprovalRespond:
		// Owner-only: a steerer may ask the agent to do something, but only the person whose
		// credentials are at stake may authorize a tool that acts with them.
		if !h.requireCapability(conn, env.ID, capApprove, "answer an approval") {
			return
		}
		var req protocol.ApprovalRespond
		_ = env.Unmarshal(&req)
		h.mu.Lock()
		m := h.approvals[req.ApprovalID]
		pend := h.approvalReqs[req.ApprovalID]
		delete(h.approvals, req.ApprovalID)
		delete(h.approvalReqs, req.ApprovalID)
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
		// An MCP approval is the daemon's own question (a blocked gateway call), not something the
		// provider is waiting on — deliver it to the waiter instead of down to the harness.
		if !h.resolveMCPApproval(req.ApprovalID, req.Decision) {
			if err := m.sess.Respond(ctx, req.ApprovalID, req.Decision); err != nil {
				h.sendErr(conn, env.ID, err.Error())
				return
			}
		}
		// ALWAYS persists ACROSS sessions, so permissions are truly asked once — not once per session.
		// The client may narrow what "always" means (this exact command shape, this subtree, this
		// project); with no Scope it stays the historical provider+tool rule.
		if req.Decision == protocol.DecisionAlways {
			h.addApprovalRule(ruleFromDecision(pend, req.Scope))
			h.broadcast(protocol.TypeApprovalRulesChanged, h.approvalRulesList())
		}
		h.sendOK(conn, env.ID, nil)
		// Tell every client this approval was answered, so its card clears everywhere.
		resolved := protocol.ApprovalResolved{ApprovalID: req.ApprovalID, Decision: req.Decision}
		h.broadcast(protocol.TypeApprovalResolved, resolved)
		// AND put it in the session's own history. A hub-wide announcement reaches only clients
		// connected at that moment: the approval.request is in the transcript, so a device that opens
		// the session later replayed the question without its answer and resurrected a dead modal.
		if sid := pend.req.SessionID; sid != "" {
			if m := h.managed(sid); m != nil {
				if raw, err := (agent.Event{Type: protocol.TypeApprovalResolved, Payload: resolved}).Encode(); err == nil {
					m.broadcast(raw)
				}
			}
		}

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
		// Record the directory and the server we attached to. This used to persist an EMPTY meta, so a
		// taken-over session survived a daemon restart in name only: the restore knew the id but not
		// where the session lived, and reopened it empty or against the wrong project.
		attachCwd := req.Cwd
		if dr, ok := sess.(agent.DirReporter); ok {
			if real := dr.Dir(); real != "" {
				attachCwd = real
			}
		}
		m := h.addSession(sess, sessionMeta{cwd: attachCwd, providerURL: req.URL})
		m.seedStatus(protocol.StatusIdle) // an attached session is idle until a real event says otherwise
		h.sendOK(conn, env.ID, m.info())
		m.subscribe(conn)
		go m.run()

	case protocol.TypeSessionRestart:
		var req protocol.SessionRef
		_ = env.Unmarshal(&req)
		// Already live (e.g. another client restarted it first) → just subscribe.
		if m := h.managed(req.SessionID); m != nil {
			log.Printf("session.restart %s: already live — subscribing", req.SessionID)
			h.sendOK(conn, env.ID, m.info())
			m.subscribe(conn)
			return
		}
		m, err := h.restartSession(ctx, req.SessionID)
		if err != nil {
			log.Printf("session.restart %s: FAILED: %v", req.SessionID, err)
			if t := h.tel(); t != nil {
				t.Record("session.restart", "", 0, err)
			}
			h.sendErr(conn, env.ID, err.Error())
			return
		}
		log.Printf("session.restart %s: recreated as %s", req.SessionID, m.sess.ID())
		h.sendOK(conn, env.ID, m.info())
		m.subscribe(conn)
		go m.run()
		h.broadcastSessionList() // the id changed (old stopped → new live); converge every client

	case protocol.TypeSessionRecover:
		var req protocol.SessionRef
		_ = env.Unmarshal(&req)
		m, err := h.recoverSession(ctx, req.SessionID)
		if err != nil {
			log.Printf("session.recover %s: FAILED: %v", req.SessionID, err)
			if t := h.tel(); t != nil {
				t.Record("session.recover", "", 0, err)
			}
			h.sendErr(conn, env.ID, err.Error())
			return
		}
		log.Printf("session.recover %s: re-attached (directory healed)", req.SessionID)
		h.sendOK(conn, env.ID, m.info())
		m.subscribe(conn)
		go m.run()
		h.broadcastSessionList() // id is unchanged but its cwd/status may have; converge clients

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

	case protocol.TypeInviteCreate:
		if !h.requireCapability(conn, env.ID, capApprove, "create an invite") {
			return
		}
		var req protocol.InviteCreate
		_ = env.Unmarshal(&req)
		inv := h.invites.create(req.Label, req.Role, time.Duration(req.TTLHours)*time.Hour)
		out := protocol.InviteCreated{Invite: protocol.Invite{
			ID: inv.ID, Label: inv.Label, Role: inv.Role,
			ExpiresAt: inv.ExpiresAt.Unix(),
		}}
		h.mu.Lock()
		build := h.pairURL
		h.mu.Unlock()
		if build != nil {
			out.URL = build(inv.Secret)
		}
		log.Printf("invites: created %q (%s, expires %s)", inviteLabel(inv), inv.Role, inv.ExpiresAt.Format(time.RFC3339))
		h.sendOK(conn, env.ID, out)

	case protocol.TypeInviteList:
		if !h.requireCapability(conn, env.ID, capApprove, "list invites") {
			return
		}
		h.sendOK(conn, env.ID, h.inviteList())

	case protocol.TypeInviteRevoke:
		if !h.requireCapability(conn, env.ID, capApprove, "revoke an invite") {
			return
		}
		var ref protocol.InviteRef
		if err := env.Unmarshal(&ref); err != nil || ref.ID == "" {
			h.sendErr(conn, env.ID, "bad invite.revoke")
			return
		}
		if !h.invites.revoke(ref.ID) {
			h.sendErr(conn, env.ID, "no such invite")
			return
		}
		h.sendOK(conn, env.ID, h.inviteList())
		h.broadcastParticipants()

	case protocol.TypeParticipants:
		h.sendOK(conn, env.ID, h.participants())

	case protocol.TypeRolesEnable:
		if !h.requireCapability(conn, env.ID, capApprove, "change sharing settings") {
			return
		}
		var req protocol.RolesEnable
		if err := env.Unmarshal(&req); err != nil {
			h.sendErr(conn, env.ID, "bad roles.enable")
			return
		}
		// Whoever turns enforcement ON becomes the owner — otherwise enabling it would instantly
		// demote the person doing the enabling to an observer of their own machine.
		h.roles.setRole(conn, RoleOwner)
		h.SetRolesEnabled(req.Enabled)
		h.sendOK(conn, env.ID, h.participants())
		h.broadcastParticipants()

	case protocol.TypeRoleGrant:
		if !h.requireCapability(conn, env.ID, capApprove, "grant a role") {
			return
		}
		var req protocol.RoleGrant
		if err := env.Unmarshal(&req); err != nil {
			h.sendErr(conn, env.ID, "bad role.grant")
			return
		}
		switch req.Role {
		case RoleOwner, RoleSteerer, RoleObserver:
		default:
			h.sendErr(conn, env.ID, "unknown role")
			return
		}
		if !h.grantRole(req.Name, req.Role) {
			h.sendErr(conn, env.ID, "no connected participant by that name")
			return
		}
		h.sendOK(conn, env.ID, h.participants())

	case protocol.TypeClientIdentify:
		var req protocol.ClientIdentify
		if err := env.Unmarshal(&req); err != nil {
			h.sendErr(conn, env.ID, "bad client.identify")
			return
		}
		name := strings.TrimSpace(req.Name)
		if len(name) > 60 {
			name = name[:60]
		}
		h.mu.Lock()
		if c := h.clients[conn]; c != nil {
			c.setName(name)
		}
		h.mu.Unlock()
		h.sendOK(conn, env.ID, nil)
		h.broadcastParticipants() // presence: everyone sees who joined

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
		if !h.requireCapability(conn, env.ID, capSteer, "interrupt a turn") {
			return
		}
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
			m.markUserStopped()  // intentional delete → run() drops the record (not a crash to preserve)
			_ = m.sess.Stop(ctx) // interrupt any running turn
			// Permanently delete server-side (opencode) so it can't be re-attached/re-discovered and
			// reappear — the "deleted session keeps coming back" bug. Best-effort, before Close.
			if d, ok := m.sess.(agent.Deleter); ok {
				if err := d.Delete(ctx); err != nil {
					log.Printf("session.stop %s: server-side delete failed: %v", req.SessionID, err)
				}
			}
			_ = m.sess.Close() // end the event stream -> run() -> removeSession (drops the record)
			h.removeSession(req.SessionID, m)
			if h.db != nil {
				_ = h.db.SetName(req.SessionID, "") // clear the orphaned rename
			}
		} else {
			// Not a LIVE session — it's a STOPPED/restartable record (e.g. a claude-code session that
			// couldn't re-attach after a restart). Deleting it must still drop the durable record, or
			// it re-appears from the store on every session.list — the "deleted session keeps coming
			// back" bug for stopped sessions.
			h.removeSession(req.SessionID, nil) // record-only (no live binding) → force removal
			log.Printf("session.stop %s: removed stopped/restartable record", req.SessionID)
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

	case protocol.TypeLogSubscribe:
		h.mu.Lock()
		h.logSubs[conn] = true
		lh := h.logHub
		h.mu.Unlock()
		var lines []string
		if lh != nil {
			lines = lh.Recent()
		}
		h.sendOK(conn, env.ID, protocol.LogHistory{Lines: lines})

	case protocol.TypeLogUnsubscribe:
		h.mu.Lock()
		delete(h.logSubs, conn)
		h.mu.Unlock()
		h.sendOK(conn, env.ID, nil)

	case protocol.TypeActivityList:
		h.mu.Lock()
		a := h.activity
		h.mu.Unlock()
		out := protocol.ActivityList{Events: []protocol.ActivityEvent{}}
		if a != nil {
			for _, e := range a.Recent() {
				out.Events = append(out.Events, toProtoActivity(e))
			}
		}
		h.sendOK(conn, env.ID, out)

	case protocol.TypeActivityMarkRead:
		var req protocol.ActivityMarkRead
		_ = env.Unmarshal(&req)
		h.mu.Lock()
		a := h.activity
		h.mu.Unlock()
		if a != nil {
			a.MarkRead(req.IDs)
		}
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
	if len(m.meta.roots) > 0 {
		return append([]string(nil), m.meta.roots...) // explicit picked folders (shared multi-repo)
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

// sendEvent pushes an id-less event to a single client (e.g. session.create progress to the client
// that requested it).
func (h *Hub) sendEvent(conn *transport.Conn, typ string, payload any) {
	if raw, err := protocol.Encode("", typ, payload); err == nil {
		_ = conn.Send(raw)
	}
}

// clientName returns the human name a connection declared via client.identify ("" if it never did).
func (h *Hub) clientName(conn *transport.Conn) string {
	h.mu.Lock()
	c := h.clients[conn]
	h.mu.Unlock()
	if c == nil {
		return ""
	}
	return c.displayName()
}

// authorOrLocal renders an author for logs.
func authorOrLocal(author string) string {
	if author == "" {
		return "an unidentified client"
	}
	return author
}

// formValuesText renders a form's collected values as the readable lines that become part of the
// user's next turn. Keys are sorted so the same answers always produce the same text (a model
// re-reading its own transcript shouldn't see the order shuffle).
//
// Values are rendered as plain "label: value" lines rather than JSON because they ARE the user's
// reply — the agent should read them the way a person would have typed them.
func formValuesText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var values map[string]any
	if err := json.Unmarshal(raw, &values); err != nil || len(values) == 0 {
		return ""
	}
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		v := values[k]
		if s, ok := v.(string); ok && strings.TrimSpace(s) == "" {
			continue // an untouched optional field shouldn't add a blank line
		}
		fmt.Fprintf(&b, "%s: %v\n", k, v)
	}
	return strings.TrimRight(b.String(), "\n")
}
