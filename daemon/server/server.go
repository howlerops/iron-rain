// Package server exposes the hub over WebSocket: it upgrades connections, runs the
// encrypted transport handshake, and hands each authenticated client to the hub.
package server

import (
	"context"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"

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
			log.Printf("server: ws accept failed: %v", err)
			return
		}
		log.Printf("server: ws accepted from %s (ua=%q)", r.RemoteAddr, r.UserAgent())
		_ = s.ServeConn(r.Context(), newWSConn(r.Context(), ws))
	})
}

// ServeConn runs the encrypted handshake then the hub loop over any MsgConn
// (a direct WebSocket, or a relay-bridged connection). Blocks until the client
// disconnects.
func (s *Server) ServeConn(ctx context.Context, mc transport.MsgConn) error {
	conn, err := transport.ServerHandshake(mc, s.kp, s.authorize)
	if err != nil {
		if isBenignHandshakeClose(err) {
			// Silent by default, because this is what SUCCESS looks like.
			//
			// The app races every route it knows — LAN direct plus each relay — and exactly one
			// wins; the rest are closed mid-handshake by design. So a single healthy connection
			// produces several of these, and only these: one real session was measured at 36 benign
			// closes, 1 successful handshake, and 0 failures.
			//
			// Printed at the same volume as a real error, that buries the line that matters. It read
			// as "a bunch of errors" when nothing was wrong — and worse, it camouflages a genuine
			// `handshake failed: unauthorized` in a wall of identical-looking noise.
			//
			// Set OCULUS_LOG_HANDSHAKE=1 to see them when actually debugging connection races.
			if os.Getenv("OCULUS_LOG_HANDSHAKE") == "1" {
				log.Printf("server: client disconnected during handshake (benign, likely a lost connect race): %v", err)
			}
		} else {
			log.Printf("server: handshake failed: %v", err)
		}
		_ = mc.Close()
		return err
	}
	log.Printf("server: client connected (handshake ok)")
	defer conn.Close()
	return s.hub.Serve(ctx, conn)
}

func isBenignHandshakeClose(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "use of closed network connection") ||
		strings.Contains(msg, "websocket: close") ||
		strings.Contains(msg, "connection reset by peer") ||
		strings.Contains(msg, "broken pipe")
}
