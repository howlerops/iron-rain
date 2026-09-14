package cli

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/protocol"
)

// An agent that TALKS about rate limits must not have its turn killed.
//
// detectRateLimit emitted StatusError, and turnOnStatus treats any adapter error status as a terminal
// turn failure — it closes the turn, publishes a verdict and fires a push notification. So an agent
// answering the user's question about a 429, or reporting "Rate limit exceeded, retrying in 30s"
// before going on to SUCCEED, ended the turn mid-sentence while the subprocess kept streaming into a
// turn the hub considered over.
//
// A rate limit is a reason the turn is slow, not a reason it is finished. If the agent genuinely
// cannot proceed it exits, and the non-zero exit path reports that as the error it is.
func TestMentioningARateLimitDoesNotFailTheTurn(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "agent.sh")
	// The shape of a real answer to "why is this endpoint returning 429?".
	body := "#!/bin/sh\n" +
		"echo 'A 429 means the server is rate limiting you.'\n" +
		"echo 'Here is how to add a retry-after backoff.'\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}

	p := NewProvider(Config{Name: "faker", Command: script, Args: []string{"{prompt}"}})
	sess, err := p.Create(context.Background(), dir, "")
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	if err := sess.Prompt(context.Background(), "why 429?"); err != nil {
		t.Fatal(err)
	}

	deadline := time.After(10 * time.Second)
	for {
		select {
		case ev, ok := <-sess.Events():
			if !ok {
				return // stream ended without an error status: correct
			}
			st, isStatus := ev.Payload.(protocol.SessionStatus)
			if !isStatus {
				continue
			}
			if st.Status == protocol.StatusError {
				t.Fatalf("the turn was failed because the agent's ANSWER mentioned a rate limit: %q\n\n"+
					"turnOnStatus closes the turn on any adapter error status, so the user's question "+
					"ends mid-answer, the tool cards are sealed, and a push notification says the "+
					"agent failed — while the subprocess is still streaming the rest of the reply.",
					st.Detail)
			}
			if st.Status == protocol.StatusIdle {
				return // the turn finished normally, which is the truth here
			}
		case <-deadline:
			t.Fatal("the turn never reached a terminal status")
		}
	}
}

// The signal itself must survive — the point is to report it without ending the turn.
func TestARateLimitIsStillReported(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "agent.sh")
	body := "#!/bin/sh\necho 'Rate limit exceeded, retry after 30 seconds'\necho 'done anyway'\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}

	p := NewProvider(Config{Name: "faker", Command: script, Args: []string{"{prompt}"}})
	sess, err := p.Create(context.Background(), dir, "")
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	if err := sess.Prompt(context.Background(), "go"); err != nil {
		t.Fatal(err)
	}

	sawRateLimit := false
	deadline := time.After(10 * time.Second)
	for {
		select {
		case ev, ok := <-sess.Events():
			if !ok {
				if !sawRateLimit {
					t.Fatal("the rate limit was never reported at all — the user has no idea why the " +
						"agent is slow")
				}
				return
			}
			if st, isStatus := ev.Payload.(protocol.SessionStatus); isStatus {
				if st.Detail != "" && st.Status == protocol.StatusRunning {
					sawRateLimit = true
					if st.Detail == "" {
						t.Error("the rate-limit status carried no detail")
					}
				}
				if st.Status == protocol.StatusIdle && sawRateLimit {
					return
				}
			}
		case <-deadline:
			if !sawRateLimit {
				t.Fatal("no rate-limit status was emitted")
			}
			return
		}
	}
}
