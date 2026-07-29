// Package osctitle extracts terminal-title (OSC) sequences from an agent's output stream and
// classifies them into a running / waiting / idle status — so ANY terminal CLI agent that sets its
// title (Claude Code, Codex, and most TUIs do) gets a live status in the app without a bespoke
// per-provider adapter. This mirrors how Orca derives its status dots from the OSC title the CLIs
// already emit.
//
// It recognizes OSC 0/1/2 ("set icon name and/or window title"):
//
//	ESC ] 0 ; <title> BEL      or   ESC ] 0 ; <title> ESC \
//	ESC ] 1 ; <title> BEL/ST        (icon name)
//	ESC ] 2 ; <title> BEL/ST        (window title)
//
// The Scanner is stateful across Write calls, so a title split across read chunks is still captured.
package osctitle

import "strings"

const (
	esc = 0x1b // ESC
	bel = 0x07 // BEL
)

// Scanner consumes a byte stream and invokes OnTitle for each complete OSC title it finds. It never
// blocks and holds only a small partial-sequence buffer.
type Scanner struct {
	OnTitle func(title string)
	inOSC   bool   // currently inside an ESC ] … sequence
	sawEsc  bool   // last byte was ESC (watching for ] to open, or \ to close via ST)
	acc     []byte // accumulated bytes of the current OSC payload (after the "Ps;")
	max     int    // cap so a stream that never terminates a sequence can't grow unbounded
}

// New returns a Scanner that calls onTitle for each extracted title.
func New(onTitle func(string)) *Scanner {
	return &Scanner{OnTitle: onTitle, max: 8192}
}

// Write feeds bytes through the scanner (implements io.Writer so it can tee an output stream).
func (s *Scanner) Write(p []byte) (int, error) {
	for _, b := range p {
		s.step(b)
	}
	return len(p), nil
}

func (s *Scanner) step(b byte) {
	if !s.inOSC {
		if s.sawEsc && b == ']' {
			s.inOSC = true
			s.sawEsc = false
			s.acc = s.acc[:0]
			return
		}
		s.sawEsc = b == esc
		return
	}
	// Inside an OSC sequence. Terminators: BEL, or ST (ESC \).
	if b == bel {
		s.finish()
		return
	}
	if s.sawEsc {
		s.sawEsc = false
		if b == '\\' { // ST
			s.finish()
			return
		}
		// A stray ESC inside the OSC — abandon (malformed); reset.
		s.reset()
		if b == ']' { // ESC ] restarts a fresh OSC
			s.inOSC = true
		}
		return
	}
	if b == esc {
		s.sawEsc = true
		return
	}
	if len(s.acc) < s.max {
		s.acc = append(s.acc, b)
	}
}

// finish parses the accumulated "Ps;title" payload and emits the title (for Ps in 0,1,2).
func (s *Scanner) finish() {
	payload := string(s.acc)
	s.reset()
	// payload is "Ps;title". Split on the first ';'.
	i := strings.IndexByte(payload, ';')
	if i < 0 {
		return
	}
	ps := payload[:i]
	title := payload[i+1:]
	switch ps {
	case "0", "1", "2":
		if s.OnTitle != nil && title != "" {
			s.OnTitle(title)
		}
	}
}

func (s *Scanner) reset() {
	s.inOSC = false
	s.sawEsc = false
	s.acc = s.acc[:0]
}

// Status classification — mirrors protocol status strings without importing protocol (keeps this
// package dependency-free and unit-testable in isolation).
const (
	StatusRunning = "running"
	StatusWaiting = "awaiting_approval"
	StatusIdle    = "idle"
)

// Classify maps a terminal title to a coarse agent status. Heuristic and case-insensitive:
//   - waiting markers (waiting, awaiting, input, approve, permission, "?", "y/n") → awaiting
//   - idle/done markers (idle, done, ready, complete, finished, "✓") → idle
//   - anything else (a live task title like "editing main.go") → running
//
// An empty title is treated as idle (many shells clear the title when the agent returns to a prompt).
func Classify(title string) string {
	t := strings.ToLower(strings.TrimSpace(title))
	if t == "" {
		return StatusIdle
	}
	for _, m := range []string{"waiting", "awaiting", "needs input", "your input", "approve", "permission", "confirm", "(y/n)", "y/n"} {
		if strings.Contains(t, m) {
			return StatusWaiting
		}
	}
	// A title that ENDS in a question mark is a genuine prompt ("Overwrite file?"). A "?" anywhere in
	// the title over-triggered — a progress message like "Analyzing: what changed?" or "Reading foo?bar"
	// falsely marked the session "needs you". The explicit keywords above already catch phrased asks.
	if strings.HasSuffix(t, "?") {
		return StatusWaiting
	}
	for _, m := range []string{"idle", "done", "ready", "complete", "finished", "✓", "✔"} {
		if strings.Contains(t, m) {
			return StatusIdle
		}
	}
	return StatusRunning
}
