// Package wsmsg adapts a WebSocket to transport.MsgConn and provides Dial, shared
// by the daemon's direct server, the relay, and tests.
package wsmsg

import (
	"context"

	"github.com/coder/websocket"
	"github.com/howlerops/oculus/daemon/transport"
)

// Conn is a transport.MsgConn over a WebSocket (one binary message per protocol message).
type Conn struct {
	ws  *websocket.Conn
	ctx context.Context
}

var _ transport.MsgConn = (*Conn)(nil)

// New wraps an accepted/dialed websocket.
func New(ctx context.Context, ws *websocket.Conn) *Conn {
	ws.SetReadLimit(8 * 1024 * 1024)
	return &Conn{ws: ws, ctx: ctx}
}

func (c *Conn) WriteMsg(b []byte) error { return c.ws.Write(c.ctx, websocket.MessageBinary, b) }

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
