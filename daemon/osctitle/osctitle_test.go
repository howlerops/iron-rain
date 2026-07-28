package osctitle

import "testing"

func collect(chunks ...string) []string {
	var got []string
	s := New(func(t string) { got = append(got, t) })
	for _, c := range chunks {
		s.Write([]byte(c))
	}
	return got
}

func TestExtractsBELTerminatedTitle(t *testing.T) {
	// ESC ] 2 ; editing main.go BEL
	got := collect("\x1b]2;editing main.go\x07")
	if len(got) != 1 || got[0] != "editing main.go" {
		t.Fatalf("got %v", got)
	}
}

func TestExtractsSTTerminatedTitle(t *testing.T) {
	// ESC ] 0 ; Claude Code ESC \
	got := collect("\x1b]0;Claude Code\x1b\\")
	if len(got) != 1 || got[0] != "Claude Code" {
		t.Fatalf("got %v", got)
	}
}

func TestTitleSplitAcrossWrites(t *testing.T) {
	// A title split across three chunks (mid-escape, mid-payload) must still be captured whole.
	got := collect("\x1b]", "2;running ", "tests\x07")
	if len(got) != 1 || got[0] != "running tests" {
		t.Fatalf("got %v", got)
	}
}

func TestIgnoresNonTitleOSC(t *testing.T) {
	// OSC 4 (set color) is not a title — must be ignored.
	got := collect("\x1b]4;1;rgb:ff/00/00\x07")
	if len(got) != 0 {
		t.Fatalf("expected no titles, got %v", got)
	}
}

func TestInterleavedTextAndTitles(t *testing.T) {
	got := collect("some output\x1b]2;step one\x07more text\x1b]2;step two\x07done")
	if len(got) != 2 || got[0] != "step one" || got[1] != "step two" {
		t.Fatalf("got %v", got)
	}
}

func TestClassify(t *testing.T) {
	cases := map[string]string{
		"editing main.go":            StatusRunning,
		"running the test suite":     StatusRunning,
		"":                           StatusIdle,
		"idle":                       StatusIdle,
		"Done — 4 passed":            StatusIdle,
		"✓ complete":                 StatusIdle,
		"waiting for your input":     StatusWaiting,
		"Approve edit to config.go?": StatusWaiting,
		"proceed? (y/n)":             StatusWaiting,
		"needs input":                StatusWaiting,
	}
	for title, want := range cases {
		if got := Classify(title); got != want {
			t.Errorf("Classify(%q) = %q, want %q", title, got, want)
		}
	}
}

func TestMalformedSequenceDoesNotHang(t *testing.T) {
	// An unterminated OSC followed by a fresh valid one: the valid one still lands.
	got := collect("\x1b]2;never terminated", "\x1b]2;good\x07")
	if len(got) != 1 || got[0] != "good" {
		t.Fatalf("got %v", got)
	}
}
