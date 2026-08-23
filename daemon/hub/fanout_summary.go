package hub

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
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
		isSynth, synthSources := m.meta.fanoutSynth, append([]int(nil), m.meta.fanoutSources...)
		worktreePath, baseCommit, branch := m.meta.worktreePath, m.meta.baseCommit, m.meta.branch
		status, model := m.lastStatus, m.model
		started := m.turnStarted
		m.mu.Unlock()

		r := protocol.FanoutVariantResult{
			SessionID:      m.sess.ID(),
			Variant:        variant,
			Model:          model,
			Status:         status,
			Branch:         branch,
			Failed:         status == protocol.StatusError,
			IsSynthesis:    isSynth,
			SourceVariants: synthSources,
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
// uncommitted work (an agent that didn't commit still did the work) AND newly created files.
//
// The untracked half is not a refinement — without it the feature reports the opposite of the truth
// for the commonest case there is. `git diff` only ever shows files git already knows about, so an
// agent that CREATES a file (the usual outcome: write the doc, add the module, add the test) shows
// as "0 files — this agent finished without touching the tree". Observed exactly that: two fan-out
// variants each wrote a 2KB NOTES.md and the comparison offered them as two identical do-nothings,
// which makes the whole compare-and-merge screen worse than useless — it is confidently wrong.
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
	defer func() {
		nf, ni := untrackedStat(ctx, worktreePath)
		files += nf
		insertions += ni
	}()
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

// maxUntrackedCounted bounds the work this does. A worktree can contain a large untracked tree that
// .gitignore doesn't cover (a build directory an agent created, a downloaded fixture), and the
// summary is drawn while the user is waiting. Past this many files the count is reported as-is and
// the line counting stops: a slightly low insertion count on a huge change set is a far better
// failure than a comparison screen that takes ten seconds to appear.
const maxUntrackedCounted = 500

// maxUntrackedFileBytes skips line-counting for anything implausibly large for source (a binary an
// agent produced, a lock file, a fixture dump). It still counts as a FILE — it was created — just
// not as N insertions.
const maxUntrackedFileBytes = 1 << 20

// untrackedStat counts newly created files and their lines, respecting .gitignore.
//
// `--exclude-standard` is what makes this safe to include at all: without it every ignored build
// artefact in the worktree would be reported as the agent's work.
func untrackedStat(ctx context.Context, worktreePath string) (files, insertions int) {
	out, err := exec.CommandContext(ctx, "git", "-C", worktreePath,
		"ls-files", "--others", "--exclude-standard", "-z").Output()
	if err != nil {
		return 0, 0
	}
	for _, name := range strings.Split(strings.TrimRight(string(out), "\x00"), "\x00") {
		if name == "" {
			continue
		}
		files++
		if files > maxUntrackedCounted {
			continue
		}
		full := filepath.Join(worktreePath, name)
		info, err := os.Stat(full)
		if err != nil || info.IsDir() || info.Size() > maxUntrackedFileBytes {
			continue
		}
		data, err := os.ReadFile(full)
		if err != nil {
			continue
		}
		insertions += bytes.Count(data, []byte{'\n'})
		// A last line with no trailing newline is still a line.
		if len(data) > 0 && data[len(data)-1] != '\n' {
			insertions++
		}
	}
	return files, insertions
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
