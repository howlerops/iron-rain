package server

import (
	"context"
	"log"
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

// MaxFrameBytes is the largest protocol frame either end will handle. The client sets the identical
// limit (OculusClient.maxFrameBytes); a frame larger than this on either side kills the connection,
// so the two numbers must stay equal.
const MaxFrameBytes = 8 * 1024 * 1024

func newWSConn(ctx context.Context, ws *websocket.Conn) *wsConn {
	ws.SetReadLimit(MaxFrameBytes) // agent output frames can be large
	return &wsConn{ws: ws, ctx: ctx}
}

func (c *wsConn) WriteMsg(b []byte) error {
	// An oversized frame is fatal at the other end, so say so HERE rather than letting the peer
	// drop with no explanation. Nothing capped what the daemon sends, and the client's receive limit
	// used to be the 1 MiB URLSession default — so a large tool result disconnected the app and
	// looked like a network problem. The frame is still attempted (the peer may be a newer build),
	// but the log now names the cause.
	if len(b) > MaxFrameBytes {
		log.Printf("server: outbound frame is %d bytes, over the %d-byte limit — the peer will drop this connection",
			len(b), MaxFrameBytes)
	}

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
