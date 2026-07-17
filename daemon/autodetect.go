package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"

	"github.com/howlerops/oculus/daemon/agent/claudecode"
	"github.com/howlerops/oculus/daemon/agent/opencode"
	"github.com/howlerops/oculus/daemon/agent/pi"
	"github.com/howlerops/oculus/daemon/discovery"
	"github.com/howlerops/oculus/daemon/hub"
)

// enableProviders registers every provider available on the host — a running (or
// auto-started) opencode, the claude-code sidecar if claude+node are present, and pi
// if it's on PATH. Explicit flags override the auto-detected value for that provider.
func enableProviders(ctx context.Context, h *hub.Hub, opencodeURL, claudeSidecar, piBin string) []string {
	var enabled []string

	if opencodeURL == "" {
		opencodeURL = detectOrStartOpenCode(ctx)
	}
	if opencodeURL != "" {
		h.Register(opencode.New(opencodeURL))
		enabled = append(enabled, "opencode      -> "+opencodeURL)
	}

	if claudeSidecar == "" {
		claudeSidecar = detectClaudeSidecar()
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

	return enabled
}

// detectOrStartOpenCode returns the URL of a running `opencode serve`, starting one
// (persistent, reused by later daemon runs) if only the binary is present.
func detectOrStartOpenCode(ctx context.Context) string {
	dctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if servers, _ := discovery.FindOpenCodeServers(dctx); len(servers) > 0 {
		return servers[0].URL
	}
	bin, err := exec.LookPath("opencode")
	if err != nil {
		return ""
	}
	port := freePort()
	if port == 0 {
		return ""
	}
	cmd := exec.Command(bin, "serve", "--hostname", "127.0.0.1", "--port", strconv.Itoa(port))
	cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
	if err := cmd.Start(); err != nil {
		return ""
	}
	url := fmt.Sprintf("http://127.0.0.1:%d", port)
	if !waitOpenCodeReady(url, 12*time.Second) {
		_ = cmd.Process.Kill()
		return ""
	}
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

// detectClaudeSidecar finds the claude-code sidecar if claude + node are present and
// the sidecar (with its node_modules) is installed at a known location.
func detectClaudeSidecar() string {
	if _, err := exec.LookPath("claude"); err != nil {
		return ""
	}
	if _, err := exec.LookPath("node"); err != nil {
		return ""
	}
	return firstUsableSidecar(claudeSidecarCandidates())
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
