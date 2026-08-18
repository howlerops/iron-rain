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
	"io"
	"log"
	mrand "math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/mdp/qrterminal/v3"

	"github.com/howlerops/oculus/daemon/accounts"
	"github.com/howlerops/oculus/daemon/activity"
	"github.com/howlerops/oculus/daemon/agent"
	"github.com/howlerops/oculus/daemon/agent/opencode"
	"github.com/howlerops/oculus/daemon/crypto"
	"github.com/howlerops/oculus/daemon/discovery"
	"github.com/howlerops/oculus/daemon/genui"
	"github.com/howlerops/oculus/daemon/hub"
	"github.com/howlerops/oculus/daemon/issues"
	"github.com/howlerops/oculus/daemon/loghub"
	"github.com/howlerops/oculus/daemon/mcp"
	"github.com/howlerops/oculus/daemon/project"
	"github.com/howlerops/oculus/daemon/protocol"
	"github.com/howlerops/oculus/daemon/push"
	"github.com/howlerops/oculus/daemon/relay"
	"github.com/howlerops/oculus/daemon/selfupdate"
	"github.com/howlerops/oculus/daemon/server"
	"github.com/howlerops/oculus/daemon/slack"
	"github.com/howlerops/oculus/daemon/sshremote"
	"github.com/howlerops/oculus/daemon/store"
	"github.com/howlerops/oculus/daemon/telemetry"
	"github.com/howlerops/oculus/daemon/transcript"
	"github.com/howlerops/oculus/daemon/wake"
	"github.com/howlerops/oculus/daemon/worktree"
)

// version is stamped at build time via -ldflags "-X main.version=<tag>" (see .github/workflows/
// release.yml). A dev build keeps the placeholder, which disables self-update.
var version = "0.0.0-dev"

// defaultRelayURL is the comma-separated list of shared relays a daemon registers on by default so
// the app can reach it from anywhere (off-LAN) with zero setup. The app races them (plus LAN), so
// order is preference: the Cloudflare Durable-Object relay is primary (edge-local, hibernates so
// idle cost ≈ 0, no single-region SPOF); the Fly relay is a portable fallback. Override with
// --relay (or "" for LAN-only).
//
// A relay only ever sees CIPHERTEXT — the Noise channel is end-to-end between the daemon and the
// paired device — so pointing this at someone else's host leaks metadata (that a daemon exists, and
// when it is busy) but never code or conversation. That is what makes a shared default acceptable
// at all, and why self-hosting is a one-flag change rather than a fork.
//
// TODO(ops): these are personal hostnames. Move to relay1/relay2.ironrain.dev with the workers.dev
// and fly.dev names kept as trailing fallbacks, so the addresses survive an account change. That is
// a DNS + deploy task, not a code change: the list below is the only place to edit.
const defaultRelayURL = "wss://oculus-relay.jacobbeck-dev.workers.dev/ws,wss://oculus-relay-howlerops.fly.dev/ws"

// relayEnvOverride lets a self-hoster point every daemon at their own relay without editing flags in
// a launchd plist or a systemd unit — the two places these processes usually start from, where
// changing an argument is far more awkward than setting an environment variable.
const relayEnvOverride = "OCULUS_RELAY"

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
	printSecret := fs.Bool("print-pairing-secret", false, "print the permanent pairing secret to stdout (it otherwise never appears in terminal scrollback)")
	keyPath := fs.String("key", defaultKeyPath(), "path to the daemon private key")
	apnsKey := fs.String("apns-key", "", "path to an APNs auth key (.p8) to enable push")
	apnsKeyID := fs.String("apns-key-id", "", "APNs Key ID (with --apns-key)")
	apnsTeamID := fs.String("apns-team-id", "", "Apple Team ID (with --apns-key)")
	apnsBundle := fs.String("apns-bundle", "com.howlerops.oculus", "app bundle id / APNs topic")
	apnsSandbox := fs.Bool("apns-sandbox", false, "use the APNs sandbox endpoint")
	publicURL := fs.String("public-url", "", "reachable base ws/wss URL for the pairing QR (e.g. wss://x.ngrok-free.app); default derives a LAN URL from --addr")
	name := fs.String("name", "", "human name for this desktop shown in the app (default: hostname)")
	slackWebhook := fs.String("slack-webhook", "", "Slack Incoming Webhook URL to mirror agent events to a channel (or set it in ~/.oculus/slack.json)")
	relayURL := fs.String("relay", defaultRelayURL, "comma-separated relay ws URLs for remote access from anywhere (empty = LAN-only). The app races them + LAN; order is preference. Default: Cloudflare DO relay, then Fly fallback.")
	if err := fs.Parse(args); err != nil {
		return err
	}

	// Tee the standard logger into a ring buffer so the app's Developer log panel can tail this
	// daemon live (local OR remote) — captured before anything else logs so early lines are kept.
	// Only `log` output is captured; the pairing-QR banner (printed with fmt) stays out of the stream.
	lh := loghub.New(1000)
	log.SetOutput(io.MultiWriter(os.Stderr, lh))

	// Under launchd the daemon inherits a minimal PATH, so agent harnesses installed via nvm /
	// homebrew (which live in ~/.zshrc, not the login-only path) aren't found — the "native agents
	// aren't detected on my other Mac" bug. Merge in the user's real interactive-shell PATH + common
	// tool dirs so LookPath finds opencode/claude/node/pi/codex/gemini regardless of how they run us.
	augmentPATH()

	// Keep the daemon in lockstep with releases: if a newer one exists, self-update + re-exec BEFORE
	// binding, so every (re)start runs the latest. No-op for dev builds / non-installs. This is why
	// updating the app (which restarts the daemon) now also updates the daemon.
	selfupdate.MaybeUpdateAndReexec(version)

	// A self-hoster starting the daemon from a launchd plist or a systemd unit finds it far easier to
	// set an environment variable than to edit an argument list, so honour one — but never override an
	// EXPLICIT --relay, which is the more specific instruction.
	if env := os.Getenv(relayEnvOverride); env != "" && *relayURL == defaultRelayURL {
		*relayURL = env
	}

	kp, err := loadOrCreateKey(*keyPath)
	if err != nil {
		return err
	}
	sec := *secret
	if sec == "" {
		// Load the pre-upgrade permanent secret if this machine has one, and do NOT create one if it
		// doesn't. A fresh install has no permanent pairing secret at all: devices enroll with a
		// single-use pairing code and then hold their own credential (daemon/hub/credentials.go).
		// Writing a permanent owner-equivalent credential to disk on first run — which is what
		// loadOrCreateSecret used to do here — would recreate the exact problem the code lifecycle
		// exists to remove, on every new machine.
		sec = loadLegacySecret(secretPath())
	}

	h := hub.New()
	defer h.Shutdown() // stop language servers AND reap every agent child on exit (see hub.Shutdown)
	h.SetWakeGuard(wake.New())
	// Per-device enrollment: pairing records WHICH device connected and mints it a credential of its
	// own, so one device can be revoked without rotating anything or re-pairing everything you own.
	h.SetDevicesPath(filepath.Join(filepath.Dir(secretPath()), "devices.json"))
	// Where the migration clock lives: how long the pre-upgrade permanent secret keeps working. It has
	// to survive a restart, or restarting the daemon would silently hand that secret back its full
	// lifetime every time.
	h.SetCredentialsPath(filepath.Join(filepath.Dir(secretPath()), "credentials.json"))
	h.SetLogHub(lh)
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
		// One-time repair: an earlier build persisted generative-UI cards and sub-agent rows with a
		// NULL message id, so every restart appended another copy of the same card. Idempotent and a
		// no-op on a clean store.
		if n, err := db.DedupeRenderables(); err == nil && n > 0 {
			log.Printf("transcript: removed %d duplicate card/sub-agent row(s) from an earlier build", n)
		}
	}
	// Anonymized diagnostics (on by default; toggle via the app). Ships lifecycle events + scrubbed
	// error classes to the Cloudflare telemetry Worker so failures in the wild are traceable.
	tel := telemetry.New(telemetryPath(), version)
	h.SetTelemetry(tel)
	tel.Record("daemon.start", "", 0, nil) // heartbeat: makes restarts visible + confirms the pipeline
	// Durable append-only per-session transcript (~/.oculus/transcripts): write-aheads user prompts
	// before send and mirrors assistant/tool/status events, so a silent send failure, a provider
	// losing its copy, or a daemon restart can never vaporize the user's work.
	if tr := transcript.New(transcriptsDir()); tr != nil {
		h.SetTranscripts(tr)
		defer tr.Close()
	}
	// Cross-session activity feed (Activity destination / Needs-You inbox / ticker backbone): one
	// durable typed event log every surface reads from, so they can never desync.
	if act := activity.New(activityPath(), 500); act != nil {
		h.SetActivity(act)
	}
	go tel.Run(context.Background())

	// Trackers (Linear/Jira): load saved tokens, connect, and poll every 60s.
	issuesMgr := issues.NewManager(integrationsPath(), h.BroadcastIssues)
	h.SetIssues(issuesMgr)
	h.EnableLoops(loopsPath())                      // recurring autonomous ticket workflows
	h.SetAgentsPath(agentsPath(), agentPrefsPath()) // custom CLI agents + picker visibility
	h.SetNotifyPrefsPath(notifyPrefsPath())         // per-category push-notification toggles
	h.SetApprovalRulesPath(approvalRulesPath())     // persistent "Always allow" (asked once, ever)
	// Per-repo approvals of worktree setup commands. Without a path these last only as long as the
	// daemon does, which means re-approving the same install command after every restart.
	h.SetWorktreeSetupTrustPath(worktreeSetupTrustPath())
	// Daemon-owned MCP host: servers are registered ONCE here and injected into every harness, instead
	// of the user configuring the same server separately for each agent.
	mcpReg := mcp.NewRegistry(mcpRegistryPath())
	h.SetMCPRegistry(mcpReg)
	// The gateway supervises each server ONCE and lets every harness reach it over local HTTP,
	// instead of each agent spawning its own copy of the same server.
	mcpMgr := mcp.NewManager(mcpReg)
	defer mcpMgr.Shutdown()
	mcpToken := loadOrCreateSecret(mcpTokenPath())
	mcpGateway := mcp.NewGateway(mcpMgr, mcpToken)
	h.SetMCPGateway(mcpGateway, mcpToken)
	if len(issuesMgr.Connected()) > 0 {
		go func() { _ = issuesMgr.Refresh(context.Background()) }() // initial fetch
	}
	issuesMgr.StartPolling(context.Background(), 60*time.Second)
	// Keep OAuth (Jira) alive + detect when it dies, so the app can prompt a reconnect.
	issuesMgr.StartTokenRefresh(context.Background(), 40*time.Minute)
	// The OAuth callback is served on a browser-safe loopback port (not the daemon's
	// possibly-blocked WS port), so the redirect URI the tracker sends the browser to loads.
	// Each provider gets its own /oauth/{provider}/callback path.
	oauthAddr := net.JoinHostPort("127.0.0.1", *oauthPort)
	h.SetOAuthAddr(oauthAddr)
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
	// Install the iron:ui generative-UI skill natively into each present harness (claude-code/pi skills
	// dirs, codex AGENTS.md) so it lazy-loads there; harmless where absent. The first-turn injection
	// stays as the universal fallback (opencode + one-shot CLIs).
	if home, err := os.UserHomeDir(); err == nil {
		if notes := genui.InstallNativeSkills(home); len(notes) > 0 {
			log.Printf("iron:ui skill installed: %s", strings.Join(notes, "; "))
		}
	}
	// Let the app trigger a rescan (provider.refresh) without a restart — re-detects harnesses on
	// PATH. setupOff so a rescan never blocks on an interactive sidecar install. The PATH is logged
	// so the in-app Daemon Logs reveal a PATH problem when a harness "isn't detected" on some Mac.
	h.SetRedetect(func() {
		log.Printf("provider.refresh: scanning with PATH=%s", os.Getenv("PATH"))
		found := enableProviders(context.Background(), h, *opencodeURL, *claudeSidecar, *piBin, setupOff)
		log.Printf("provider.refresh: detected %d provider(s): %v", len(found), found)
	})
	// Multi-account credentials: hot-swap which login/key new sessions use. Wired AFTER providers
	// register so the CLI agents resolve the active account's env at each spawn.
	h.SetAccounts(accounts.Load(accountsPath()))
	// SSH remote hosts: run/inspect a worktree on a remote box over SSH.
	h.SetRemotes(sshremote.LoadRegistry(remotesPath()), sshremote.New())

	// Re-own sessions that survived a previous run (opencode/claude sessions persist
	// server-side) and periodically prune stale records with incremental auto-vacuum.
	// Restore runs in the background so a slow/absent opencode can't delay serving.
	const sessionTTL = 7 * 24 * time.Hour
	go h.RestoreSessions(context.Background(), sessionTTL)
	h.StartSessionPruning(context.Background(), 6*time.Hour, sessionTTL)
	h.StartConflictSweep(context.Background(), 45*time.Second) // passive merge-conflict badge for worktree sessions
	// Reclaim worktrees whose repo no longer exists. These accumulate silently — one machine held
	// 1133 — and each one also leaves a stale registration that every `git worktree` command walks.
	// Only worktrees whose git admin dir is GONE are touched, so a live one can never match.
	go func() {
		if n, err := worktree.SweepOrphans(worktree.DefaultBase()); err == nil && n > 0 {
			log.Printf("worktrees: swept %d orphaned worktree(s) whose repo no longer exists", n)
		}
	}()
	h.StartHeartbeat(context.Background())       // supervise autonomous sessions (nudge/checkpoint/escalate)
	h.StartCredentialSweep(context.Background()) // expire pairing codes + lapsed invites on time, not on next use

	// A long-running daemon (e.g. a launchd agent on a server) would otherwise never pick up a new
	// release until it happened to restart. Re-check periodically so it stays current on its own;
	// on an update it swaps + re-execs (sessions restore on the fresh start). No-op for dev builds.
	go func() {
		t := time.NewTicker(6 * time.Hour)
		defer t.Stop()
		for range t.C {
			selfupdate.MaybeUpdateAndReexec(version)
		}
	}()

	// Slack mirror (optional): agent events also post to a Slack channel via an Incoming Webhook.
	slackURL := *slackWebhook
	if slackURL == "" {
		slackURL = loadSlackWebhook(slackWebhookPath())
	}
	slackEnabled := false
	if slackURL != "" {
		h.SetSlack(slack.New(slackURL))
		slackEnabled = true
	}

	pushEnabled := false
	if *apnsKey != "" {
		if err := enablePush(h, *apnsKey, *apnsKeyID, *apnsTeamID, *apnsBundle, *apnsSandbox); err != nil {
			return err
		}
		pushEnabled = true
	}

	// The daemon accepts a device's own credential, a single-use pairing code, a live invite, or —
	// during migration — the old permanent secret. An invited guest is authenticated exactly like any
	// other client; what differs is the ROLE their connection gets. See daemon/hub/credentials.go.
	//
	// An explicit --secret is treated differently from the one we generated ourselves: the operator
	// configured it on purpose (a script, a container), so it is never auto-retired.
	accept := h.AcceptSecret(sec)
	if *secret != "" {
		accept = h.AcceptConfiguredSecret(sec)
	}
	srv := server.New(h, kp, accept)
	// Once the migration window has closed, take the dead credential off disk. Leaving a retired
	// permanent secret sitting in ~/.oculus/secret is leaving a string that LOOKS like a key to this
	// machine in a file people copy into backups and support threads.
	if sec != "" && *secret == "" {
		if live, _ := h.LegacySecretStatus(); !live {
			_ = os.Remove(secretPath())
		}
	}

	// Remote access: keep a host registration on the shared relay so the app can reach this daemon
	// from anywhere (off-LAN) with zero port-forwarding. The relay only forwards ciphertext — the
	// same end-to-end-encrypted session runs over it. The relay server_id is the daemon pubkey, so
	// pairing needs nothing extra beyond the relay URL (the app already has `pub`).
	for _, ru := range splitRelays(*relayURL) {
		go relayHost(ru, hex.EncodeToString(kp.Public()), kp.PrivateBytes(), srv)
	}

	mux := http.NewServeMux()
	mux.Handle("/ws", srv.Handler())
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	// Local MCP gateway. Bearer-authenticated: loopback is shared with every other process on this
	// machine, so being local is not by itself an authorization.
	mux.Handle("/mcp/", mcpGateway)
	// Loopback OAuth callback for tracker connect (Linear + Jira). The redirect URI must be
	// registered on each provider's OAuth app. It is served both on the main mux (reachable via
	// --public-url tunnels) and on a dedicated browser-safe loopback port below, since the
	// local browser can't load the daemon's WS port when it's a browser-restricted one (6000).
	oauthCallback := func(provider string) http.HandlerFunc {
		redirect := issues.OAuthRedirectURI(oauthAddr, provider)
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			code, state := r.URL.Query().Get("code"), r.URL.Query().Get("state")
			if err := issuesMgr.OAuthCallback(r.Context(), code, state, redirect); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprintf(w, "<h2>%s connection failed</h2><p>%s</p>", provider, err.Error())
				return
			}
			fmt.Fprintf(w, "<h2>Iron Rain connected to %s ✓</h2><p>You can close this tab and return to the app.</p>", provider)
		}
	}
	mux.HandleFunc("/oauth/linear/callback", oauthCallback("linear"))
	mux.HandleFunc("/oauth/jira/callback", oauthCallback("jira"))
	// Dedicated browser-safe loopback listener for the OAuth callback.
	if *oauthPort != "" {
		oauthMux := http.NewServeMux()
		oauthMux.HandleFunc("/oauth/linear/callback", oauthCallback("linear"))
		oauthMux.HandleFunc("/oauth/jira/callback", oauthCallback("jira"))
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
	// The pairing secret is NOT printed. It used to land in terminal scrollback, in tmux buffers, in
	// screen recordings, and in the logs of anything that ever ran `oculusd` in CI — and it granted
	// permanent owner access to this Mac. The QR below carries a single-use code that expires instead.
	// --print-pairing-secret exists for the one case that genuinely needs it: recovering a device you
	// can't re-pair by scanning, at a terminal you already trust.
	if *printSecret {
		fmt.Printf("  pairing secret: %s  (permanent — treat it like a password)\n", sec)
	}
	fmt.Printf("  oauth redirect: %s  (register /oauth/linear/callback + /oauth/jira/callback on your OAuth apps)\n", oauthAddr)
	for _, pv := range providers {
		fmt.Printf("  provider:       %s\n", pv)
	}
	if pushEnabled {
		fmt.Printf("  push:           APNs enabled (bundle %s)\n", *apnsBundle)
	}
	if slackEnabled {
		fmt.Printf("  slack:          mirroring agent events to your webhook\n")
	}
	desktopName := *name
	if desktopName == "" {
		if hn, err := os.Hostname(); err == nil {
			desktopName = hn
		}
	}
	pubURL := wsPublicURL(*publicURL, *addr)
	fmt.Printf("  desktop name:   %s\n", desktopName)
	if *relayURL != "" {
		fmt.Printf("  relay:          %s  (remote access from anywhere)\n", *relayURL)
	}
	// Let the hub render pairing codes and invite links using the same reachable URL the startup QR
	// uses, so a code minted from the app works from wherever a normal pairing would.
	h.SetPairURLBuilder(func(secret string) string {
		return buildPairURL(pubURL, hex.EncodeToString(kp.Public()), secret, desktopName, *relayURL)
	})
	// Drop a local pairing file so an app on THIS machine (the macOS app) can auto-discover + connect
	// with zero config. 0600, same-user only. It carries the LOCAL bootstrap code, which is rotated
	// on every daemon start — so the file is never the permanent secret, and a copy of it taken from a
	// backup stops working the next time the daemon restarts.
	h.SetLocalPairingRotator(func(code string) {
		writeLocalPairing(localWSURL(*addr), pubURL, hex.EncodeToString(kp.Public()), code, desktopName, *relayURL)
	})
	// The startup QR carries a single-use code that expires in minutes, not the permanent secret.
	// Scrollback, a screen recording, or a photo of the terminal is then a dead credential rather than
	// a shell on this Mac. Pair from the Mac app (Pair a phone…) to mint a fresh one any time.
	if code, expires := h.MintPairCode(0); code != "" {
		printPairing(pubURL, hex.EncodeToString(kp.Public()), code, desktopName, *relayURL, expires)
	}
	// ReadHeaderTimeout bounds header-slowloris on the plain HTTP routes
	// (/healthz, /oauth/linear/callback) when exposed via --public-url. Leave
	// Write/Idle timeouts unset so long-lived /ws WebSocket upgrades aren't cut off.
	// Tell the hub where the MCP gateway is reachable, now that the bind address is settled. Harnesses
	// are always pointed at LOOPBACK, never at whatever --addr is (the installed launchd agent binds
	// 0.0.0.0), so an agent on this machine never routes its tool calls over the network. The route
	// itself is still exposed on that interface, which is exactly why it requires a bearer token — an
	// MCP server runs with this machine's credentials.
	h.SetMCPGatewayBase("http://127.0.0.1:" + portOf(*addr))
	httpSrv := &http.Server{Addr: *addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}

	// Graceful shutdown. Without this the process only ever ended by signal, which meant EVERY defer
	// in this function was dead code: language servers were never stopped, the SQLite handle was never
	// closed, the transcript writer never flushed, and every agent child (sidecar, pi, CLI) was
	// orphaned. Catching SIGTERM/SIGINT lets ListenAndServe return normally so those defers actually
	// run — launchd sends SIGTERM on `launchctl kickstart -k` and on logout.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	errCh := make(chan error, 1)
	go func() {
		err := httpSrv.ListenAndServe()
		if err == http.ErrServerClosed {
			err = nil
		}
		errCh <- err
	}()
	select {
	case err := <-errCh:
		return err // the listener failed on its own (port in use, etc.)
	case sig := <-stop:
		log.Printf("daemon: %s received — shutting down", sig)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		// Stop accepting first, then let the deferred Shutdown/Close chain above unwind.
		_ = httpSrv.Shutdown(ctx)
		// Reap the agent children HERE rather than leaving it to the deferred h.Shutdown(). Defers run
		// LIFO, and h.Shutdown is registered FIRST, so it would run LAST — after the SQLite store and
		// the transcript writer had already been closed underneath the session goroutines that are
		// still draining their final events. Calling it explicitly ends every agent child (which
		// procutil.Isolate means nothing else will) while the stores it writes through are still open.
		// The defer stays as the backstop for the error-return path above; Shutdown is idempotent.
		h.Shutdown()
		return nil
	}
}

// relayHost keeps a single host registration on the shared relay so the app can bridge to this
// daemon from anywhere. relay.ServeHost registers, waits for one client, serves it (blocking until
// that client disconnects), then returns — so we loop to re-register for the next client. A quick
// return means the relay was unreachable (down / network), so we back off; a long-lived return
// means we served a real session, so we re-register promptly. Traffic stays end-to-end encrypted.
func relayHost(relayURL, serverID string, hostPriv []byte, srv *server.Server) {
	ctx := context.Background()
	backoff := time.Second
	for {
		start := time.Now()
		// ServeHostKey, not ServeHost: it answers the relay's proof-of-possession challenge, which is
		// the only thing that makes the relay-side check worth anything. The relay verifies a host
		// ONLY when the host opts in — deliberately, so daemons already in the field are not locked
		// out — so without this line the host slot is still granted on presentation of serverID, a
		// value printed to stdout, embedded in every pairing QR, and logged by the relay itself.
		// Anyone who has seen it could evict this daemon and take the bridge position.
		//
		// It degrades rather than fails: a relay not yet redeployed ignores the offer, and a key that
		// doesn't match serverID falls back to an unproven registration, so this cannot break remote
		// access on its own.
		_ = relay.ServeHostKey(ctx, relayURL, serverID, hostPriv, relay.DefaultKeepalive, srv.ServeConn)
		if time.Since(start) > 5*time.Second {
			backoff = time.Second // served a client (or waited on one) — re-register immediately
			continue
		}
		// Relay unreachable — retry with capped exponential backoff plus full jitter, so a relay
		// restart doesn't trigger a thundering herd of every daemon reconnecting in lockstep.
		time.Sleep(backoff/2 + time.Duration(mrand.Int63n(int64(backoff/2)+1)))
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

// splitRelays parses the comma-separated --relay value into individual relay URLs.
func splitRelays(list string) []string {
	var out []string
	for _, s := range strings.Split(list, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
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
func writeLocalPairing(wsURL, publicWS, pub, secret, name, relay string) {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	dir := filepath.Join(home, ".oculus")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(dir, "pairing.json"), pairingJSON(wsURL, publicWS, pub, secret, name, relay), 0o600)
}

// pairingJSON is the ~/.oculus/pairing.json body the local app reads (name lets it label
// this desktop). Pure for testing.
func pairingJSON(wsURL, publicWS, pub, secret, name, relay string) []byte {
	data, _ := json.Marshal(map[string]string{
		"ws": wsURL, "public": publicWS, "pub": pub, "secret": secret, "name": name, "relay": relay,
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
// label this desktop (for grouping multiple paired Macs). relay, when set, lets the app
// reach this daemon from anywhere via the shared relay (server_id = pub); the app prefers
// the relay and falls back to the LAN ws URL.
func buildPairURL(wsURL, pubHex, secret, name, relay string) string {
	u := fmt.Sprintf("oculus://pair?ws=%s&pub=%s&secret=%s",
		url.QueryEscape(wsURL), pubHex, url.QueryEscape(secret))
	if name != "" {
		u += "&name=" + url.QueryEscape(name)
	}
	if relay != "" {
		u += "&relay=" + url.QueryEscape(relay)
	}
	return u
}

// printPairing prints the oculus:// pairing URL and a scannable QR to the terminal.
//
// The URL carries a SINGLE-USE code with an expiry, not the permanent secret, so the QR that ends up
// in scrollback (or in a photo of someone's screen) is worthless minutes later. The expiry is printed
// because a credential whose lifetime is invisible is one people assume is permanent — and then
// screenshot.
func printPairing(wsURL, pubHex, code, name, relay string, expires time.Time) {
	pairURL := buildPairURL(wsURL, pubHex, code, name, relay)
	fmt.Printf("\n  pair from your phone — scan this QR (Iron Rain app → Scan QR):\n\n")
	qrterminal.GenerateWithConfig(pairURL, qrterminal.Config{
		Level: qrterminal.L, Writer: os.Stdout, HalfBlocks: true,
		BlackChar: qrterminal.BLACK_BLACK, WhiteChar: qrterminal.WHITE_WHITE,
		QuietZone: 1,
	})
	fmt.Printf("\n  or paste: %s\n", pairURL)
	fmt.Printf("  this code pairs ONE device and expires at %s — mint another from the Mac app (Pair a phone…)\n\n",
		expires.Format("15:04:05"))
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

// portOf extracts the port from a listen address, defaulting to the daemon's standard port.
func portOf(addr string) string {
	if _, port, err := net.SplitHostPort(addr); err == nil && port != "" {
		return port
	}
	return "6000"
}

// mcpTokenPath holds the bearer token harnesses present to the local MCP gateway (0600).
func mcpTokenPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "oculus-mcp-token"
	}
	return filepath.Join(home, ".oculus", "mcp-token")
}

// mcpRegistryPath is where MCP server definitions live (0600 — Env/Headers hold credentials).
func mcpRegistryPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "oculus-mcp.json"
	}
	return filepath.Join(home, ".oculus", "mcp.json")
}

func approvalRulesPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "oculus-approval-rules.json"
	}
	return filepath.Join(home, ".oculus", "approval-rules.json")
}

// worktreeSetupTrustPath is where per-repo approvals of worktree setup commands live. It is kept
// apart from approval-rules.json deliberately — see setup_trust.go: these records grant a shell and
// are pinned to an exact command hash, and must not be reachable by a broad hand-written rule.
func worktreeSetupTrustPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "oculus-worktree-setup-trust.json"
	}
	return filepath.Join(home, ".oculus", "worktree-setup-trust.json")
}

func notifyPrefsPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "oculus-notify.json"
	}
	return filepath.Join(home, ".oculus", "notify.json")
}

func integrationsPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "oculus-integrations.json"
	}
	return filepath.Join(home, ".oculus", "integrations.json")
}

// augmentPATH merges the user's real interactive-login-shell PATH plus common tool directories into
// this process's PATH, so agent-harness detection works even when the daemon runs under launchd with
// a stripped-down PATH. Best-effort and idempotent (a dir already present isn't re-added).
func augmentPATH() {
	current := os.Getenv("PATH")
	have := map[string]bool{}
	var order []string
	add := func(dir string) {
		dir = strings.TrimSpace(dir)
		if dir == "" || have[dir] {
			return
		}
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
			return
		}
		have[dir] = true
		order = append(order, dir)
	}
	for _, d := range filepath.SplitList(current) {
		add(d)
	}

	// The user's real PATH from an interactive login shell — this is where nvm/homebrew set it
	// (~/.zshrc is interactive-only, so a plain login shell would miss it). Bounded so a slow rc
	// file can't hang startup.
	if shell := os.Getenv("SHELL"); shell != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		cmd := exec.CommandContext(ctx, shell, "-ilc", "printf %s \"$PATH\"")
		out, err := cmd.Output()
		cancel()
		if err == nil {
			for _, d := range filepath.SplitList(strings.TrimSpace(string(out))) {
				add(d)
			}
		}
	}

	// Common macOS/Linux tool locations, as a backstop.
	home, _ := os.UserHomeDir()
	for _, d := range []string{
		"/opt/homebrew/bin", "/opt/homebrew/sbin", "/usr/local/bin", "/usr/local/sbin",
		filepath.Join(home, ".local", "bin"), filepath.Join(home, "go", "bin"),
		filepath.Join(home, ".bun", "bin"), filepath.Join(home, ".deno", "bin"),
		filepath.Join(home, ".cargo", "bin"),
		filepath.Join(home, ".opencode", "bin"), // opencode's own installer dir
		filepath.Join(home, ".npm-global", "bin"), filepath.Join(home, "node_modules", ".bin"),
	} {
		add(d)
	}
	// nvm-managed node versions (opencode/claude/codex are often npm-global there).
	if matches, _ := filepath.Glob(filepath.Join(home, ".nvm", "versions", "node", "*", "bin")); len(matches) > 0 {
		for _, d := range matches {
			add(d)
		}
	}

	merged := strings.Join(order, string(os.PathListSeparator))
	if merged != current {
		_ = os.Setenv("PATH", merged)
		log.Printf("PATH augmented for agent detection (%d dirs)", len(order))
	}
}

func accountsPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "oculus-accounts.json"
	}
	return filepath.Join(home, ".oculus", "accounts.json")
}

func remotesPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "oculus-remotes.json"
	}
	return filepath.Join(home, ".oculus", "remotes.json")
}

func transcriptsDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "oculus-transcripts"
	}
	return filepath.Join(home, ".oculus", "transcripts")
}

func activityPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "oculus-activity.jsonl"
	}
	return filepath.Join(home, ".oculus", "activity.jsonl")
}

func loopsPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "oculus-loops.json"
	}
	return filepath.Join(home, ".oculus", "loops.json")
}

func slackWebhookPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "oculus-slack.json"
	}
	return filepath.Join(home, ".oculus", "slack.json")
}

// loadSlackWebhook reads {"webhook_url":"..."} from path (missing file → ""), for a persisted
// Slack webhook so the app can enable Slack without re-passing a flag.
func loadSlackWebhook(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var cfg struct {
		WebhookURL string `json:"webhook_url"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return ""
	}
	return strings.TrimSpace(cfg.WebhookURL)
}

func agentsPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "oculus-agents.json"
	}
	return filepath.Join(home, ".oculus", "agents.json")
}

func agentPrefsPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "oculus-agent-visibility.json"
	}
	return filepath.Join(home, ".oculus", "agent-visibility.json")
}

func secretPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "oculusd.secret"
	}
	return filepath.Join(home, ".oculus", "secret")
}

// loadLegacySecret returns the pre-upgrade permanent pairing secret if this machine has one, and ""
// otherwise. It never creates the file.
//
// The distinction matters on upgrade: a machine WITH this file has devices in the wild that know
// nothing else, so the daemon must keep accepting it long enough for them to migrate to per-device
// credentials. A machine without one has nothing to migrate, and should never acquire a permanent
// owner-equivalent credential in the first place.
func loadLegacySecret(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// loadOrCreateSecret returns a stable secret persisted at path, generating + writing one on first
// run. Still used for the MCP gateway's machine-wide bearer token, which is a local-loopback
// credential with no pairing lifecycle of its own.
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

func telemetryPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "telemetry.json"
	}
	return filepath.Join(home, ".oculus", "telemetry.json")
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
