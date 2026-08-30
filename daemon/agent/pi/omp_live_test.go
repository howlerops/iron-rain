package pi

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/protocol"
)

// TestLive_OhMyPiSharesThePiProtocol drives a REAL omp through this adapter.
//
// oh-my-pi is a fork of pi and offers the same `--mode rpc` stdio interface, which is why it is
// registered on this adapter rather than getting a second copy of the same JSONL parser. That is a
// reasonable expectation, not a fact — and the history of this file is a list of reasonable
// expectations that were wrong on the wire (pi's fork entries key on `entryId` not `id`; its
// responses nest under `data`). So the claim is asserted against the real binary, not assumed.
//
// Skips when omp is not installed, so it costs nothing until it is.
func TestLive_OhMyPiSharesThePiProtocol(t *testing.T) {
	bin, err := exec.LookPath("omp")
	if err != nil {
		t.Skip("omp not installed")
	}

	p := NewNamed("oh-my-pi", []string{bin, "--mode", "rpc"}, "")
	if p.Name() != "oh-my-pi" {
		t.Fatalf("provider name = %q, want oh-my-pi", p.Name())
	}
	sess, err := p.Create(context.Background(), t.TempDir(), "Reply with exactly: OK")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	defer sess.Close()

	// Session ids are prefixed by PROVIDER, so an omp session never claims to be pi.
	if !strings.HasPrefix(sess.ID(), "oh-my-pi_") {
		t.Errorf("session id = %q, want an oh-my-pi_ prefix", sess.ID())
	}

	var text strings.Builder
	var failure string
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
			case protocol.SessionStatus:
				if pl.Status == protocol.StatusError {
					failure = pl.Detail
				}
				if pl.Status == protocol.StatusIdle || pl.Status == protocol.StatusError {
					done = true
				}
			}
		case <-deadline:
			t.Fatal("omp never finished its first turn — the RPC frames may differ from pi's")
		}
	}
	// Either outcome proves the adapter drives omp: a reply, or a REPORTED failure.
	//
	// What must never happen is silence. On this machine omp defaults to an ollama model that does
	// not support tools, so the turn legitimately fails — and the adapter used to render that as a
	// finished turn with an empty reply and no reason at all. A test that only accepted text would
	// have called a working adapter broken and sent me hunting the protocol.
	if strings.TrimSpace(text.String()) == "" && failure == "" {
		t.Fatal("omp finished with neither output nor a reported error — a silent empty turn is the " +
			"one result that tells the user nothing")
	}
	if failure != "" {
		t.Logf("omp reported a turn failure (expected if no usable model is configured): %s", failure)
	} else {
		t.Logf("omp replied: %q", strings.TrimSpace(text.String()))
	}
}

// oh-my-pi must NOT inherit pi's thread operations until they are verified against it.
//
// It is a pi fork, so it probably has them — "probably" is exactly the reasoning that put /tree in
// front of prime-agent's users before anyone had checked, and that emptied claude-code's manifest on
// an assumption that turned out to be false. Absent is the correct declaration for unverified.
func TestOhMyPiDoesNotInheritUnverifiedThreadOps(t *testing.T) {
	s := &session{id: "s1", p: NewNamed("oh-my-pi", []string{"omp"}, "")}
	caps := s.Capabilities()
	if caps.Provider != "oh-my-pi" {
		t.Fatalf("provider = %q, want oh-my-pi", caps.Provider)
	}
	if caps.Thread != (protocol.ThreadCaps{}) {
		t.Errorf("oh-my-pi declares thread caps %+v that nobody has verified against it", caps.Thread)
	}
	// The things it DOES get come from the shared protocol, not from pi's product.
	if len(caps.Modes) == 0 {
		t.Error("every provider gets the daemon-enforced modes")
	}
}
