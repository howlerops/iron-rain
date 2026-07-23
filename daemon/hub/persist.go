package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
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
	Members       []worktree.Member `json:"members,omitempty"`
	ParentID      string            `json:"parent_id,omitempty"`
	Subtask       string            `json:"subtask,omitempty"`
	// Model is stored so a restarted session (session.restart) comes back on the same model.
	Model         string            `json:"model,omitempty"`
	ModelProvider string            `json:"model_provider,omitempty"`
}

func metaToPersisted(m sessionMeta) persistedMeta {
	return persistedMeta{
		ProjectID: m.projectID, Cwd: m.cwd, WorkspaceName: m.workspaceName, Branch: m.branch,
		WorktreePath: m.worktreePath, BaseCommit: m.baseCommit, RepoRoot: m.repoRoot, Port: m.port,
		IssueID: m.issueID, IssueKey: m.issueKey, IssueProvider: m.issueProvider, Members: m.members,
		ParentID: m.parentID, Subtask: m.subtask,
	}
}

func (pm persistedMeta) toMeta() sessionMeta {
	return sessionMeta{
		projectID: pm.ProjectID, cwd: pm.Cwd, workspaceName: pm.WorkspaceName, branch: pm.Branch,
		worktreePath: pm.WorktreePath, baseCommit: pm.BaseCommit, repoRoot: pm.RepoRoot, port: pm.Port,
		issueID: pm.IssueID, issueKey: pm.IssueKey, issueProvider: pm.IssueProvider, members: pm.Members,
		parentID: pm.ParentID, subtask: pm.Subtask,
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
	m.mu.Unlock()
	pm := metaToPersisted(meta)
	pm.Model, pm.ModelProvider = model, modelProvider
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
	h.mu.Lock()
	list := make([]protocol.Session, 0, len(h.sessions))
	for _, m := range h.sessions {
		list = append(list, m.info())
	}
	h.mu.Unlock()
	list = append(list, h.stoppedSessions()...)
	h.broadcast(protocol.TypeSessionList, protocol.SessionList{Sessions: list})
}

// RestoreSessions re-attaches every persisted session on startup so a daemon restart doesn't
// lose the user's running work (opencode/claude sessions persist server-side). It prunes
// expired records first, then attaches each survivor; a record that can't be re-attached
// (provider can't attach, or the session is gone server-side) is dropped. Call once, after
// providers are registered.
func (h *Hub) RestoreSessions(ctx context.Context, ttl time.Duration) {
	h.mu.Lock()
	db := h.db
	h.mu.Unlock()
	if db == nil {
		return
	}
	// Drop stale records before we try to attach them (nothing is live yet at startup, so
	// this is a pure age-based prune).
	if n, err := db.PruneSessions(time.Now().Unix() - int64(ttl.Seconds())); err == nil && n > 0 {
		log.Printf("restore: pruned %d expired session record(s)", n)
	}
	recs, err := db.Sessions()
	if err != nil {
		log.Printf("restore: read sessions: %v", err)
		return
	}
	restored, stopped := 0, 0
	for _, r := range recs {
		att := h.attacherFor(r.Provider)
		if att != nil {
			if sess, err := att.Attach(ctx, r.ID, r.Cwd); err == nil {
				var pm persistedMeta
				_ = json.Unmarshal([]byte(r.Meta), &pm)
				m := h.addSession(sess, pm.toMeta())
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
	if restored > 0 || stopped > 0 {
		log.Printf("restore: re-attached %d session(s), %d kept as stopped/restartable", restored, stopped)
		h.broadcastSessionList()
	}
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

	att := h.attacherFor(provider)
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
	return h.addSession(sess, meta), nil
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
		})
	}
	return out
}

// attacherFor resolves an Attacher for a provider: the client-less factory first (opencode
// with the daemon's configured URL), else the registered provider if it implements Attacher.
func (h *Hub) attacherFor(provider string) agent.Attacher {
	h.mu.Lock()
	factory := h.attach
	registered := h.providers[provider]
	h.mu.Unlock()
	if factory != nil {
		if att := factory(provider, ""); att != nil {
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
	if n, err := db.PruneSessions(time.Now().Unix() - int64(ttl.Seconds())); err != nil {
		log.Printf("prune: %v", err)
	} else if n > 0 {
		log.Printf("prune: removed %d stale session record(s)", n)
	}
}
