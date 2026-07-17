package server

import (
	"context"
	"time"

	"github.com/coder/websocket"
	"github.com/howlerops/oculus/daemon/transport"
)

// writeTimeout bounds a single WebSocket write. A dead-but-not-reset mobile peer
// can let its TCP send buffer fill, which would otherwise park the writing
// goroutine forever on ws.Write; the deadline drops such a client instead.
const writeTimeout = 30 * time.Second

// wsConn adapts a WebSocket to transport.MsgConn (each WS binary message is one
// protocol/handshake message).
//
// ctx is the connection's lifetime scope (from the HTTP request). It is used as
// the parent for per-message deadlines below rather than being passed raw to
// ws.Write, so a stalled peer can't wedge the writer indefinitely.
type wsConn struct {
	ws  *websocket.Conn
	ctx context.Context
}

var _ transport.MsgConn = (*wsConn)(nil)

func newWSConn(ctx context.Context, ws *websocket.Conn) *wsConn {
	ws.SetReadLimit(8 * 1024 * 1024) // agent output frames can be large
	return &wsConn{ws: ws, ctx: ctx}
}

func (c *wsConn) WriteMsg(b []byte) error {
	ctx, cancel := context.WithTimeout(c.ctx, writeTimeout)
	defer cancel()
	err := c.ws.Write(ctx, websocket.MessageBinary, b)
	if err != nil && c.ctx.Err() == nil {
		// The write stalled (or otherwise failed) while the connection itself is still
		// live: drop the client so a filled send buffer can't block the goroutine
		// broadcasting to it. CloseNow (not a graceful Close handshake, which would
		// itself block on the same stalled peer) tears down the TCP connection now.
		_ = c.ws.CloseNow()
	}
	return err
}

func (c *wsConn) ReadMsg() ([]byte, error) {
	_, data, err := c.ws.Read(c.ctx)
	return data, err
}

func (c *wsConn) Close() error {
	return c.ws.Close(websocket.StatusNormalClosure, "")
}
