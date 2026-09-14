package hub

import (
	"strings"

	"github.com/howlerops/oculus/daemon/agent"
	"github.com/howlerops/oculus/daemon/transcript"
)

// recapTurns is how many earlier exchanges are replayed to a provider with no memory of its own.
// Enough for "now also fix the tests" to make sense; short enough that a long session does not turn
// every prompt into a wall of text the agent has to re-read.
const recapTurns = 6

// recapBudget bounds the recap in characters. A cold CLI takes its prompt as an argv element, and
// argv is not unbounded — and a recap so large it crowds out the actual question is worse than none.
const recapBudget = 6000

// hasContinuity reports whether a session remembers its own previous turns.
//
// Anything that does not implement ContinuityReporter is assumed to — that is true of every native
// adapter, which owns a conversation and is handed one message at a time.
func hasContinuity(sess agent.Session) bool {
	if r, ok := sess.(agent.ContinuityReporter); ok {
		return r.HasContinuity()
	}
	return true
}

// buildRecap renders the tail of a session's durable transcript as a short brief.
//
// This exists because ten of the built-in CLI agents are amnesiac: every turn is a cold process, so
// the second message in a session reaches an agent that has never seen the first — while the client
// renders a full scrolling transcript that says otherwise. The user types "now also fix the tests"
// and is answered by something with no idea what "also" refers to. Multi-provider support is the
// product's headline, and for those providers the conversation was one turn deep.
//
// The daemon already holds the whole conversation durably, so the fix is to hand back a bounded
// slice of it rather than to invent provider-specific resume flags — which the audit's verifier
// showed is unsafe: `codex exec resume --last` and friends select by cwd-recency, so turn two of one
// session could land in another session's conversation, or in the user's own terminal run.
//
// `pending` is the prompt about to be SENT. The write-ahead records it before this runs — that is
// the whole point of a write-ahead — so without excluding it the brief ends with the very message
// that follows it, and the agent is told it has already been asked the question being asked.
//
// Returns "" when there is nothing worth replaying, so a first turn is never prefixed.
func buildRecap(entries []transcript.Entry, pending string) string {
	// Drop the pending prompt from the tail. Only a TRAILING user entry, and only one: an identical
	// message sent twice earlier in the conversation is real history and belongs in the brief.
	pending = strings.TrimSpace(pending)
	if pending != "" {
		for i := len(entries) - 1; i >= 0; i-- {
			if entries[i].Kind == "status" {
				continue // statuses interleave with prompts and are not part of the conversation
			}
			if entries[i].Kind == "user" && strings.TrimSpace(entries[i].Text) == pending {
				entries = entries[:i]
			}
			break
		}
	}
	// Walk backwards collecting user/assistant text, then reverse: the tail is what matters.
	type line struct{ who, text string }
	var picked []line
	answered := false
	for i := len(entries) - 1; i >= 0 && len(picked) < recapTurns*2; i-- {
		e := entries[i]
		if e.Kind == "assistant" {
			answered = true
		}
		var who string
		switch e.Kind {
		case "user":
			who = "Me"
		case "assistant":
			who = "You"
		default:
			continue // tool cards and statuses are noise in a brief
		}
		t := strings.TrimSpace(e.Text)
		if t == "" {
			continue
		}
		picked = append(picked, line{who, t})
	}
	if len(picked) < 2 {
		return "" // nothing to recall: a first turn, or a session with only one message
	}
	// A brief with no answers in it is not a conversation, it is a list of questions — and prefixing
	// it with "you have no memory of this" tells the agent it already handled things it never saw.
	// Reachable for a session whose turns predate assistant entries being recorded at all.
	if !answered {
		return ""
	}
	for i, j := 0, len(picked)-1; i < j; i, j = i+1, j-1 {
		picked[i], picked[j] = picked[j], picked[i]
	}

	var b strings.Builder
	b.WriteString("[Earlier in this conversation — you have no memory of it, so it is repeated here:]\n")
	for _, l := range picked {
		text := l.text
		// Per-line trim before the overall budget, so one enormous answer cannot consume the whole
		// recap and push out every other turn.
		if len(text) > recapBudget/4 {
			text = text[:recapBudget/4] + "…"
		}
		b.WriteString(l.who)
		b.WriteString(": ")
		b.WriteString(text)
		b.WriteString("\n")
	}
	out := b.String()
	if len(out) > recapBudget {
		// Keep the END: the most recent exchange is the one the new message refers to.
		out = "[Earlier in this conversation — truncated:]\n…" + out[len(out)-recapBudget:]
	}
	return out + "\n[End of recap. The message that follows is the new one.]\n\n"
}
