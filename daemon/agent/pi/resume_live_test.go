package pi

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/agent"
	"github.com/howlerops/oculus/daemon/protocol"
)

// TestLive_PiResumeRestoresAgentMemory proves a restart actually brings the CONVERSATION back, not
// just the transcript shown above it.
//
// v0.2.160 re-keyed the stored history onto the restarted session, which made the UI look right
// while the agent behind it remembered nothing — the user saw turns it could not recall. v0.2.162
// made restart prefer the provider's own resume path. The distinction is invisible to any test that
// checks state or stored rows, so this plants a fact, resumes the session the way restartSession
// does, and asks for the fact back.
//
// Skips without pi installed.
func TestLive_PiResumeRestoresAgentMemory(t *testing.T) {
	bin, err := exec.LookPath("pi")
	if err != nil {
		t.Skip("no pi")
	}
	dir := t.TempDir()
	p := New([]string{bin, "--mode", "rpc"})

	// Turn 1: plant a fact.
	s1, err := p.Create(context.Background(), dir, "Remember this word: BANANA. Reply with just: stored")
	if err != nil {
		t.Fatal(err)
	}
	t1 := collectText(t, s1, 90*time.Second)
	t.Logf("turn1 text = %q  sessionID=%s", t1, s1.ID())
	_ = s1.Close()
	time.Sleep(2 * time.Second)

	// Now resume that same session id the way restartSession does.
	if !p.CanResume(s1.ID()) {
		t.Fatalf("CanResume(%s) = false — restart would fall back to a cold session", s1.ID())
	}
	s2, err := p.Attach(context.Background(), s1.ID(), dir)
	if err != nil {
		t.Fatalf("attach: %v", err)
	}
	defer s2.Close()
	// Drain the replayed history first.
	time.Sleep(3 * time.Second)
	for drained := false; !drained; {
		select {
		case <-s2.Events():
		default:
			drained = true
		}
	}
	if err := s2.Prompt(context.Background(), "What word did I ask you to remember? Reply with just that word."); err != nil {
		t.Fatal(err)
	}
	t2 := collectText(t, s2, 90*time.Second)
	t.Logf("turn2 text = %q", t2)
	if !strings.Contains(strings.ToUpper(t2), "BANANA") {
		t.Errorf("resumed agent did not recall the fact — got %q", t2)
	}
}

// collectText drains a session until it reports a terminal status, returning the assistant text.
func collectText(t *testing.T, s agent.Session, d time.Duration) string {
	t.Helper()
	var b strings.Builder
	deadline := time.After(d)
	for {
		select {
		case ev, ok := <-s.Events():
			if !ok {
				return b.String()
			}
			switch pl := ev.Payload.(type) {
			case protocol.OutputDelta:
				b.WriteString(pl.Text)
			case protocol.SessionStatus:
				if pl.Status == protocol.StatusIdle || pl.Status == protocol.StatusError {
					return b.String()
				}
			}
		case <-deadline:
			return b.String()
		}
	}
}
