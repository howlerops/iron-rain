package hub

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/activity"
	"github.com/howlerops/oculus/daemon/protocol"
	"github.com/howlerops/oculus/daemon/push"
)

// recNotifier records every push the hub fires so a test can assert the user was actually told.
type recNotifier struct{ ch chan push.Notification }

func (r recNotifier) Notify(_ context.Context, _ string, n push.Notification) error {
	r.ch <- n
	return nil
}

// wireVerdictHub attaches an activity store, a recording push notifier and a fake hub-wide client to
// the harness hub, returning the push channel and the client's outbound frame channel.
func wireVerdictHub(t *testing.T, h *Hub) (chan push.Notification, chan []byte) {
	t.Helper()
	h.SetActivity(activity.New(filepath.Join(t.TempDir(), "activity.jsonl"), 100))
	notes := make(chan push.Notification, 16)
	h.mu.Lock()
	h.notifier = recNotifier{ch: notes}
	h.pushTokens = []string{"device-1"}
	h.pushConcurrency, h.pushTimeout = 1, 2*time.Second
	hubFrames := make(chan []byte, 256)
	// A client that is connected to the DAEMON but not subscribed to this session — the sidebar's
	// point of view, which is what the per-session state broadcast has to reach.
	h.clients[nil] = &hubClient{ch: hubFrames, done: make(chan struct{})}
	h.mu.Unlock()
	return notes, hubFrames
}

// nextHubEvent waits for the next hub-wide broadcast of the given type whose payload satisfies pred.
func nextHubEvent(t *testing.T, frames chan []byte, typ, what string, pred func(json.RawMessage) bool) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case raw := <-frames:
			env, err := protocol.Decode(raw)
			if err != nil || env.Type != typ {
				continue
			}
			if pred(env.Payload) {
				return
			}
		case <-deadline:
			t.Fatalf("timeout waiting for hub-wide %s: %s", typ, what)
		}
	}
}

func unreadNeedsYou(h *Hub, sessionID string) int {
	n := 0
	h.mu.Lock()
	a := h.activity
	h.mu.Unlock()
	for _, e := range a.Recent() {
		if e.NeedsYou && !e.Read && e.SessionID == sessionID {
			n++
		}
	}
	return n
}

// TestAbandonedTurnPublishesVerdict is the "a dead agent reads Live forever" bug. The reconciler
// declaring a turn abandoned is the ONLY authority on that fact: if closeTurn keeps it to itself,
// m.lastStatus stays "running" (so session.list keeps serving a lie), nothing lands in the activity
// feed, no push goes out, and every client not subscribed to that session shows a working agent that
// has been dead for minutes.
func TestAbandonedTurnPublishesVerdict(t *testing.T) {
	m, _, frames := turnHarness(t, func(context.Context) (bool, error) { return false, context.DeadlineExceeded })
	notes, hubFrames := wireVerdictHub(t, m.hub)

	m.openTurn("")
	nextTurnState(t, frames, "abandoned", func(ts protocol.TurnState) bool { return ts.State == "abandoned" })

	// 1. The rendered status flips: session.list / info() must not keep saying "running".
	deadline := time.Now().Add(2 * time.Second)
	for {
		m.mu.Lock()
		st := m.lastStatus
		m.mu.Unlock()
		if st == protocol.StatusError {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("lastStatus = %q after abandonment, want %q — the session list keeps lying", st, protocol.StatusError)
		}
		time.Sleep(5 * time.Millisecond)
	}

	// 2. Every connected client hears the per-session state edge, even unsubscribed ones.
	nextHubEvent(t, hubFrames, protocol.TypeSessionStatus, "abandoned session state", func(p json.RawMessage) bool {
		var ss protocol.SessionStatus
		return json.Unmarshal(p, &ss) == nil && ss.SessionID == "t1" && ss.Status == protocol.StatusError
	})

	// 3. It lands in the activity feed as a needs-you, so the inbox shows the death.
	if got := unreadNeedsYou(m.hub, "t1"); got != 1 {
		t.Fatalf("abandoned turn produced %d unread needs-you activity events, want 1", got)
	}

	// 4. And the user is actually told, since they walked away — that is the whole point.
	select {
	case n := <-notes:
		if n.Category != "AGENT_ERROR" {
			t.Fatalf("push category = %q, want AGENT_ERROR", n.Category)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no push fired for an abandoned turn — the agent died and nobody was told")
	}
}

// TestTurnOpenBroadcastsSessionState covers the other edge: a turn STARTING must reach every client
// too, so a sidebar row flips to running without waiting for a session.list refetch.
func TestTurnOpenBroadcastsSessionState(t *testing.T) {
	m, _, _ := turnHarness(t, func(context.Context) (bool, error) { return true, nil })
	_, hubFrames := wireVerdictHub(t, m.hub)
	m.openTurn("running bash")
	defer m.closeTurn(protocol.StatusIdle, "")
	nextHubEvent(t, hubFrames, protocol.TypeSessionStatus, "turn open", func(p json.RawMessage) bool {
		var ss protocol.SessionStatus
		return json.Unmarshal(p, &ss) == nil && ss.SessionID == "t1" && ss.Status == protocol.StatusRunning
	})
}

// TestApprovalAnsweredClearsNeedsYou: answering the ask is the event that makes the badge false.
// The session that was answered loses its live needs-you (and the clients are told, so the phone's
// badge clears without a refetch); a DIFFERENT session's ask must be left completely alone.
func TestApprovalAnsweredClearsNeedsYou(t *testing.T) {
	m, _, _ := turnHarness(t, func(context.Context) (bool, error) { return true, nil })
	h := m.hub
	_, hubFrames := wireVerdictHub(t, h)

	h.recordActivity(activity.Event{Kind: activity.KindNeedsInput, SessionID: "t1", Title: "approve bash", NeedsYou: true})
	h.recordActivity(activity.Event{Kind: activity.KindNeedsInput, SessionID: "other", Title: "approve write", NeedsYou: true})
	if unreadNeedsYou(h, "t1") != 1 || unreadNeedsYou(h, "other") != 1 {
		t.Fatal("test setup: both sessions should start with one live needs-you")
	}

	m.openTurn("")
	defer m.closeTurn(protocol.StatusIdle, "")
	m.turnOnStatus(protocol.SessionStatus{SessionID: "t1", Status: protocol.StatusAwaitingApproval})
	// The answer: the provider resumes the turn. This is the daemon's proof the ask was resolved.
	m.turnOnStatus(protocol.SessionStatus{SessionID: "t1", Status: protocol.StatusRunning})

	if got := unreadNeedsYou(h, "t1"); got != 0 {
		t.Fatalf("answered session still has %d unread needs-you — the badge outlives the reason", got)
	}
	if got := unreadNeedsYou(h, "other"); got != 1 {
		t.Fatalf("another session's needs-you was cleared too (%d live) — answering one ask must not blank the inbox", got)
	}
	// Clients that already hold the feed must be told, or the phone keeps its stale badge.
	nextHubEvent(t, hubFrames, protocol.TypeActivityEvent, "cleared needs-you", func(p json.RawMessage) bool {
		var e protocol.ActivityEvent
		return json.Unmarshal(p, &e) == nil && e.SessionID == "t1" && e.NeedsYou && e.Read
	})
}

// TestErrorRecoveryClearsNeedsYou: a session that errored (needs-you) and then started running again
// has recovered — nothing is waiting on the user any more.
func TestErrorRecoveryClearsNeedsYou(t *testing.T) {
	m, _, _ := turnHarness(t, func(context.Context) (bool, error) { return true, nil })
	h := m.hub
	wireVerdictHub(t, h)
	h.recordActivity(activity.Event{Kind: activity.KindError, SessionID: "t1", Title: "errored", NeedsYou: true})

	m.openTurn("") // a fresh turn on a previously-errored session = recovery
	defer m.closeTurn(protocol.StatusIdle, "")
	if got := unreadNeedsYou(h, "t1"); got != 0 {
		t.Fatalf("recovered session still has %d unread needs-you", got)
	}
}
