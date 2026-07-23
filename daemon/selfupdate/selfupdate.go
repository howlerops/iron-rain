// Package selfupdate keeps the daemon binary in lockstep with releases so it never drifts behind
// the app (the app is just UI; the daemon does the real work, and a stale daemon silently misses
// features — e.g. tracker field mapping). On start the daemon checks the latest GitHub release and,
// if a newer one exists AND this is a real install, downloads the arch-matched binary, swaps itself
// in place, and re-execs into the new version. A dev build (placeholder version) or an unwritable /
// non-install path skips all of this.
package selfupdate

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const releaseAPI = "https://api.github.com/repos/howlerops/iron-rain/releases/latest"

// MaybeUpdateAndReexec checks for a newer release and, if found + installable, swaps this binary and
// re-execs into it (this call never returns on success). Safe to call at the very start of serve;
// any failure just logs and returns so the current binary keeps running. Bounded by an internal
// timeout so a slow/unreachable GitHub can't delay startup for long.
func MaybeUpdateAndReexec(current string) {
	if !updatable(current) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()

	exe, err := os.Executable()
	if err != nil {
		return
	}
	if p, err := filepath.EvalSymlinks(exe); err == nil {
		exe = p
	}
	if !isRealInstall(exe) {
		return // dev/temp path, or not writable — don't touch it
	}

	latest, asset, err := latestRelease(ctx)
	if err != nil || latest == "" || asset == "" {
		return
	}
	if !isNewer(latest, current) {
		return
	}
	log.Printf("selfupdate: %s available (have %s) — updating…", latest, current)

	newBin, err := downloadBinary(ctx, asset, filepath.Dir(exe))
	if err != nil {
		log.Printf("selfupdate: download failed: %v", err)
		return
	}
	defer os.Remove(newBin) // removed only if the swap below doesn't consume it

	if !runsOK(newBin) {
		log.Printf("selfupdate: downloaded binary failed a sanity run — keeping current")
		return
	}
	// Atomic in-place swap (same dir → same filesystem). Replacing a running executable's file is
	// allowed on macOS/Linux — the running process keeps the old inode; the path now points at the
	// new binary, which the re-exec below runs.
	if err := os.Rename(newBin, exe); err != nil {
		log.Printf("selfupdate: swap failed: %v", err)
		return
	}
	log.Printf("selfupdate: updated %s → %s; re-exec", current, latest)
	// Re-exec into the new binary with the same args/env. Never returns on success.
	if err := syscall.Exec(exe, os.Args, os.Environ()); err != nil {
		log.Printf("selfupdate: re-exec failed (%v) — the new binary applies on next restart", err)
	}
}

// updatable gates on the version: dev/placeholder builds can't be compared to releases, so they're
// left alone (this is what keeps the scratchpad/dev daemon from ever self-updating).
func updatable(v string) bool {
	v = strings.TrimSpace(v)
	return v != "" && v != "0.0.0-dev" && !strings.Contains(v, "dev")
}

// isRealInstall skips build/temp/repo paths and anything we can't write.
func isRealInstall(exe string) bool {
	low := strings.ToLower(exe)
	for _, bad := range []string{"/go-build", "/tmp/", "/t/", "/scratchpad", "/var/folders", "/projects/oculus/daemon"} {
		if strings.Contains(low, bad) {
			return false
		}
	}
	// Writable check: try to open the file for writing (O_WRONLY) without truncating.
	f, err := os.OpenFile(exe, os.O_WRONLY, 0)
	if err != nil {
		return false
	}
	_ = f.Close()
	return true
}

func latestRelease(ctx context.Context) (tag, assetURL string, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, releaseAPI, nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", "", fmt.Errorf("github: HTTP %d", resp.StatusCode)
	}
	var rel struct {
		Tag    string `json:"tag_name"`
		Assets []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return "", "", err
	}
	want := fmt.Sprintf("oculusd_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	for _, a := range rel.Assets {
		if a.Name == want {
			return strings.TrimPrefix(rel.Tag, "v"), a.URL, nil
		}
	}
	return strings.TrimPrefix(rel.Tag, "v"), "", nil
}

// downloadBinary fetches the tar.gz, extracts the "oculusd" entry to a temp file in dir (so the
// later rename is atomic on the same filesystem), makes it executable, and returns its path.
func downloadBinary(ctx context.Context, url, dir string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("download: HTTP %d", resp.StatusCode)
	}
	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		return "", err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return "", fmt.Errorf("oculusd not found in archive")
		}
		if err != nil {
			return "", err
		}
		if filepath.Base(hdr.Name) != "oculusd" {
			continue
		}
		out, err := os.CreateTemp(dir, ".oculusd-update-*")
		if err != nil {
			return "", err
		}
		if _, err := io.Copy(out, tr); err != nil {
			out.Close()
			os.Remove(out.Name())
			return "", err
		}
		out.Close()
		if err := os.Chmod(out.Name(), 0o755); err != nil {
			os.Remove(out.Name())
			return "", err
		}
		// CI cross-compiles the darwin binary on Linux, so it ships UNSIGNED. Apple Silicon's AMFI
		// SIGKILLs an unsigned/invalid-CDHash Mach-O at exec — which meant the sanity run (and every
		// subsequent launch) died, so on arm64 the update silently aborted and the daemon never moved
		// off its stale version. Ad-hoc re-sign locally (and drop any quarantine xattr) so the freshly
		// written binary is valid on this machine before we sanity-run and swap it in.
		adhocSign(out.Name())
		return out.Name(), nil
	}
}

// adhocSign gives a Mach-O a valid local (ad-hoc) code signature and strips quarantine so Apple
// Silicon will actually exec it. No-op off macOS. Best-effort: if codesign is missing we proceed and
// let the sanity run catch a genuinely broken binary.
func adhocSign(path string) {
	if runtime.GOOS != "darwin" {
		return
	}
	exec.Command("/usr/bin/xattr", "-c", path).Run()
	if err := exec.Command("/usr/bin/codesign", "--force", "--sign", "-", path).Run(); err != nil {
		log.Printf("selfupdate: ad-hoc codesign failed (%v) — the binary may not launch on Apple Silicon", err)
	}
}

// runsOK sanity-checks the freshly downloaded binary by running `--version` (which prints + exits 0).
func runsOK(path string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, path, "--version").Run() == nil
}

// isNewer reports whether release version a is strictly newer than current b (semver-ish, numeric
// dotted compare; non-numeric/odd → be conservative and treat as newer only if clearly greater).
func isNewer(a, b string) bool {
	pa, pb := parseVer(a), parseVer(b)
	for i := 0; i < 3; i++ {
		if pa[i] != pb[i] {
			return pa[i] > pb[i]
		}
	}
	return false
}

func parseVer(v string) [3]int {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexAny(v, "-+"); i >= 0 {
		v = v[:i]
	}
	var out [3]int
	for i, p := range strings.SplitN(v, ".", 3) {
		if i > 2 {
			break
		}
		out[i], _ = strconv.Atoi(strings.TrimSpace(p))
	}
	return out
}
