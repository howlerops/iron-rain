package opencode

import (
	"context"

	"fmt"
	"github.com/howlerops/oculus/daemon/agent"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// Attaching to a real session used to hang forever.
//
// replayHistory emitted one event per history message into s.events, which is 64-buffered, and
// NOBODY reads Events() until Attach has RETURNED the session — hub.go and persist.go both do
// `sess, err := att.Attach(...)` and only then addSession + `go m.run()`. So once the buffer filled,
// emit parked on a `done` channel that only Close() closes, and Close cannot be called on a session
// that was never returned. Deadlock, with no error and no log line.
//
// 64 messages is not a large conversation — any real agentic session passes it in minutes. Two
// user-visible failures followed. Taking a session over from the app (session.attach) hung the
// client's request and leaked the handler goroutine. Worse, RestoreSessions is a SERIAL loop, so one
// such session stalled a daemon restart and every session after it in the list was never restored at
// all — they simply vanished from the sidebar.
//
// claudecode (`go cs.replayTranscript`) and pi (`go s.replayTranscript`) already replayed
// off-goroutine; opencode was the one that did not.
func TestAttachDoesNotDeadlockOnALongHistory(t *testing.T) {
	// Comfortably past the 64-slot buffer, and every message carries text so every one emits.
	var msgs []string
	for i := 0; i < 200; i++ {
		msgs = append(msgs, fmt.Sprintf(
			`{"info":{"id":"msg_%d","role":"assistant","modelID":"claude-opus-4","providerID":"anthropic"},`+
				`"parts":[{"type":"text","text":"reply %d"}]}`, i, i))
	}
	stub := newAttachStub("ses_long", "["+strings.Join(msgs, ",")+"]")
	srv := httptest.NewServer(stub)
	defer srv.Close()

	type attached struct {
		sess agent.Session
		err  error
	}
	done := make(chan attached, 1)
	go func() {
		sess, err := New(srv.URL).Attach(context.Background(), "ses_long", "/tmp/proj")
		done <- attached{sess, err}
	}()

	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("attach failed: %v", got.err)
		}
		// Close releases the replay goroutine, which is still parked on the unread event buffer —
		// in production the hub starts draining it immediately, but nothing here does.
		got.sess.Close()
	case <-time.After(10 * time.Second):
		t.Fatal("Attach never returned for a session with 200 history messages.\n\n" +
			"The replay is filling the 64-slot event buffer with nobody reading it, and emit is " +
			"parked on a channel that only Close() closes — which cannot be called on a session that " +
			"was never returned. On a daemon restart this stalls the serial restore loop, so every " +
			"session after this one is silently never restored.")
	}
}

// The history must still ARRIVE — moving the emits off the attach path must not drop them.
func TestTheReplayedHistoryStillReachesTheEventStream(t *testing.T) {
	stub := newAttachStub("ses_short", `[
	  {"info":{"id":"m1","role":"user"},"parts":[{"type":"text","text":"hello"}]},
	  {"info":{"id":"m2","role":"assistant","modelID":"claude-opus-4","providerID":"anthropic"},
	   "parts":[{"type":"text","text":"hi back"}]}
	]`)
	srv := httptest.NewServer(stub)
	defer srv.Close()

	sess, err := New(srv.URL).Attach(context.Background(), "ses_short", "/tmp/proj")
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	seen := map[string]bool{}
	deadline := time.After(10 * time.Second)
	for len(seen) < 2 {
		select {
		case ev := <-sess.Events():
			if b, err := ev.Encode(); err == nil {
				for _, want := range []string{"hello", "hi back"} {
					if strings.Contains(string(b), want) {
						seen[want] = true
					}
				}
			}
		case <-deadline:
			t.Fatalf("only saw %v of the replayed history — moving the emits off the attach path "+
				"dropped them, so a taken-over session opens blank", seen)
		}
	}
}
