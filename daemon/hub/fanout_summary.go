package hub

import (
	"context"
	"fmt"
	"log"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/howlerops/oculus/daemon/activity"
	"github.com/howlerops/oculus/daemon/protocol"
)

// Fan-out AGGREGATION. Racing N agents on one prompt is table stakes — every ADE in this space can
// fan out. What none of them do is close the loop: the human is left to open N sessions and diff
// them by hand, which is where the time savings evaporate.
//
// This assembles a comparison the moment the last variant settles, and it does so WITHOUT an extra
// model call: each agent already writes a handoff record (title + summary of what it did), and the
// worktree already has a stable base commit to diff against. So the expensive part is free.

// buildFanoutSummary collects every variant's result for a finished group.
func (h *Hub) buildFanoutSummary(group string) protocol.FanoutSummary {
	h.mu.Lock()
	var members []*managedSession
	for _, m := range h.sessions {
		if m.meta.fanoutGroup == group {
			members = append(members, m)
		}
	}
	db := h.db
	prompt := h.fanoutPrompt[group]
	h.mu.Unlock()

	out := protocol.FanoutSummary{Group: group, Prompt: prompt, Results: make([]protocol.FanoutVariantResult, 0, len(members))}
	for _, m := range members {
		m.mu.Lock()
		variant := m.meta.fanoutVariant
		worktreePath, baseCommit, branch := m.meta.worktreePath, m.meta.baseCommit, m.meta.branch
		status, model := m.lastStatus, m.model
		started := m.turnStarted
		m.mu.Unlock()

		r := protocol.FanoutVariantResult{
			SessionID: m.sess.ID(),
			Variant:   variant,
			Model:     model,
			Status:    status,
			Branch:    branch,
			Failed:    status == protocol.StatusError,
		}
		// The agent's OWN account of what it did — no extra inference, no second model call.
		if db != nil {
			if ho, ok := db.Handoff(m.sess.ID()); ok {
				r.Title, r.Summary = ho.Title, ho.Summary
			}
		}
		if worktreePath != "" {
			r.FilesChanged, r.Insertions, r.Deletions = diffStat(worktreePath, baseCommit)
		}
		if !started.IsZero() {
			r.DurationSec = int(time.Since(started).Seconds())
		}
		out.Results = append(out.Results, r)
	}
	// Stable, meaningful order: biggest change first, then variant index. A reviewer wants the
	// substantive attempts at the top, not whatever order the map iterated in.
	sortFanoutResults(out.Results)
	return out
}

// sortFanoutResults orders results: successes before failures, then by size of change, then variant.
func sortFanoutResults(rs []protocol.FanoutVariantResult) {
	for i := 1; i < len(rs); i++ {
		for j := i; j > 0 && fanoutLess(rs[j], rs[j-1]); j-- {
			rs[j], rs[j-1] = rs[j-1], rs[j]
		}
	}
}

func fanoutLess(a, b protocol.FanoutVariantResult) bool {
	if a.Failed != b.Failed {
		return !a.Failed
	}
	if a.FilesChanged != b.FilesChanged {
		return a.FilesChanged > b.FilesChanged
	}
	return a.Variant < b.Variant
}

// diffStat returns files/insertions/deletions for a worktree against its base commit, including
// uncommitted work (an agent that didn't commit still did the work).
func diffStat(worktreePath, baseRef string) (files, insertions, deletions int) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	args := []string{"-C", worktreePath, "diff", "--shortstat"}
	if baseRef != "" {
		args = append(args, baseRef)
	}
	outBytes, err := exec.CommandContext(ctx, "git", args...).Output()
	if err != nil {
		return 0, 0, 0
	}
	// " 3 files changed, 42 insertions(+), 7 deletions(-)"
	for _, part := range strings.Split(strings.TrimSpace(string(outBytes)), ",") {
		part = strings.TrimSpace(part)
		numStr, _, _ := strings.Cut(part, " ")
		n, err := strconv.Atoi(numStr)
		if err != nil {
			continue
		}
		switch {
		case strings.Contains(part, "file"):
			files = n
		case strings.Contains(part, "insertion"):
			insertions = n
		case strings.Contains(part, "deletion"):
			deletions = n
		}
	}
	return files, insertions, deletions
}

// broadcastFanoutSummary assembles and sends the comparison. Called once per group, when the last
// variant settles. Git work happens off the hub lock.
func (h *Hub) broadcastFanoutSummary(group string) {
	sum := h.buildFanoutSummary(group)
	if len(sum.Results) == 0 {
		return
	}
	log.Printf("fanout %s: all %d variants finished — broadcasting comparison", group, len(sum.Results))
	h.broadcast(protocol.TypeFanoutSummary, sum)
	// The comparison is the actionable artifact, so it belongs in the activity inbox too — tapping it
	// opens the comparison rather than an arbitrary one of the N sessions.
	changed := 0
	for _, r := range sum.Results {
		if r.FilesChanged > 0 {
			changed++
		}
	}
	// Optional advisory judge, if this group asked for one.
	h.mu.Lock()
	spec, wantsJudge := h.fanoutJudge[group]
	delete(h.fanoutJudge, group)
	h.mu.Unlock()
	if wantsJudge {
		go h.judgeFanout(context.Background(), sum, spec.provider, spec.projectID)
	}
	h.recordActivity(activity.Event{
		Kind:   activity.KindFanoutDone,
		Title:  fmt.Sprintf("Fan-out ready to compare: %d of %d agents made changes", changed, len(sum.Results)),
		Detail: "tap to compare",
	})
}

// maxFanoutVariants bounds a fan-out. A divided fan-out takes an arbitrary-length subtask list, and
// every variant is a real worktree plus a real agent process — an unbounded list would exhaust the
// machine long before it produced anything useful.
const maxFanoutVariants = 12
