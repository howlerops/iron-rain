package store

import (
	"path/filepath"
	"strings"
	"testing"
)

// A card that advances state must REPLACE its stored row, not be dropped.
//
// AppendTranscript is INSERT OR IGNORE — right for messages, wrong for cards. A sub-agent lane is
// written "running" and later sealed "done" under the same stable id, and IGNORE silently discarded
// the seal: on reload the lane replayed in the state it started in and span forever for a sub-agent
// that had already finished. The row must also keep its ORIGINAL position, because that is where the
// card belongs in the conversation.
func TestUpsertRenderableAdvancesInPlace(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer s.Close()

	if _, err := s.AppendTranscript("sess", 1, "sub:abc", []byte(`{"status":"running"}`)); err != nil {
		t.Fatalf("append: %v", err)
	}
	// Something else lands after it, so "kept its position" is observable.
	if _, err := s.AppendTranscript("sess", 2, "msg:1", []byte(`{"text":"later"}`)); err != nil {
		t.Fatalf("append 2: %v", err)
	}
	if err := s.UpsertRenderable("sess", 3, "sub:abc", []byte(`{"status":"done"}`)); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	rows, err := s.Transcript("sess")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var seen []string
	for _, r := range rows {
		seen = append(seen, string(r))
	}
	joined := strings.Join(seen, "\n")
	if strings.Contains(joined, `"running"`) {
		t.Fatalf("the seal did not replace the running row:\n%s", joined)
	}
	if !strings.Contains(joined, `"done"`) {
		t.Fatalf("the sealed state is missing entirely:\n%s", joined)
	}
	if len(seen) != 2 {
		t.Fatalf("expected the card plus the later message, got %d rows:\n%s", len(seen), joined)
	}
	// Position preserved: the card still precedes the message that arrived after it.
	if !strings.Contains(seen[0], `"done"`) {
		t.Fatalf("the advanced card lost its place in the conversation:\n%s", joined)
	}
}
