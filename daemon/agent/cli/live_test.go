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
	// The SHIPPED config, not a hand-written copy of it. A local Config drifts from the builtin and
	// then the test proves nothing about what users run — which already happened here: the builtin
	// gained DropLinePrefixes and this test, still constructing its own, kept reporting gemini's
	// startup noise as though the filter did not work.
	cfg := builtinConfig("gemini")
	if cfg.Command == "" {
		t.Fatal("no built-in gemini entry — this test must exercise the shipped configuration")
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
	// The built-in argv, substituted. A preflight with different flags would fail for a reason that
	// has nothing to do with whether the agent works — a test that lies about why it opted out is
	// worse than no test.
	preArgs := substitute(cfg.Args, "Reply with exactly: OK", "", cfg.Model, "")
	pre, preErr := exec.Command(cfg.Command, preArgs...).CombinedOutput()
	if preErr != nil {
		t.Skipf("gemini cannot run on this machine (%v): %s", preErr, firstLineOf(string(pre)))
	}
	p := NewProvider(cfg)
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

// The noise filter, exercised without needing a live agent: a prefix split across read boundaries
// must still be recognised, and everything else must pass through byte-for-byte.
func TestDropNoiseHandlesSplitLines(t *testing.T) {
	s := &session{cfg: Config{DropLinePrefixes: []string{"[STARTUP]"}}}
	var carry string
	var out string
	// "[STARTUP] noise\n" arrives in three pieces, straddling the prefix and the newline.
	for _, chunk := range []string{"[STAR", "TUP] noise\nreal ", "answer\n"} {
		out += s.dropNoise(chunk, &carry)
	}
	if out != "real answer\n" {
		t.Fatalf("filtered output = %q, want %q", out, "real answer\n")
	}
}

// With nothing declared the filter must be a pure pass-through — every other agent depends on that.
func TestDropNoiseIsAPassThroughByDefault(t *testing.T) {
	s := &session{cfg: Config{}}
	var carry string
	if got := s.dropNoise("[STARTUP] keep me\n", &carry); got != "[STARTUP] keep me\n" {
		t.Fatalf("unfiltered agent lost output: %q", got)
	}
}

// Output need not end with a newline. The carry buffer holds a trailing partial line so a prefix
// split across reads is caught — which means the stream's end MUST flush it, or the agent's last
// line vanishes. That is exactly how this filter first broke: a reply of "OK" with no trailing
// newline produced an empty turn.
func TestDropNoiseFinalFlushKeepsTheLastLine(t *testing.T) {
	s := &session{cfg: Config{DropLinePrefixes: []string{"[STARTUP]"}}}
	var carry string
	if got := s.dropNoise("[STARTUP] noise\nOK", &carry); got != "" {
		t.Fatalf("mid-stream output = %q, want the noise dropped and OK held back", got)
	}
	if carry != "OK" {
		t.Fatalf("carry = %q, want the unterminated final line held", carry)
	}
	if got := s.dropNoiseFinal(carry); got != "OK" {
		t.Fatalf("final flush = %q, want %q", got, "OK")
	}
	if got := s.dropNoiseFinal("[STARTUP] trailing noise"); got != "" {
		t.Fatalf("final flush kept noise: %q", got)
	}
}
