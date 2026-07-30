// Package transport carries the Oculus protocol over an encrypted channel.
//
// A MsgConn moves discrete byte messages (a WebSocket is one; tests use an
// in-memory pair). Handshake announces the client's static X25519 public key in
// the clear (a public key — safe), derives directional session keys from
// static-static ECDH (see ../crypto), then the client proves the pairing secret
// by sending it *encrypted* as the first sealed frame. Every message after is
// sealed with ChaCha20-Poly1305. The relay, sitting on the MsgConn, only ever
// sees ciphertext — the pairing secret never transits in the clear, and a passive
// relay cannot verify secret guesses without a private key (ECDH hardness).
package transport

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/howlerops/oculus/daemon/crypto"
)

// MsgConn is a bidirectional stream of discrete byte messages.
type MsgConn interface {
	WriteMsg([]byte) error
	ReadMsg() ([]byte, error)
	Close() error
}

type clientHello struct {
	ClientPub string `json:"client_pub"`
}

type serverHello struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// ErrUnauthorized is returned by ServerHandshake when authorize rejects a client.
var ErrUnauthorized = errors.New("transport: unauthorized")

// ClientHandshake authenticates to the daemon and returns an encrypted Conn.
// daemonPub is the daemon's static public key (obtained during pairing). The
// pairing secret is sent encrypted (never in the clear over the relay).
func ClientHandshake(mc MsgConn, kp crypto.KeyPair, daemonPub []byte, secret string) (*Conn, error) {
	// 1. announce our static public key (a public key — safe in the clear).
	if err := writeJSON(mc, clientHello{ClientPub: hex.EncodeToString(kp.Public())}); err != nil {
		return nil, err
	}
	// 2. derive the channel from static-static ECDH (no secret needed).
	keys, err := crypto.DeriveSessionKeys(kp, daemonPub)
	if err != nil {
		return nil, err
	}
	conn, err := newConn(mc, keys.C2D, keys.D2C) // client sends c2d, receives d2c
	if err != nil {
		return nil, err
	}
	// 3. prove the pairing secret by sending it ENCRYPTED (relay sees ciphertext).
	if err := conn.Send([]byte(secret)); err != nil {
		return nil, err
	}
	// 4. read the server's encrypted verdict.
	raw, err := conn.Recv()
	if err != nil {
		return nil, err
	}
	var resp serverHello
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, err
	}
	if !resp.OK {
		return nil, fmt.Errorf("transport: handshake rejected: %s", resp.Error)
	}
	return conn, nil
}

// ServerHandshake accepts a client, checks authorize(clientPub, secret) against
// the encrypted pairing proof, and returns an encrypted Conn.
func ServerHandshake(mc MsgConn, kp crypto.KeyPair, authorize func(clientPub []byte, secret string) bool) (*Conn, error) {
	var hello clientHello
	if err := readJSON(mc, &hello); err != nil {
		return nil, err
	}
	clientPub, err := hex.DecodeString(hello.ClientPub)
	if err != nil {
		return nil, fmt.Errorf("transport: bad client_pub: %w", err)
	}
	keys, err := crypto.DeriveSessionKeys(kp, clientPub)
	if err != nil {
		return nil, err
	}
	conn, err := newConn(mc, keys.D2C, keys.C2D) // server sends d2c, receives c2d
	if conn != nil {
		conn.peerPub = clientPub
	}
	if err != nil {
		return nil, err
	}
	// read the encrypted pairing secret (first sealed frame from the client).
	raw, err := conn.Recv()
	if err != nil {
		return nil, err
	}
	if authorize == nil || !authorize(clientPub, string(raw)) {
		_ = sendHello(conn, serverHello{OK: false, Error: "unauthorized"})
		return nil, ErrUnauthorized
	}
	if err := sendHello(conn, serverHello{OK: true}); err != nil {
		return nil, err
	}
	return conn, nil
}

func sendHello(conn *Conn, h serverHello) error {
	b, err := json.Marshal(h)
	if err != nil {
		return err
	}
	return conn.Send(b)
}

// Conn is an encrypted, message-oriented connection.
type Conn struct {
	mc     MsgConn
	sendMu sync.Mutex
	sealer *crypto.Sealer
	opener *crypto.Opener
	// peerPub is the client's public key from the handshake. It identifies WHICH paired device this
	// connection is, which is what lets an invited guest be given a different role from the owner.
	peerPub []byte
}

// PeerPublicKey returns the client's handshake public key (nil for a client-side Conn).
func (c *Conn) PeerPublicKey() []byte { return append([]byte(nil), c.peerPub...) }

func newConn(mc MsgConn, sendKey, recvKey []byte) (*Conn, error) {
	sealer, err := crypto.NewSealer(sendKey)
	if err != nil {
		return nil, err
	}
	opener, err := crypto.NewOpener(recvKey)
	if err != nil {
		return nil, err
	}
	return &Conn{mc: mc, sealer: sealer, opener: opener}, nil
}

// Send encrypts and writes one message. The lock is held across both the seal and the
// write so concurrent senders can't interleave frames on the wire (which would reorder
// the ordered event stream a client replays).
func (c *Conn) Send(plaintext []byte) error {
	c.sendMu.Lock()
	defer c.sendMu.Unlock()
	frame, err := c.sealer.Seal(plaintext)
	if err != nil {
		return err
	}
	return c.mc.WriteMsg(frame)
}

// Recv reads and decrypts one message.
func (c *Conn) Recv() ([]byte, error) {
	frame, err := c.mc.ReadMsg()
	if err != nil {
		return nil, err
	}
	return c.opener.Open(frame)
}

// Close closes the underlying connection.
func (c *Conn) Close() error { return c.mc.Close() }

func writeJSON(mc MsgConn, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return mc.WriteMsg(b)
}

func readJSON(mc MsgConn, v any) error {
	b, err := mc.ReadMsg()
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}
