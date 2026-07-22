package cli

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/protocol"
)

func TestSubstitute(t *testing.T) {
	// {prompt} + {cwd} + {model} expand in place.
	got := substitute([]string{"--model", "{model}", "--cwd", "{cwd}", "{prompt}"}, "fix the bug", "/repo", "gpt-5")
	want := []string{"--model", "gpt-5", "--cwd", "/repo", "fix the bug"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("substitute = %v, want %v", got, want)
	}
	// No {prompt} token → prompt appended as the last arg.
	got = substitute([]string{"exec"}, "hello", "/repo", "")
	if len(got) != 2 || got[1] != "hello" {
		t.Errorf("substitute (append) = %v, want [exec hello]", got)
	}
}

func TestStripANSI(t *testing.T) {
	in := "\x1b[32mgreen\x1b[0m\r\ndone"
	if out := stripANSI(in); out != "green\ndone" {
		t.Errorf("stripANSI = %q, want %q", out, "green\ndone")
	}
}

func TestMerge_UserOverridesBuiltin(t *testing.T) {
	detected := []Config{{Name: "codex", Command: "codex"}, {Name: "gemini", Command: "gemini"}}
	user := []Config{{Name: "codex", Command: "/custom/codex"}, {Name: "mine", Command: "mine"}}
	got := Merge(detected, user)
	if len(got) != 3 {
		t.Fatalf("merged len = %d, want 3", len(got))
	}
	if got[0].Command != "/custom/codex" { // user overrode built-in in place
		t.Errorf("codex not overridden: %+v", got[0])
	}
	if got[2].Name != "mine" { // new user agent appended
		t.Errorf("new agent missing: %+v", got)
	}
}

// A full turn: `echo` streams the prompt as output and the session goes idle, then Close() ends the
// event stream (channel closes) — exercising the pump + single-close lifecycle.
func TestSession_RunsStreamsAndCloses(t *testing.T) {
	p := NewProvider(Config{Name: "echoer", Command: "echo", Args: []string{"{prompt}"}})
	sess, err := p.Create(context.Background(), t.TempDir(), "hi there")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	var output strings.Builder
	sawIdle := false
	deadline := time.After(5 * time.Second)
loop:
	for {
		select {
		case ev, ok := <-sess.Events():
			if !ok {
				break loop
			}
			switch e := ev.Payload.(type) {
			case protocol.OutputDelta:
				output.WriteString(e.Text)
			case protocol.SessionStatus:
				if e.Status == protocol.StatusIdle {
					sawIdle = true
					sess.Close() // end the session; the events channel should then close
				}
			}
		case <-deadline:
			t.Fatal("timed out waiting for turn to finish")
		}
	}
	if !sawIdle {
		t.Error("never saw idle status")
	}
	if !strings.Contains(output.String(), "hi there") {
		t.Errorf("output = %q, want it to contain 'hi there'", output.String())
	}
}

func TestSession_RejectsUnknownCommand(t *testing.T) {
	p := NewProvider(Config{Name: "nope", Command: "definitely-not-a-real-binary-xyz", Args: []string{"{prompt}"}})
	if _, err := p.Create(context.Background(), t.TempDir(), "x"); err == nil {
		t.Error("expected error for a command not on PATH")
	}
}
