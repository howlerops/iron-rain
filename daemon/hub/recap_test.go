package hub

import (
	"strings"
	"testing"

	"github.com/howlerops/oculus/daemon/transcript"
)

// Ten of the built-in CLI agents spawn a cold process per turn, so the second message in a session
// reaches an agent that has never seen the first — while the client renders a full scrolling
// transcript that says otherwise. The user types "now also fix the tests" and is answered by
// something with no idea what "also" refers to. Multi-provider support is the product's headline and
// for those providers the conversation was one turn deep.
func TestRecapCarriesTheConversationForward(t *testing.T) {
	entries := []transcript.Entry{
		{Kind: "user", Text: "add a retry to the fetch helper"},
		{Kind: "tool", Text: "edit"}, // noise: must not appear in a brief
		{Kind: "assistant", Text: "Added a retry with backoff in fetchWithRetry."},
		{Kind: "status", Text: "idle"},
	}
	recap := buildRecap(entries)
	if recap == "" {
		t.Fatal("no recap built from a two-turn conversation")
	}
	for _, want := range []string{"add a retry", "fetchWithRetry"} {
		if !strings.Contains(recap, want) {
			t.Errorf("recap dropped %q: %s", want, recap)
		}
	}
	if strings.Contains(recap, "idle") {
		t.Error("status lines are noise in a brief and must not be replayed")
	}
	if !strings.Contains(recap, "no memory") {
		t.Error("the agent should be told why it is being given this")
	}
}

// A first turn must not be prefixed — there is nothing to recall, and a recap saying so would be
// both wrong and a waste of the prompt.
func TestNoRecapOnAFirstTurn(t *testing.T) {
	if got := buildRecap(nil); got != "" {
		t.Errorf("expected no recap, got %q", got)
	}
	one := []transcript.Entry{{Kind: "user", Text: "hello"}}
	if got := buildRecap(one); got != "" {
		t.Errorf("a single message is not a conversation to recall, got %q", got)
	}
}

// A cold CLI takes its prompt as an argv element, and a recap large enough to crowd out the actual
// question is worse than none. The END is kept: the newest exchange is what the new message refers to.
func TestRecapIsBounded(t *testing.T) {
	var entries []transcript.Entry
	for i := 0; i < 200; i++ {
		entries = append(entries,
			transcript.Entry{Kind: "user", Text: strings.Repeat("q", 4000)},
			transcript.Entry{Kind: "assistant", Text: strings.Repeat("a", 4000)})
	}
	entries = append(entries, transcript.Entry{Kind: "assistant", Text: "THE-MOST-RECENT-ANSWER"})
	recap := buildRecap(entries)
	if len(recap) > recapBudget*2 {
		t.Errorf("recap is %d chars — it would crowd out the question", len(recap))
	}
	if !strings.Contains(recap, "THE-MOST-RECENT-ANSWER") {
		t.Error("truncation dropped the newest exchange, which is the one the new message refers to")
	}
}

// Providers that own their own conversation must be left alone.
func TestNativeProvidersKeepTheirOwnContinuity(t *testing.T) {
	if !hasContinuity(&subSess{}) {
		t.Error("a provider that does not report continuity is assumed to have it")
	}
}
