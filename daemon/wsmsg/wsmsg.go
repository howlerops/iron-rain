// Package wsmsg adapts a WebSocket to transport.MsgConn and provides Dial, shared
// by the daemon's direct server, the relay, and tests.
package wsmsg

import (
	"context"
	"time"

	"github.com/coder/websocket"
	"github.com/howlerops/oculus/daemon/transport"
)

// defaultWriteTimeout bounds a single WebSocket write so a stalled peer (filled TCP send
// buffer on a half-open connection) can't park the writing goroutine forever.
const defaultWriteTimeout = 30 * time.Second

// Conn is a transport.MsgConn over a WebSocket (one binary message per protocol message).
//
// ctx is the connection's lifetime scope, captured at New() time. The
// transport.MsgConn interface has no per-call context, so ctx is the parent for
// the per-message write deadline below rather than being handed raw to ws.Write;
// that keeps a slow write from wedging the writer indefinitely. Reads still use
// the lifetime scope: cancelling it unblocks a stalled reader by closing the
// whole connection.
type Conn struct {
	ws           *websocket.Conn
	ctx          context.Context
	writeTimeout time.Duration // per-message write bound (field so tests can shorten it)
}

var _ transport.MsgConn = (*Conn)(nil)

// New wraps an accepted/dialed websocket.
func New(ctx context.Context, ws *websocket.Conn) *Conn {
	ws.SetReadLimit(8 * 1024 * 1024)
	return &Conn{ws: ws, ctx: ctx, writeTimeout: defaultWriteTimeout}
}

func (c *Conn) WriteMsg(b []byte) error {
	ctx, cancel := context.WithTimeout(c.ctx, c.writeTimeout)
	defer cancel()
	err := c.ws.Write(ctx, websocket.MessageBinary, b)
	if err != nil && c.ctx.Err() == nil {
		// The write stalled (or failed) while the connection is still live: drop the
		// peer so it can't block the sender. CloseNow (not Close) tears down the TCP
		// connection immediately — a graceful close handshake would itself block on the
		// same stalled peer, defeating the timeout.
		_ = c.ws.CloseNow()
	}
	return err
}

func (c *Conn) ReadMsg() ([]byte, error) {
	_, data, err := c.ws.Read(c.ctx)
	return data, err
}

func (c *Conn) Close() error { return c.ws.Close(websocket.StatusNormalClosure, "") }

// Dial connects to a WebSocket URL and returns a Conn.
func Dial(ctx context.Context, url string) (*Conn, error) {
	ws, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		return nil, err
	}
	return New(ctx, ws), nil
}
