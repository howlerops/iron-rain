// Command oculus-e2e boots a self-contained Oculus daemon (with a stub opencode
// backend) for cross-language end-to-end tests. It prints one line:
//
//	READY <wsURL> <daemonPubHex> <secret>
//
// then serves until killed. The Swift OculusKit live test spawns this, connects,
// and drives a full session (create -> output -> approval -> respond -> idle).
package main

import (
	"encoding/hex"
	"fmt"
	"net"
	"net/http"

	"github.com/howlerops/oculus/daemon/agent/opencode"
	"github.com/howlerops/oculus/daemon/agent/opencode/octest"
	"github.com/howlerops/oculus/daemon/crypto"
	"github.com/howlerops/oculus/daemon/hub"
	"github.com/howlerops/oculus/daemon/server"
)

func main() {
	// Stub opencode backend.
	stubLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}
	go func() { _ = http.Serve(stubLn, octest.New()) }()
	stubURL := "http://" + stubLn.Addr().String()

	// Daemon.
	kp, err := crypto.GenerateKeyPair()
	if err != nil {
		panic(err)
	}
	const secret = "e2e-secret"
	h := hub.New()
	h.Register(opencode.New(stubURL))
	srv := server.New(h, kp, func(_ []byte, s string) bool { return s == secret })

	mux := http.NewServeMux()
	mux.Handle("/ws", srv.Handler())
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		panic(err)
	}

	fmt.Printf("READY ws://%s/ws %s %s\n", ln.Addr().String(), hex.EncodeToString(kp.Public()), secret)
	_ = http.Serve(ln, mux)
}
