package cli

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/protocol"
)

// firstLineOf trims a tool's error output to something readable in a skip message.
func firstLineOf(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 160 {
		s = s[:160] + "…"
	}
	return s
}

// TestLive_CLIAgentProducesOutput drives a REAL bring-your-own CLI agent end to end.
//
// The cli adapter is the thinnest of the four — status and output deltas only — and was the last one
// with no live coverage. That mattered because the two worst bugs this codebase has had in the
// adapters (pi saying nothing, claude-code's frame decoding) were both invisible to unit tests
// against fakes and only surfaced when a real agent was on the other end.
//
// gemini is used because it is the built-in entry most likely to be installed and non-interactive.
// Skips when it isn't there.
func TestLive_CLIAgentProducesOutput(t *testing.T) {
	if _, err := exec.LookPath("gemini"); err != nil {
		t.Skip("gemini not installed")
	}
	// Pre-flight the agent OUTSIDE our adapter first.
	//
	// Without this the test cannot tell "our adapter is broken" from "this third-party CLI won't
	// start", and it fails for the latter — which is noise that trains people to ignore it. Both
	// built-in CLI agents are currently unusable on this machine for their own reasons (codex ships
	// a broken vendored binary; gemini's individual tier was discontinued), neither of which says
	// anything about this code.
	//
	// So: if the agent cannot answer when we invoke it directly, SKIP. If it can, any failure that
	// follows is ours, and the test is worth believing.
	pre, preErr := exec.Command("gemini", "-p", "Reply with exactly: OK").CombinedOutput()
	if preErr != nil {
		t.Skipf("gemini cannot run on this machine (%v): %s", preErr, firstLineOf(string(pre)))
	}
	p := NewProvider(Config{
		Name: "gemini", Command: "gemini", Args: []string{"-p", "{prompt}"},
	})
	sess, err := p.Create(context.Background(), t.TempDir(), "Reply with exactly: OK")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer sess.Close()

	var text strings.Builder
	deadline := time.After(120 * time.Second)
	for {
		select {
		case ev, ok := <-sess.Events():
			if !ok {
				goto done
			}
			switch pl := ev.Payload.(type) {
			case protocol.OutputDelta:
				text.WriteString(pl.Text)
			case protocol.SessionMessage:
				if pl.Role == "assistant" {
					text.WriteString(pl.Text)
				}
			case protocol.SessionStatus:
				if pl.Status == protocol.StatusError {
					t.Fatalf("turn errored: %s", pl.Detail)
				}
				if pl.Status == protocol.StatusIdle {
					goto done
				}
			}
		case <-deadline:
			t.Fatalf("no completed turn; text so far = %q", text.String())
		}
	}
done:
	if strings.TrimSpace(text.String()) == "" {
		t.Fatal("turn completed but streamed no output — the same failure mode pi shipped with")
	}
	t.Logf("cli agent replied: %q", strings.TrimSpace(text.String()))
}
