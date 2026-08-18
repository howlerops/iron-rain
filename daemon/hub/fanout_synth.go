package hub

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/howlerops/oculus/daemon/protocol"
	"github.com/howlerops/oculus/daemon/store"
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
		peers     []*managedSession
		provider  string
		project   string
		nextIdx   int
		groupBase string
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
		// The originals all branched from the same commit; take it from whichever we see first so
		// the synthesis starts where they did rather than at today's HEAD.
		if groupBase == "" {
			groupBase = m.meta.baseCommit
		}
	}
	prompt := h.fanoutPrompt[group]
	h.mu.Unlock()

	if len(peers) < 2 {
		return "", fmt.Errorf("synthesis needs at least 2 variants to combine (found %d)", len(peers))
	}

	body, sources := synthPrompt(ctx, prompt, peers, h.db)
	if len(sources) < 2 {
		return "", fmt.Errorf("synthesis needs at least 2 readable diffs (got %d — the rest were empty or too large)", len(sources))
	}

	create := protocol.SessionCreate{
		Provider:      provider,
		ProjectID:     project,
		Prompt:        body,
		Worktree:      true,
		WorkspaceName: fmt.Sprintf("fanout-%s-synth", group),
	}
	// fanoutVariant places it last in the comparison, after the attempts it read. baseRefOverride
	// pins it to the base the ORIGINALS branched from: synthesis is triggered by hand, minutes or
	// hours later, and startSession would otherwise take whatever HEAD is by then. A synthesis
	// measured against a different base produces a diff that cannot be compared with its siblings',
	// which is the one thing the comparison screen exists to do.
	meta := sessionMeta{
		fanoutGroup: group, fanoutVariant: nextIdx, fanoutSynth: true,
		fanoutSources: sources, baseRefOverride: groupBase,
	}
	ms, err := h.startSession(ctx, create, meta, nil)
	if err != nil {
		return "", err
	}
	// Re-arm the "all variants finished" latch. checkFanoutDone fires ONCE per group, and it already
	// fired when the originals settled — so without this the synthesis runs a full agent turn in a
	// real worktree and its result is never broadcast to anything. No summary, no judge, no card:
	// the work silently vanishes, which is strictly worse than not offering the feature.
	h.mu.Lock()
	delete(h.fanoutNotified, group)
	h.mu.Unlock()
	go ms.run()
	log.Printf("fanout %s: spawned a synthesis variant from %d attempt(s)", group, len(sources))
	return ms.sess.ID(), nil
}

// synthPrompt renders the original task plus each variant's diff into an instruction to combine
// them. Returns the prompt and the 1-based variant numbers whose diffs it actually carried.
func synthPrompt(ctx context.Context, task string, peers []*managedSession, db *store.Store) (string, []int) {
	b := &strings.Builder{}
	b.WriteString("Several agents independently attempted the same task. Your job is to produce the " +
		"BEST SINGLE implementation, taking the strongest parts of each.\n\n")
	if task != "" {
		b.WriteString("The original task was:\n" + task + "\n\n")
	}

	var sources []int
	total := 0
	for _, m := range peers {
		m.mu.Lock()
		wtPath, base := m.meta.worktreePath, m.meta.baseCommit
		name, num := m.meta.workspaceName, m.meta.fanoutVariant+1
		id := ""
		if m.sess != nil { // a peer is always bound in production; guard so this stays unit-testable
			id = m.sess.ID()
		}
		m.mu.Unlock()
		if wtPath == "" {
			continue
		}
		diff, err := worktree.Diff(ctx, wtPath, base)
		if err != nil || strings.TrimSpace(diff) == "" {
			continue
		}
		tooBig := len(diff) > maxSynthDiffBytes || total+len(diff) > maxSynthTotalBytes
		if tooBig {
			// Fall back to the agent's OWN account plus a diffstat — the shape the judge uses. A
			// bare "(omitted)" tells the synthesiser an attempt exists and nothing about it, which
			// is barely better than hiding it; knowing "#3 rewrote the auth layer to use JWT, 47
			// files, +1200/-300" lets it reason about the approach even without the code. Skipped
			// WHOLE rather than truncated: half a diff looks complete and silently drops the rest.
			files, ins, del := diffStat(wtPath, base)
			fmt.Fprintf(b, "### #%d %s\n(diff too large to include — %d bytes. %d files, +%d/-%d)\n",
				num, name, len(diff), files, ins, del)
			if db != nil {
				if ho, ok := db.Handoff(id); ok && (ho.Title != "" || ho.Summary != "") {
					fmt.Fprintf(b, "Its own summary: %s %s\n", ho.Title, truncate(ho.Summary, 500))
				}
			}
			b.WriteString("\n")
			continue
		}
		fmt.Fprintf(b, "### #%d %s\n```diff\n%s\n```\n\n", num, name, diff)
		total += len(diff)
		sources = append(sources, num)
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
	return b.String(), sources
}
