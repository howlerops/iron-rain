// Command oculusd is the Oculus daemon.
//
// It drives coding agents (claude-code, opencode) on the host and exposes an
// end-to-end-encrypted WebSocket protocol to the Oculus apps (macOS + iOS),
// directly on the LAN or via a stateless relay for access from anywhere.
//
// Status: scaffold. The P0 spike implements the E2EE handshake + one streamed
// session + one approval round-trip. See ../docs/plan-native-ade.md and
// ../skills/oculus-protocol.
package main

import (
	"flag"
	"fmt"
	"os"
)

const version = "0.0.0-dev"

func main() {
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println("oculusd", version)
		return
	}

	fmt.Fprintln(os.Stderr, "oculusd", version, "— scaffold; not yet serving. (P0: handshake + stream + approval)")
}
