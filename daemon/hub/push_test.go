package hub_test

import (
	"context"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/agent"
	"github.com/howlerops/oculus/daemon/crypto"
	"github.com/howlerops/oculus/daemon/hub"
	"github.com/howlerops/oculus/daemon/protocol"
	"github.com/howlerops/oculus/daemon/push"
	"github.com/howlerops/oculus/daemon/transport"
)

type fakeNotifier struct{ calls chan pushCall }
type pushCall struct {
	token string
	n     push.Notification
}

func (f *fakeNotifier) Notify(_ context.Context, token string, n push.Notification) error {
	f.calls <- pushCall{token, n}
	return nil
}

// fakeApprovalProvider creates a session that immediately emits one approval.
type fakeApprovalProvider struct{}

func (fakeApprovalProvider) Name() string { return "fake" }
func (fakeApprovalProvider) List(context.Context) ([]protocol.Session, error) {
	return nil, nil
}
func (fakeApprovalProvider) Create(context.Context, string, string) (agent.Session, error) {
	s := &fakeApprovalSession{events: make(chan agent.Event, 2)}
	s.events <- agent.Event{Type: protocol.TypeApprovalRequest, Payload: protocol.ApprovalRequest{
		ApprovalID: "ap_fake", SessionID: "fake_sess", Tool: "bash",
	}}
	close(s.events)
	return s, nil
}

type fakeApprovalSession struct{ events chan agent.Event }

func (s *fakeApprovalSession) ID() string                           { return "fake_sess" }
func (s *fakeApprovalSession) Provider() string                     { return "fake" }
func (s *fakeApprovalSession) Events() <-chan agent.Event           { return s.events }
func (s *fakeApprovalSession) Prompt(context.Context, string) error { return nil }
func (s *fakeApprovalSession) Respond(context.Context, string, string) error {
	return nil
}
func (s *fakeApprovalSession) Stop(context.Context) error { return nil }
func (s *fakeApprovalSession) Close() error               { return nil }

// TestDeviceRegisterThenPush proves a client can register its device token over
// the protocol (device.register) and then receive approval pushes on it.
func TestDeviceRegisterThenPush(t *testing.T) {
	fn := &fakeNotifier{calls: make(chan pushCall, 4)}
	h := hub.New()
	h.Register(fakeApprovalProvider{})
	h.SetNotifier(fn)

	clientKP, _ := crypto.GenerateKeyPair()
	daemonKP, _ := crypto.GenerateKeyPair()
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
	defer client.Close()

	// register a device token, wait for the ok, then create a session that approves.
	reg, _ := protocol.Encode("r1", protocol.TypeDeviceRegister, protocol.DeviceRegister{Token: "tok-registered"})
	if err := client.Send(reg); err != nil {
		t.Fatal(err)
	}
	waitOK(t, client, "r1")

	create, _ := protocol.Encode("c1", protocol.TypeSessionCreate, protocol.SessionCreate{Provider: "fake"})
	if err := client.Send(create); err != nil {
		t.Fatal(err)
	}

	select {
	case call := <-fn.calls:
		if call.token != "tok-registered" {
			t.Errorf("token = %q, want the registered one", call.token)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout: registered device got no push")
	}
}

func waitOK(t *testing.T, client *transport.Conn, id string) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatalf("timeout waiting for ok %q", id)
		default:
		}
		b, err := client.Recv()
		if err != nil {
			t.Fatal(err)
		}
		env, _ := protocol.Decode(b)
		if env.Type == protocol.TypeOK && env.ID == id {
			return
		}
	}
}

// TestApprovalTriggersPush proves that an approval request forwarded to a client
// also fires an actionable push to every registered device.
func TestApprovalTriggersPush(t *testing.T) {
	fn := &fakeNotifier{calls: make(chan pushCall, 4)}
	h := hub.New()
	h.Register(fakeApprovalProvider{})
	h.SetNotifier(fn)
	h.RegisterDevice("device-token-1")

	clientKP, _ := crypto.GenerateKeyPair()
	daemonKP, _ := crypto.GenerateKeyPair()
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
	defer client.Close()

	raw, _ := protocol.Encode("c1", protocol.TypeSessionCreate, protocol.SessionCreate{Provider: "fake"})
	if err := client.Send(raw); err != nil {
		t.Fatal(err)
	}

	select {
	case call := <-fn.calls:
		if call.token != "device-token-1" {
			t.Errorf("token = %q", call.token)
		}
		if call.n.Category != "APPROVAL" {
			t.Errorf("category = %q", call.n.Category)
		}
		if call.n.Custom["approval_id"] != "ap_fake" {
			t.Errorf("approval_id = %v", call.n.Custom["approval_id"])
		}
		if call.n.Title != "Approve bash" {
			t.Errorf("title = %q", call.n.Title)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout: approval did not trigger a push")
	}
}
