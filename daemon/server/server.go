// Package server exposes the hub over WebSocket: it upgrades connections, runs the
// encrypted transport handshake, and hands each authenticated client to the hub.
package server

import (
	"context"
	"net/http"

	"github.com/coder/websocket"
	"github.com/howlerops/oculus/daemon/crypto"
	"github.com/howlerops/oculus/daemon/hub"
	"github.com/howlerops/oculus/daemon/transport"
)

// Authorizer decides whether a client (by its public key + presented secret) may connect.
type Authorizer func(clientPub []byte, secret string) bool

// Server serves a hub over WebSocket.
type Server struct {
	hub       *hub.Hub
	kp        crypto.KeyPair
	authorize Authorizer
}

// New builds a Server. kp is the daemon's static key pair; clients pin its public key.
func New(h *hub.Hub, kp crypto.KeyPair, authorize Authorizer) *Server {
	return &Server{hub: h, kp: kp, authorize: authorize}
}

// PublicKey returns the daemon's static public key (shared during pairing).
func (s *Server) PublicKey() []byte { return s.kp.Public() }

// Handler returns the HTTP handler that accepts client WebSocket connections.
func (s *Server) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			// The security boundary is the E2EE handshake + pairing secret, not the
			// HTTP origin; allow cross-origin so the app/relay can connect. (TODO:
			// tighten if the daemon is ever exposed to a browser context.)
			InsecureSkipVerify: true,
		})
		if err != nil {
			return
		}
		_ = s.ServeConn(r.Context(), newWSConn(r.Context(), ws))
	})
}

// ServeConn runs the encrypted handshake then the hub loop over any MsgConn
// (a direct WebSocket, or a relay-bridged connection). Blocks until the client
// disconnects.
func (s *Server) ServeConn(ctx context.Context, mc transport.MsgConn) error {
	conn, err := transport.ServerHandshake(mc, s.kp, s.authorize)
	if err != nil {
		_ = mc.Close()
		return err
	}
	defer conn.Close()
	return s.hub.Serve(ctx, conn)
}
