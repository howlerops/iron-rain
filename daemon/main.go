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

	"github.com/howlerops/oculus/daemon/agent"
	"github.com/howlerops/oculus/daemon/agent/opencode"
	"github.com/howlerops/oculus/daemon/crypto"
	"github.com/howlerops/oculus/daemon/discovery"
	"github.com/howlerops/oculus/daemon/hub"
	"github.com/howlerops/oculus/daemon/issues"
	"github.com/howlerops/oculus/daemon/project"
	"github.com/howlerops/oculus/daemon/protocol"
	"github.com/howlerops/oculus/daemon/push"
	"github.com/howlerops/oculus/daemon/server"
	"github.com/howlerops/oculus/daemon/store"
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
	// Browsers hard-block port 6000 (X11) with ERR_UNSAFE_PORT, so the loopback OAuth
	// callback gets its own browser-safe port. The WebSocket (app, not a browser) is unaffected.
	oauthPort := fs.String("oauth-port", "6900", "browser-safe loopback port for the OAuth callback")
	// Providers are AUTO-DETECTED (a running/installed opencode, the claude-code sidecar
	// if claude+node are present, and pi on PATH). These flags only OVERRIDE detection.
	opencodeURL := fs.String("opencode", "", "override: URL of a running `opencode serve` (else auto-detected/started)")
	claudeSidecar := fs.String("claude-sidecar", "", "override: path to the claude-code sidecar.mjs (else auto-detected)")
	piBin := fs.String("pi", "", "override: path to the pi binary (else auto-detected on PATH)")
	claudeSetup := fs.String("claude-setup", "ask", "claude-code sidecar one-time install when missing: ask|auto|off")
	autoProjects := fs.Bool("auto-projects", true, "auto-register projects from the folders active agents run in")
	secret := fs.String("secret", "", "pairing secret clients must present (default: generated)")
	keyPath := fs.String("key", defaultKeyPath(), "path to the daemon private key")
	apnsKey := fs.String("apns-key", "", "path to an APNs auth key (.p8) to enable push")
	apnsKeyID := fs.String("apns-key-id", "", "APNs Key ID (with --apns-key)")
	apnsTeamID := fs.String("apns-team-id", "", "Apple Team ID (with --apns-key)")
	apnsBundle := fs.String("apns-bundle", "com.howlerops.oculus", "app bundle id / APNs topic")
	apnsSandbox := fs.Bool("apns-sandbox", false, "use the APNs sandbox endpoint")
	publicURL := fs.String("public-url", "", "reachable base ws/wss URL for the pairing QR (e.g. wss://x.ngrok-free.app); default derives a LAN URL from --addr")
	name := fs.String("name", "", "human name for this desktop shown in the app (default: hostname)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	kp, err := loadOrCreateKey(*keyPath)
	if err != nil {
		return err
	}
	sec := *secret
	if sec == "" {
		sec = loadOrCreateSecret(secretPath()) // stable across restarts so paired clients stay authorized
	}

	h := hub.New()
	defer h.Shutdown() // stop language servers on exit
	h.SetDiscoverer(discovery.Scan)
	if reg, err := project.Load(projectsPath()); err != nil {
		fmt.Fprintf(os.Stderr, "  warning: could not load project registry: %v\n", err)
	} else {
		h.SetProjects(reg)
		h.SetAutoProjects(*autoProjects)
	}
	// Durable local state (session names, etc.): a pure-Go SQLite DB in ~/.oculus.
	// Best-effort — if it can't open, we log and run with in-memory-only names.
	if err := os.MkdirAll(filepath.Dir(dbPath()), 0o700); err != nil {
		fmt.Fprintf(os.Stderr, "  warning: could not create state dir: %v\n", err)
	} else if db, err := store.Open(dbPath()); err != nil {
		fmt.Fprintf(os.Stderr, "  warning: could not open local database: %v\n", err)
	} else {
		h.SetStore(db)
		defer db.Close()
	}
	// Trackers (Linear/Jira): load saved tokens, connect, and poll every 60s.
	issuesMgr := issues.NewManager(integrationsPath(), h.BroadcastIssues)
	h.SetIssues(issuesMgr)
	if len(issuesMgr.Connected()) > 0 {
		go func() { _ = issuesMgr.Refresh(context.Background()) }() // initial fetch
	}
	issuesMgr.StartPolling(context.Background(), 60*time.Second)
	// The OAuth callback is served on a browser-safe loopback port (not the daemon's
	// possibly-blocked WS port), so the redirect URI Linear sends the browser to loads.
	oauthRedirect := issues.OAuthRedirectURI(net.JoinHostPort("127.0.0.1", *oauthPort))
	h.SetOAuthRedirect(oauthRedirect)
	h.SetAttacherFactory(func(provider, url string) agent.Attacher {
		if provider == "opencode" && url != "" {
			return opencode.New(url)
		}
		return nil
	})
	// Auto-detect every provider present on this host; the flags override a specific one.
	providers := enableProviders(context.Background(), h, *opencodeURL, *claudeSidecar, *piBin, parseSetupMode(*claudeSetup))
	if len(providers) == 0 {
		fmt.Fprintln(os.Stderr, "  warning: no coding-agent providers detected (install opencode, claude-code, or pi and re-run) — serving anyway")
	}

	// Re-own sessions that survived a previous run (opencode/claude sessions persist
	// server-side) and periodically prune stale records with incremental auto-vacuum.
	// Restore runs in the background so a slow/absent opencode can't delay serving.
	const sessionTTL = 7 * 24 * time.Hour
	go h.RestoreSessions(context.Background(), sessionTTL)
	h.StartSessionPruning(context.Background(), 6*time.Hour, sessionTTL)
	h.StartHeartbeat(context.Background()) // supervise autonomous sessions (nudge/checkpoint/escalate)

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
	// Loopback OAuth callback for tracker connect (Linear). The redirect URI must be
	// registered on the Linear OAuth app. It is served both on the main mux (reachable via
	// --public-url tunnels) and on a dedicated browser-safe loopback port below, since the
	// local browser can't load the daemon's WS port when it's a browser-restricted one (6000).
	oauthCallback := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		code, state := r.URL.Query().Get("code"), r.URL.Query().Get("state")
		if err := issuesMgr.OAuthCallback(r.Context(), code, state, oauthRedirect); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, "<h2>Linear connection failed</h2><p>%s</p>", err.Error())
			return
		}
		fmt.Fprint(w, "<h2>Iron Rain connected to Linear ✓</h2><p>You can close this tab and return to the app.</p>")
	}
	mux.HandleFunc("/oauth/linear/callback", oauthCallback)
	// Dedicated browser-safe loopback listener for the OAuth callback.
	if *oauthPort != "" {
		oauthMux := http.NewServeMux()
		oauthMux.HandleFunc("/oauth/linear/callback", oauthCallback)
		oauthSrv := &http.Server{Addr: net.JoinHostPort("127.0.0.1", *oauthPort), Handler: oauthMux, ReadHeaderTimeout: 10 * time.Second}
		go func() {
			if err := oauthSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				fmt.Fprintf(os.Stderr, "  oauth callback listener (%s): %v\n", *oauthPort, err)
			}
		}()
	}

	fmt.Printf("oculusd %s\n", version)
	fmt.Printf("  listening:      ws://%s/ws\n", *addr)
	fmt.Printf("  daemon pubkey:  %s\n", hex.EncodeToString(kp.Public()))
	fmt.Printf("  pairing secret: %s\n", sec)
	fmt.Printf("  oauth redirect: %s  (register this on your Linear OAuth app)\n", oauthRedirect)
	for _, pv := range providers {
		fmt.Printf("  provider:       %s\n", pv)
	}
	if pushEnabled {
		fmt.Printf("  push:           APNs enabled (bundle %s)\n", *apnsBundle)
	}
	desktopName := *name
	if desktopName == "" {
		if hn, err := os.Hostname(); err == nil {
			desktopName = hn
		}
	}
	pubURL := wsPublicURL(*publicURL, *addr)
	fmt.Printf("  desktop name:   %s\n", desktopName)
	printPairing(pubURL, hex.EncodeToString(kp.Public()), sec, desktopName)
	// Drop a local pairing file so an app on THIS machine (the macOS app) can
	// auto-discover + connect with zero config, and show a QR (using the reachable
	// public URL) to pair a phone. 0600, same-user only.
	writeLocalPairing(localWSURL(*addr), pubURL, hex.EncodeToString(kp.Public()), sec, desktopName)
	// ReadHeaderTimeout bounds header-slowloris on the plain HTTP routes
	// (/healthz, /oauth/linear/callback) when exposed via --public-url. Leave
	// Write/Idle timeouts unset so long-lived /ws WebSocket upgrades aren't cut off.
	httpSrv := &http.Server{Addr: *addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	return httpSrv.ListenAndServe()
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
// ws is the loopback URL the local app connects to; publicWS is the reachable URL
// encoded into the QR shown to a phone.
func writeLocalPairing(wsURL, publicWS, pub, secret, name string) {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	dir := filepath.Join(home, ".oculus")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(dir, "pairing.json"), pairingJSON(wsURL, publicWS, pub, secret, name), 0o600)
}

// pairingJSON is the ~/.oculus/pairing.json body the local app reads (name lets it label
// this desktop). Pure for testing.
func pairingJSON(wsURL, publicWS, pub, secret, name string) []byte {
	data, _ := json.Marshal(map[string]string{
		"ws": wsURL, "public": publicWS, "pub": pub, "secret": secret, "name": name,
	})
	return data
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

// buildPairURL builds the oculus://pair deep link the QR encodes. name lets the app
// label this desktop (for grouping multiple paired Macs).
func buildPairURL(wsURL, pubHex, secret, name string) string {
	u := fmt.Sprintf("oculus://pair?ws=%s&pub=%s&secret=%s",
		url.QueryEscape(wsURL), pubHex, url.QueryEscape(secret))
	if name != "" {
		u += "&name=" + url.QueryEscape(name)
	}
	return u
}

// printPairing prints the oculus:// pairing URL and a scannable QR to the terminal.
func printPairing(wsURL, pubHex, secret, name string) {
	pairURL := buildPairURL(wsURL, pubHex, secret, name)
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

func projectsPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "oculus-projects.json"
	}
	return filepath.Join(home, ".oculus", "projects.json")
}

func integrationsPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "oculus-integrations.json"
	}
	return filepath.Join(home, ".oculus", "integrations.json")
}

func secretPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "oculusd.secret"
	}
	return filepath.Join(home, ".oculus", "secret")
}

// loadOrCreateSecret returns a stable pairing secret persisted at path, generating + writing one
// on first run. This keeps the secret constant across daemon restarts/reinstalls so an already
// paired phone (and the local app) stay authorized — a regenerated secret every start would make
// every restart reject existing pairings with "unauthorized".
func loadOrCreateSecret(path string) string {
	if b, err := os.ReadFile(path); err == nil {
		if s := strings.TrimSpace(string(b)); s != "" {
			return s
		}
	}
	s := randomHex(16)
	if dir := filepath.Dir(path); dir != "." {
		_ = os.MkdirAll(dir, 0o700)
	}
	_ = os.WriteFile(path, []byte(s), 0o600) // 0600: the secret is a credential
	return s
}

func dbPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "oculus.db"
	}
	return filepath.Join(home, ".oculus", "oculus.db")
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
