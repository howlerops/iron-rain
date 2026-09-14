package transcript

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A deleted session left every prompt the user ever typed into it on disk, verbatim, forever.
//
// The SQLite side was always swept — DeleteSession, DeleteTranscript and PruneSessions all run — so
// the session vanished from every device and the app reported it gone. The write-ahead JSONL beside
// it stayed. Nothing in the daemon could remove it: this package exposed New/Append/Read/Close and
// no Delete at all. That is a privacy claim the product makes and was not keeping, and a directory
// that only ever grows on a machine whose daemon self-updates and restarts all day.
func TestDeletingASessionRemovesItsTranscriptFile(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	if s == nil {
		t.Fatal("could not open the store")
	}
	const sid = "ses_secret"
	if err := s.Append(sid, Entry{Kind: "user", Text: "the database password is hunter2"}); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(dir, sid+".jsonl")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("precondition: the transcript was never written: %v", err)
	}

	if err := s.Delete(sid); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		b, _ := os.ReadFile(path)
		t.Fatalf("the file is still there after the session was deleted, and it still contains %q",
			strings.TrimSpace(string(b)))
	}
}

// The open handle has to go too. Deleting the file while the store still holds a descriptor for it
// means the next Append silently resurrects the data into an unlinked inode — the session's prompts
// keep being written to a file nobody can see and nothing will ever clean up.
func TestDeleteDropsTheOpenHandleSoAppendsDoNotResurrectIt(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	const sid = "ses_reused"
	_ = s.Append(sid, Entry{Kind: "user", Text: "first"})
	if err := s.Delete(sid); err != nil {
		t.Fatal(err)
	}

	// A later append must start a NEW file containing only what came after the delete.
	_ = s.Append(sid, Entry{Kind: "user", Text: "second"})
	got, err := s.Read(sid)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Text != "second" {
		t.Errorf("after delete + append the transcript reads %+v, want only the new entry — the "+
			"store is still writing through a handle to the deleted file", got)
	}
}

// Deleting something that was never written, or a store that does not exist, must not error: both
// are ordinary on a delete path that runs for every session.
func TestDeletingNothingIsNotAnError(t *testing.T) {
	s := New(t.TempDir())
	if err := s.Delete("never_existed"); err != nil {
		t.Errorf("deleting an absent transcript errored: %v", err)
	}
	if err := s.Delete(""); err != nil {
		t.Errorf("deleting an empty id errored: %v", err)
	}
	var nilStore *Store
	if err := nilStore.Delete("x"); err != nil {
		t.Errorf("a nil store must be a no-op, got %v", err)
	}
}
