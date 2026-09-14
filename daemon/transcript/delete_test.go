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

// The open handle has to go too, AND a later append must not bring the file back.
//
// Two separate resurrections. Deleting the file while the store still holds a descriptor means the
// next Append writes into an unlinked inode — data nobody can see and nothing will clean up. And
// Append opens O_APPEND|O_CREATE, so once the handle was dropped it simply recreated the file.
//
// The second one is reachable on the ordinary delete path, not in theory: session.stop runs Close
// then removeSession on the DISPATCH goroutine while the session's pump is still unwinding, and that
// unwind ends in closeTurn → finalizeTurnTranscript → Append. The agent's last reply therefore landed
// back on disk verbatim, with no session record anywhere — and nothing would ever remove it, because
// the TTL prune enumerates ids from the session records that were just deleted.
//
// This asserts the stronger guarantee: after a delete, that session id is closed for writing. Session
// ids are never reused (a restart mints a new one), so the only thing that can append afterwards is
// the unwinding pump, and its output is exactly what must not survive.
func TestADeletedTranscriptStaysDeleted(t *testing.T) {
	dir := t.TempDir()
	s := New(dir)
	const sid = "ses_deleted"
	_ = s.Append(sid, Entry{Kind: "user", Text: "something private"})
	if err := s.Delete(sid); err != nil {
		t.Fatal(err)
	}

	// The pump, still unwinding, writes the turn's final reply.
	_ = s.Append(sid, Entry{Kind: "assistant", Text: "the agent's last reply"})

	if _, err := os.Stat(filepath.Join(dir, sid+".jsonl")); !os.IsNotExist(err) {
		b, _ := os.ReadFile(filepath.Join(dir, sid+".jsonl"))
		t.Errorf("the transcript came BACK after the session was deleted, holding %q.\n\n"+
			"There is no session record for it, so no prune will ever find it — the delete's privacy "+
			"claim is simply untrue.", strings.TrimSpace(string(b)))
	}
	got, err := s.Read(sid)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("reading a deleted session returned %+v, want nothing", got)
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
