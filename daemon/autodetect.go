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

	"github.com/howlerops/oculus/daemon/agent/claudecode"
	"github.com/howlerops/oculus/daemon/agent/cli"
	"github.com/howlerops/oculus/daemon/agent/opencode"
	"github.com/howlerops/oculus/daemon/agent/pi"
	"github.com/howlerops/oculus/daemon/discovery"
	"github.com/howlerops/oculus/daemon/hub"
)

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
		opencodeURL = detectOrStartOpenCode(ctx)
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
	if piBin != "" {
		h.Register(pi.New([]string{piBin, "--mode", "rpc"}))
		enabled = append(enabled, "pi            -> "+piBin+" --mode rpc")
	}

	// Generic CLI agents: auto-detected known CLIs (codex/gemini/cursor-agent/aider) plus any the
	// user defines in ~/.oculus/agents.json. Each becomes its own provider so it shows up in the
	// app's agent picker. The native integrations above are richer, so a native name always wins a
	// collision.
	native := map[string]bool{"opencode": true, "claude-code": true, "pi": true}
	userAgents, _ := cli.Load(agentsPath())
	for _, cfg := range cli.Merge(cli.Detect(), userAgents) {
		if native[cfg.Name] {
			continue
		}
		h.Register(cli.NewProvider(cfg))
		enabled = append(enabled, "cli           -> "+cfg.Name+" ("+cfg.Command+")")
	}

	return enabled
}

// detectOrStartOpenCode returns the URL of a running `opencode serve`, starting one
// (persistent, reused by later daemon runs) if only the binary is present.
func detectOrStartOpenCode(ctx context.Context) string {
	dctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if servers, _ := discovery.FindOpenCodeServers(dctx); len(servers) > 0 {
		log.Printf("opencode: found a running server at %s", servers[0].URL)
		return servers[0].URL
	}
	// No running server — start one from the binary. Every failure below is logged so the in-app
	// Daemon Logs reveal why opencode "isn't detected" (the #1 report is the binary not on PATH).
	bin, err := exec.LookPath("opencode")
	if err != nil {
		log.Printf("opencode: NOT found on PATH (looked in %s). Install it or add its dir to PATH; then Re-scan.", os.Getenv("PATH"))
		return ""
	}
	log.Printf("opencode: no server running — starting %s serve …", bin)
	port := freePort()
	if port == 0 {
		log.Printf("opencode: couldn't allocate a local port")
		return ""
	}
	// Capture stderr so a start failure has a reason in the log (was io.Discard → silent).
	var errBuf bytes.Buffer
	cmd := exec.Command(bin, "serve", "--hostname", "127.0.0.1", "--port", strconv.Itoa(port))
	cmd.Stdout = io.Discard
	cmd.Stderr = &errBuf
	if err := cmd.Start(); err != nil {
		log.Printf("opencode: failed to start %s: %v", bin, err)
		return ""
	}
	url := fmt.Sprintf("http://127.0.0.1:%d", port)
	if !waitOpenCodeReady(url, 12*time.Second) {
		_ = cmd.Process.Kill()
		log.Printf("opencode: started %s but it wasn't ready on %s within 12s: %s", bin, url, strings.TrimSpace(errBuf.String()))
		return ""
	}
	log.Printf("opencode: started at %s", url)
	return url
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
