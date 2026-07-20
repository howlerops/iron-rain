package store

import (
	"path/filepath"
	"testing"
	"time"
)

func TestHandoffsRoundTrip(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "h.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	now := time.Now().Unix()
	if err := db.UpsertHandoff(HandoffRecord{SessionID: "s1", Cwd: "/repo", Path: "/repo/.oculus/handoff/s1.md", Title: "T1", Summary: "sum", UpdatedAt: now}); err != nil {
		t.Fatalf("upsert s1: %v", err)
	}
	if err := db.UpsertHandoff(HandoffRecord{SessionID: "s2", Cwd: "/other", Path: "/other/.oculus/handoff/s2.md", Title: "T2", UpdatedAt: now + 1}); err != nil {
		t.Fatalf("upsert s2: %v", err)
	}
	// Upsert same session updates in place (no duplicate row).
	if err := db.UpsertHandoff(HandoffRecord{SessionID: "s1", Cwd: "/repo", Path: "/repo/.oculus/handoff/s1.md", Title: "T1b", UpdatedAt: now + 2}); err != nil {
		t.Fatalf("re-upsert s1: %v", err)
	}

	all, err := db.Handoffs("")
	if err != nil || len(all) != 2 {
		t.Fatalf("Handoffs(all) = %d, %v; want 2", len(all), err)
	}
	if all[0].SessionID != "s1" || all[0].Title != "T1b" { // most-recent first, updated title
		t.Fatalf("ordering/update wrong: %+v", all[0])
	}
	scoped, err := db.Handoffs("/other")
	if err != nil || len(scoped) != 1 || scoped[0].SessionID != "s2" {
		t.Fatalf("Handoffs(/other) = %+v, %v", scoped, err)
	}
	if err := db.DeleteHandoff("s1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if all, _ := db.Handoffs(""); len(all) != 1 {
		t.Fatalf("after delete = %d, want 1", len(all))
	}
}

func TestSessionRecordsRoundTripAndPrune(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "sess.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	now := time.Now().Unix()
	if err := db.SaveSession(SessionRecord{ID: "a", Provider: "opencode", Cwd: "/repo", Meta: `{"cwd":"/repo"}`}, now); err != nil {
		t.Fatalf("save a: %v", err)
	}
	// An old record (updated 30 days ago) should be prunable.
	if err := db.SaveSession(SessionRecord{ID: "old", Provider: "opencode", Cwd: "/x"}, now-30*24*3600); err != nil {
		t.Fatalf("save old: %v", err)
	}

	recs, err := db.Sessions()
	if err != nil || len(recs) != 2 {
		t.Fatalf("Sessions() = %d recs, %v; want 2", len(recs), err)
	}

	// Touch 'a' to now so a 7-day prune keeps it but drops 'old'.
	if err := db.TouchSessions([]string{"a"}, now); err != nil {
		t.Fatalf("touch: %v", err)
	}
	n, err := db.PruneSessions(now - 7*24*3600)
	if err != nil || n != 1 {
		t.Fatalf("PruneSessions = %d, %v; want 1 removed", n, err)
	}
	recs, _ = db.Sessions()
	if len(recs) != 1 || recs[0].ID != "a" || recs[0].Meta != `{"cwd":"/repo"}` {
		t.Fatalf("after prune: %+v; want only 'a' with its meta", recs)
	}

	// Delete removes it.
	if err := db.DeleteSession("a"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if recs, _ := db.Sessions(); len(recs) != 0 {
		t.Fatalf("after delete: %d recs; want 0", len(recs))
	}
}

func TestAutoVacuumEnabled(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "vac.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	var mode int
	if err := db.db.QueryRow(`PRAGMA auto_vacuum`).Scan(&mode); err != nil {
		t.Fatalf("pragma: %v", err)
	}
	if mode != 2 { // 2 = INCREMENTAL
		t.Fatalf("auto_vacuum = %d; want 2 (INCREMENTAL)", mode)
	}
}

func TestSessionNamesRoundTrip(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()

	// Unknown id -> not found.
	if _, ok := db.Name("nope"); ok {
		t.Fatal("expected no name for unknown id")
	}

	// Set + read back.
	if err := db.SetName("sess-1", "  My Session  "); err != nil {
		t.Fatalf("set: %v", err)
	}
	if got, ok := db.Name("sess-1"); !ok || got != "My Session" {
		t.Fatalf("got %q, %v; want trimmed 'My Session', true", got, ok)
	}

	// Upsert overwrites.
	if err := db.SetName("sess-1", "Renamed"); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if got, _ := db.Name("sess-1"); got != "Renamed" {
		t.Fatalf("got %q; want 'Renamed'", got)
	}

	// Blank clears (reset to default).
	if err := db.SetName("sess-1", "   "); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if _, ok := db.Name("sess-1"); ok {
		t.Fatal("expected name cleared by blank set")
	}
}

func TestNamesPersistAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "persist.db")
	db, err := Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.SetName("a", "Alpha"); err != nil {
		t.Fatalf("set: %v", err)
	}
	db.Close()

	db2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer db2.Close()
	if got, ok := db2.Name("a"); !ok || got != "Alpha" {
		t.Fatalf("got %q, %v; want 'Alpha', true after reopen", got, ok)
	}
	all, err := db2.Names()
	if err != nil {
		t.Fatalf("names: %v", err)
	}
	if all["a"] != "Alpha" {
		t.Fatalf("Names() = %v; want a=Alpha", all)
	}
}
