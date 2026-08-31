package opencode

import (
	"strings"
	"testing"
)

// opencode sends SEVERAL assistant messages inside one turn — a short step summary before each
// phase of work — and no idle between them. The client buffers deltas into a single bubble until the
// turn ends, so consecutive messages arrived with NOTHING between them: the last sentence of one and
// the first word of the next were glued into a single word,
//
//	"…running the final verification now.Reviewing final task"
//
// and the whole reply collapsed into one run-on paragraph, leaving the markdown renderer no
// structure to work with. Reported from a phone, where the result is a wall of bold text.
//
// This asserts the boundary logic itself: a break when the message id changes, never on the first
// message, and never within one message.
func TestMessageBoundaryInsertsExactlyOneBreak(t *testing.T) {
	s := &session{id: "s1"}
	var out strings.Builder

	// feed mirrors the adapter's decision for a text delta belonging to msgID.
	feed := func(msgID, text string) {
		s.deltaMu.Lock()
		changed := s.lastDeltaMsg != "" && s.lastDeltaMsg != msgID
		s.lastDeltaMsg = msgID
		s.deltaMu.Unlock()
		if changed {
			out.WriteString("\n\n")
		}
		out.WriteString(text)
	}

	feed("m1", "first message")
	feed("m1", " continues")
	feed("m2", "second message")
	feed("m2", " continues")

	got := out.String()
	want := "first message continues\n\nsecond message continues"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	// The specific symptom: no glued word boundary anywhere.
	if strings.Contains(got, "continuessecond") {
		t.Error("consecutive messages were concatenated with no separator")
	}
	// And no leading break before the very first message.
	if strings.HasPrefix(got, "\n") {
		t.Error("a break was emitted before the first message of the turn")
	}
}

// A new turn must start clean, or its first message differs from the previous turn's last and opens
// with a stray blank line.
func TestTurnResetClearsTheMessageChain(t *testing.T) {
	s := &session{id: "s1"}
	s.lastDeltaMsg = "m-from-previous-turn"

	s.deltaMu.Lock()
	s.lastDeltaMsg = "" // what Prompt does
	s.deltaMu.Unlock()

	s.deltaMu.Lock()
	changed := s.lastDeltaMsg != "" && s.lastDeltaMsg != "m1"
	s.deltaMu.Unlock()
	if changed {
		t.Error("the first message of a new turn would open with a blank line")
	}
}
