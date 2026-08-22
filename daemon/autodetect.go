package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/howlerops/oculus/daemon/agent/agui"
	"github.com/howlerops/oculus/daemon/agent/claudecode"
	"github.com/howlerops/oculus/daemon/agent/cli"
	"github.com/howlerops/oculus/daemon/agent/opencode"
	"github.com/howlerops/oculus/daemon/agent/pi"
	"github.com/howlerops/oculus/daemon/discovery"
	"github.com/howlerops/oculus/daemon/hub"
	"github.com/howlerops/oculus/daemon/procutil"
)

// primeSessionsRoot is where prime-agent keeps its conversation JSONL files. Same shape as pi's
// (~/.pi/agent/sessions), one directory over.
func primeSessionsRoot() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".prime", "agent", "sessions")
}

// setupMode controls whether a missing claude-code sidecar (node_modules) is installed.
type setupMode int

const (
	setupOff  setupMode = iota // never install
	setupAsk                   // prompt on a TTY, else skip
	setupAuto                  // install without asking
)

// enableProviders registers every provider available on the host — a running (or
// auto-started) opencode, the claude-code sidecar if claude+node are present, and pi
// if it's on PATH. Explicit flags override the auto-detected value for that provider.
func enableProviders(ctx context.Context, h *hub.Hub, opencodeURL, claudeSidecar, piBin string, claudeSetup setupMode) []string {
	var enabled []string

	if opencodeURL == "" {
		opencodeURL = detectOrStartOpenCode(ctx, h)
	}
	if opencodeURL != "" {
		h.Register(opencode.New(opencodeURL))
		enabled = append(enabled, "opencode      -> "+opencodeURL)
	}

	if claudeSidecar == "" {
		claudeSidecar = detectOrSetupClaudeSidecar(claudeSetup)
	}
	if claudeSidecar != "" {
		h.Register(claudecode.New([]string{"node", claudeSidecar}))
		enabled = append(enabled, "claude-code   -> node "+claudeSidecar+" (your claude subscription)")
	}

	if piBin == "" {
		if p, err := exec.LookPath("pi"); err == nil {
			piBin = p
		}
	}
	// prime-agent (Prime Intellect) speaks the SAME JSONL RPC protocol as pi, so it rides the same
	// adapter rather than a second copy of a parser we already have — verified by driving a real
	// prime-agent through it unchanged. It gets the full treatment as a result: streamed text,
	// tool cards, usage/cost, and resume from its own session files.
	if p, err := exec.LookPath("prime-agent"); err == nil {
		h.Register(pi.NewNamed("prime-agent", []string{p, "--mode", "rpc"}, primeSessionsRoot()))
		enabled = append(enabled, "prime-agent   -> "+p+" --mode rpc")
	}
	if piBin != "" {
		h.Register(pi.New([]string{piBin, "--mode", "rpc"}))
		enabled = append(enabled, "pi            -> "+piBin+" --mode rpc")
	}

	// Generic CLI agents: auto-detected known CLIs (codex/gemini/cursor-agent/aider) plus any the
	// user defines in ~/.oculus/agents.json. Each becomes its own provider so it shows up in the
	// app's agent picker. The native integrations above are richer, so a native name always wins a
	// collision.
	native := map[string]bool{"opencode": true, "claude-code": true, "pi": true, "prime-agent": true}
	userAgents, _ := cli.Load(agentsPath())
	for _, cfg := range cli.Merge(cli.Detect(), userAgents) {
		if native[cfg.Name] {
			continue
		}
		if cfg.IsAGUI() {
			h.Register(agui.New(agui.Config{Name: cfg.Name, Endpoint: cfg.Endpoint, Headers: cfg.Headers}))
			enabled = append(enabled, "ag-ui         -> "+cfg.Name+" ("+cfg.Endpoint+")")
			continue
		}
		h.Register(cli.NewProvider(cfg))
		enabled = append(enabled, "cli           -> "+cfg.Name+" ("+cfg.Command+")")
	}

	return enabled
}

// detectOrStartOpenCode returns the URL of the opencode server the daemon should use. It prefers a
// DEDICATED, daemon-managed server so Iron Rain is isolated from any opencode the user runs for their
// OWN work: latching onto an arbitrary discovered server (lsof order) attached to the user's opencode,
// picked a NON-DETERMINISTIC server that could switch across restarts, and orphaned Iron Rain's
// sessions (a session created on server A vanishes when the daemon later reconnects to server B).
// Priority: (1) our OWN server from a previous run (remembered port, still alive) → sticky reconnect;
// (2) start a fresh dedicated server; (3) LAST RESORT, a discovered running server (may be the user's).
func detectOrStartOpenCode(ctx context.Context, h *hub.Hub) string {
	// (1) Reconnect to the server WE started last time, if it's still up — same server ⇒ our sessions
	// are still there, and we never drift onto the user's opencode.
	if url := rememberedOpenCodeURL(); url != "" && waitOpenCodeReady(url, 1*time.Second) {
		log.Printf("opencode: reusing our managed server at %s", url)
		return url
	}
	// (2) Start our OWN dedicated server.
	if bin, err := exec.LookPath("opencode"); err == nil {
		if url := startManagedOpenCode(bin, h.OpenCodeMCPConfig()); url != "" {
			return url
		}
	} else {
		log.Printf("opencode: NOT found on PATH (looked in %s). Install it or add its dir to PATH; then Re-scan.", os.Getenv("PATH"))
	}
	// (3) Last resort: reuse a running server (may be the user's own opencode). Better than nothing,
	// but NOT isolated — warn so a confusing "found the wrong sessions / lost my session" is explained.
	dctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if servers, _ := discovery.FindOpenCodeServers(dctx); len(servers) > 0 {
		log.Printf("opencode: WARNING using a running server at %s that the daemon did NOT start — if this is your own opencode, Iron Rain's sessions aren't isolated and may be lost when it stops. Install opencode so the daemon can run its own.", servers[0].URL)
		return servers[0].URL
	}
	return ""
}

// startManagedOpenCode launches a dedicated `opencode serve` (sanitized env), waits for it to become
// ready, and remembers its port so a later daemon run reconnects to it. Returns "" on failure.
func startManagedOpenCode(bin, mcpConfig string) string {
	port := freePort()
	if port == 0 {
		log.Printf("opencode: couldn't allocate a local port")
		return ""
	}
	log.Printf("opencode: starting our own %s serve on :%d …", bin, port)
	var errBuf bytes.Buffer
	cmd := exec.Command(bin, "serve", "--hostname", "127.0.0.1", "--port", strconv.Itoa(port))
	cmd.Stdout = io.Discard
	cmd.Stderr = &errBuf
	// Run agent tool commands NON-INTERACTIVELY. An agent bash step like `git merge` (opens $EDITOR
	// for the merge message), `git commit`, a pager, or a credential prompt would otherwise block on
	// stdin FOREVER — wedging the whole opencode turn (and every queued prompt behind it) for hours.
	cmd.Env = append(os.Environ(),
		"GIT_EDITOR=true", // git "opens" /usr/bin/true → succeeds instantly, no editor wait
		"EDITOR=true",     // generic editor fallback
		"VISUAL=true",
		"GIT_PAGER=cat", // no interactive pager
		"PAGER=cat",
		"GIT_TERMINAL_PROMPT=0", // git fails fast instead of prompting for credentials
	)
	// Daemon-owned MCP servers. OPENCODE_CONFIG_CONTENT MERGES with the user's own opencode.json
	// rather than replacing it, so this adds our servers without disturbing their setup.
	//
	// Only ever set on a server WE started (this function). Path (3) in detectOrStartOpenCode falls
	// back to the user's OWN running opencode — injecting there would leak Iron Rain's servers, and
	// its credentials, into their personal tool.
	if mcpConfig != "" {
		cmd.Env = append(cmd.Env, "OPENCODE_CONFIG_CONTENT="+mcpConfig)
		log.Printf("opencode: injecting %d daemon-managed MCP server(s)", strings.Count(mcpConfig, "\"type\""))
	}
	procutil.Isolate(cmd) // opencode serve spawns tool subprocesses — own the whole group
	if err := cmd.Start(); err != nil {
		log.Printf("opencode: failed to start %s: %v", bin, err)
		return ""
	}
	// Reap it. Without a Wait the exited server lingers as a zombie and its CommandContext watcher
	// goroutine never returns.
	go func() {
		_ = cmd.Wait()
	}()
	// NOTE: deliberately NOT terminated on daemon shutdown. A surviving opencode server is re-adopted
	// on the next start via ~/.oculus/opencode-port (that's what keeps its sessions alive across a
	// daemon restart or self-update). Isolate/TerminateGroup exist here for the failed-start path.
	url := fmt.Sprintf("http://127.0.0.1:%d", port)
	if !waitOpenCodeReady(url, 12*time.Second) {
		procutil.TerminateGroup(cmd)
		log.Printf("opencode: started %s but it wasn't ready on %s within 12s: %s", bin, url, strings.TrimSpace(errBuf.String()))
		return ""
	}
	rememberOpenCodePort(port)
	log.Printf("opencode: started our managed server at %s", url)
	return url
}

// opencodePortFile is where the daemon records the port of the opencode server IT started, so a later
// run reconnects to the SAME one (where its sessions live) instead of re-discovering an arbitrary
// server — the fix for orphaned sessions on a machine also running the user's own opencode.
func opencodePortFile() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".oculus", "opencode-port")
}

func rememberedOpenCodeURL() string {
	f := opencodePortFile()
	if f == "" {
		return ""
	}
	b, err := os.ReadFile(f)
	if err != nil {
		return ""
	}
	port := strings.TrimSpace(string(b))
	if _, err := strconv.Atoi(port); err != nil {
		return ""
	}
	return "http://127.0.0.1:" + port
}

func rememberOpenCodePort(port int) {
	f := opencodePortFile()
	if f == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(f), 0o700)
	_ = os.WriteFile(f, []byte(strconv.Itoa(port)), 0o600)
}

func waitOpenCodeReady(url string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		req, _ := http.NewRequest(http.MethodGet, url+"/session", nil)
		if resp, err := http.DefaultClient.Do(req); err == nil {
			resp.Body.Close()
			if resp.StatusCode/100 == 2 {
				return true
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	return false
}

func freePort() int {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// detectOrSetupClaudeSidecar finds a ready claude-code sidecar; if claude+node are
// present but the sidecar isn't installed, it materializes the embedded sidecar into
// ~/.oculus/claude-sidecar and (per mode) installs node_modules — auto, or after a
// TTY prompt. Returns the sidecar.mjs path, or "" if unavailable/declined.
func detectOrSetupClaudeSidecar(mode setupMode) string {
	if _, err := exec.LookPath("claude"); err != nil {
		log.Printf("claude-code: `claude` NOT found on PATH — install the Claude CLI, then Re-scan.")
		return ""
	}
	if _, err := exec.LookPath("node"); err != nil {
		log.Printf("claude-code: `node` NOT found on PATH — the sidecar needs Node; install it, then Re-scan.")
		return ""
	}
	// Already installed somewhere? Use it — but refresh it first so an UPGRADED daemon actually
	// ships sidecar fixes (previously an existing install was returned as-is and never updated,
	// so sidecar bug fixes never reached machines that already had it).
	if p := firstUsableSidecar(claudeSidecarCandidates()); p != "" {
		refreshSidecarIfStale(p)
		return p
	}
	if mode == setupOff {
		fmt.Fprintln(os.Stderr, "  note: claude-code available but its sidecar isn't installed — run `oculusd serve` (it will offer to set it up) or `cd daemon/agent/claudecode/sidecar && npm install`")
		return ""
	}

	dir := defaultSidecarDir()
	if dir == "" {
		return ""
	}
	mjs := filepath.Join(dir, "sidecar.mjs")
	if err := materializeSidecar(dir); err != nil {
		fmt.Fprintf(os.Stderr, "  claude-code setup: could not write sidecar to %s: %v\n", dir, err)
		return ""
	}

	if !isDir(filepath.Join(dir, "node_modules")) {
		if mode == setupAsk && !confirmSidecarInstall(dir) {
			fmt.Fprintf(os.Stderr, "  claude-code: skipped setup — run `cd %s && npm install` to enable it later\n", dir)
			return ""
		}
		fmt.Fprintf(os.Stderr, "  claude-code: installing sidecar deps in %s (one-time)...\n", dir)
		if err := npmInstall(dir); err != nil {
			fmt.Fprintf(os.Stderr, "  claude-code setup failed: %v\n", err)
			return ""
		}
	}
	if isFile(mjs) && isDir(filepath.Join(dir, "node_modules")) {
		return mjs
	}
	return ""
}

func defaultSidecarDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".oculus", "claude-sidecar")
}

// refreshSidecarIfStale overwrites an already-installed sidecar.mjs with the daemon's embedded copy
// when they differ, so upgrading the daemon actually ships sidecar fixes (node_modules is left
// intact — package.json is unchanged by JS-only fixes, so no npm re-install is needed). Only the
// daemon-managed default dir is touched; an external/override sidecar (env or repo) is left alone.
func refreshSidecarIfStale(mjs string) {
	if mjs != filepath.Join(defaultSidecarDir(), "sidecar.mjs") {
		return
	}
	if cur, err := os.ReadFile(mjs); err == nil && bytes.Equal(cur, claudecode.SidecarMJS) {
		return // already current
	}
	if err := os.WriteFile(mjs, claudecode.SidecarMJS, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "  claude-code: could not refresh sidecar.mjs: %v\n", err)
		return
	}
	_ = os.WriteFile(filepath.Join(filepath.Dir(mjs), "package.json"), claudecode.SidecarPackageJSON, 0o644)
	fmt.Fprintln(os.Stderr, "  claude-code: refreshed sidecar.mjs to match the upgraded daemon")
}

// materializeSidecar writes the embedded sidecar.mjs + package.json into dir (creating
// it). Existing files are overwritten so an upgraded daemon refreshes the sidecar.
func materializeSidecar(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "sidecar.mjs"), claudecode.SidecarMJS, 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "package.json"), claudecode.SidecarPackageJSON, 0o644)
}

// confirmSidecarInstall asks on a TTY; a non-interactive stdin declines (can't prompt).
func confirmSidecarInstall(dir string) bool {
	if !stdinIsTTY() {
		fmt.Fprintf(os.Stderr, "  claude-code: sidecar not installed and stdin isn't a terminal — skipping (use --claude-setup=auto to install non-interactively)\n")
		return false
	}
	fmt.Fprintf(os.Stderr, "  claude-code needs a one-time setup: run `npm install` in %s now? [Y/n] ", dir)
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	line = strings.ToLower(strings.TrimSpace(line))
	return line == "" || line == "y" || line == "yes"
}

func stdinIsTTY() bool {
	fi, err := os.Stdin.Stat()
	return err == nil && (fi.Mode()&os.ModeCharDevice) != 0
}

// npmInstall runs the node package installer in dir, streaming progress to stderr.
func npmInstall(dir string) error {
	bin, args := pickInstaller()
	if bin == "" {
		return fmt.Errorf("no node package manager found (need npm or bun on PATH)")
	}
	cmd := exec.Command(bin, args...)
	cmd.Dir = dir
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	return cmd.Run()
}

func pickInstaller() (string, []string) {
	if p, err := exec.LookPath("npm"); err == nil {
		return p, []string{"install", "--no-fund", "--no-audit"}
	}
	if p, err := exec.LookPath("bun"); err == nil {
		return p, []string{"install"}
	}
	return "", nil
}

func parseSetupMode(s string) setupMode {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "auto", "yes", "y":
		return setupAuto
	case "off", "no", "n", "false":
		return setupOff
	default:
		return setupAsk
	}
}

func claudeSidecarCandidates() []string {
	var c []string
	if env := os.Getenv("OCULUS_CLAUDE_SIDECAR"); env != "" {
		c = append(c, env)
	}
	if wd, err := os.Getwd(); err == nil {
		c = append(c, filepath.Join(wd, "agent", "claudecode", "sidecar", "sidecar.mjs"))
	}
	if exe, err := os.Executable(); err == nil {
		d := filepath.Dir(exe)
		c = append(c,
			filepath.Join(d, "claude-sidecar", "sidecar.mjs"),
			filepath.Join(d, "sidecar", "sidecar.mjs"),
		)
	}
	if home, err := os.UserHomeDir(); err == nil {
		c = append(c, filepath.Join(home, ".oculus", "claude-sidecar", "sidecar.mjs"))
	}
	return c
}

// firstUsableSidecar returns the first candidate that exists AND has a node_modules
// alongside it (so the Agent SDK is installed).
func firstUsableSidecar(candidates []string) string {
	for _, p := range candidates {
		if p == "" {
			continue
		}
		if isFile(p) && isDir(filepath.Join(filepath.Dir(p), "node_modules")) {
			return p
		}
	}
	return ""
}

func isFile(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir()
}
func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}
