// Package transport carries the Oculus protocol over an encrypted channel.
//
// A MsgConn moves discrete byte messages (a WebSocket is one; tests use an
// in-memory pair). Handshake exchanges static X25519 public keys (plus a pairing
// secret the server authorizes) in the clear, derives directional session keys
// (see ../crypto), and every message after is sealed with ChaCha20-Poly1305.
// The relay, sitting on the MsgConn, only ever sees ciphertext.
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
	Secret    string `json:"secret"`
}

type serverHello struct {
	OK        bool   `json:"ok"`
	DaemonPub string `json:"daemon_pub,omitempty"`
	Error     string `json:"error,omitempty"`
}

// ErrUnauthorized is returned by ServerHandshake when authorize rejects a client.
var ErrUnauthorized = errors.New("transport: unauthorized")

// ClientHandshake authenticates to the daemon and returns an encrypted Conn.
// daemonPub is the daemon's static public key (obtained during pairing).
func ClientHandshake(mc MsgConn, kp crypto.KeyPair, daemonPub []byte, secret string) (*Conn, error) {
	if err := writeJSON(mc, clientHello{ClientPub: hex.EncodeToString(kp.Public()), Secret: secret}); err != nil {
		return nil, err
	}
	var resp serverHello
	if err := readJSON(mc, &resp); err != nil {
		return nil, err
	}
	if !resp.OK {
		return nil, fmt.Errorf("transport: handshake rejected: %s", resp.Error)
	}
	keys, err := crypto.DeriveSessionKeys(kp, daemonPub)
	if err != nil {
		return nil, err
	}
	// client sends on c2d, receives on d2c
	return newConn(mc, keys.C2D, keys.D2C)
}

// ServerHandshake accepts a client, checks authorize(clientPub, secret), and
// returns an encrypted Conn.
func ServerHandshake(mc MsgConn, kp crypto.KeyPair, authorize func(clientPub []byte, secret string) bool) (*Conn, error) {
	var hello clientHello
	if err := readJSON(mc, &hello); err != nil {
		return nil, err
	}
	clientPub, err := hex.DecodeString(hello.ClientPub)
	if err != nil {
		_ = writeJSON(mc, serverHello{OK: false, Error: "bad client_pub"})
		return nil, fmt.Errorf("transport: bad client_pub: %w", err)
	}
	if authorize == nil || !authorize(clientPub, hello.Secret) {
		_ = writeJSON(mc, serverHello{OK: false, Error: "unauthorized"})
		return nil, ErrUnauthorized
	}
	if err := writeJSON(mc, serverHello{OK: true, DaemonPub: hex.EncodeToString(kp.Public())}); err != nil {
		return nil, err
	}
	keys, err := crypto.DeriveSessionKeys(kp, clientPub)
	if err != nil {
		return nil, err
	}
	// server sends on d2c, receives on c2d
	return newConn(mc, keys.D2C, keys.C2D)
}

// Conn is an encrypted, message-oriented connection.
type Conn struct {
	mc     MsgConn
	sendMu sync.Mutex
	sealer *crypto.Sealer
	opener *crypto.Opener
}

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

// Send encrypts and writes one message.
func (c *Conn) Send(plaintext []byte) error {
	c.sendMu.Lock()
	frame, err := c.sealer.Seal(plaintext)
	c.sendMu.Unlock()
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
