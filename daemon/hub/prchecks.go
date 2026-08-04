package hub

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/howlerops/oculus/daemon/activity"
	"github.com/howlerops/oculus/daemon/protocol"
	"github.com/howlerops/oculus/daemon/worktree"
)

// PR check watching. worktree.status already reports a CI rollup, but only when someone ASKS — so
// the red build is something you discover, hours later, by happening to open the worktree panel.
// That is backwards for the product this daemon exists to serve: the whole premise of driving agents
// from a phone is that the phone tells you when something needs you. This file polls the open PRs
// the daemon already knows about and pushes exactly one notification when a build turns red.
//
// Everything below is shaped by one asymmetry: a WRONG "CI failed" costs far more than a late one.
// The first false alarm teaches the user that the badge lies; the second gets the whole category
// switched off in Notifications, and then the true failure never arrives either. So the poll is
// deliberately slow, the notification deliberately edge-triggered, and — the part that matters most —
// an unanswerable poll is never allowed to become a verdict. worktree.PRState folds "gh isn't
// installed", "gh isn't authenticated", "GitHub returned 403 rate limit", "the wifi dropped" and
// "this branch simply has no PR" into the same empty PRInfo with a nil error, on purpose, because a
// status screen that errors out is worse than one missing a check. That is the right call for a
// request/response status screen and a trap for a watcher: the only thing that may move the
// notification state machine is a rollup we actually parsed.
const (
	// prSweepEvery is how often the watcher LOOKS for sessions that are due. It is not the poll
	// rate — every session carries its own next-poll time — so this only bounds how late a due poll
	// can be, and a half-minute of lateness on a two-minute cadence is invisible.
	prSweepEvery = 30 * time.Second

	// prPollActive is the gh cadence while checks are still running. This is the only window in
	// which the transition worth waking someone for (green/pending → red) can happen, so it is as
	// fast as this ever goes. Two minutes is deliberately unhurried: gh spends from a GraphQL budget
	// of 5000 points/hour shared with the user and every agent on the machine, and a failure that
	// arrives two minutes late costs nothing — nobody is staring at a tick, they are waiting for a
	// notification that would otherwise never come at all.
	prPollActive = 2 * time.Minute

	// prPollSettled is the cadence once the rollup has stopped moving (every check reported, or the
	// PR has no CI at all). Nothing changes from here without a new push, and a push puts checks
	// back into PENDING, which the next poll sees and speeds back up. Ten minutes holds a long-lived
	// open PR to six gh calls an hour.
	prPollSettled = 10 * time.Minute

	// prPollBackoffMax caps the retry interval for polls that could not be answered AT ALL. Those
	// causes are indistinguishable from one another (see the file comment) and most of them — no gh,
	// no PR for this branch — are permanent for the life of the session, so the interval doubles up
	// to this ceiling instead of retrying a hopeless call every few minutes. Half an hour is short
	// enough that a laptop coming back from a dead tunnel recovers on its own without anyone
	// touching it, and long enough that a repo with no gh costs two wasted exec() calls an hour.
	prPollBackoffMax = 30 * time.Minute

	// prPollConcurrency caps gh processes in flight across the whole hub. A fleet with a dozen
	// worktree sessions would otherwise fork a dozen network-bound processes on the same tick, and
	// hitting a rate-limited API in lockstep is precisely how a watcher gets itself throttled — and
	// a throttled watcher reports nothing, which is the one outcome this feature cannot have.
	prPollConcurrency = 3

	// prIdleSweepsBeforeStop retires the watcher after this many sweeps that found nothing worth
	// watching (~10 minutes). The next worktree session starts it again, so nothing is lost; this
	// just keeps a daemon that has finished all its worktrees — and a test binary that made one —
	// from holding a ticker open for the rest of the process's life.
	prIdleSweepsBeforeStop = 20
)

// Rollup verdicts, as recorded per session. These are worktree.PRChecks.State values plus one of our
// own: prNoChecks, for a PR that exists and has no CI. A repo without CI is not a failure, and it
// must stay distinguishable from one whose checks have not reported yet.
const (
	prSuccess  = "SUCCESS"
	prFailure  = "FAILURE"
	prPending  = "PENDING"
	prNoChecks = "NONE"
)

// prWatchers records which Hubs already have a watcher goroutine, so ensurePRWatch can be called
// from every worktree session's event pump and still produce exactly ONE ticker per hub — not one
// goroutine per session, which would have every worktree independently forking gh. The watcher is
// started on demand rather than at daemon startup so a daemon that never opens a worktree never runs
// the ticker at all, and it removes its own entry when it retires so the map self-empties.
// ensurePRWatch starts this hub's PR-check watcher if it isn't already running. Cheap and idempotent:
// called from managedSession.run() for every worktree session, including the ones restored after a
// daemon restart.
//
// The live/not-live flag is a plain field guarded by the hub's own lock, deliberately NOT a
// sync.Once: the loop RETIRES itself once nothing is worth watching, and the next worktree session
// has to be able to start it again. A Once would make that first retirement permanent, so a daemon
// that finished its worktrees in the morning would silently never watch a PR again until restarted.
func (h *Hub) ensurePRWatch() {
	h.mu.Lock()
	already := h.prWatching
	h.prWatching = true
	h.mu.Unlock()
	if already {
		return
	}
	go h.prWatchLoop()
}

func (h *Hub) prWatchLoop() {
	// Clear the flag on the way out — including on a panic — or a retired watcher could never be
	// restarted and PR checks would go unwatched for the life of the process with nothing to show why.
	defer func() {
		h.mu.Lock()
		h.prWatching = false
		h.mu.Unlock()
	}()
	t := time.NewTicker(prSweepEvery)
	defer t.Stop()
	idle := 0
	for range t.C {
		if h.prSweep(time.Now()) > 0 {
			idle = 0
			continue
		}
		if idle++; idle >= prIdleSweepsBeforeStop {
			return
		}
	}
}

// prSweep starts a poll for every worktree session whose next poll is due, and reports how many
// sessions are still worth watching at all so the loop can retire itself.
func (h *Hub) prSweep(now time.Time) int {
	h.mu.Lock()
	sessions := make([]*managedSession, 0, len(h.sessions))
	for _, m := range h.sessions {
		sessions = append(sessions, m)
	}
	// Nobody is connected and no device is enrolled for push: whatever gh would tell us cannot reach
	// a human, so spending a rate-limited API call on it is pure waste. Note this is NOT "no client
	// is connected" — a registered push token is exactly the case this feature is for (your laptop
	// is running agents while you are out), so a phone that has paired keeps the poll alive even
	// with no live connection. Nothing is scheduled while skipping, so the first sweep after someone
	// connects polls immediately instead of waiting out a stale timer.
	observers := len(h.clients) > 0 || len(h.pushTokens) > 0
	h.mu.Unlock()

	watchable, due := 0, []*managedSession(nil)
	for _, m := range sessions {
		m.mu.Lock()
		watching := m.meta.worktreePath != "" && m.meta.branch != "" && !m.prWatchDone
		if watching {
			watchable++
		}
		// prPolling is claimed under the same lock that reads it: gh can outlive a sweep interval
		// (it is a network call with a timeout measured in tens of seconds), and two overlapping
		// polls of one PR would race each other's verdicts into the state machine.
		start := watching && observers && !m.prPolling && !now.Before(m.prNextPoll)
		if start {
			m.prPolling = true
		}
		m.mu.Unlock()
		if start {
			due = append(due, m)
		}
	}
	if len(due) > 0 {
		go runPRPolls(due)
	}
	return watchable
}

// runPRPolls polls the due sessions off the sweep goroutine, a bounded number at a time.
func runPRPolls(due []*managedSession) {
	sem := make(chan struct{}, prPollConcurrency)
	var wg sync.WaitGroup
	for _, m := range due {
		sem <- struct{}{}
		wg.Add(1)
		go func(m *managedSession) {
			defer wg.Done()
			defer func() { <-sem }()
			m.pollPRChecks(context.Background(), time.Now())
		}(m)
	}
	wg.Wait()
}

// pollPRChecks runs ONE gh query for this session and applies the result. It always clears prPolling
// and always reschedules, so a session can never wedge itself out of the rotation by failing.
func (m *managedSession) pollPRChecks(ctx context.Context, now time.Time) {
	defer func() {
		m.mu.Lock()
		m.prPolling = false
		m.mu.Unlock()
	}()

	m.mu.Lock()
	path, branch, fetch := m.meta.worktreePath, m.meta.branch, m.prPoll
	m.mu.Unlock()
	if fetch == nil {
		fetch = worktree.PRState
	}

	// A worktree that is gone cannot be polled — gh would run in a directory that no longer exists
	// and fail in a way indistinguishable from every other gh failure, quietly backing off forever.
	// Retire the watch instead; a removed worktree is finished work by definition.
	if fi, err := os.Stat(path); err != nil || !fi.IsDir() {
		m.mu.Lock()
		m.prWatchDone = true
		m.mu.Unlock()
		return
	}

	info, err := fetch(ctx, path, branch)

	// The load-bearing branch (see the file comment). An empty State means the poll was not
	// ANSWERED — no PR, no gh, no auth, no network, rate limited, we cannot tell which. We learned
	// nothing, so nothing moves: no notification, no state change, and specifically no "the checks
	// went away, so they must have failed". Back off and try again later.
	if err != nil || info.State == "" {
		m.mu.Lock()
		m.prBackoff = nextPRBackoff(m.prBackoff)
		m.prNextPoll = now.Add(m.prBackoff)
		m.mu.Unlock()
		return
	}

	// Merged or closed: nothing about this PR can turn into a failure worth waking someone for.
	// Stopping here is what keeps a long-lived daemon's gh usage proportional to OPEN work rather
	// than to every branch it has ever built. Broadcast the final state first so an open panel shows
	// "Merged" without its Refresh button.
	if info.State == "MERGED" || info.State == "CLOSED" {
		m.mu.Lock()
		m.prWatchDone = true
		m.mu.Unlock()
		m.broadcastPRStatus(info)
		return
	}

	verdict := prVerdict(info)

	m.mu.Lock()
	prev := m.prLastState
	fire := ""
	switch {
	case prev == "":
		// First verdict this daemon has seen for this PR: ADOPT it silently. A build that was
		// already red before the watcher started did not transition into failure while we were
		// watching — announcing it here would mean every daemon restart re-notifies every failing PR
		// the user already knows about, and a notification that fires on restart rather than on
		// change is exactly the noise that gets a category muted.
		m.prLastState = verdict
	case verdict == prFailure && prev != prFailure:
		m.prLastState = prFailure
		fire = prFailure
	case verdict == prSuccess && prev != prSuccess:
		if prev == prFailure {
			fire = prSuccess
		}
		m.prLastState = prSuccess
	}
	// PENDING and NONE deliberately do NOT overwrite a recorded verdict. A re-run puts a failing PR
	// back into PENDING for minutes at a time, and if that cleared the state then the same failure
	// coming back would notify all over again — one flapping build would produce one push per
	// re-run, which is the spam this state machine exists to prevent. Only an actual SUCCESS clears
	// a FAILURE.
	m.prBackoff = 0
	if verdict == prPending {
		m.prNextPoll = now.Add(prPollActive)
	} else {
		m.prNextPoll = now.Add(prPollSettled)
	}
	m.mu.Unlock()

	m.broadcastPRStatus(info)

	sid := m.sess.ID()
	m.mu.Lock()
	label := m.meta.label
	if label == "" {
		label = m.meta.workspaceName
	}
	label = pushLabel(label, m.meta.execHost) // a lock screen must say WHICH box's CI broke
	m.mu.Unlock()
	switch fire {
	case prFailure:
		log.Printf("session %s: PR checks went RED (%s) — notifying", sid, info.URL)
		// Reuses the TESTS_FAILED push, and therefore its existing Notifications toggle: from the
		// user's side "my tests are failing" is one concern whether the runner was the daemon's own
		// or GitHub's, and inventing a ninth category would ship a notification with no off switch.
		m.hub.pushTestsFailed(sid, label, prFailureSummary(info.Checks))
		// A red build is a genuine needs-you, so it also belongs in the Activity inbox — a push can
		// be missed or dismissed on a locked screen, and then nothing would remember it happened.
		m.hub.recordActivity(activity.Event{
			Kind: activity.KindError, SessionID: sid, NeedsYou: true,
			Title: m.activityTitle() + ": CI failed", Detail: prFailureSummary(info.Checks),
		})
	case prSuccess:
		// Recovering to green gets a notification, but ONLY out of a failure we notified about. The
		// argument for it: the red push asked the user to do something, and if a re-run or someone
		// else's fix already made it green, telling them closes the loop and saves a context switch
		// into a problem that no longer exists. The argument against — noise — only applies to
		// unsolicited green, which never fires here: a PR that has been green all along says
		// nothing, because prLastState was adopted or set to SUCCESS without ever notifying.
		log.Printf("session %s: PR checks recovered to green (%s) — notifying", sid, info.URL)
		m.hub.pushPRFinished(sid, label, info.URL)
	}
}

// prVerdict reduces a poll to the one value the state machine tracks. Checks == nil means the PR has
// no CI at all, which must stay distinct from PENDING (checks exist and haven't reported): the first
// can never become a failure, the second is the state a failure comes out of.
func prVerdict(info worktree.PRInfo) string {
	if info.Checks == nil || info.Checks.State == "" {
		return prNoChecks
	}
	return info.Checks.State
}

// nextPRBackoff doubles the retry interval for unanswerable polls, starting at the settled cadence
// (there is no point retrying a missing gh in two minutes) and stopping at the ceiling.
func nextPRBackoff(cur time.Duration) time.Duration {
	if cur <= 0 {
		return prPollSettled
	}
	if next := cur * 2; next < prPollBackoffMax {
		return next
	}
	return prPollBackoffMax
}

// prFailureSummary renders the failing checks into pushTestsFailed's body, which reads
// "<summary> — tap to review". Names lead, because which check broke is the only part that tells
// someone whether this is worth getting out of bed for; the count follows only when the daemon
// capped the list.
func prFailureSummary(c *worktree.PRChecks) string {
	if c == nil {
		return "CI failed"
	}
	if len(c.Failing) == 0 {
		return fmt.Sprintf("CI failed (%d checks)", c.Failed)
	}
	s := "CI failed: " + strings.Join(c.Failing, ", ")
	if c.Failed > len(c.Failing) {
		s += fmt.Sprintf(" +%d more", c.Failed-len(c.Failing))
	}
	return s
}

// broadcastPRStatus republishes worktree.status to every client when the rollup actually changed, so
// a WorktreePanel someone left open turns red on its own instead of waiting for its Refresh button.
// It reuses the REQUEST type as the event type, the way session.list is both a request and the
// daemon's proactive push, so a client needs one dispatch case and no new payload shape. An
// unchanged poll broadcasts nothing: waking every connected device every couple of minutes to say
// "still green" is how a phone's battery ends up paying for a feature nobody asked to watch.
func (m *managedSession) broadcastPRStatus(info worktree.PRInfo) {
	fp := prFingerprint(info)
	m.mu.Lock()
	unchanged := fp == m.prFingerprint
	m.prFingerprint = fp
	branch, path := m.meta.branch, m.meta.worktreePath
	m.mu.Unlock()
	if unchanged {
		return
	}
	res := protocol.WorktreeStatusResult{
		SessionID: m.sess.ID(), Branch: branch, State: info.State, URL: info.URL,
		HasRemote: worktree.HasRemote(path),
	}
	if c := info.Checks; c != nil {
		res.Checks = &protocol.PRChecks{
			State: c.State, Passed: c.Passed, Failed: c.Failed, Pending: c.Pending, Failing: c.Failing,
		}
	}
	m.hub.broadcast(protocol.TypeWorktreeStatus, res)
}

// prFingerprint is the part of a poll a client would render, so an identical poll can be dropped.
func prFingerprint(info worktree.PRInfo) string {
	if info.Checks == nil {
		return info.State + "|none"
	}
	c := info.Checks
	return fmt.Sprintf("%s|%s|%d/%d/%d|%s", info.State, c.State, c.Passed, c.Failed, c.Pending,
		strings.Join(c.Failing, ","))
}
