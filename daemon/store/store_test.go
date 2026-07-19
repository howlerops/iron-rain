package store

import (
	"path/filepath"
	"testing"
)

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
