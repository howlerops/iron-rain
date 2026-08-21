package claudecode

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/protocol"
)

// TestLive_ClaudeCodeProducesOutput drives the REAL sidecar and asserts the turn streams text.
//
// It exists because of what the pi adapter taught us: a turn that opens, runs and closes idle is NOT
// evidence that the agent said anything. pi satisfied exactly that state sequence while every
// assistant delta was being silently dropped on a decode error, and both the unit tests and the
// turn-state smoke test passed throughout. The only check that catches this is asserting on the
// TEXT, against a real agent.
//
// Skips unless the sidecar and its prerequisites are present, so CI without them stays green.
func TestLive_ClaudeCodeProducesOutput(t *testing.T) {
	sidecar := findSidecar(t)
	if sidecar == "" {
		t.Skip("claude-code sidecar not available")
	}
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude CLI not installed")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}

	p := New([]string{node, sidecar})
	sess, err := p.Create(context.Background(), t.TempDir(), "Reply with exactly: OK — nothing else.")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer sess.Close()

	var text strings.Builder
	done := false
	deadline := time.After(120 * time.Second)
	for !done {
		select {
		case ev, ok := <-sess.Events():
			if !ok {
				done = true
				break
			}
			switch pl := ev.Payload.(type) {
			case protocol.OutputDelta:
				text.WriteString(pl.Text)
			case protocol.SessionMessage:
				if pl.Role == "assistant" {
					text.WriteString(pl.Text)
				}
			case protocol.SessionStatus:
				if pl.Status == protocol.StatusIdle || pl.Status == protocol.StatusError {
					if pl.Status == protocol.StatusError {
						t.Fatalf("turn errored: %s", pl.Detail)
					}
					done = true
				}
			}
		case <-deadline:
			t.Fatalf("no completed turn; text so far = %q", text.String())
		}
	}

	// The wording is the model's business. That ANY assistant text arrived is the contract, and an
	// empty string here is the exact failure this test exists to catch: a turn that reports success
	// while the user sees nothing.
	if strings.TrimSpace(text.String()) == "" {
		t.Fatal("turn completed but streamed no assistant text")
	}
}

// findSidecar locates sidecar.mjs — the copy the daemon materializes into ~/.oculus, else the one
// vendored in this repo.
func findSidecar(t *testing.T) string {
	t.Helper()
	if home, err := os.UserHomeDir(); err == nil {
		p := filepath.Join(home, ".oculus", "claude-sidecar", "sidecar.mjs")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	// Test working directory is the package dir.
	if p, err := filepath.Abs("sidecar/sidecar.mjs"); err == nil {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}
