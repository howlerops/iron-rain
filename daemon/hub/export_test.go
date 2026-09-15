package hub

import (
	"testing"
	"time"
)

// CompressTurnWindowsForTest shrinks the Turn Engine's elapsed-time verdict windows for the duration
// of one test, and restores them on cleanup.
//
// It exists for the external (hub_test) soak, which drives sessions through the real hub API and so
// cannot reach a managedSession's per-instance knobs the way the in-package tests do. The windows it
// touches are deliberately long in production — two minutes before an agent refusing connections is
// called dead, because that is less time than a MacBook takes to wake — and a test that waited them
// out honestly would add minutes to every CI run.
//
// SAFETY, because mutating package state from a test has burned this file's neighbours before: these
// are read when a turn loop STARTS, so a loop already running under the old values keeps them. No
// test in this package calls t.Parallel, so the only overlap is with a turn loop left running by an
// earlier test — which is why the restore is registered rather than deferred inline, and why the
// per-session knobs (m.hbEvery and friends) remain the right tool everywhere they are reachable.
func CompressTurnWindowsForTest(t *testing.T, unreachable, slow, quiet, tick time.Duration) {
	t.Helper()
	oldU, oldS, oldQ, oldT := turnUnreachableWindow, turnSlowWindow, turnQuietAfter, turnReconcileTick
	turnUnreachableWindow, turnSlowWindow = unreachable, slow
	turnQuietAfter, turnReconcileTick = quiet, tick
	t.Cleanup(func() {
		turnUnreachableWindow, turnSlowWindow = oldU, oldS
		turnQuietAfter, turnReconcileTick = oldQ, oldT
	})
}
