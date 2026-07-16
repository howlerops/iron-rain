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
	"encoding/json"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mdp/qrterminal/v3"

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
	publicURL := fs.String("public-url", "", "reachable base ws/wss URL for the pairing QR (e.g. wss://x.ngrok-free.app); default derives a LAN URL from --addr")
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
	printPairing(wsPublicURL(*publicURL, *addr), hex.EncodeToString(kp.Public()), sec)
	// Drop a local pairing file so an app on THIS machine (the macOS app) can
	// auto-discover + connect with zero config. 0600, same-user only.
	writeLocalPairing(localWSURL(*addr), hex.EncodeToString(kp.Public()), sec)
	return http.ListenAndServe(*addr, mux)
}

// localWSURL is the loopback ws URL for a same-machine app.
func localWSURL(addr string) string {
	_, port, err := net.SplitHostPort(addr)
	if err != nil || port == "" {
		return "ws://127.0.0.1:6000/ws"
	}
	return "ws://127.0.0.1:" + port + "/ws"
}

// writeLocalPairing writes ~/.oculus/pairing.json for the local app to read.
func writeLocalPairing(wsURL, pub, secret string) {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	dir := filepath.Join(home, ".oculus")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	data, err := json.Marshal(map[string]string{"ws": wsURL, "pub": pub, "secret": secret})
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(dir, "pairing.json"), data, 0o600)
}

// wsPublicURL returns the reachable ws URL clients should dial. If publicURL is set
// it's used as the base (…/ws appended); otherwise a LAN URL is derived from addr.
func wsPublicURL(publicURL, addr string) string {
	if publicURL != "" {
		base := strings.TrimRight(publicURL, "/")
		if strings.HasSuffix(base, "/ws") {
			return base
		}
		return base + "/ws"
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "ws://" + addr + "/ws"
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = lanIP()
	}
	return "ws://" + net.JoinHostPort(host, port) + "/ws"
}

// lanIP returns the first non-loopback IPv4 address, or 127.0.0.1.
func lanIP() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "127.0.0.1"
	}
	for _, a := range addrs {
		if ipnet, ok := a.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			if ip4 := ipnet.IP.To4(); ip4 != nil {
				return ip4.String()
			}
		}
	}
	return "127.0.0.1"
}

// printPairing prints the oculus:// pairing URL and a scannable QR to the terminal.
func printPairing(wsURL, pubHex, secret string) {
	pairURL := fmt.Sprintf("oculus://pair?ws=%s&pub=%s&secret=%s",
		url.QueryEscape(wsURL), pubHex, url.QueryEscape(secret))
	fmt.Printf("\n  pair from your phone — scan this QR (Oculus app → Scan QR):\n\n")
	qrterminal.GenerateWithConfig(pairURL, qrterminal.Config{
		Level: qrterminal.L, Writer: os.Stdout, HalfBlocks: true,
		BlackChar: qrterminal.BLACK_BLACK, WhiteChar: qrterminal.WHITE_WHITE,
		QuietZone: 1,
	})
	fmt.Printf("\n  or paste: %s\n\n", pairURL)
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
