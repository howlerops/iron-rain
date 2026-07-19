package hub

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"github.com/howlerops/oculus/daemon/agent"
	"github.com/howlerops/oculus/daemon/protocol"
	"github.com/howlerops/oculus/daemon/store"
)

// persistedMeta is the JSON-serializable snapshot of sessionMeta stored in the DB so a
// managed session can be reconstructed (worktree diff/review, issue links, ports) after a
// daemon restart. The user-set label is NOT stored here — the session_names table owns it,
// so a rename has a single source of truth (addSession re-applies it on restore).
type persistedMeta struct {
	ProjectID     string `json:"project_id,omitempty"`
	Cwd           string `json:"cwd,omitempty"`
	WorkspaceName string `json:"workspace_name,omitempty"`
	Branch        string `json:"branch,omitempty"`
	WorktreePath  string `json:"worktree_path,omitempty"`
	BaseCommit    string `json:"base_commit,omitempty"`
	RepoRoot      string `json:"repo_root,omitempty"`
	Port          int    `json:"port,omitempty"`
	IssueID       string `json:"issue_id,omitempty"`
	IssueKey      string `json:"issue_key,omitempty"`
	IssueProvider string `json:"issue_provider,omitempty"`
}

func metaToPersisted(m sessionMeta) persistedMeta {
	return persistedMeta{
		ProjectID: m.projectID, Cwd: m.cwd, WorkspaceName: m.workspaceName, Branch: m.branch,
		WorktreePath: m.worktreePath, BaseCommit: m.baseCommit, RepoRoot: m.repoRoot, Port: m.port,
		IssueID: m.issueID, IssueKey: m.issueKey, IssueProvider: m.issueProvider,
	}
}

func (pm persistedMeta) toMeta() sessionMeta {
	return sessionMeta{
		projectID: pm.ProjectID, cwd: pm.Cwd, workspaceName: pm.WorkspaceName, branch: pm.Branch,
		worktreePath: pm.WorktreePath, baseCommit: pm.BaseCommit, repoRoot: pm.RepoRoot, port: pm.Port,
		issueID: pm.IssueID, issueKey: pm.IssueKey, issueProvider: pm.IssueProvider,
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
	m.mu.Unlock()
	blob, err := json.Marshal(metaToPersisted(meta))
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
	restored := 0
	for _, r := range recs {
		att := h.attacherFor(r.Provider)
		if att == nil {
			_ = db.DeleteSession(r.ID) // provider can't attach — nothing to restore
			continue
		}
		sess, err := att.Attach(ctx, r.ID, r.Cwd)
		if err != nil {
			log.Printf("restore: attach %s (%s) failed, dropping: %v", r.ID, r.Provider, err)
			_ = db.DeleteSession(r.ID) // gone server-side
			continue
		}
		var pm persistedMeta
		_ = json.Unmarshal([]byte(r.Meta), &pm)
		m := h.addSession(sess, pm.toMeta())
		go m.run()
		restored++
	}
	if restored > 0 {
		log.Printf("restore: re-attached %d session(s)", restored)
		h.broadcastSessionList()
	}
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
