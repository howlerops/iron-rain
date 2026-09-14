package hub

import (
	"strings"
	"testing"

	"github.com/howlerops/oculus/daemon/transcript"
)

// The never-lose-work backstop held every question and no answers.
//
// The transcript package's doc says it "mirrors assistant/tool/status events". It never did: the
// only Appends anywhere in the tree were the user write-ahead and error statuses. So after a crash
// the recoverable record of a session was the user talking to themselves — and the CLI recap built
// on top of it handed an amnesiac agent a list of its user's questions with nothing between them,
// while telling it that this was the conversation it had forgotten.
//
// This drives the production path rather than hand-building entries. Asserting that buildRecap
// formats an assistant entry correctly proves nothing when nothing in production ever writes one,
// which is exactly how this shipped: recap_test.go supplied the entry the daemon never did.
func TestAFinishedTurnRecordsItsReplyInTheWriteAheadLog(t *testing.T) {
	const sid = "sub_parent" // subSess.ID()
	tr := transcript.New(t.TempDir())
	if tr == nil {
		t.Fatal("could not open a transcript store")
	}
	h := &Hub{transcripts: tr, sessions: map[string]*managedSession{}}
	m := newManagedSession(h, &subSess{}, sessionMeta{})

	// A turn's worth of streamed assistant text, accumulated the way the pump accumulates it.
	m.accMu.Lock()
	m.asstAccum.WriteString("Added a retry with backoff in fetchWithRetry.")
	m.accMu.Unlock()

	m.finalizeTurnTranscript()

	entries, err := tr.Read(sid)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var assistant []string
	for _, e := range entries {
		if e.Kind == "assistant" {
			assistant = append(assistant, e.Text)
		}
	}
	if len(assistant) != 1 {
		t.Fatalf("the write-ahead log holds %d assistant entries, want 1 — a crash here loses every "+
			"reply the agent ever made, and the CLI recap has no answers to replay", len(assistant))
	}
	if !strings.Contains(assistant[0], "fetchWithRetry") {
		t.Errorf("recorded %q, want the turn's reply", assistant[0])
	}
}

// It must be recorded even with no SQLite store attached. That is the case the backstop exists FOR,
// and the case the old placement — inside the `db != nil` branch — skipped entirely.
func TestTheReplyIsRecordedEvenWithNoDatabase(t *testing.T) {
	const sid = "sub_parent"
	tr := transcript.New(t.TempDir())
	h := &Hub{transcripts: tr, sessions: map[string]*managedSession{}} // db is nil
	m := newManagedSession(h, &subSess{}, sessionMeta{})

	m.accMu.Lock()
	m.asstAccum.WriteString("the only copy of this sentence")
	m.accMu.Unlock()
	m.finalizeTurnTranscript()

	entries, _ := tr.Read(sid)
	for _, e := range entries {
		if e.Kind == "assistant" && strings.Contains(e.Text, "only copy") {
			return
		}
	}
	t.Error("with no database attached the reply was written nowhere at all — the durable backstop " +
		"is silent in precisely the situation it exists to cover")
}

// A turn that produced no text must not append an empty entry: an empty "assistant" line would make
// the recap's has-an-answer check pass while adding nothing to read.
func TestAnEmptyTurnRecordsNothing(t *testing.T) {
	const sid = "sub_parent"
	tr := transcript.New(t.TempDir())
	h := &Hub{transcripts: tr, sessions: map[string]*managedSession{}}
	m := newManagedSession(h, &subSess{}, sessionMeta{})

	m.accMu.Lock()
	m.asstAccum.WriteString("   \n  ")
	m.accMu.Unlock()
	m.finalizeTurnTranscript()

	entries, _ := tr.Read(sid)
	for _, e := range entries {
		if e.Kind == "assistant" {
			t.Errorf("recorded an empty reply: %q", e.Text)
		}
	}
}
