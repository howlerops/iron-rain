// Command oculus-relay-e2e exercises the full remote-access path against a live relay: it stands up
// a daemon that registers as a host on the relay, then connects as a client THROUGH the relay and
// drives the encrypted handshake plus one protocol round-trip (provider.list). It proves the relay
// bridges host↔client by server_id and that the E2E session survives the hop.
//
// Usage:
//
//	oculus-relay-e2e [relayURL]   # default: the deployed HowlerOps relay
//
// Exits 0 and prints "PASS" on success; non-zero with a reason otherwise.
package main

import (
	"context"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/howlerops/oculus/daemon/agent/opencode"
	"github.com/howlerops/oculus/daemon/agent/opencode/octest"
	"github.com/howlerops/oculus/daemon/crypto"
	"github.com/howlerops/oculus/daemon/hub"
	"github.com/howlerops/oculus/daemon/protocol"
	"github.com/howlerops/oculus/daemon/relay"
	"github.com/howlerops/oculus/daemon/server"
	"github.com/howlerops/oculus/daemon/transport"
)

func main() {
	relayURL := "wss://oculus-relay-howlerops.fly.dev/ws"
	if len(os.Args) > 1 {
		relayURL = os.Args[1]
	}
	if err := run(relayURL); err != nil {
		fmt.Printf("FAIL: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("PASS")
}

func run(relayURL string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// --- Daemon (host) ---
	stubLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer stubLn.Close()
	go func() { _ = http.Serve(stubLn, octest.New()) }()

	kp, err := crypto.GenerateKeyPair()
	if err != nil {
		return err
	}
	const secret = "relay-e2e-secret"
	h := hub.New()
	h.Register(opencode.New("http://" + stubLn.Addr().String()))
	srv := server.New(h, kp, func(_ []byte, s string) bool { return s == secret })
	serverID := hex.EncodeToString(kp.Public())

	// Keep a host registration on the relay for the duration of the test.
	go func() {
		for ctx.Err() == nil {
			_ = relay.ServeHost(ctx, relayURL, serverID, srv.ServeConn)
		}
	}()

	// --- Client (app) dialing THROUGH the relay ---
	clientKP, err := crypto.GenerateKeyPair()
	if err != nil {
		return err
	}

	// The host registration races our first client dial; retry until the relay has a host for us.
	var conn *transport.Conn
	deadline := time.Now().Add(20 * time.Second)
	for {
		mc, derr := relay.DialClient(ctx, relayURL, serverID)
		if derr == nil {
			conn, derr = transport.ClientHandshake(mc, clientKP, kp.Public(), secret)
		}
		if derr == nil {
			break
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("handshake through relay never succeeded: %w", derr)
		}
		time.Sleep(500 * time.Millisecond)
	}
	fmt.Println("  handshake OK through relay")

	// --- One protocol round-trip over the bridged, encrypted channel ---
	req, err := protocol.Encode("1", protocol.TypeProviderList, nil)
	if err != nil {
		return err
	}
	if err := conn.Send(req); err != nil {
		return fmt.Errorf("send provider.list: %w", err)
	}
	raw, err := conn.Recv()
	if err != nil {
		return fmt.Errorf("recv provider.list reply: %w", err)
	}
	env, err := protocol.Decode(raw)
	if err != nil {
		return fmt.Errorf("decode reply: %w", err)
	}
	if env.Type != protocol.TypeOK {
		return fmt.Errorf("reply type = %q, want ok", env.Type)
	}
	var pl protocol.ProviderList
	if err := env.Unmarshal(&pl); err != nil {
		return fmt.Errorf("unmarshal provider list: %w", err)
	}
	fmt.Printf("  provider.list round-trip OK: %v\n", pl.Providers)
	return nil
}
