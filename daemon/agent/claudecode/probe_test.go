package claudecode

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// pingSidecar answers pings the way the real sidecar does — from its stdin loop, so it replies even
// while a turn is "in flight" — and reports busy until it is told to stop.
const pingSidecar = `#!/bin/sh
echo '{"t":"session","id":"'"$OCULUS_SESSION_ID"'"}'
busy=false
while IFS= read -r line; do
  case "$line" in
    *'"t":"prompt"'*) busy=true;;
    *'"t":"stop"'*)   busy=false;;
    *'"t":"ping"'*)
      id=` + "`" + `echo "$line" | sed 's/.*"id":"\([^"]*\)".*/\1/'` + "`" + `
      echo '{"t":"pong","id":"'"$id"'","busy":'"$busy"'}';;
  esac
done
`

// deafSidecar accepts input and never answers anything — a sidecar wedged so hard its stdin loop is
// gone. A probe against it must TIME OUT with an error rather than hang or claim idle.
const deafSidecar = `#!/bin/sh
echo '{"t":"session","id":"'"$OCULUS_SESSION_ID"'"}'
while IFS= read -r line; do :; done
`

func startProbeSession(t *testing.T, script string) interface {
	Probe(context.Context) (bool, error)
	Prompt(context.Context, string) error
	Stop(context.Context) error
	Close() error
} {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "sidecar.sh")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	sess, err := New([]string{path}).Create(context.Background(), dir, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sess.Close() })
	p, ok := sess.(interface {
		Probe(context.Context) (bool, error)
		Prompt(context.Context, string) error
		Stop(context.Context) error
		Close() error
	})
	if !ok {
		t.Fatal("claude-code session does not implement agent.Prober — the hub's reconciler skips " +
			"any session that doesn't, so a wedged turn would heartbeat 'working' forever")
	}
	return p
}

// TestClaudeCodeProbeReportsBusyAndIdle: the round-trip the hub's reconciler depends on to tell a
// working agent from a finished one when the event stream has gone quiet.
func TestClaudeCodeProbeReportsBusyAndIdle(t *testing.T) {
	s := startProbeSession(t, pingSidecar)
	ctx := context.Background()

	busy, err := probeSoon(t, s, 2*time.Second)
	if err != nil {
		t.Fatalf("probe on a fresh session failed: %v", err)
	}
	if busy {
		t.Fatal("probe says busy before any prompt was sent")
	}

	if err := s.Prompt(ctx, "go"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		busy, err = probeSoon(t, s, 2*time.Second)
		if err != nil {
			t.Fatalf("probe during a turn failed: %v", err)
		}
		if busy {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("probe never reported busy during a turn — the reconciler would conclude the " +
				"turn was done and close it out from under a working agent")
		}
		time.Sleep(20 * time.Millisecond)
	}

	if err := s.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(3 * time.Second)
	for {
		busy, err = probeSoon(t, s, 2*time.Second)
		if err != nil {
			t.Fatalf("probe after stop failed: %v", err)
		}
		if !busy {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("probe still reports busy after the turn ended")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestClaudeCodeProbeErrorsWhenTheSidecarIsDeaf: no answer must surface as an error (which the
// reconciler counts toward abandoning the turn), never as a hang and never as a false "idle" —
// a false idle would close a turn whose agent might still be running.
func TestClaudeCodeProbeErrorsWhenTheSidecarIsDeaf(t *testing.T) {
	s := startProbeSession(t, deafSidecar)

	ctx, cancel := context.WithTimeout(context.Background(), 400*time.Millisecond)
	defer cancel()
	done := make(chan struct{})
	var err error
	go func() { _, err = s.Probe(ctx); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Probe hung past its context deadline — it would wedge the reconciler goroutine")
	}
	if err == nil {
		t.Fatal("a sidecar that never answered was reported as a clean probe result")
	}
}

func probeSoon(t *testing.T, s interface {
	Probe(context.Context) (bool, error)
}, d time.Duration) (bool, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	return s.Probe(ctx)
}
