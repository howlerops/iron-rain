// Command oculusd is the Oculus daemon.
//
// It drives coding agents (opencode today; claude-code next) on the host and
// serves an end-to-end-encrypted WebSocket protocol to the Oculus apps.
//
//	oculusd serve --opencode http://127.0.0.1:4096 [--addr 127.0.0.1:6000] [--secret S]
//
// See ../docs/plan-native-ade.md and ../skills/oculus-daemon.
package main

import (
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/howlerops/oculus/daemon/agent/opencode"
	"github.com/howlerops/oculus/daemon/crypto"
	"github.com/howlerops/oculus/daemon/hub"
	"github.com/howlerops/oculus/daemon/server"
)

const version = "0.0.0-dev"

func main() {
	if len(os.Args) >= 2 && os.Args[1] == "serve" {
		if err := serve(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "oculusd:", err)
			os.Exit(1)
		}
		return
	}

	fs := flag.NewFlagSet("oculusd", flag.ExitOnError)
	showVersion := fs.Bool("version", false, "print version and exit")
	_ = fs.Parse(os.Args[1:])
	if *showVersion {
		fmt.Println("oculusd", version)
		return
	}
	fmt.Fprintln(os.Stderr, "usage: oculusd serve --opencode URL [--addr 127.0.0.1:6000] [--secret S]")
}

func serve(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:6000", "listen address")
	opencodeURL := fs.String("opencode", "", "URL of a running `opencode serve` (e.g. http://127.0.0.1:4096)")
	secret := fs.String("secret", "", "pairing secret clients must present (default: generated)")
	keyPath := fs.String("key", defaultKeyPath(), "path to the daemon private key")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *opencodeURL == "" {
		return fmt.Errorf("--opencode URL required for v0 (attach to a running `opencode serve`)")
	}

	kp, err := loadOrCreateKey(*keyPath)
	if err != nil {
		return err
	}
	sec := *secret
	if sec == "" {
		sec = randomHex(16)
	}

	h := hub.New()
	h.Register(opencode.New(*opencodeURL))

	srv := server.New(h, kp, func(_ []byte, presented string) bool {
		return presented == sec
	})

	mux := http.NewServeMux()
	mux.Handle("/ws", srv.Handler())
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })

	fmt.Printf("oculusd %s\n", version)
	fmt.Printf("  listening:      ws://%s/ws\n", *addr)
	fmt.Printf("  daemon pubkey:  %s\n", hex.EncodeToString(kp.Public()))
	fmt.Printf("  pairing secret: %s\n", sec)
	fmt.Printf("  provider:       opencode -> %s\n", *opencodeURL)
	return http.ListenAndServe(*addr, mux)
}

func defaultKeyPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "oculusd.key"
	}
	return filepath.Join(home, ".oculus", "daemon.key")
}

func loadOrCreateKey(path string) (crypto.KeyPair, error) {
	if b, err := os.ReadFile(path); err == nil {
		raw, err := hex.DecodeString(string(trimSpace(b)))
		if err != nil {
			return crypto.KeyPair{}, fmt.Errorf("bad key file %s: %w", path, err)
		}
		return crypto.KeyPairFromPrivate(raw)
	}
	kp, err := crypto.GenerateKeyPair()
	if err != nil {
		return crypto.KeyPair{}, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return crypto.KeyPair{}, err
	}
	if err := os.WriteFile(path, []byte(hex.EncodeToString(kp.PrivateBytes())), 0o600); err != nil {
		return crypto.KeyPair{}, err
	}
	return kp, nil
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func trimSpace(b []byte) []byte {
	i, j := 0, len(b)
	for i < j && (b[i] == ' ' || b[i] == '\n' || b[i] == '\r' || b[i] == '\t') {
		i++
	}
	for j > i && (b[j-1] == ' ' || b[j-1] == '\n' || b[j-1] == '\r' || b[j-1] == '\t') {
		j--
	}
	return b[i:j]
}
