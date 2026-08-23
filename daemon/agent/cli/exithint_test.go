package cli

import (
	"strings"
	"testing"
)

// The exit marker this adapter itself writes into the stream must not be read back as the agent's
// diagnosis — that would report "[gemini exited: exit status 41]" as the reason for exit status 41.
func TestExitHintIgnoresOurOwnExitMarker(t *testing.T) {
	s := &session{cfg: Config{Name: "gemini"}}
	s.recordTail("fatal: could not read config\n[gemini exited: exit status 41]\n")
	if got := s.exitHint(); got != "fatal: could not read config" {
		t.Fatalf("exitHint() = %q", got)
	}
}

// The diagnosis and the instruction are often on separate lines; keeping only one drops the half
// that matters. gemini's real output names the variable on the first line and says what to do on
// the second, so a hint built from the last line alone never mentions GEMINI_API_KEY at all.
func TestExitHintJoinsAMultiLineMessage(t *testing.T) {
	s := &session{cfg: Config{Name: "gemini"}}
	s.recordTail("When using Gemini API, you must specify the GEMINI_API_KEY environment variable.\n" +
		"Update your environment and try again (no reload needed if using .env)!\n")
	got := s.exitHint()
	if !strings.Contains(got, "GEMINI_API_KEY") {
		t.Fatalf("hint lost the variable name: %q", got)
	}
	if !strings.Contains(got, "Update your environment") {
		t.Fatalf("hint lost the instruction: %q", got)
	}
}

// Stack frames and error-object dumps are indented; the diagnosis is not. Joining everything would
// bury the one readable line under "at ChildProcess._handle.onexit (node:internal/…)". This is
// codex's real failure output.
func TestExitHintDropsStackFramesAndKeepsTheDiagnosis(t *testing.T) {
	s := &session{cfg: Config{Name: "codex"}}
	s.recordTail("Error: spawn /usr/local/bin/codex ENOENT\n" +
		"    at ChildProcess._handle.onexit (node:internal/child_process:286:19)\n" +
		"  errno: -2,\n}\n")
	if got := s.exitHint(); got != "Error: spawn /usr/local/bin/codex ENOENT" {
		t.Fatalf("exitHint() = %q", got)
	}
}

func TestExitHintEmptyWhenNothingWasSaid(t *testing.T) {
	s := &session{cfg: Config{Name: "x"}}
	if got := s.exitHint(); got != "" {
		t.Fatalf("exitHint() = %q, want empty", got)
	}
}

// Bounded: this string lands in a push notification and on a fleet card.
func TestExitHintIsClipped(t *testing.T) {
	s := &session{cfg: Config{Name: "x"}}
	long := ""
	for i := 0; i < 400; i++ {
		long += "x"
	}
	s.recordTail(long + "\n")
	// 160 is a BYTE budget and Clip appends an ellipsis when it shortens, so the ceiling is
	// 160 + len(ellipsis) — the point is that a runaway line cannot reach a push notification, not
	// that the result is exactly 160.
	if got := s.exitHint(); len(got) > 160+4 {
		t.Fatalf("exitHint() not clipped: %d bytes", len(got))
	}
}

// The tail is a diagnostic of last resort, not a second transcript.
func TestRecordTailKeepsOnlyTheLastFewLines(t *testing.T) {
	s := &session{cfg: Config{Name: "x"}}
	for i := 0; i < 50; i++ {
		s.recordTail("line number " + string(rune('a'+i%26)) + "\n")
	}
	if len(s.tail) > 6 {
		t.Fatalf("tail grew to %d lines", len(s.tail))
	}
}
