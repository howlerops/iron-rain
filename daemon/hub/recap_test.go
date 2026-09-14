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
//
// Entries are built in the shape PRODUCTION produces — ending with the write-ahead entry for the
// prompt being sent. The first version of this test hand-built a list ending in an assistant reply,
// which no real call ever looks like, and it passed while the shipped recap duplicated every
// question and contained no answers at all.
func TestRecapCarriesTheConversationForward(t *testing.T) {
	const pending = "now also fix the tests"
	entries := []transcript.Entry{
		{Kind: "user", Text: "add a retry to the fetch helper"},
		{Kind: "tool", Text: "edit"}, // noise: must not appear in a brief
		{Kind: "assistant", Text: "Added a retry with backoff in fetchWithRetry."},
		{Kind: "status", Text: "idle"},
		{Kind: "user", Text: pending}, // the write-ahead, appended BEFORE the send
	}
	recap := buildRecap(entries, pending)
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
	// The recap is a PREFIX to the pending message. Including it here sends it twice, and tells the
	// agent it has already been asked the question it is about to be asked.
	if strings.Contains(recap, pending) {
		t.Errorf("the prompt being sent is replayed as history immediately before itself:\n%s", recap)
	}
}

// The same prompt sent twice EARLIER is real history. Only a trailing write-ahead entry is the
// pending one, and only one of them.
func TestAnEarlierIdenticalPromptIsStillHistory(t *testing.T) {
	const pending = "run the tests"
	entries := []transcript.Entry{
		{Kind: "user", Text: pending},
		{Kind: "assistant", Text: "Ran them: 3 failures in auth_test.go."},
		{Kind: "user", Text: pending},
	}
	recap := buildRecap(entries, pending)
	if !strings.Contains(recap, "3 failures") {
		t.Errorf("dropped the earlier exchange entirely: %q", recap)
	}
	if strings.Count(recap, pending) != 1 {
		t.Errorf("expected the earlier ask once and the pending one never, got:\n%s", recap)
	}
}

// A brief with no answers in it is not a conversation, it is a list of questions.
//
// This is the state EVERY session was in: the write-ahead store only ever received user prompts and
// error statuses, despite its package doc claiming it mirrors assistant events. So the shipped recap
// told an amnesiac agent "you have no memory of this" and then handed it the user's own questions
// with nothing between them — worse than sending no recap at all.
func TestNoRecapWhenNothingWasEverAnswered(t *testing.T) {
	entries := []transcript.Entry{
		{Kind: "user", Text: "add a retry to the fetch helper"},
		{Kind: "status", Text: "idle"},
		{Kind: "user", Text: "now also fix the tests"},
		{Kind: "user", Text: "and push it"},
	}
	if got := buildRecap(entries, "and push it"); got != "" {
		t.Errorf("built a recap containing no answers — it claims the agent has already handled "+
			"things it has never seen:\n%s", got)
	}
}

// A first turn must not be prefixed — there is nothing to recall, and a recap saying so would be
// both wrong and a waste of the prompt.
func TestNoRecapOnAFirstTurn(t *testing.T) {
	if got := buildRecap(nil, "hello"); got != "" {
		t.Errorf("expected no recap, got %q", got)
	}
	one := []transcript.Entry{{Kind: "user", Text: "hello"}}
	if got := buildRecap(one, "hello"); got != "" {
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
	recap := buildRecap(entries, "the new question")
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
