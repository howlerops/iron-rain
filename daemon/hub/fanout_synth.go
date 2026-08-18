package hub

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/howlerops/oculus/daemon/protocol"
	"github.com/howlerops/oculus/daemon/worktree"
)

// Fan-out SYNTHESIS: an agent that combines the variants, instead of you cherry-picking between
// branches by hand.
//
// The judge (fanout_judge.go) recommends ONE winner, which is the right default — variants usually
// make coherent, mutually exclusive choices, and picking preserves that coherence. But it leaves the
// case where two attempts each got something right, and the only remedy was to diff two branches and
// graft one into the other yourself. That is the manual work fanning out was supposed to remove.
//
// The synthesis is spawned as an ADDITIONAL VARIANT in the same group, not as a replacement:
//
//   - it branches from the same base as its siblings, so its diff is comparable to theirs;
//   - it lands in the same comparison, gets the same summary, and the judge weighs it alongside
//     the rest;
//   - fanout.resolve keeps it exactly the way it keeps any other variant.
//
// That ordering is the whole safety argument. Combining two working approaches can easily produce
// something worse than either — different architectures, merged, are frequently incoherent — so a
// synthesis that OVERWROTE the winner would risk losing the best attempt to a bad graft. Competing
// instead of overriding means the downside is one wasted agent turn, and the upside is a variant
// nobody would have written. It also preserves the property the judge was careful about: no attempt
// is ever silently discarded.
const (
	// maxSynthDiffBytes bounds ONE variant's diff in the synthesis prompt. Unlike the judge — which
	// reads handoff summaries and diffstats precisely to stay cheap — the synthesiser needs the
	// actual code, so this is the one place raw diffs enter a prompt. A variant over the bound is
	// named and skipped rather than truncated mid-hunk: half a diff is worse than none, because it
	// looks complete and silently drops the rest of the change.
	maxSynthDiffBytes = 60_000
	// maxSynthTotalBytes bounds the whole prompt across variants.
	maxSynthTotalBytes = 180_000
)

// synthesizeFanout spawns a variant whose job is to read the others' diffs and produce the best
// combined implementation. Returns the new session id.
func (h *Hub) synthesizeFanout(ctx context.Context, group string) (string, error) {
	h.mu.Lock()
	var (
		peers    []*managedSession
		provider string
		project  string
		nextIdx  int
	)
	for _, m := range h.sessions {
		if m.meta.fanoutGroup != group {
			continue
		}
		peers = append(peers, m)
		if m.meta.fanoutVariant >= nextIdx {
			nextIdx = m.meta.fanoutVariant + 1
		}
		if provider == "" {
			provider = m.sess.Provider()
		}
		if project == "" {
			project = m.meta.projectID
		}
	}
	prompt := h.fanoutPrompt[group]
	h.mu.Unlock()

	if len(peers) < 2 {
		return "", fmt.Errorf("synthesis needs at least 2 variants to combine (found %d)", len(peers))
	}

	body, included := synthPrompt(ctx, prompt, peers)
	if included < 2 {
		return "", fmt.Errorf("synthesis needs at least 2 readable diffs (got %d — the rest were empty or too large)", included)
	}

	create := protocol.SessionCreate{
		Provider:      provider,
		ProjectID:     project,
		Prompt:        body,
		Worktree:      true,
		WorkspaceName: fmt.Sprintf("fanout-%s-synth", group),
	}
	// fanoutVariant places it last in the comparison, after the attempts it read.
	meta := sessionMeta{fanoutGroup: group, fanoutVariant: nextIdx, fanoutSynth: true}
	ms, err := h.startSession(ctx, create, meta, nil)
	if err != nil {
		return "", err
	}
	go ms.run()
	log.Printf("fanout %s: spawned a synthesis variant from %d attempt(s)", group, included)
	return ms.sess.ID(), nil
}

// synthPrompt renders the original task plus each variant's diff into an instruction to combine
// them. Returns the prompt and how many diffs it actually carried.
func synthPrompt(ctx context.Context, task string, peers []*managedSession) (string, int) {
	b := &strings.Builder{}
	b.WriteString("Several agents independently attempted the same task. Your job is to produce the " +
		"BEST SINGLE implementation, taking the strongest parts of each.\n\n")
	if task != "" {
		b.WriteString("The original task was:\n" + task + "\n\n")
	}

	included, total := 0, 0
	for _, m := range peers {
		m.mu.Lock()
		wtPath, base, name := m.meta.worktreePath, m.meta.baseCommit, m.meta.workspaceName
		m.mu.Unlock()
		if wtPath == "" {
			continue
		}
		diff, err := worktree.Diff(ctx, wtPath, base)
		if err != nil || strings.TrimSpace(diff) == "" {
			continue
		}
		// Say what was left out, in the prompt itself. An agent told it has every attempt will
		// reason as though it does; one told an attempt is missing can say so in its answer.
		if len(diff) > maxSynthDiffBytes {
			fmt.Fprintf(b, "### %s\n(diff omitted — %d bytes, too large to include)\n\n", name, len(diff))
			continue
		}
		if total+len(diff) > maxSynthTotalBytes {
			fmt.Fprintf(b, "### %s\n(diff omitted — the combined size limit was reached)\n\n", name)
			continue
		}
		fmt.Fprintf(b, "### %s\n```diff\n%s\n```\n\n", name, diff)
		total += len(diff)
		included++
	}

	b.WriteString("Write the combined implementation in THIS worktree, which starts from the same " +
		"base commit the attempts did. Do not assume any of their code is already present — none of " +
		"it is; you are starting from the same clean base and writing the result yourself.\n\n" +
		"Where the attempts agree, follow them. Where they differ, choose the better approach and " +
		"say briefly why in your final message. Where one solved something the others missed, keep " +
		"it. Do NOT mechanically concatenate them: two coherent designs spliced together are usually " +
		"worse than either, so if the approaches are fundamentally incompatible, pick the better one " +
		"outright and say that is what you did.\n\n" +
		"Then verify your result the way this repo expects (run its tests or build if it has them). " +
		"Report what you kept from where, and anything you could not reconcile.")
	return b.String(), included
}
