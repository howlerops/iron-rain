package server

import (
	"context"

	"github.com/coder/websocket"
	"github.com/howlerops/oculus/daemon/crypto"
	"github.com/howlerops/oculus/daemon/transport"
)

// Dial connects to an Oculus daemon over WebSocket and completes the encrypted
// handshake, returning a ready transport.Conn. Used by the Go CLI client and tests;
// the Swift app performs the equivalent handshake with CryptoKit.
func Dial(ctx context.Context, url string, kp crypto.KeyPair, daemonPub []byte, secret string) (*transport.Conn, error) {
	ws, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		return nil, err
	}
	return transport.ClientHandshake(newWSConn(ctx, ws), kp, daemonPub, secret)
}
