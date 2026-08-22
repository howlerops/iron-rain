package pi

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestLive_PrimeAgentSharesThePiProtocol drives a REAL prime-agent through this adapter.
//
// prime-agent (Prime Intellect) is registered as its own provider but reuses this code, because its
// `--mode rpc` emits the identical event vocabulary — agent_start, turn_start, message_update
// carrying an assistantMessageEvent text_delta, message_end with usage, agent_end — and accepts the
// same {"type":"prompt","message":...} on stdin.
//
// This test is the justification for that reuse. If the two protocols ever diverge, sharing an
// adapter becomes a liability rather than a saving, and the failure would otherwise show up as a
// provider that accepts prompts and says nothing — exactly how pi's own breakage presented.
func TestLive_PrimeAgentSharesThePiProtocol(t *testing.T) {
	bin, err := exec.LookPath("prime-agent")
	if err != nil {
		t.Skip("prime-agent not installed")
	}
	p := NewNamed("prime-agent", []string{bin, "--mode", "rpc"}, "")
	if p.Name() != "prime-agent" {
		t.Fatalf("provider name = %q, want prime-agent — it must not masquerade as pi in the picker", p.Name())
	}
	sess, err := p.Create(context.Background(), t.TempDir(), "Reply with exactly: OK")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer sess.Close()

	text := collectText(t, sess, 120*time.Second)
	if strings.TrimSpace(text) == "" {
		t.Fatal("turn completed but streamed no assistant text — the protocols have diverged")
	}
	t.Logf("prime-agent replied: %q", strings.TrimSpace(text))
}
