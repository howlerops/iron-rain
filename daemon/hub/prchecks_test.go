package hub

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/agent"
	"github.com/howlerops/oculus/daemon/push"
	"github.com/howlerops/oculus/daemon/worktree"
)

// prFakeSess is a provider session that does nothing; the PR watcher only ever needs its id.
type prFakeSess struct{ ch chan agent.Event }

func (f *prFakeSess) ID() string                                    { return "wt1" }
func (f *prFakeSess) Provider() string                              { return "fake" }
func (f *prFakeSess) Events() <-chan agent.Event                    { return f.ch }
func (f *prFakeSess) Prompt(context.Context, string) error          { return nil }
func (f *prFakeSess) Respond(context.Context, string, string) error { return nil }
func (f *prFakeSess) Stop(context.Context) error                    { return nil }
func (f *prFakeSess) Close() error                                  { return nil }

// prHarness builds a worktree session on a real (empty) directory — the watcher retires any session
// whose worktree is gone, so the path has to exist — with a scripted PR fetcher and a notifier that
// records every delivered push.
func prHarness(t *testing.T, replies ...func() (worktree.PRInfo, error)) (*managedSession, chan push.Notification) {
	t.Helper()
	got := make(chan push.Notification, 8)
	h := New()
	h.notifier = &prRecordingNotifier{got: got}
	h.pushTokens = []string{"dev"}
	h.SetNotifyPrefsPath(t.TempDir() + "/notify.json")

	m := newManagedSession(h, &prFakeSess{ch: make(chan agent.Event)}, sessionMeta{
		worktreePath: t.TempDir(), branch: "feat/x", workspaceName: "Feature",
	})
	i := 0
	m.prPoll = func(context.Context, string, string) (worktree.PRInfo, error) {
		r := replies[i]
		if i < len(replies)-1 {
			i++ // the last reply repeats, so a test can poll as often as it likes
		}
		return r()
	}
	return m, got
}

type prRecordingNotifier struct{ got chan push.Notification }

func (r *prRecordingNotifier) Notify(_ context.Context, _ string, n push.Notification) error {
	r.got <- n
	return nil
}

func prOpen(checks *worktree.PRChecks) func() (worktree.PRInfo, error) {
	return func() (worktree.PRInfo, error) {
		return worktree.PRInfo{State: "OPEN", URL: "https://example.test/pr/1", Checks: checks}, nil
	}
}

// ghSilent is what worktree.PRState returns for EVERY unanswerable poll: no gh, no auth, rate
// limited, no network, or simply no PR for this branch. Empty PRInfo, nil error.
func ghSilent() (worktree.PRInfo, error) { return worktree.PRInfo{}, nil }

// nextPush waits briefly for a delivered notification.
func nextPush(t *testing.T, got chan push.Notification) (push.Notification, bool) {
	t.Helper()
	select {
	case n := <-got:
		return n, true
	case <-time.After(2 * time.Second):
		return push.Notification{}, false
	}
}

// noPush asserts nothing was delivered. The window is short on purpose: pushNotify fans out on its
// own goroutine, so a real push arrives in microseconds, and a test that waits seconds for the
// absence of one is a test nobody runs.
func noPush(t *testing.T, got chan push.Notification, why string) {
	t.Helper()
	select {
	case n := <-got:
		t.Fatalf("%s: got an unwanted %s push (%q / %q)", why, n.Category, n.Title, n.Body)
	case <-time.After(300 * time.Millisecond):
	}
}

// TestPRChecksNotifiesOnceIntoFailure is the feature's whole point and its whole risk: the user must
// be TOLD the build went red, exactly once. Re-runs put a failing PR back into PENDING and then back
// into FAILURE, and if either of those notified again, one flapping build would push once per run —
// after which the user turns the category off and never hears about a real failure again.
func TestPRChecksNotifiesOnceIntoFailure(t *testing.T) {
	green := &worktree.PRChecks{State: prSuccess, Passed: 3}
	red := &worktree.PRChecks{State: prFailure, Passed: 2, Failed: 1, Failing: []string{"build"}}
	pending := &worktree.PRChecks{State: prPending, Passed: 2, Pending: 1}

	m, got := prHarness(t, prOpen(green), prOpen(red), prOpen(pending), prOpen(red))
	now := time.Now()

	m.pollPRChecks(context.Background(), now) // green — the first sighting is adopted, never announced
	noPush(t, got, "a PR that is green on first sight")

	m.pollPRChecks(context.Background(), now) // → red: the transition worth waking someone for
	n, ok := nextPush(t, got)
	if !ok {
		t.Fatal("no push when CI went red — the one thing this watcher exists to do")
	}
	if n.Category != notifyTests {
		t.Fatalf("red-CI push used category %q, want %q (it must honour the Tests-failed toggle)", n.Category, notifyTests)
	}
	if want := "CI failed: build"; !strings.HasPrefix(n.Body, want) {
		t.Fatalf("push body = %q, want it to lead with %q — the check name is the whole signal", n.Body, want)
	}

	m.pollPRChecks(context.Background(), now) // a re-run: PENDING must not clear the recorded failure
	noPush(t, got, "a re-run putting a failed PR back into pending")

	m.pollPRChecks(context.Background(), now) // still red — already told them
	noPush(t, got, "the same failure observed a second time")

	if st := m.prLastState; st != prFailure {
		t.Fatalf("prLastState = %q after red→pending→red, want %q", st, prFailure)
	}
}

// TestPRChecksGhFailureNeverNotifies is the correctness core. PRState reports a missing gh, an
// expired token, a 403 rate limit, a dropped tunnel and "this branch has no PR" identically: empty
// PRInfo, nil error. None of them is a CI failure, and turning any of them into one would make the
// feature cry wolf on every laptop that closes its lid.
func TestPRChecksGhFailureNeverNotifies(t *testing.T) {
	red := &worktree.PRChecks{State: prFailure, Failed: 1, Failing: []string{"test"}}
	m, got := prHarness(t, prOpen(red), ghSilent)
	now := time.Now()

	m.pollPRChecks(context.Background(), now) // adopt red silently
	noPush(t, got, "adopting an already-red PR on first sight")

	// Now gh goes quiet. A watcher that read "no checks" as "checks failed" would push here; one
	// that read it as "recovered" would push a green. It must do neither.
	for i := 0; i < 3; i++ {
		m.pollPRChecks(context.Background(), now)
		noPush(t, got, "a poll gh could not answer")
	}
	if st := m.prLastState; st != prFailure {
		t.Fatalf("prLastState = %q after gh went silent, want it UNCHANGED at %q", st, prFailure)
	}
	// And it must have backed off rather than retrying a hopeless call every couple of minutes.
	if wait := time.Until(m.prNextPoll); wait <= prPollActive {
		t.Fatalf("next poll in %s after 3 unanswerable polls — no backoff (active cadence is %s)", wait, prPollActive)
	}
	if m.prWatchDone {
		t.Fatal("silence from gh retired the watch — a tunnel coming back must resume on its own")
	}
}

// TestPRChecksMergedStopsPolling bounds the daemon's GitHub usage to OPEN work. Without this, every
// worktree the user has ever built keeps costing gh calls forever.
func TestPRChecksMergedStopsPolling(t *testing.T) {
	merged := func() (worktree.PRInfo, error) {
		return worktree.PRInfo{State: "MERGED", URL: "https://example.test/pr/1"}, nil
	}
	m, got := prHarness(t, prOpen(&worktree.PRChecks{State: prSuccess, Passed: 1}), merged)
	now := time.Now()

	m.pollPRChecks(context.Background(), now)
	m.pollPRChecks(context.Background(), now)
	noPush(t, got, "a PR merging")

	if !m.prWatchDone {
		t.Fatal("a MERGED PR left the watch armed — it would poll gh forever for a branch that landed")
	}
	// The sweep is what actually enforces it: a done session must not be counted as watchable, and
	// must never be handed to a poller again.
	m.hub.mu.Lock()
	m.hub.sessions[m.sess.ID()] = m
	m.hub.mu.Unlock()
	if n := m.hub.prSweep(now.Add(time.Hour)); n != 0 {
		t.Fatalf("prSweep still counts %d watchable session(s) after the PR merged", n)
	}
	if m.prPolling {
		t.Fatal("prSweep started a poll for a merged PR")
	}
}

// TestPRChecksRecoveryNotifiesOnlyAfterFailure covers the deliberate second notification: going
// green closes a loop the red push opened. A PR that was never red says nothing — that direction is
// pure noise, and it is the one that would fire on every routine build.
func TestPRChecksRecoveryNotifiesOnlyAfterFailure(t *testing.T) {
	green := &worktree.PRChecks{State: prSuccess, Passed: 3}
	red := &worktree.PRChecks{State: prFailure, Failed: 1, Failing: []string{"lint"}}

	// Never-red: pending → green must stay silent.
	quiet, quietPush := prHarness(t, prOpen(&worktree.PRChecks{State: prPending, Pending: 2}), prOpen(green))
	quiet.pollPRChecks(context.Background(), time.Now())
	quiet.pollPRChecks(context.Background(), time.Now())
	noPush(t, quietPush, "a PR that went pending → green without ever failing")

	// Red → green: one recovery push, under the PR category so it can be muted separately.
	m, got := prHarness(t, prOpen(green), prOpen(red), prOpen(green), prOpen(green))
	for i := 0; i < 2; i++ { // adopt green, then go red
		m.pollPRChecks(context.Background(), time.Now())
	}
	if n, ok := nextPush(t, got); !ok || n.Category != notifyTests {
		t.Fatalf("expected the red-CI push first, got %+v (ok=%v)", n, ok)
	}
	m.pollPRChecks(context.Background(), time.Now())
	n, ok := nextPush(t, got)
	if !ok {
		t.Fatal("no push when a failing PR went green again — the red push asked for action nobody closed out")
	}
	if n.Category != notifyPR {
		t.Fatalf("recovery push used category %q, want %q", n.Category, notifyPR)
	}
	m.pollPRChecks(context.Background(), time.Now())
	noPush(t, got, "a PR that is still green a poll later")
}

// TestPRChecksNoCIIsNotAFailure guards the distinction a9e0a45 was built around: a repo with no CI
// at all must never be mistaken for one whose CI failed.
func TestPRChecksNoCIIsNotAFailure(t *testing.T) {
	m, got := prHarness(t, prOpen(nil))
	for i := 0; i < 3; i++ {
		m.pollPRChecks(context.Background(), time.Now())
	}
	noPush(t, got, "an open PR in a repo with no CI")
	if st := m.prLastState; st != prNoChecks {
		t.Fatalf("prLastState = %q for a PR with no checks, want %q", st, prNoChecks)
	}
}

// TestPRSweepSkipsWithoutObservers proves the poll doesn't burn GitHub quota when nothing can hear
// the answer, and — the part that actually matters — that it resumes IMMEDIATELY once something can,
// rather than sitting out a timer it never set.
func TestPRSweepSkipsWithoutObservers(t *testing.T) {
	m, _ := prHarness(t, prOpen(&worktree.PRChecks{State: prSuccess, Passed: 1}))
	h := m.hub
	h.mu.Lock()
	h.sessions[m.sess.ID()] = m
	h.pushTokens = nil // no phone enrolled and no client connected → nobody to tell
	h.mu.Unlock()

	if n := h.prSweep(time.Now()); n != 1 {
		t.Fatalf("prSweep counts %d watchable sessions, want 1 (it is still worth watching)", n)
	}
	if m.prPolling || !m.prNextPoll.IsZero() {
		t.Fatal("prSweep polled (or scheduled) with no client and no push token registered")
	}

	// A phone pairs: the very next sweep must poll, not wait.
	h.mu.Lock()
	h.pushTokens = []string{"dev"}
	h.mu.Unlock()
	h.prSweep(time.Now())
	deadline := time.After(2 * time.Second)
	for {
		m.mu.Lock()
		polled := m.prLastState != ""
		m.mu.Unlock()
		if polled {
			return
		}
		select {
		case <-deadline:
			t.Fatal("prSweep did not poll on the first tick after a device enrolled")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// TestNextPRBackoff pins the retry curve: start at the settled cadence (retrying a missing gh in two
// minutes is pointless), double, stop at the ceiling.
func TestNextPRBackoff(t *testing.T) {
	d := nextPRBackoff(0)
	if d != prPollSettled {
		t.Fatalf("first backoff = %s, want %s", d, prPollSettled)
	}
	for i := 0; i < 12; i++ {
		next := nextPRBackoff(d)
		if next < d {
			t.Fatalf("backoff went backwards: %s → %s", d, next)
		}
		d = next
	}
	if d != prPollBackoffMax {
		t.Fatalf("backoff settled at %s, want the %s ceiling", d, prPollBackoffMax)
	}
}
