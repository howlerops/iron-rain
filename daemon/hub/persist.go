package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/howlerops/oculus/daemon/agent"
	"github.com/howlerops/oculus/daemon/protocol"
	"github.com/howlerops/oculus/daemon/store"
	"github.com/howlerops/oculus/daemon/worktree"
)

// persistedMeta is the JSON-serializable snapshot of sessionMeta stored in the DB so a
// managed session can be reconstructed (worktree diff/review, issue links, ports) after a
// daemon restart. The user-set label is NOT stored here — the session_names table owns it,
// so a rename has a single source of truth (addSession re-applies it on restore).
type persistedMeta struct {
	ProjectID     string            `json:"project_id,omitempty"`
	Cwd           string            `json:"cwd,omitempty"`
	WorkspaceName string            `json:"workspace_name,omitempty"`
	Branch        string            `json:"branch,omitempty"`
	WorktreePath  string            `json:"worktree_path,omitempty"`
	BaseCommit    string            `json:"base_commit,omitempty"`
	RepoRoot      string            `json:"repo_root,omitempty"`
	Port          int               `json:"port,omitempty"`
	IssueID       string            `json:"issue_id,omitempty"`
	IssueKey      string            `json:"issue_key,omitempty"`
	IssueProvider string            `json:"issue_provider,omitempty"`
	ProviderURL   string            `json:"provider_url,omitempty"`
	Members       []worktree.Member `json:"members,omitempty"`
	Roots         []string          `json:"roots,omitempty"`
	ParentID      string            `json:"parent_id,omitempty"`
	Subtask       string            `json:"subtask,omitempty"`
	// Model is stored so a restarted session (session.restart) comes back on the same model.
	Model         string `json:"model,omitempty"`
	ModelProvider string `json:"model_provider,omitempty"`
	Mode          string `json:"mode,omitempty"` // code | ask | architect (see hub/modes.go)
	// Where the session runs. This has to be durable or the restart path lies: a remote session is a
	// CLI-provider session, so it can NEVER be re-attached and always comes back through
	// stoppedSessions/restartSession — i.e. every daemon restart would resurrect an ssh session
	// looking exactly like one running on this Mac.
	ExecKind string `json:"exec_kind,omitempty"`
	ExecHost string `json:"exec_host,omitempty"`
}

func metaToPersisted(m sessionMeta) persistedMeta {
	return persistedMeta{
		ProjectID: m.projectID, Cwd: m.cwd, WorkspaceName: m.workspaceName, Branch: m.branch,
		WorktreePath: m.worktreePath, BaseCommit: m.baseCommit, RepoRoot: m.repoRoot, Port: m.port,
		IssueID: m.issueID, IssueKey: m.issueKey, IssueProvider: m.issueProvider, Members: m.members,
		Roots: m.roots, ParentID: m.parentID, Subtask: m.subtask, ProviderURL: m.providerURL,
		ExecKind: m.execKind, ExecHost: m.execHost,
	}
}

func (pm persistedMeta) toMeta() sessionMeta {
	return sessionMeta{
		projectID: pm.ProjectID, cwd: pm.Cwd, workspaceName: pm.WorkspaceName, branch: pm.Branch,
		worktreePath: pm.WorktreePath, baseCommit: pm.BaseCommit, repoRoot: pm.RepoRoot, port: pm.Port,
		issueID: pm.IssueID, issueKey: pm.IssueKey, issueProvider: pm.IssueProvider, members: pm.Members,
		roots: pm.Roots, parentID: pm.ParentID, subtask: pm.Subtask, providerURL: pm.ProviderURL,
		execKind: pm.ExecKind, execHost: pm.ExecHost,
	}
}

// persistSession writes/updates a session's durable record stamped with the current time
// (on create/attach). Best-effort — persistence never blocks the session lifecycle.
func (h *Hub) persistSession(m *managedSession) { h.persistSessionAt(m, time.Now().Unix()) }

// persistSessionAt upserts a session's record with an explicit updated_at, which is the TTL
// clock. The periodic touch uses the session's last-activity time here so idle sessions age
// out; create/attach uses "now".
func (h *Hub) persistSessionAt(m *managedSession, updatedAt int64) {
	h.mu.Lock()
	db := h.db
	h.mu.Unlock()
	if db == nil {
		return
	}
	m.mu.Lock()
	meta := m.meta
	model, modelProvider := m.model, m.modelProvider
	mode := m.mode
	m.mu.Unlock()
	pm := metaToPersisted(meta)
	pm.Model, pm.ModelProvider = model, modelProvider
	pm.Mode = mode
	blob, err := json.Marshal(pm)
	if err != nil {
		return
	}
	rec := store.SessionRecord{ID: m.sess.ID(), Provider: m.sess.Provider(), Cwd: meta.cwd, Meta: string(blob)}
	if err := db.SaveSession(rec, updatedAt); err != nil {
		log.Printf("persist session %s: %v", m.sess.ID(), err)
	}
}

// broadcastSessionList pushes the current session list to every client (after restore,
// delete, or rename) so all devices converge on the same set.
func (h *Hub) broadcastSessionList() {
	h.broadcast(protocol.TypeSessionList, protocol.SessionList{Sessions: h.sessionList()})
}

func (h *Hub) sessionList() []protocol.Session {
	h.mu.Lock()
	list := make([]protocol.Session, 0, len(h.sessions))
	for _, m := range h.sessions {
		if s := m.info(); isPrimarySession(s) {
			list = append(list, s)
		}
	}
	h.mu.Unlock()
	list = append(list, h.stoppedSessions()...)
	return list
}

func isPrimarySession(s protocol.Session) bool {
	return s.ParentID == ""
}

// RestoreSessions re-attaches every persisted session on startup so a daemon restart doesn't lose
// the user's running work (opencode/claude/pi sessions all survive outside this process). The daemon
// self-updates on a ~6-hourly cadence, so this path runs constantly in normal use — it is the
// difference between a taken-over terminal session that keeps working and one that comes back empty.
//
// It prunes expired records first, then attaches each survivor USING ITS OWN PERSISTED META: the
// server URL it was taken over from and the directory it belongs to. A record that can't be
// re-attached — provider can't attach, backend doesn't hold it any more, directory unresolvable — is
// KEPT and listed as stopped + restartable. Never attached-but-blind: that produces a session that
// looks live, shows nothing and silently swallows every message. Call once, after providers are
// registered.
func (h *Hub) RestoreSessions(ctx context.Context, ttl time.Duration) {
	h.mu.Lock()
	db := h.db
	h.mu.Unlock()
	if db == nil {
		return
	}
	// Drop stale records before we try to attach them (nothing is live yet at startup, so
	// this is a pure age-based prune). Reclaim their worktrees FIRST, for the same reason as the
	// periodic prune: the record is the only thing that remembers the directory, so once it is gone
	// the worktree is orphaned permanently.
	cutoff := time.Now().Unix() - int64(ttl.Seconds())
	h.reclaimExpiredWorktrees(db, cutoff)
	if n, err := db.PruneSessions(cutoff); err == nil && n > 0 {
		log.Printf("restore: pruned %d expired session record(s)", n)
	}
	recs, err := db.Sessions()
	if err != nil {
		log.Printf("restore: read sessions: %v", err)
		return
	}
	restored, stopped, dropped := 0, 0, 0
	for _, r := range recs {
		// Read the persisted meta FIRST: it names the opencode server this session lives on and the
		// directory it belongs to, and both decide HOW to attach. It used to be unmarshalled after the
		// attach had already happened, so the restore always attached to the daemon's default server,
		// in whatever directory the coarse record column happened to hold.
		var pm persistedMeta
		_ = json.Unmarshal([]byte(r.Meta), &pm)
		cwd := pm.Cwd
		if cwd == "" {
			cwd = r.Cwd
		}
		att := h.attacherFor(r.Provider, pm.ProviderURL)
		// Drop unrecoverable HUSKS: the provider says this id can't actually resume (e.g. a
		// claude-code session that never completed a first turn, so no UUID was ever recorded) AND we
		// hold no durable history for it. "Restoring" it would create a fresh empty session lying
		// about being the old one — the "clicking it does nothing" rows.
		if rc, ok := att.(agent.ResumeChecker); ok && !rc.CanResume(r.ID) {
			if rows, err := db.Transcript(r.ID); err == nil && len(rows) == 0 {
				_ = db.DeleteSession(r.ID)
				_ = db.DeleteTranscript(r.ID)
				dropped++
				continue
			}
		}
		if att != nil {
			if sess, err := restoreAttach(ctx, att, r.ID, cwd); err == nil {
				m := h.addSession(sess, pm.toMeta())
				// A re-attached session defaults to IDLE, not "running". opencode's /event has no replay,
				// so an already-idle restored session would otherwise emit no status and show "working"
				// forever (info() renders an unknown status as running). If it IS mid-turn server-side,
				// the next real event flips it back to running.
				m.seedStatus(protocol.StatusIdle)
				h.applyRestoredModel(m, pm)
				m.mu.Lock()
				m.mode = pm.Mode
				m.mu.Unlock()
				go m.run()
				restored++
				continue
			} else {
				log.Printf("restore: attach %s (%s) failed: %v", r.ID, r.Provider, err)
			}
		}
		// Couldn't re-attach (provider isn't an Attacher — e.g. a CLI agent — or the underlying
		// session is gone server-side). KEEP the record instead of deleting it, so it shows as a
		// "stopped" session the user can restart (a fresh session in the same folder/agent/model),
		// rather than silently vanishing. It ages out on the normal TTL.
		stopped++
	}
	if restored > 0 || stopped > 0 || dropped > 0 {
		log.Printf("restore: re-attached %d session(s), %d kept as stopped/restartable, %d unrecoverable husk(s) dropped", restored, stopped, dropped)
		h.broadcastSessionList()
	}
}

// verifiedAttacher is an Attacher that can PROVE the backend still holds the session before
// returning one (opencode implements it). Declared here, at the consumer, because only the restore
// path needs the distinction.
//
// It matters because an attach can succeed against a backend that has never heard of the session
// (opencode's /event is a server-wide bus, so it accepts any subscriber). That produces the worst
// possible outcome after a restart: a row that looks live, shows nothing, and silently drops every
// message. A restore must get a session or an ERROR — never a plausible-looking impostor.
type verifiedAttacher interface {
	AttachVerified(ctx context.Context, sessionID, cwd string) (agent.Session, error)
}

// restoreAttach attaches strictly where the provider supports it, so a failure lands the record in
// stopped/restartable (visible, recoverable) instead of attached-and-empty.
func restoreAttach(ctx context.Context, att agent.Attacher, id, cwd string) (agent.Session, error) {
	if va, ok := att.(verifiedAttacher); ok {
		return va.AttachVerified(ctx, id, cwd)
	}
	return att.Attach(ctx, id, cwd)
}

// applyRestoredModel puts a restored session back on the model the user chose. Nothing re-applied
// it, so every daemon restart silently moved the conversation onto the provider's default —
// mid-task, with the session's own header still naming the old model.
func (h *Hub) applyRestoredModel(m *managedSession, pm persistedMeta) {
	model, provider := pm.Model, pm.ModelProvider
	if model != "" {
		setter, ok := m.sess.(agent.ModelSetter)
		if !ok {
			return // can't put the model back — don't display a claim we didn't honor
		}
		if err := setter.SetModel(provider, model); err != nil {
			log.Printf("restore: %s: re-applying model %s/%s failed: %v", m.sess.ID(), provider, model, err)
			return
		}
	} else if r, ok := m.sess.(interface{ Model() (string, string) }); ok {
		// No stored choice (a taken-over session we never set a model on): adopt what the session
		// itself reports — opencode reads it back off the conversation on attach — so the header names
		// the model the turns will actually run on.
		provider, model = r.Model()
	}
	if model == "" {
		return
	}
	m.mu.Lock()
	m.model, m.modelProvider = model, provider
	m.mu.Unlock()
}

// restartSession re-creates a stopped (persisted-but-not-live) session as a FRESH provider session
// in the same folder, agent, and model, carrying over the user-set name. The underlying provider
// conversation can't be resumed (that's precisely why it's stopped — a CLI agent has no server-side
// session, or the previous one is gone), so this starts a NEW conversation in the same context.
// Returns the new managed session; the old record is removed (the session id changes).
func (h *Hub) restartSession(ctx context.Context, oldID string) (*managedSession, error) {
	h.mu.Lock()
	db := h.db
	h.mu.Unlock()
	if db == nil {
		return nil, fmt.Errorf("no session store")
	}
	recs, err := db.Sessions()
	if err != nil {
		return nil, err
	}
	var rec *store.SessionRecord
	for i := range recs {
		if recs[i].ID == oldID {
			rec = &recs[i]
			break
		}
	}
	if rec == nil {
		return nil, fmt.Errorf("no such session")
	}
	h.mu.Lock()
	p := h.providers[rec.Provider]
	h.mu.Unlock()
	if p == nil {
		return nil, fmt.Errorf("agent %q isn't available — is it installed and enabled?", rec.Provider)
	}
	var pm persistedMeta
	_ = json.Unmarshal([]byte(rec.Meta), &pm)
	cwd := pm.Cwd
	if cwd == "" {
		cwd = rec.Cwd
	}
	sess, err := p.Create(ctx, cwd, "")
	if err != nil {
		return nil, err
	}
	// Restart amnesia: a CLI/pi agent keeps continuity in the process we just lost, so a fresh
	// session would re-run the agent's COLD invocation — a new conversation shown under the old
	// session's history. Durable transcript rows are the daemon's own proof this conversation had
	// turns; when it did, tell the new session to resume (the adapter no-ops if its agent declares
	// no resume invocation, where cold really is the only option).
	if seq, err := db.MaxTranscriptSeq(oldID); err == nil && seq > 0 {
		if r, ok := sess.(interface{ MarkResumed() }); ok {
			r.MarkResumed()
			log.Printf("session.restart %s: prior turns found — resuming rather than starting cold", oldID)
		}
	}
	if pm.Model != "" {
		if setter, ok := sess.(agent.ModelSetter); ok {
			_ = setter.SetModel(pm.ModelProvider, pm.Model)
		}
	}
	// Carry the user-set name onto the new id before dropping the old record.
	if name, ok := db.Name(oldID); ok && name != "" {
		_ = db.SetName(sess.ID(), name)
	}
	m := h.addSession(sess, pm.toMeta())
	m.mu.Lock()
	m.model, m.modelProvider = pm.Model, pm.ModelProvider
	m.mode = pm.Mode
	m.mu.Unlock()
	h.persistSession(m)
	if sess.ID() != oldID {
		_ = db.DeleteSession(oldID) // the stopped record is replaced by the new live one
	}
	return m, nil
}

// recoverSession re-attaches an existing session with a FRESH provider attach, which re-resolves the
// session's real directory (opencode reports it) and heals a stale/wrong stored cwd — the fix for a
// session that OPENS but whose sends silently fail because the daemon bound it to the wrong directory
// partition. Unlike restartSession it KEEPS the conversation (same id + full history); it just re-binds
// the daemon to it correctly. Requires an Attacher provider (opencode); a CLI agent that can't attach
// returns an error (there's nothing server-side to re-attach — use Restart to start fresh).
func (h *Hub) recoverSession(ctx context.Context, id string) (*managedSession, error) {
	h.mu.Lock()
	db := h.db
	live := h.sessions[id]
	h.mu.Unlock()

	var provider string
	var meta sessionMeta
	if live != nil {
		provider = live.sess.Provider()
		meta = live.meta
	} else {
		if db == nil {
			return nil, fmt.Errorf("no session store")
		}
		recs, err := db.Sessions()
		if err != nil {
			return nil, err
		}
		var rec *store.SessionRecord
		for i := range recs {
			if recs[i].ID == id {
				rec = &recs[i]
				break
			}
		}
		if rec == nil {
			return nil, fmt.Errorf("no such session")
		}
		provider = rec.Provider
		var pm persistedMeta
		_ = json.Unmarshal([]byte(rec.Meta), &pm)
		meta = pm.toMeta()
	}

	// Recover re-binds to the session's OWN server (meta.providerURL), not the daemon's default —
	// otherwise "recovering" a taken-over session moves it to a server that doesn't have it.
	att := h.attacherFor(provider, meta.providerURL)
	if att == nil {
		return nil, fmt.Errorf("agent %q can't recover an existing conversation — use Restart to start fresh in the same folder", provider)
	}

	// Drop the broken live binding first so its stale event goroutine ends and it stops holding the id.
	if live != nil {
		h.mu.Lock()
		delete(h.sessions, id)
		h.mu.Unlock()
		_ = live.sess.Close()
	}

	sess, err := att.Attach(ctx, id, meta.cwd) // re-resolves the real directory; addSession heals meta.cwd
	if err != nil {
		return nil, err
	}
	m := h.addSession(sess, meta)
	m.seedStatus(protocol.StatusIdle) // recovered session is idle until a real event says otherwise (see restore)
	return m, nil
}

// stoppedSessions returns persisted records that aren't currently live as protocol.Session entries
// marked Status=stopped + Restartable, so the app lists them (and offers a restart) instead of the
// session just disappearing after a daemon restart.
func (h *Hub) stoppedSessions() []protocol.Session {
	h.mu.Lock()
	db := h.db
	live := make(map[string]bool, len(h.sessions))
	for id := range h.sessions {
		live[id] = true
	}
	h.mu.Unlock()
	if db == nil {
		return nil
	}
	recs, err := db.Sessions()
	if err != nil {
		return nil
	}
	out := make([]protocol.Session, 0, len(recs))
	for _, r := range recs {
		if live[r.ID] {
			continue // already surfaced by its live managedSession.info()
		}
		var pm persistedMeta
		_ = json.Unmarshal([]byte(r.Meta), &pm)
		name := ""
		if n, ok := db.Name(r.ID); ok {
			name = n
		}
		out = append(out, protocol.Session{
			ID: r.ID, Provider: r.Provider, Status: protocol.StatusStopped, Restartable: true,
			Name: name, Cwd: pm.Cwd, ProjectID: pm.ProjectID, WorkspaceName: pm.WorkspaceName,
			Branch: pm.Branch, ParentID: pm.ParentID, Subtask: pm.Subtask,
			IssueKey: pm.IssueKey, IssueID: pm.IssueID, Port: pm.Port,
			Model: pm.Model, ModelProvider: pm.ModelProvider,
			// A stopped remote session is the common case, not an edge one (ssh sessions run on the CLI
			// provider, which has nothing to re-attach to), so dropping the host here would mean the
			// host is missing from exactly the rows that outlive a restart.
			ExecKind: pm.ExecKind, ExecHost: pm.ExecHost,
		})
	}
	kept := out[:0]
	for _, s := range out {
		if isPrimarySession(s) {
			kept = append(kept, s)
		}
	}
	return kept
}

// attacherFor resolves an Attacher for a provider, preferring the factory bound to the session's OWN
// server URL — the one recorded when we attached to it.
//
// url is what makes a taken-over session survivable. A session started from a terminal lives on THAT
// terminal's `opencode serve`, which is generally not the daemon's configured one; re-attaching to
// the default server yields a session the server has never heard of. Empty url (a daemon-created
// session, or a record written before the URL was persisted) falls back to the registered provider.
func (h *Hub) attacherFor(provider, url string) agent.Attacher {
	h.mu.Lock()
	factory := h.attach
	registered := h.providers[provider]
	h.mu.Unlock()
	if factory != nil {
		if att := factory(provider, url); att != nil {
			return att
		}
	}
	att, _ := registered.(agent.Attacher)
	return att
}

// StartSessionPruning periodically touches live sessions (so they never expire while
// running) then prunes records older than the TTL, reclaiming freed pages. Runs until ctx
// is cancelled.
func (h *Hub) StartSessionPruning(ctx context.Context, interval, ttl time.Duration) {
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				h.touchAndPrune(ttl)
			}
		}
	}()
}

// StartConflictSweep periodically checks each worktree session for a would-be merge conflict with
// its default branch and flips the session's `conflicted` flag, re-broadcasting the list on any
// change — so a passive "conflict" badge appears without the user having to try a merge. Non-
// destructive (git merge-tree). Cheap: only worktree sessions are checked, on a slow interval.
func (h *Hub) StartConflictSweep(ctx context.Context, interval time.Duration) {
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				h.sweepConflicts(ctx)
			}
		}
	}()
}

func (h *Hub) sweepConflicts(ctx context.Context) {
	h.mu.Lock()
	var wts []*managedSession
	for _, m := range h.sessions {
		if m.meta.worktreePath != "" {
			wts = append(wts, m)
		}
	}
	h.mu.Unlock()

	changed := false
	for _, m := range wts {
		cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		paths, err := worktree.WouldConflict(cctx, m.meta.worktreePath, "")
		cancel()
		if err != nil {
			continue // transient git error — leave the flag as-is
		}
		conflicted := len(paths) > 0
		m.mu.Lock()
		if m.conflicted != conflicted {
			m.conflicted = conflicted
			changed = true
			if conflicted {
				log.Printf("session %s: worktree now CONFLICTS with default branch (%v)", m.sess.ID(), paths)
			} else {
				log.Printf("session %s: worktree conflict resolved", m.sess.ID())
			}
		}
		m.mu.Unlock()
	}
	if changed {
		h.broadcastSessionList()
	}
}

func (h *Hub) touchAndPrune(ttl time.Duration) {
	h.mu.Lock()
	db := h.db
	live := make([]*managedSession, 0, len(h.sessions))
	for _, m := range h.sessions {
		live = append(live, m)
	}
	h.mu.Unlock()
	if db == nil {
		return
	}
	// Re-stamp each live session's record with its LAST-ACTIVITY time (not "now"): an
	// active session gets a fresh timestamp (and is re-created if it had been pruned),
	// while a session that is in memory but has produced nothing for the TTL window (e.g.
	// a restored session whose server-side session is actually gone) keeps an old stamp
	// and is pruned below — instead of being kept alive forever by mere presence.
	for _, m := range live {
		h.persistSessionAt(m, m.lastActive())
	}
	cutoff := time.Now().Unix() - int64(ttl.Seconds())
	// Reclaim worktrees BEFORE the records go, because the record is the only thing that remembers
	// the directory exists. Prune first and the worktree is orphaned permanently: the session
	// vanishes from every UI while its checkout stays on disk forever — invisible AND undeletable
	// through the app. An abandoned fanout is the common way to get there, since nothing else ever
	// tears its variants down.
	h.reclaimExpiredWorktrees(db, cutoff)
	if n, err := db.PruneSessions(cutoff); err != nil {
		log.Printf("prune: %v", err)
	} else if n > 0 {
		log.Printf("prune: removed %d stale session record(s)", n)
	}
}

// reclaimExpiredWorktrees removes the worktrees of sessions that are about to be pruned — but ONLY
// the ones holding nothing a human would miss.
//
// Age is not evidence that work was abandoned. A worktree untouched for a week may still hold an
// afternoon of uncommitted changes, so the decision is made on CONTENT (clean tree, no unmerged
// commits) rather than on the clock, and anything else is deliberately left behind with a log line.
// Leaving a stale directory is annoying; deleting someone's unpushed work is not recoverable.
func (h *Hub) reclaimExpiredWorktrees(db *store.Store, cutoff int64) {
	recs, err := db.ExpiringSessions(cutoff)
	if err != nil || len(recs) == 0 {
		return
	}
	for _, r := range recs {
		if r.Meta == "" {
			continue
		}
		var pm persistedMeta
		if json.Unmarshal([]byte(r.Meta), &pm) != nil || pm.WorktreePath == "" {
			continue
		}
		// Only ever touch a worktree this daemon created and recorded. A path we didn't put there is
		// not ours to reason about, let alone delete.
		if _, err := os.Stat(pm.WorktreePath); err != nil {
			continue // already gone
		}
		removed, why, _ := worktree.RemoveIfUnchanged(pm.RepoRoot, pm.WorktreePath, pm.BaseCommit)
		if removed {
			log.Printf("prune: reclaimed the worktree of expired session %s (%s)", r.ID, pm.WorktreePath)
			continue
		}
		log.Printf("prune: KEEPING the worktree of expired session %s — %s (%s)", r.ID, why, pm.WorktreePath)
	}
}
