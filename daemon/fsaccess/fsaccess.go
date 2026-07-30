// Package fsaccess is the daemon's guarded file-system layer for the built-in editor.
// Every operation is validated against a set of allowed roots (registered project roots +
// active session working dirs) so a remote client can never read or write outside them —
// including via symlink escape. Text files only; binaries and oversized files are surfaced
// as read-only metadata.
package fsaccess

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

// maxReadBytes caps how much of a file the editor loads. Larger files come back truncated
// (read-only in the UI); the cap also bounds memory per request.
const maxReadBytes = 2 << 20 // 2 MiB

// maxBytesRead caps a raw-bytes read (images shown inline in the editor).
const maxBytesRead = 16 << 20 // 16 MiB

// skipDirs are never listed — build/vendor noise that would swamp the tree.
var skipDirs = map[string]bool{
	".git": true, "node_modules": true, ".build": true, ".swiftpm": true,
	"DerivedData": true, ".next": true, "dist": true, "vendor": true,
}

// Guard validates that paths stay inside a set of allowed roots.
type Guard struct{ roots []string }

// New builds a Guard from roots, resolving each to an absolute, symlink-free, cleaned path
// and de-duplicating. Empty/invalid roots are dropped.
func New(roots []string) *Guard {
	seen := map[string]bool{}
	out := make([]string, 0, len(roots))
	for _, r := range roots {
		if r == "" {
			continue
		}
		abs, err := filepath.Abs(r)
		if err != nil {
			continue
		}
		if real, err := filepath.EvalSymlinks(abs); err == nil {
			abs = real
		}
		abs = filepath.Clean(abs)
		if !seen[abs] {
			seen[abs] = true
			out = append(out, abs)
		}
	}
	return &Guard{roots: out}
}

// Roots returns the allowed roots (absolute, cleaned).
func (g *Guard) Roots() []string { return append([]string(nil), g.roots...) }

// Resolve cleans p to an absolute path and confirms it lies within an allowed root, resolving
// symlinks on the longest existing prefix so a symlink can't escape the sandbox. The returned
// path is the unresolved absolute path (safe to open); the containment check uses the resolved
// one. The target need not exist yet (so writes to new files pass).
func (g *Guard) Resolve(p string) (string, error) {
	if p == "" {
		return "", errors.New("empty path")
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	abs = filepath.Clean(abs)
	real := resolveExisting(abs)
	if !g.contains(real) {
		return "", fmt.Errorf("path outside allowed roots: %s", p)
	}
	return abs, nil
}

func (g *Guard) contains(abs string) bool {
	for _, r := range g.roots {
		if abs == r || strings.HasPrefix(abs, r+string(os.PathSeparator)) {
			return true
		}
	}
	return false
}

// resolveExisting resolves symlinks on the longest existing ancestor of abs, then rejoins the
// non-existing tail — so a partially-existing write target is still checked against the real
// location of its existing parent.
func resolveExisting(abs string) string {
	cur := abs
	var tail []string
	for {
		if _, err := os.Lstat(cur); err == nil {
			if real, err := filepath.EvalSymlinks(cur); err == nil {
				cur = real
			}
			break
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			break
		}
		tail = append([]string{filepath.Base(cur)}, tail...)
		cur = parent
	}
	return filepath.Join(append([]string{cur}, tail...)...)
}

// Node is one directory entry.
type Node struct {
	Name string
	Path string
	Dir  bool
	Size int64
}

// Tree lists a directory's immediate entries (dirs first, then files, case-insensitive),
// skipping build/vendor noise. Lazy by design — the caller expands one directory at a time.
func (g *Guard) Tree(dir string) ([]Node, error) {
	abs, err := g.Resolve(dir)
	if err != nil {
		return nil, err
	}
	ents, err := os.ReadDir(abs)
	if err != nil {
		return nil, err
	}
	nodes := make([]Node, 0, len(ents))
	for _, e := range ents {
		name := e.Name()
		if e.IsDir() && skipDirs[name] {
			continue
		}
		var size int64
		if info, err := e.Info(); err == nil {
			size = info.Size()
		}
		nodes = append(nodes, Node{Name: name, Path: filepath.Join(abs, name), Dir: e.IsDir(), Size: size})
	}
	sort.Slice(nodes, func(i, j int) bool {
		if nodes[i].Dir != nodes[j].Dir {
			return nodes[i].Dir
		}
		return strings.ToLower(nodes[i].Name) < strings.ToLower(nodes[j].Name)
	})
	return nodes, nil
}

// File is a read file's content + identity. Sha is over the returned content (== the full
// file for non-truncated text), used for write-conflict detection.
type File struct {
	Content   string
	Sha       string
	ModTime   int64
	Size      int64
	Binary    bool
	Truncated bool
}

// Read returns a text file's content (capped at maxReadBytes). Binary files come back with
// Binary=true and no content; oversized files with Truncated=true (read-only in the UI).
func (g *Guard) Read(path string) (File, error) {
	abs, err := g.Resolve(path)
	if err != nil {
		return File{}, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return File{}, err
	}
	if info.IsDir() {
		return File{}, errors.New("is a directory")
	}
	f, err := os.Open(abs)
	if err != nil {
		return File{}, err
	}
	defer f.Close()

	limit := info.Size()
	truncated := false
	if limit > maxReadBytes {
		limit = maxReadBytes
		truncated = true
	}
	buf := make([]byte, limit)
	n, err := io.ReadFull(f, buf)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return File{}, err
	}
	buf = buf[:n]
	out := File{Sha: shaHex(buf), ModTime: info.ModTime().Unix(), Size: info.Size(), Truncated: truncated}
	// Binary if it has a NUL byte, or (for a fully-read file) isn't valid UTF-8 — editing such
	// a file would let a JSON round-trip substitute U+FFFD and corrupt bytes on save. A
	// truncated read may cut a multi-byte rune, so skip the UTF-8 check there (already read-only).
	if isBinary(buf) || (!truncated && !utf8.Valid(buf)) {
		out.Binary = true
		return out, nil
	}
	out.Content = string(buf)
	return out, nil
}

// Write saves content, but only if baseSha matches the current on-disk sha (optimistic
// concurrency against the agent editing the same file). Returns conflict=true when the file
// moved since the client read it (caller should prompt reload/overwrite); no write happens.
func (g *Guard) Write(path, content, baseSha string) (out File, conflict bool, err error) {
	abs, err := g.Resolve(path)
	if err != nil {
		return File{}, false, err
	}
	cur, rerr := os.ReadFile(abs)
	switch {
	case rerr == nil:
		if shaHex(cur) != baseSha {
			return File{}, true, nil
		}
	case os.IsNotExist(rerr):
		if baseSha != "" { // client edited a file that has since been deleted
			return File{}, true, nil
		}
	default:
		return File{}, false, rerr
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		return File{}, false, err
	}
	info, _ := os.Stat(abs)
	var mtime int64
	if info != nil {
		mtime = info.ModTime().Unix()
	}
	return File{Content: content, Sha: shaHex([]byte(content)), ModTime: mtime, Size: int64(len(content))}, false, nil
}

// ReadBytes returns a file's raw bytes (capped at maxBytesRead) plus a best-effort MIME
// type, for rendering images inline in the editor. Scoped through the same guard as text
// reads; a directory or oversized file is an error.
func (g *Guard) ReadBytes(path string) (mime string, data []byte, err error) {
	abs, err := g.Resolve(path)
	if err != nil {
		return "", nil, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", nil, err
	}
	if info.IsDir() {
		return "", nil, errors.New("is a directory")
	}
	if info.Size() > maxBytesRead {
		return "", nil, fmt.Errorf("file too large (%d bytes)", info.Size())
	}
	b, err := os.ReadFile(abs)
	if err != nil {
		return "", nil, err
	}
	mime = mimeByExt(filepath.Ext(abs))
	if mime == "" {
		mime = http.DetectContentType(b)
	}
	return mime, b, nil
}

// mimeByExt maps common image extensions to a MIME type (http.DetectContentType misses a
// few, e.g. svg, and we prefer the explicit mapping for correctness).
func mimeByExt(ext string) string {
	switch strings.ToLower(ext) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".bmp":
		return "image/bmp"
	case ".tif", ".tiff":
		return "image/tiff"
	case ".ico":
		return "image/x-icon"
	case ".heic":
		return "image/heic"
	case ".svg":
		return "image/svg+xml"
	default:
		return ""
	}
}

func shaHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// isBinary reports whether buf looks like a binary file (contains a NUL byte in its head).
func isBinary(buf []byte) bool {
	n := len(buf)
	if n > 8000 {
		n = 8000
	}
	for i := 0; i < n; i++ {
		if buf[i] == 0 {
			return true
		}
	}
	return false
}

// NormalizePath canonicalizes p for comparison against a configured prefix: expand ~, make it
// absolute, resolve symlinks on the longest existing ancestor, and Clean it. It returns "" when p
// doesn't denote a filesystem path at all (an approval Detail is often a bare command like
// "npm test", which must never be mistaken for a path and matched against a subtree rule).
//
// Callers use this for POLICY decisions (does this request fall inside an allowed subtree). Guard.Resolve
// remains the enforcement path for actual reads/writes.
func NormalizePath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	if strings.HasPrefix(p, "~") {
		if home, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	}
	// Only absolute paths are comparable to a configured prefix. A relative token ("npm test",
	// "src/main.go") has no single meaning here, so decline rather than guess a working directory.
	if !filepath.IsAbs(p) {
		return ""
	}
	return filepath.Clean(resolveExisting(filepath.Clean(p)))
}
