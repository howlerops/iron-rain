package cli

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/agent"
	"github.com/howlerops/oculus/daemon/protocol"
)

// resumeCfg runs /bin/sh so the chosen ARG TEMPLATE is observable in the turn's own output: the
// cold template prints "cold", the resume template prints "resumed".
func resumeCfg() Config {
	return Config{
		Name:       "faux",
		Command:    "/bin/sh",
		Args:       []string{"-c", "printf cold", "{prompt}"},
		ResumeArgs: []string{"-c", "printf resumed", "{prompt}"},
	}
}

func firstOutput(t *testing.T, sess agent.Session) string {
	t.Helper()
	deadline := time.After(10 * time.Second)
	var out strings.Builder
	for {
		select {
		case ev, ok := <-sess.Events():
			if !ok {
				return out.String()
			}
			if ev.Type == protocol.TypeOutputDelta {
				out.WriteString(ev.Payload.(protocol.OutputDelta).Text)
			}
			if ev.Type == protocol.TypeSessionStatus {
				if ss := ev.Payload.(protocol.SessionStatus); ss.Status == protocol.StatusIdle || ss.Status == protocol.StatusError {
					return out.String()
				}
			}
		case <-deadline:
			t.Fatal("turn produced no terminal status")
		}
	}
}

// TestResumedSessionUsesResumeArgsOnItsFirstTurn is the CLI half of restart amnesia. A CLI agent has
// no server-side session: after a daemon restart the hub can only re-RUN the command. Turn counting
// lives in the (now dead) process, so the fresh session counted zero turns and re-ran the agent's
// COLD invocation — starting a brand-new conversation while the app still showed the old one's
// history. When the hub knows the session had turns it must say so, and the very first turn after
// the restart must use the configured resume invocation.
func TestResumedSessionUsesResumeArgsOnItsFirstTurn(t *testing.T) {
	p := NewProvider(resumeCfg())
	sess, err := p.Create(context.Background(), t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	mr, ok := sess.(interface{ MarkResumed() })
	if !ok {
		t.Fatal("a cli session cannot be told it continues a prior conversation — the hub has no way to avoid a cold restart")
	}
	mr.MarkResumed()

	if err := sess.Prompt(context.Background(), "carry on"); err != nil {
		t.Fatal(err)
	}
	if got := firstOutput(t, sess); !strings.Contains(got, "resumed") {
		t.Fatalf("first turn after a restart ran %q, want the resume invocation — the agent started a cold conversation", got)
	}
}

// TestFreshSessionUsesColdArgs guards the other direction: handing an agent its resume flags with no
// prior session to resume typically makes it fail to start at all.
func TestFreshSessionUsesColdArgs(t *testing.T) {
	p := NewProvider(resumeCfg())
	sess, err := p.Create(context.Background(), t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	if err := sess.Prompt(context.Background(), "start"); err != nil {
		t.Fatal(err)
	}
	if got := firstOutput(t, sess); !strings.Contains(got, "cold") {
		t.Fatalf("a fresh session ran %q, want the cold invocation", got)
	}
}
