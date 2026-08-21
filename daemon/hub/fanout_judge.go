package hub

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/howlerops/oculus/daemon/protocol"
)

// The fan-out JUDGE: an optional second opinion on which variant to keep.
//
// It is deliberately ADVISORY. The judge answers through a generative-UI `choice` component, so its
// recommendation arrives as something you tap — and the Keep buttons in the comparison stay exactly
// as available as they were. An auto-merge would be the wrong trade: the whole point of fanning out
// is that the human picks, and a judge that silently discarded four attempts would be strictly worse
// than no judge at all.
//
// The judge reads each variant's OWN handoff summary plus its diffstat — never the raw diffs, which
// would blow the context budget for a decision that rarely needs them.

// fanoutJudgeSpec remembers how to spawn the judge for one group (recorded at fan-out time, used
// when the last variant settles).
type fanoutJudgeSpec struct {
	provider  string
	projectID string
}

// judgePromptFor renders the comparison into a prompt asking for a recommendation as an iron:ui
// choice, where each action's prompt is the phrase the user would have typed to keep that variant.
func judgePromptFor(sum protocol.FanoutSummary) string {
	var b strings.Builder
	b.WriteString("You are reviewing several independent attempts at the SAME task, each by a different agent in its own git worktree.\n\n")
	if sum.Prompt != "" {
		b.WriteString("The task was:\n" + sum.Prompt + "\n\n")
	}
	b.WriteString("The attempts:\n\n")
	for _, r := range sum.Results {
		fmt.Fprintf(&b, "### Attempt %d", r.Variant+1)
		if r.Model != "" {
			fmt.Fprintf(&b, " (%s)", r.Model)
		}
		b.WriteString("\n")
		if r.Failed {
			b.WriteString("- ENDED IN ERROR\n")
		}
		fmt.Fprintf(&b, "- changed %d file(s), +%d/-%d lines\n", r.FilesChanged, r.Insertions, r.Deletions)
		if r.Title != "" {
			fmt.Fprintf(&b, "- the agent's own summary: %s\n", r.Title)
		}
		if r.Summary != "" {
			fmt.Fprintf(&b, "%s\n", truncate(r.Summary, 1200))
		}
		b.WriteString("\n")
	}
	b.WriteString(`Recommend ONE attempt to keep.

Answer with a SHORT paragraph of reasoning, then exactly one iron:ui "choice" component whose
actions let me keep any attempt. Phrase each action's prompt from my side, e.g. "Keep attempt 2".
Put your recommendation FIRST in the action list. Be concrete about the trade-offs you saw — if two
attempts are close, say what distinguishes them rather than hedging.`)
	return b.String()
}

func truncate(s string, n int) string {
	// Delegates so there is ONE truncation rule in this package. Its own byte-slice implementation
	// cut multi-byte characters in half; see truncRunes in heartbeat.go.
	return truncRunes(s, n)
}

// judgeFanout asks a fresh agent to recommend a winner, and broadcasts its answer as a gen-UI
// component attached to the fan-out group.
//
// It runs in an EPHEMERAL session: the judge is a one-shot opinion, not a conversation worth
// persisting, and it must never appear in the session list as a sibling of the variants it judged.
func (h *Hub) judgeFanout(ctx context.Context, sum protocol.FanoutSummary, provider, projectID string) {
	if len(sum.Results) < 2 {
		return
	}
	create := protocol.SessionCreate{
		Provider:  provider,
		ProjectID: projectID,
		Prompt:    judgePromptFor(sum),
		Ephemeral: true,
	}
	ms, err := h.startSession(ctx, create, sessionMeta{ephemeral: true, label: "fan-out judge"}, nil)
	if err != nil {
		log.Printf("fanout %s: judge unavailable (%v) — the comparison is still usable", sum.Group, err)
		return
	}
	log.Printf("fanout %s: judge session %s reviewing %d attempts", sum.Group, ms.sess.ID(), len(sum.Results))
}

// fanoutJudgeComponent builds the fallback choice component used when the judge itself fails to emit
// one — so the user still gets tappable Keep actions rather than a wall of prose.
func fanoutJudgeComponent(sum protocol.FanoutSummary) (protocol.UIComponent, bool) {
	if len(sum.Results) == 0 {
		return protocol.UIComponent{}, false
	}
	actions := make([]protocol.UIAction, 0, len(sum.Results))
	for _, r := range sum.Results {
		label := fmt.Sprintf("Keep attempt %d", r.Variant+1)
		if r.Title != "" {
			label = fmt.Sprintf("Keep #%d — %s", r.Variant+1, truncate(r.Title, 60))
		}
		actions = append(actions, protocol.UIAction{
			ID: "keep-" + r.SessionID, Kind: "prompt", Label: label,
			Prompt: fmt.Sprintf("Keep attempt %d", r.Variant+1),
		})
	}
	props, err := json.Marshal(map[string]any{"prompt": "Which attempt should I keep?"})
	if err != nil {
		return protocol.UIComponent{}, false
	}
	return protocol.UIComponent{
		ID: "fanout-choice-" + sum.Group, Component: "choice", SchemaV: 1, Status: "ready",
		Props: props, Actions: actions,
		FallbackText: "Choose an attempt to keep.",
	}, true
}
