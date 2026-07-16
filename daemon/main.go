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
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/howlerops/oculus/daemon/agent/claudecode"
	"github.com/howlerops/oculus/daemon/agent/opencode"
	"github.com/howlerops/oculus/daemon/crypto"
	"github.com/howlerops/oculus/daemon/discovery"
	"github.com/howlerops/oculus/daemon/hub"
	"github.com/howlerops/oculus/daemon/protocol"
	"github.com/howlerops/oculus/daemon/push"
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

	if len(os.Args) >= 2 && os.Args[1] == "discover" {
		if err := runDiscover(); err != nil {
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
	fmt.Fprintln(os.Stderr, "usage:")
	fmt.Fprintln(os.Stderr, "  oculusd serve --opencode URL [--addr 127.0.0.1:6000] [--secret S]")
	fmt.Fprintln(os.Stderr, "  oculusd discover   # autodetect active opencode/claude-code sessions on this host")
}

func serve(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:6000", "listen address")
	opencodeURL := fs.String("opencode", "", "URL of a running `opencode serve` (e.g. http://127.0.0.1:4096)")
	claudeBin := fs.String("claude", "", "claude-code binary to enable the claude-code provider (e.g. claude)")
	secret := fs.String("secret", "", "pairing secret clients must present (default: generated)")
	keyPath := fs.String("key", defaultKeyPath(), "path to the daemon private key")
	apnsKey := fs.String("apns-key", "", "path to an APNs auth key (.p8) to enable push")
	apnsKeyID := fs.String("apns-key-id", "", "APNs Key ID (with --apns-key)")
	apnsTeamID := fs.String("apns-team-id", "", "Apple Team ID (with --apns-key)")
	apnsBundle := fs.String("apns-bundle", "com.howlerops.oculus", "app bundle id / APNs topic")
	apnsSandbox := fs.Bool("apns-sandbox", false, "use the APNs sandbox endpoint")
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *opencodeURL == "" && *claudeBin == "" {
		return fmt.Errorf("enable at least one provider: --opencode URL and/or --claude BINARY")
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
	h.SetDiscoverer(discovery.Scan)
	providers := []string{}
	if *opencodeURL != "" {
		h.Register(opencode.New(*opencodeURL))
		providers = append(providers, "opencode -> "+*opencodeURL)
	}
	if *claudeBin != "" {
		h.Register(claudecode.New(*claudeBin))
		providers = append(providers, "claude-code -> "+*claudeBin)
	}

	pushEnabled := false
	if *apnsKey != "" {
		if err := enablePush(h, *apnsKey, *apnsKeyID, *apnsTeamID, *apnsBundle, *apnsSandbox); err != nil {
			return err
		}
		pushEnabled = true
	}

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
	for _, pv := range providers {
		fmt.Printf("  provider:       %s\n", pv)
	}
	if pushEnabled {
		fmt.Printf("  push:           APNs enabled (bundle %s)\n", *apnsBundle)
	}
	return http.ListenAndServe(*addr, mux)
}

// enablePush parses the .p8 auth key and installs an APNs notifier on the hub.
func enablePush(h *hub.Hub, keyPath, keyID, teamID, bundle string, sandbox bool) error {
	if keyID == "" || teamID == "" {
		return fmt.Errorf("push: --apns-key-id and --apns-team-id are required with --apns-key")
	}
	pem, err := os.ReadFile(keyPath)
	if err != nil {
		return fmt.Errorf("push: read %s: %w", keyPath, err)
	}
	key, err := push.ParseP8(pem)
	if err != nil {
		return err
	}
	baseURL := "https://api.push.apple.com"
	if sandbox {
		baseURL = "https://api.sandbox.push.apple.com"
	}
	n, err := push.NewAPNs(push.APNsConfig{
		KeyID: keyID, TeamID: teamID, BundleID: bundle, Key: key, BaseURL: baseURL,
	})
	if err != nil {
		return err
	}
	h.SetNotifier(n)
	return nil
}

// runDiscover autodetects active agent sessions on this host and prints them.
func runDiscover() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	items, err := discovery.Scan(ctx)
	if err != nil {
		return err
	}
	if len(items) == 0 {
		fmt.Println("no active opencode servers or recent claude-code sessions found")
		return nil
	}
	for _, it := range items {
		switch {
		case it.Provider == "opencode" && it.Kind == protocol.KindServer:
			fmt.Printf("opencode server   %s (pid %d)\n", it.URL, it.PID)
		case it.Provider == "opencode":
			fmt.Printf("  opencode session %s  %s  (%s)\n", it.SessionID, it.Title, it.URL)
		default:
			fmt.Printf("claude-code session %s  cwd=%s\n", it.SessionID, it.Cwd)
		}
	}
	return nil
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
