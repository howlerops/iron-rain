package store

import (
	"path/filepath"
	"testing"
	"time"
)

// PruneSessions must evict orphaned handoffs, which its own comment has always claimed.
//
// It deleted orphaned transcript_events and transcript_archive rows and left handoffs alone.
// DeleteHandoff's only caller is removeSession, and the TTL path never reaches it — so the table
// grew monotonically for the life of the install: one row per indexed handoff file, none of them
// reachable from any session that still exists, and nothing that would ever remove them.
func TestPruneSessionsEvictsOrphanedHandoffs(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	now := time.Now().Unix()
	old := now - 30*24*3600

	// One session that will age out, one that will not. Both have a handoff.
	if err := db.SaveSession(SessionRecord{ID: "stale", Provider: "fake"}, old); err != nil {
		t.Fatalf("save stale: %v", err)
	}
	if err := db.SaveSession(SessionRecord{ID: "live", Provider: "fake"}, now); err != nil {
		t.Fatalf("save live: %v", err)
	}
	for _, id := range []string{"stale", "live"} {
		if err := db.UpsertHandoff(HandoffRecord{
			SessionID: id, Cwd: "/work", Path: "/work/HANDOFF.md", Title: id, UpdatedAt: now,
		}); err != nil {
			t.Fatalf("upsert handoff %s: %v", id, err)
		}
	}

	if _, err := db.PruneSessions(now - 7*24*3600); err != nil {
		t.Fatalf("prune: %v", err)
	}

	hs, err := db.Handoffs("")
	if err != nil {
		t.Fatalf("handoffs: %v", err)
	}
	var live, stale int
	for _, h := range hs {
		switch h.SessionID {
		case "live":
			live++
		case "stale":
			stale++
		}
	}
	if stale != 0 {
		t.Errorf("the pruned session's handoff survived. Nothing else deletes it — DeleteHandoff's only "+
			"caller is removeSession, which the TTL path never reaches — so this table only ever grows. "+
			"(%d orphaned rows left)", stale)
	}
	if live != 1 {
		t.Errorf("a LIVE session's handoff was deleted (%d rows left) — the handoff index is what makes a "+
			"session's work findable across repos", live)
	}
}
