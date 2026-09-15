package opencode

import (
	"errors"
	"fmt"
	"io"
	"syscall"
	"testing"
)

// connectionBroke decides who is allowed to end a turn, so its two answers are not symmetric.
//
// Say "broken" about a refusal and a genuinely dead agent stays "working" until the reconciler's
// unreachable window expires — slower, but it still ends, and it ends with the right reason. Say
// "refused" about a broken pipe and a wifi handover kills a turn whose agent is still working, which
// is the failure the whole Turn Engine exists to prevent. The table below is written with that
// asymmetry in mind: the established-then-died cases are the ones that must never regress.
func TestConnectionBrokeTellsAPipeFromARefusal(t *testing.T) {
	cases := []struct {
		name  string
		err   error
		broke bool
	}{
		// Established, then died — the reconciler must get to decide.
		{"eof", io.EOF, true},
		{"unexpected eof", io.ErrUnexpectedEOF, true},
		{"wrapped eof", fmt.Errorf(`Post "http://x/session/s/message": %w`, io.EOF), true},
		{"connection reset", syscall.ECONNRESET, true},
		{"broken pipe", syscall.EPIPE, true},
		{"idle connection closed", errors.New("http: server closed idle connection"), true},
		{"http2 connection lost", errors.New("http2: client connection lost"), true},
		{"wrapped reset", fmt.Errorf(`Get "http://x": %w`, syscall.ECONNRESET), true},

		// Never established — absence, reportable immediately.
		{"refused", syscall.ECONNREFUSED, false},
		{"wrapped refused", fmt.Errorf(`Post "http://x": %w`, syscall.ECONNREFUSED), false},
		{"host unreachable", syscall.EHOSTUNREACH, false},
		{"network unreachable", syscall.ENETUNREACH, false},
		{"dns", errors.New(`dial tcp: lookup nope.invalid: no such host`), false},
		{"nil", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := connectionBroke(tc.err); got != tc.broke {
				if tc.broke {
					t.Fatalf("connectionBroke(%v) = false; a connection that was established and "+
						"then died would be reported as a failed turn, so a network blip mid-turn "+
						"ends a turn whose agent is still working", tc.err)
				}
				t.Fatalf("connectionBroke(%v) = true; nothing was ever listening, and leaving this "+
					"to the reconciler delays a verdict that is already knowable", tc.err)
			}
		})
	}
}

// A refusal must not be swallowed by the message-fragment matching.
//
// The fragment list is a fallback for errors that do not survive errors.Is, and fallbacks like it
// grow. "connection refused" contains neither "EOF" nor "reset by peer" today; a future fragment
// that overlaps it would silently move every refusal onto the patient path.
func TestARefusalIsNeverMatchedByAFragment(t *testing.T) {
	for _, msg := range []string{
		"connect: connection refused",
		`Post "http://127.0.0.1:1/session/s/message": dial tcp 127.0.0.1:1: connect: connection refused`,
	} {
		if connectionBroke(errors.New(msg)) {
			t.Fatalf("a refusal (%q) was classified as a broken pipe by message matching — a dead "+
				"agent now waits out the full unreachable window before anyone is told", msg)
		}
	}
}
