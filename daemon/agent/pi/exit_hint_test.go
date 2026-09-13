package pi

import "testing"

// A crash must carry the reason, not just the exit code.
//
// pi's terminal error was "pi exited: exit status 1" — a number the user cannot look up, on a session
// that looks broken for no stated reason, while the line explaining it (a missing key, a bad config)
// had gone to stderr and straight past. The generic CLI adapter learned this and grew exitHint; this
// is the same treatment, fed by the stderr tail procutil now keeps.
func TestExitHintCarriesTheDiagnosis(t *testing.T) {
	// The cause and the instruction are often printed as separate lines; both matter.
	tail := []string{
		"Error: PI_API_KEY is not set",
		"Set it in your environment and try again",
	}
	got := exitHint(tail)
	if got == "" {
		t.Fatal("no hint produced from a stderr tail that plainly states the cause")
	}
	if !contains(got, "PI_API_KEY") {
		t.Errorf("the hint dropped the one fact the user needs: %q", got)
	}
}

// Stack frames and continuations are indented; they never carry the diagnosis and make the hint
// unreadable, so they are dropped.
func TestExitHintIgnoresStackFrames(t *testing.T) {
	tail := []string{
		"Error: ENOENT, no such file",
		"    at ChildProcess._handle.onexit (node:internal/child_process:286:19)",
		"    errno: -2,",
	}
	got := exitHint(tail)
	if contains(got, "child_process") || contains(got, "errno") {
		t.Errorf("stack frames leaked into the hint: %q", got)
	}
	if !contains(got, "ENOENT") {
		t.Errorf("the diagnosis line was lost: %q", got)
	}
}

func TestExitHintIsEmptyWhenNothingUsefulWasSaid(t *testing.T) {
	if got := exitHint(nil); got != "" {
		t.Errorf("expected no hint, got %q", got)
	}
	if got := exitHint([]string{"   ", ""}); got != "" {
		t.Errorf("whitespace is not a diagnosis, got %q", got)
	}
}

func contains(h, n string) bool {
	return len(h) >= len(n) && (func() bool {
		for i := 0; i+len(n) <= len(h); i++ {
			if h[i:i+len(n)] == n {
				return true
			}
		}
		return false
	}())
}
