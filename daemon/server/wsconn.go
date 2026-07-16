package server

import (
	"context"

	"github.com/coder/websocket"
	"github.com/howlerops/oculus/daemon/transport"
)

// wsConn adapts a WebSocket to transport.MsgConn (each WS binary message is one
// protocol/handshake message).
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
	return c.ws.Write(c.ctx, websocket.MessageBinary, b)
}

func (c *wsConn) ReadMsg() ([]byte, error) {
	_, data, err := c.ws.Read(c.ctx)
	return data, err
}

func (c *wsConn) Close() error {
	return c.ws.Close(websocket.StatusNormalClosure, "")
}
