package hub_test

import (
	"context"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/agent"
	"github.com/howlerops/oculus/daemon/crypto"
	"github.com/howlerops/oculus/daemon/hub"
	"github.com/howlerops/oculus/daemon/protocol"
	"github.com/howlerops/oculus/daemon/transport"
)

// --- a controllable one-session provider: emits output + an approval, waits for a
// response, then goes idle. Create returns the SAME session (one shared session). ---

type sharedProvider struct{ sess *sharedSession }

func (p *sharedProvider) Name() string                                    { return "fake" }
func (p *sharedProvider) List(context.Context) ([]protocol.Session, error) { return nil, nil }
func (p *sharedProvider) Create(context.Context, string, string) (agent.Session, error) {
	go p.sess.run()
	return p.sess, nil
}

type sharedSession struct {
	events    chan agent.Event
	responded chan string
}

func newSharedSession() *sharedSession {
	return &sharedSession{events: make(chan agent.Event, 8), responded: make(chan string, 1)}
}
func (s *sharedSession) ID() string                          { return "shared_sess" }
func (s *sharedSession) Provider() string                    { return "fake" }
func (s *sharedSession) Events() <-chan agent.Event          { return s.events }
func (s *sharedSession) Prompt(context.Context, string) error { return nil }
func (s *sharedSession) Respond(_ context.Context, _, decision string) error {
	s.responded <- decision
	return nil
}
func (s *sharedSession) Stop(context.Context) error { return nil }
func (s *sharedSession) Close() error               { return nil }
func (s *sharedSession) run() {
	s.events <- agent.Event{Type: protocol.TypeOutputDelta, Payload: protocol.OutputDelta{SessionID: "shared_sess", Text: "hello"}}
	s.events <- agent.Event{Type: protocol.TypeApprovalRequest, Payload: protocol.ApprovalRequest{ApprovalID: "ap_shared", SessionID: "shared_sess", Tool: "bash"}}
	<-s.responded
	s.events <- agent.Event{Type: protocol.TypeSessionStatus, Payload: protocol.SessionStatus{SessionID: "shared_sess", Status: protocol.StatusIdle}}
	close(s.events)
}

func connectClient(t *testing.T, h *hub.Hub, daemonKP crypto.KeyPair) *transport.Conn {
	t.Helper()
	clientKP, _ := crypto.GenerateKeyPair()
	cPipe, sPipe := newPipePair()
	go func() {
		conn, err := transport.ServerHandshake(sPipe, daemonKP, func([]byte, string) bool { return true })
		if err != nil {
			return
		}
		_ = h.Serve(context.Background(), conn)
	}()
	client, err := transport.ClientHandshake(cPipe, clientKP, daemonKP.Public(), "secret")
	if err != nil {
		t.Fatalf("client handshake: %v", err)
	}
	return client
}

// clientReader wraps a connection with a single background reader, so multiple
// waitFor calls on the same client don't race for messages.
type clientReader struct {
	conn *transport.Conn
	ch   chan protocol.Envelope
}

func newReader(c *transport.Conn) *clientReader {
	r := &clientReader{conn: c, ch: make(chan protocol.Envelope, 64)}
	go func() {
		for {
			b, err := c.Recv()
			if err != nil {
				close(r.ch)
				return
			}
			env, _ := protocol.Decode(b)
			r.ch <- env
		}
	}()
	return r
}

func (r *clientReader) waitFor(t *testing.T, what string, pred func(protocol.Envelope) bool) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case env, ok := <-r.ch:
			if !ok {
				t.Fatalf("connection closed before %s", what)
			}
			if pred(env) {
				return
			}
		case <-deadline:
			t.Fatalf("timeout waiting for %s", what)
		}
	}
}

// TestMultiClient_SharedSession: client A creates a session; client B SUBSCRIBES to
// it (no duplicate provider subscription). B must see the session's events (replayed
// + live), and when A answers the approval, BOTH get approval.resolved.
func TestMultiClient_SharedSession(t *testing.T) {
	h := hub.New()
	h.Register(&sharedProvider{sess: newSharedSession()})

	daemonKP, _ := crypto.GenerateKeyPair()
	clientA := connectClient(t, h, daemonKP)
	clientB := connectClient(t, h, daemonKP)
	defer clientA.Close()
	defer clientB.Close()
	readerA := newReader(clientA)
	readerB := newReader(clientB)

	// A creates the session.
	raw, _ := protocol.Encode("a1", protocol.TypeSessionCreate, protocol.SessionCreate{Provider: "fake"})
	if err := clientA.Send(raw); err != nil {
		t.Fatal(err)
	}
	readerA.waitFor(t, "A create ok", func(e protocol.Envelope) bool { return e.Type == protocol.TypeOK && e.ID == "a1" })

	// B subscribes to the same session (no duplicate provider subscription).
	sub, _ := protocol.Encode("b1", protocol.TypeSessionSubscribe, protocol.SessionRef{SessionID: "shared_sess"})
	if err := clientB.Send(sub); err != nil {
		t.Fatal(err)
	}

	// B must receive the session's approval request via transcript replay (it was
	// emitted before B subscribed).
	readerB.waitFor(t, "B replayed approval", func(e protocol.Envelope) bool { return e.Type == protocol.TypeApprovalRequest })

	// A answers the approval.
	resp, _ := protocol.Encode("a2", protocol.TypeApprovalRespond, protocol.ApprovalRespond{ApprovalID: "ap_shared", Decision: protocol.DecisionAllow})
	if err := clientA.Send(resp); err != nil {
		t.Fatal(err)
	}

	// BOTH clients get approval.resolved — resolving on one clears it everywhere.
	readerA.waitFor(t, "A approval.resolved", func(e protocol.Envelope) bool { return e.Type == protocol.TypeApprovalResolved })
	readerB.waitFor(t, "B approval.resolved", func(e protocol.Envelope) bool { return e.Type == protocol.TypeApprovalResolved })
}
