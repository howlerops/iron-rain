package transport

import (
	"bytes"
	"testing"

	"github.com/howlerops/oculus/daemon/crypto"
)

// pipeConn is an in-memory MsgConn for tests (discrete messages, like a WebSocket).
type pipeConn struct {
	in     chan []byte
	out    chan []byte
	closed chan struct{}
}

func newPipePair() (*pipeConn, *pipeConn) {
	a2b := make(chan []byte, 16)
	b2a := make(chan []byte, 16)
	a := &pipeConn{in: b2a, out: a2b, closed: make(chan struct{})}
	b := &pipeConn{in: a2b, out: b2a, closed: make(chan struct{})}
	return a, b
}

func (p *pipeConn) WriteMsg(b []byte) error {
	cp := append([]byte(nil), b...)
	select {
	case p.out <- cp:
		return nil
	case <-p.closed:
		return errClosedTest
	}
}
func (p *pipeConn) ReadMsg() ([]byte, error) {
	select {
	case b := <-p.in:
		return b, nil
	case <-p.closed:
		return nil, errClosedTest
	}
}
func (p *pipeConn) Close() error {
	select {
	case <-p.closed:
	default:
		close(p.closed)
	}
	return nil
}

var errClosedTest = &closedErr{}

type closedErr struct{}

func (*closedErr) Error() string { return "pipe closed" }

const testSecret = "pair-secret"

func TestHandshakeAndExchange(t *testing.T) {
	clientKP, _ := crypto.GenerateKeyPair()
	daemonKP, _ := crypto.GenerateKeyPair()
	daemonPub := daemonKP.Public()

	cConn, sConn := newPipePair()

	type res struct {
		conn *Conn
		err  error
	}
	sCh := make(chan res, 1)
	go func() {
		conn, err := ServerHandshake(sConn, daemonKP, func(clientPub []byte, secret string) bool {
			return secret == testSecret // accept any client presenting the right secret
		})
		sCh <- res{conn, err}
	}()

	client, err := ClientHandshake(cConn, clientKP, daemonPub, testSecret)
	if err != nil {
		t.Fatalf("client handshake: %v", err)
	}
	sr := <-sCh
	if sr.err != nil {
		t.Fatalf("server handshake: %v", sr.err)
	}
	server := sr.conn

	// client -> server
	if err := client.Send([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	got, err := server.Recv()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, []byte("ping")) {
		t.Fatalf("server recv = %q, want ping", got)
	}

	// server -> client
	if err := server.Send([]byte("pong")); err != nil {
		t.Fatal(err)
	}
	got, err = client.Recv()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, []byte("pong")) {
		t.Fatalf("client recv = %q, want pong", got)
	}
}

func TestHandshake_AuthFailureRejected(t *testing.T) {
	clientKP, _ := crypto.GenerateKeyPair()
	daemonKP, _ := crypto.GenerateKeyPair()

	cConn, sConn := newPipePair()
	go func() {
		_, _ = ServerHandshake(sConn, daemonKP, func(clientPub []byte, secret string) bool {
			return false // reject everyone
		})
	}()

	if _, err := ClientHandshake(cConn, clientKP, daemonKP.Public(), "wrong"); err == nil {
		t.Fatal("client handshake must fail when the server rejects auth")
	}
}

func TestWire_IsEncrypted(t *testing.T) {
	clientKP, _ := crypto.GenerateKeyPair()
	daemonKP, _ := crypto.GenerateKeyPair()
	cConn, sConn := newPipePair()

	// tap the client's outgoing messages
	tap := &tapConn{pipeConn: cConn}

	go func() {
		_, _ = ServerHandshake(sConn, daemonKP, func([]byte, string) bool { return true })
	}()
	client, err := ClientHandshake(tap, clientKP, daemonKP.Public(), testSecret)
	if err != nil {
		t.Fatal(err)
	}
	secret := []byte("TOP-SECRET-PLAINTEXT")
	if err := client.Send(secret); err != nil {
		t.Fatal(err)
	}
	for _, msg := range tap.sent {
		if bytes.Contains(msg, secret) {
			t.Fatal("plaintext leaked on the wire — Send must encrypt")
		}
	}
}

// TestWire_PairingSecretNotInClear proves the pairing secret never transits the
// wire in the clear during the handshake (it's sent as a sealed frame).
func TestWire_PairingSecretNotInClear(t *testing.T) {
	clientKP, _ := crypto.GenerateKeyPair()
	daemonKP, _ := crypto.GenerateKeyPair()
	cConn, sConn := newPipePair()

	tap := &tapConn{pipeConn: cConn}
	done := make(chan struct{})
	go func() {
		_, _ = ServerHandshake(sConn, daemonKP, func(_ []byte, secret string) bool {
			return secret == testSecret
		})
		close(done)
	}()
	client, err := ClientHandshake(tap, clientKP, daemonKP.Public(), testSecret)
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	<-done
	_ = client

	for i, msg := range tap.sent {
		if bytes.Contains(msg, []byte(testSecret)) {
			t.Fatalf("pairing secret leaked in cleartext in wire message %d: %q", i, msg)
		}
	}
	if len(tap.sent) < 2 {
		t.Fatalf("expected at least client_pub + sealed secret on the wire, got %d messages", len(tap.sent))
	}
}

type tapConn struct {
	*pipeConn
	sent [][]byte
}

func (t *tapConn) WriteMsg(b []byte) error {
	t.sent = append(t.sent, append([]byte(nil), b...))
	return t.pipeConn.WriteMsg(b)
}
