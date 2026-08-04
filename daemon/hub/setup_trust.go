package hub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log"
	"os"
	"sync"
	"time"

	"github.com/howlerops/oculus/daemon/activity"
	"github.com/howlerops/oculus/daemon/protocol"
	"github.com/howlerops/oculus/daemon/worktree"
)

// Trusting a repo's worktree setup command.
//
// THE HOLE THIS CLOSES. <repoRoot>/.oculus/project.json may carry a "setup" command that the daemon
// runs through `sh -c` when it creates a worktree (see worktree.Bootstrap). Both halves of that are
// capSteer: fs.write can create the file, session.create{Worktree:true} triggers the run. So a
// steerer — someone granted "may prompt the agent", not "owns this machine" — could hand themselves
// a shell as the owner in two ordinary-looking requests. run.test was raised to capOwner precisely
// because `sh -c` is the one thing the permission model cannot police; this was the same shell
// wearing a different label.
//
// WHY THIS IS A TRUST DECISION AND NOT A CAPABILITY FLIP. The two one-line fixes are both wrong.
// Raising session.create to capOwner guts the steerer role, whose entire point is starting work.
// Never running setup guts the feature for the solo owner who wrote that file and wants their
// worktree bootstrapped. What actually differs between the safe case and the attack is not WHICH
// endpoint was called but WHO decided the command's text — so that is what we record.
//
// WHY NOT "ONLY HONOUR A COMMITTED project.json". It was the most attractive alternative and it does
// not hold. A steerer can get a file committed: worktree.pr is capSteer and runs `git add -A && git
// commit`, and worktree.merge is capSteer and merges that branch into the main checkout — so a
// steerer can walk their own project.json into repoRoot's HEAD without ever touching a shell. Being
// committed proves the repo has a history, not that a human reviewed this line. Same for "the file
// was already there when the daemon started" (they can wait) and "the file is outside the worktree"
// (they choose the path). No on-disk signal survives an adversary who has both file-write and git.
//
// WHAT WE DO INSTEAD. A setup command runs when, and only when, one of these is true:
//
//  1. Multi-user enforcement is OFF. Then there is exactly one trust domain: every connection is
//     already the owner and already has run.test, remote.run and the rest, so demanding approval to
//     run the user's own file would add friction that buys nothing — the explicit rule roles.go
//     states for the solo case. We still RECORD the command as trusted, so turning sharing on later
//     doesn't suddenly break a repo that has been bootstrapping fine for months.
//  2. The exact command has been approved before for this repo. "Exact" means a hash of the command
//     text: editing the command — including a steerer editing it — invalidates the record and it has
//     to be approved again. Trusting a repo forever, rather than a command, would let an attacker
//     swap the payload under an approval the owner gave for `pnpm install`.
//  3. The owner approves it now, on a card that shows the command verbatim.
//
// Anything else skips the command and SAYS so, in the session and in the log. A silent skip is its
// own kind of bug: the user is left debugging a half-built worktree.
const worktreeSetupTool = "worktree.setup"

// worktreeSetupApprovalTimeout bounds how long an unanswered setup card holds its goroutine. It
// matches the MCP approval timeout: long enough to pick your phone up, short enough that a session
// nobody is watching doesn't pin resources forever. A timeout records NOTHING — the question was not
// answered, so it stays open for next time.
const worktreeSetupApprovalTimeout = 10 * time.Minute

// setupTrustRecord is one decision the owner made about one command in one repo.
type setupTrustRecord struct {
	Repo string `json:"repo"`
	// SHA is sha256 of the command text and is the ONLY thing matched on. Command is stored beside it
	// for the log and the rules UI; matching on it would let whitespace-equivalent rewrites through.
	SHA     string    `json:"sha"`
	Command string    `json:"command"`
	Allow   bool      `json:"allow"`
	At      time.Time `json:"at"`
}

// setupTrustStore persists those decisions to ~/.oculus/worktree-setup-trust.json. It is a separate
// file from approval-rules.json on purpose: an approval RULE is a pattern the user wants applied
// broadly and can be hand-edited liberally, while these are pinned to an exact command hash and
// grant shell. Mixing them would invite someone to write `{"tool":"worktree.setup"}` as a catch-all
// allow and quietly reopen this hole.
type setupTrustStore struct {
	mu   sync.Mutex
	path string
	recs []setupTrustRecord
}

func setupCommandSHA(cmd string) string {
	sum := sha256.Sum256([]byte(cmd))
	return hex.EncodeToString(sum[:])
}

// load reads the store from disk, replacing whatever is in memory.
func (s *setupTrustStore) load() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.recs = nil
	if s.path == "" {
		return
	}
	data, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	var recs []setupTrustRecord
	if err := json.Unmarshal(data, &recs); err != nil {
		log.Printf("worktree setup trust: %s is unreadable (%v) — every setup command will need re-approving", s.path, err)
		return
	}
	// Drop malformed entries rather than trusting them. A record with no repo or no hash would match
	// by accident, and "matched by accident" here means "ran a shell command by accident".
	for _, r := range recs {
		if r.Repo == "" || r.SHA == "" {
			continue
		}
		s.recs = append(s.recs, r)
	}
}

// save writes the store. Best-effort: losing the file costs an extra approval, never safety.
func (s *setupTrustStore) save() {
	s.mu.Lock()
	path := s.path
	snapshot := make([]setupTrustRecord, len(s.recs))
	copy(snapshot, s.recs)
	s.mu.Unlock()
	if path == "" {
		return
	}
	if data, err := json.MarshalIndent(snapshot, "", "  "); err == nil {
		_ = os.WriteFile(path, data, 0o600)
	}
}

// decision looks up (repo, command). known is false when this exact command has never been answered
// for this repo — which is what makes editing a command re-ask.
func (s *setupTrustStore) decision(repo, cmd string) (allow, known bool) {
	sha := setupCommandSHA(cmd)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.recs {
		if r.Repo == repo && r.SHA == sha {
			return r.Allow, true
		}
	}
	return false, false
}

// remember records an answer, replacing any previous answer for the same (repo, command).
func (s *setupTrustStore) remember(repo, cmd string, allow bool) {
	if repo == "" || cmd == "" {
		return
	}
	rec := setupTrustRecord{Repo: repo, SHA: setupCommandSHA(cmd), Command: cmd, Allow: allow, At: time.Now()}
	s.mu.Lock()
	replaced := false
	for i, r := range s.recs {
		if r.Repo != rec.Repo || r.SHA != rec.SHA {
			continue
		}
		if r.Allow == rec.Allow {
			s.mu.Unlock()
			return // unchanged — don't rewrite the file on every worktree create
		}
		s.recs[i] = rec
		replaced = true
		break
	}
	if !replaced {
		s.recs = append(s.recs, rec)
	}
	s.mu.Unlock()
	s.save()
}

// SetWorktreeSetupTrustPath records where setup-command approvals persist and loads any saved ones.
// With no path set, approvals last only as long as the daemon does.
func (h *Hub) SetWorktreeSetupTrustPath(path string) {
	s := h.setupTrustStore()
	s.mu.Lock()
	s.path = path
	s.mu.Unlock()
	s.load()
}

// setupTrustStore returns the hub's store, creating it on first use so a zero-valued Hub (as tests
// build) still works.
func (h *Hub) setupTrustStore() *setupTrustStore {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.setupTrust == nil {
		h.setupTrust = &setupTrustStore{}
	}
	return h.setupTrust
}

// requesterRoleKey carries the role of the client whose request is being served down into
// startSession. It rides the context because session.create's plumbing is shared with callers that
// have no client at all (loops, issue launches, fan-out judges, restore) — and "no client" must
// answer "not the owner", not "unknown, assume yes". mcp.WithConfig threads per-session state the
// same way a few lines further down the same function.
type requesterRoleKey struct{}

// withRequesterRole tags ctx with the role of the connection making the request.
func withRequesterRole(ctx context.Context, role string) context.Context {
	return context.WithValue(ctx, requesterRoleKey{}, role)
}

// requesterRole reads the role back. An untagged context is an autonomous/internal caller: nobody is
// there to answer a question, so it is deliberately NOT the owner.
func requesterRole(ctx context.Context) string {
	if role, ok := ctx.Value(requesterRoleKey{}).(string); ok {
		return role
	}
	return ""
}

// worktreeSetupAsk is a setup command held back at create time because only the owner can approve it
// and the session it belongs to did not exist yet. The card is asked once the session is live, so it
// lands in that session's transcript and renders on whichever device opens it — an approval with no
// session id is dropped by the client, which is why this is deferred rather than asked inline.
type worktreeSetupAsk struct {
	repoRoot     string
	worktreePath string
	cfg          worktree.Config
	port         int
}

// decideWorktreeSetup answers "may this repo's setup command run?" and, when the answer is "only the
// owner can say", reports that it is worth asking. See the file header for the reasoning behind each
// branch.
func (h *Hub) decideWorktreeSetup(ctx context.Context, repoRoot string, cfg worktree.Config) (worktree.SetupTrust, bool) {
	if cfg.Setup == "" {
		return worktree.SetupTrust{Allowed: true}, false // nothing to run, nothing to trust
	}
	if repoRoot == "" {
		// We can't key an approval to a repo we can't name, and an unkeyed approval is one that would
		// apply everywhere. Refuse rather than invent a key.
		return worktree.SetupTrust{Reason: "the repository root couldn't be determined, so its setup command can't be approved"}, false
	}
	store := h.setupTrustStore()
	if !h.roles.isEnabled() {
		// Single trust domain: every connection is already the owner (roles.go), so this command could
		// equally have been run through run.test by the same client. Record it, so enabling sharing
		// later doesn't break a repo that has been bootstrapping cleanly all along.
		store.remember(repoRoot, cfg.Setup, true)
		return worktree.SetupTrust{Allowed: true}, false
	}
	if allow, known := store.decision(repoRoot, cfg.Setup); known {
		if allow {
			return worktree.SetupTrust{Allowed: true}, false
		}
		return worktree.SetupTrust{Reason: "you previously declined this setup command for this repository"}, false
	}
	// Unknown command, sharing is on. Only offer the card when the OWNER is the one creating the
	// session. Asking on a steerer's behalf would be the confused deputy this whole gate exists to
	// prevent: the command text is written by the steerer, and putting it in front of the owner as a
	// routine-looking "approve worktree setup" prompt — with a push notification behind it — is how
	// an attacker gets a human to click yes on their shell command. A steerer gets a worktree with no
	// setup and a message saying so; the owner creating one session in that repo answers it for good.
	if requesterRole(ctx) == RoleOwner {
		return worktree.SetupTrust{Reason: "waiting for you to approve this repository's setup command"}, true
	}
	return worktree.SetupTrust{Reason: "this repository's setup command hasn't been approved by the owner yet, so it wasn't run"}, false
}

// askWorktreeSetup puts the setup command in front of the owner as a normal approval card and, if
// approved, runs it in the worktree.
//
// It reuses the existing approval machinery end to end — the card, the push, the owner-only
// approval.respond gate, the "resolved" broadcast that clears it on every device — rather than
// introducing a second consent surface. The waiter is registered in the same map MCP tool calls use,
// because this is the same kind of question: one the DAEMON is blocked on, not one the harness is.
func (h *Hub) askWorktreeSetup(m *managedSession, ask worktreeSetupAsk) {
	sid := m.sess.ID()
	ar := protocol.ApprovalRequest{
		ApprovalID: "wtsetup_" + randToken(),
		SessionID:  sid,
		Tool:       worktreeSetupTool,
		Detail:     ask.cfg.Setup,
	}
	// No SuggestedScopes: every "always" scope the client could offer ("always allow this tool",
	// "…anywhere under this path") would generalize BEYOND the exact command being approved, which is
	// the one thing this decision must never do.
	answer := make(chan string, 1)

	h.mu.Lock()
	if h.mcpApprovals == nil {
		h.mcpApprovals = map[string]chan string{}
	}
	h.mcpApprovals[ar.ApprovalID] = answer
	h.approvals[ar.ApprovalID] = m
	if h.approvalReqs == nil {
		h.approvalReqs = map[string]pendingApproval{}
	}
	h.approvalReqs[ar.ApprovalID] = pendingApproval{req: ar, provider: m.sess.Provider()}
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.mcpApprovals, ar.ApprovalID)
		delete(h.approvals, ar.ApprovalID)
		delete(h.approvalReqs, ar.ApprovalID)
		h.mu.Unlock()
	}()

	m.mu.Lock()
	m.pendingApprovals++
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		if m.pendingApprovals > 0 {
			m.pendingApprovals--
		}
		m.mu.Unlock()
	}()

	log.Printf("worktree setup: asking the owner to approve %q for %s", ask.cfg.Setup, ask.repoRoot)
	h.pushApproval(ar)
	if raw, err := encodeApprovalRequest(ar); err == nil {
		m.broadcast(raw)
	}

	var decision string
	select {
	case decision = <-answer:
	case <-time.After(worktreeSetupApprovalTimeout):
		m.emitTool("⚠︎ Worktree setup skipped — nobody approved “" + ask.cfg.Setup + "” within " + worktreeSetupApprovalTimeout.String())
		h.noteWorktreeSetupSkipped(sid, ask.repoRoot, ask.cfg.Setup, "nobody answered the approval")
		return
	}

	if decision == protocol.DecisionDeny {
		// A session that dies with the card outstanding is swept as a DENY (sweepSessionApprovals) —
		// that is a dead session, not an answer, and persisting it would silently blacklist a repo's
		// setup forever with no way back. Only remember a denial that came from a live session, i.e.
		// from a person.
		if h.managed(sid) != nil {
			h.setupTrustStore().remember(ask.repoRoot, ask.cfg.Setup, false)
			log.Printf("worktree setup: owner DENIED %q for %s — remembered", ask.cfg.Setup, ask.repoRoot)
		}
		m.emitTool("✗ Worktree setup skipped — you declined “" + ask.cfg.Setup + "”")
		h.noteWorktreeSetupSkipped(sid, ask.repoRoot, ask.cfg.Setup, "you declined it")
		return
	}

	// Allow and always mean the same thing here: this command, in this repo, is trusted until its
	// text changes. There is nothing broader to opt into.
	h.setupTrustStore().remember(ask.repoRoot, ask.cfg.Setup, true)
	log.Printf("worktree setup: owner approved %q for %s — running it", ask.cfg.Setup, ask.repoRoot)
	m.emitTool("Running worktree setup · " + ask.cfg.Setup)
	// Its own context: the create context that spawned this goroutine is cancelled the moment
	// session.create returns, and a 15-minute `pnpm install` must not die with it.
	if err := worktree.RunSetup(context.Background(), ask.worktreePath, ask.cfg, ask.port); err != nil {
		log.Printf("worktree setup: %v", err)
		m.emitTool("✗ Worktree setup failed — " + err.Error())
		return
	}
	m.emitTool("✓ Worktree setup finished")
}

// noteWorktreeSetupSkipped makes a skipped setup command visible OUTSIDE the session too. The
// in-session note (emitTool) is the primary signal, but a worktree whose install step never ran is a
// half-built workspace, and the person who needs to know may not have that session open.
func (h *Hub) noteWorktreeSetupSkipped(sessionID, repoRoot, cmd, why string) {
	log.Printf("worktree setup: SKIPPED %q in %s — %s", cmd, repoRoot, why)
	h.recordActivity(activity.Event{
		Kind:      activity.KindNeedsInput,
		SessionID: sessionID,
		Title:     "Worktree setup didn't run",
		Detail:    cmd + " — " + why,
		NeedsYou:  true,
	})
}
