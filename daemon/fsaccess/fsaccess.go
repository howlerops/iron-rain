// Package fsaccess is the daemon's guarded file-system layer for the built-in editor.
// Every operation is validated against a set of allowed roots (registered project roots +
// active session working dirs) so a remote client can never read or write outside them —
// including via symlink escape — and never inside a repository's .git, which is executable
// configuration rather than project content (see vcsMetaDirs). A second, root-INDEPENDENT rule
// refuses the daemon's own state directory and the user's credential stores no matter what the
// caller passed as a root (see protectedRules). Text files only; binaries and oversized files are
// surfaced as read-only metadata.
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
	"sync"
	"unicode/utf8"
)

// maxReadBytes caps how much of a file the editor loads. Larger files come back truncated
// (read-only in the UI); the cap also bounds memory per request.
const maxReadBytes = 2 << 20 // 2 MiB

// maxBytesRead caps a raw-bytes read (images shown inline in the editor).
const maxBytesRead = 16 << 20 // 16 MiB

// skipDirs are never listed — build/vendor noise that would swamp the tree.
//
// This list is about NOISE, not safety. It is consulted by Tree and by the search walk and by
// nothing else, so its containing ".git" has never stopped a single read or write; the rule that
// does that is vcsMetaDirs, enforced in Resolve. Do not read an entry here as a security control.
var skipDirs = map[string]bool{
	".git": true, "node_modules": true, ".build": true, ".swiftpm": true,
	"DerivedData": true, ".next": true, "dist": true, "vendor": true,
}

// vcsMetaDirs names the repository-metadata directories Resolve refuses to descend into, for reads
// as well as writes.
//
// The rule is deliberately NOT "no dotfiles". A client legitimately edits .env, .oculus/, .claude/,
// .github/workflows — dotted paths are ordinary project files and banning them would break the
// editor for no gain. What separates these two directories from those is that their contents are
// not data, they are a program someone else's tooling runs:
//
//   - .git/config offers core.fsmonitor, core.sshCommand and [alias] entries. Each is a string git
//     executes verbatim the next time git runs anywhere in that repo — and the daemon itself runs
//     git there on several steer-level actions (worktree PR, checkpoint create/restore, catchup,
//     merge, diff), as does the owner at their own shell. So a caller who can put one file into
//     .git has a shell as the owner. The 0o644 that Write uses is not a defence: it happens to
//     defuse .git/hooks/*, which needs the exec bit, and it defuses nothing else on this list.
//   - .hg/hgrc has the identical shape (its [hooks] entries are shell commands the next `hg` runs).
//     The daemon never invokes hg, but the trigger was never only the daemon — it is the owner's
//     own tooling, and they are the one whose credentials are at stake.
//
// Reads are refused too, by decision rather than by omission. Nothing in the product reads these
// paths: Tree and Search already skip .git, so the editor cannot navigate there, and no other
// caller resolves a path inside one. Against that zero cost, .git/config routinely carries a remote
// URL with an embedded token, and the object store holds every secret that was ever committed and
// later "removed". Allowing reads would trade a live credential leak for no feature at all.
//
// Known limit, stated rather than papered over: this matches the standard layout BY NAME. A repo
// whose git directory is somewhere else under a differently-named path (GIT_DIR, a .git *file*
// pointing at one) is not covered, and if that directory happened to sit inside an allowed root it
// would still be writable. That is a deliberate stopping point, not an oversight — the daemon
// creates every repo and worktree it manages in the standard layout, so the reachable case is the
// one named here. The .git file itself IS refused, which is the part that matters: rewriting it is
// how you would repoint a linked worktree at an admin dir you control.
var vcsMetaDirs = map[string]bool{".git": true, ".hg": true}

// ErrVCSMetadata is returned by Resolve for a path inside a repository-metadata directory. It is a
// hard refusal with an error the caller surfaces, never a silent no-op: a write that reports
// success while dropping the bytes would leave a client showing edited content that does not exist.
var ErrVCSMetadata = errors.New("refusing to touch repository metadata")

// ErrProtectedPath is returned by Resolve for a path inside one of the protected directories below.
// Like ErrVCSMetadata it is a surfaced error rather than a silent no-op.
var ErrProtectedPath = errors.New("refusing to touch a protected directory")

// stateDirName is the daemon's own state directory, relative to the user's home. It is spelled out
// here rather than imported because fsaccess sits BELOW the daemon package that owns those paths
// (daemon/main.go's defaultKeyPath, agentsPath, dbPath, … all join home + ".oculus" + a filename);
// importing upward would be a cycle. If that directory ever moves, it moves here too.
const stateDirName = ".oculus"

// stateDirOpen names the state-directory subtrees that stay reachable. Exactly one: worktrees.
// worktree.DefaultBase puts every session worktree under ~/.oculus/worktrees/<repo>/<name>, so that
// subtree IS ordinary project content — it is where an isolated session's agent works and where the
// editor must be able to read and write, or the whole worktree feature stops working from the app.
// Nothing else under the state directory is content; it is all key material and trust records.
var stateDirOpen = []string{"worktrees"}

// protectedRule is one absolute path Resolve refuses, whatever roots it was constructed with.
type protectedRule struct {
	// path is the literal absolute path (home + a fixed suffix).
	path string
	// real is path with symlinks resolved, or "" when it resolves to itself / doesn't exist. Both
	// forms are matched: see protectedLabel.
	real string
	// file marks a rule that covers exactly one file rather than a whole subtree.
	file bool
	// open lists immediate subdirectory names under path that stay reachable (state dir worktrees).
	open []string
	// label is what the refusal names, in the ~-relative form a user recognises.
	label string
}

// protectedRules is the root-INDEPENDENT half of this guard, and it exists because of a concrete,
// verified escalation chain that the allowed-roots mechanism could not see:
//
//	session.create is capSteer and its Cwd was an arbitrary absolute path → every live session's
//	meta.cwd becomes an allowed root in Hub.fsGuard → fs.read and fs.write are capSteer. So a
//	steerer created a session with Cwd = ~/.oculus and read and wrote the daemon's own state.
//
// What that reached, by name, because a rule whose damage is abstract gets relaxed later:
//
//   - ~/.oculus/daemon.key is the daemon's PRIVATE KEY. The transport has no forward secrecy, so one
//     read of that file decrypts every session ever recorded — past traffic an attacker captured
//     before they ever had access, and all future traffic — with no further foothold needed.
//   - ~/.oculus/AuthKey.p8 is the APNs signing key. It is not this daemon's to lose: it signs pushes
//     for the whole app, so a leak follows the developer account rather than the machine.
//   - ~/.oculus/agents.json holds each custom agent's Command and Args, which daemon/agent/cli later
//     exec's. agent.upsert is capOwner, but the gate is on the RPC, not on the bytes: writing the
//     file directly is arbitrary command execution as the owner with the owner check skipped.
//   - approval-rules.json, worktree-setup-trust.json, credentials.json, devices.json, pairing.json,
//     mcp.json, accounts.json — every persisted trust decision and credential the daemon holds,
//     forgeable by a writer and harvestable by a reader.
//
// The cwd is also validated at session.create now (hub.validateSessionCwd), and that validation is
// the narrower, more easily-bypassed half: it guards ONE entry point, and this hole was created by a
// caller adding a root nobody audited. This rule is the durable one — it holds for any future caller
// that adds a bad root, which is exactly how the escalation happened, so it is deliberately phrased
// as "never, regardless of roots" rather than "not from a session cwd".
//
// The rest of the list is here because a session cwd is ATTACKER-CHOSEN: the same trick that pointed
// a root at ~/.oculus points it at anything else the owner's home holds, so "which directories can
// never be worth editing from a phone" is the only line that holds. Each entry is one of two shapes
// — a credential store whose contents are the owner's identity elsewhere, or a file some other
// program EXECUTES on the owner's behalf (the same shape as .git/config above, which this guard
// already refuses):
//
//   - ~/.ssh — private keys, and authorized_keys, where one appended line is permanent remote login
//     as the owner. ~/.gnupg, ~/.aws, ~/.kube (whose config carries exec credential plugins — a
//     command kubectl runs), ~/.docker, ~/.config/gh, ~/.config/gcloud are the same store for a
//     different service. None is project content; the editor navigating there serves no feature.
//   - ~/Library/Keychains and ~/Library/LaunchAgents — the macOS pair. The first is the credential
//     store for everything else on the machine (binary, so nobody edits it here, but very much worth
//     exfiltrating); the second is a directory where one dropped plist runs a command at every login,
//     and it is where oculusd's OWN launchd agent lives.
//   - the login shell's rc files (.zshrc/.zshenv/.zprofile/.zlogin, .bashrc/.bash_profile/.profile,
//     .config/fish/config.fish). These are not credentials, they are code: `augmentPATH` in
//     daemon/main.go runs `$SHELL -ilc` on every daemon start, so a written rc file executes as the
//     owner the next time the daemon restarts — no user action needed at all.
//   - .netrc, .npmrc, .pypirc — plaintext tokens by design, and a registry token is a supply-chain
//     compromise of everything the owner publishes.
//
// Over-broad rules break legitimate editing, so the list is deliberately NOT "no dotfiles" and not
// "nothing under ~/.config": people keep dotfiles repos, and a project registered at ~/dotfiles or
// ~/.config edits ~/dotfiles/.zshrc, a different path that stays fully writable. Only the live
// locations — the ones another program actually reads — are refused.
//
// Known limits, stated rather than papered over. This is a NAMED list, so it cannot cover the tail:
// ZDOTDIR pointing zsh elsewhere, ~/.local/bin on the owner's PATH, /etc, a credential file some
// tool invents next year. It is also home-relative, so it protects the user the daemon runs as and
// nobody else, and with no home directory at all it protects nothing — though in that case the
// daemon has already fallen back to relative paths of its own (defaultKeyPath returns "oculusd.key"
// in the process's working directory), so there is no known location left to defend. These are
// stopping points, not oversights: the escalation being closed is reachable through a root the
// daemon itself adds, and the named locations are what that root reaches.
func protectedRules() []protectedRule {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return nil
	}
	protectedMu.Lock()
	defer protectedMu.Unlock()
	// Keyed by home rather than computed once, so a test that relocates HOME is actually judged
	// against its own temporary home instead of the developer's real one.
	if protectedCache != nil && protectedHome == home {
		return protectedCache
	}
	dir := func(label string, open ...string) protectedRule {
		return protectedRule{path: filepath.Join(home, filepath.FromSlash(label)), open: open, label: "~/" + label}
	}
	file := func(label string) protectedRule {
		return protectedRule{path: filepath.Join(home, filepath.FromSlash(label)), file: true, label: "~/" + label}
	}
	rules := []protectedRule{
		dir(stateDirName, stateDirOpen...), // the daemon's own state: daemon.key, agents.json, trust records
		dir(".ssh"), dir(".gnupg"), dir(".aws"), dir(".kube"), dir(".docker"),
		dir(".config/gh"), dir(".config/gcloud"),
		dir("Library/Keychains"), dir("Library/LaunchAgents"),
		file(".zshrc"), file(".zshenv"), file(".zprofile"), file(".zlogin"),
		file(".bashrc"), file(".bash_profile"), file(".bash_login"), file(".profile"),
		file(".config/fish/config.fish"),
		file(".netrc"), file(".npmrc"), file(".pypirc"),
	}
	// Resolve each rule ONCE (this is what the cache is for — Resolve runs per fs request, and
	// EvalSymlinks over two dozen paths per read would be felt). Storing both forms is what makes the
	// rule survive a relocated state directory: if ~/.oculus is itself a symlink to /vol/state, a
	// caller who names /vol/state/daemon.key directly matches `real`, and a caller who names
	// ~/.oculus/daemon.key matches `path`.
	for i := range rules {
		if r, err := filepath.EvalSymlinks(rules[i].path); err == nil && r != rules[i].path {
			rules[i].real = r
		}
	}
	protectedCache, protectedHome = rules, home
	return protectedCache
}

var (
	protectedMu    sync.Mutex
	protectedCache []protectedRule
	protectedHome  string
)

// protectedLabel returns the label of the protected path containing cand, or "" when cand is clear.
//
// Every candidate is checked against BOTH forms of every rule, for the same reason Resolve checks
// both forms of the path (see the comment there): one direction catches a link that launders the
// name out of the request, the other catches a link that launders it in. A rule's `open` subtrees are
// tested in both forms too — otherwise a session whose cwd is a worktree under a symlinked state
// directory would resolve to a real path the carve-out no longer recognised, and the editor would
// refuse the very files that session exists to edit.
func protectedLabel(cand string) string {
	for _, r := range protectedRules() {
		for _, base := range [2]string{r.path, r.real} {
			if base == "" {
				continue
			}
			if r.file {
				if cand == base {
					return r.label
				}
				continue
			}
			if cand != base && !strings.HasPrefix(cand, base+string(os.PathSeparator)) {
				continue
			}
			if openSubtree(base, r.open, cand) {
				continue
			}
			return r.label
		}
	}
	return ""
}

// openSubtree reports whether cand sits inside one of base's carved-out subdirectories.
func openSubtree(base string, open []string, cand string) bool {
	for _, name := range open {
		sub := filepath.Join(base, name)
		if cand == sub || strings.HasPrefix(cand, sub+string(os.PathSeparator)) {
			return true
		}
		// The carve-out itself may be a link (a worktrees dir moved to another volume), so match its
		// resolved form as well — a session cwd arrives already resolved.
		if real, err := filepath.EvalSymlinks(sub); err == nil && real != sub {
			if cand == real || strings.HasPrefix(cand, real+string(os.PathSeparator)) {
				return true
			}
		}
	}
	return false
}

// ProtectedPath reports whether p lies inside a protected directory, returning the directory's
// ~-relative label ("~/.oculus") or "" when p is fine. Both the literal and the symlink-resolved
// form of p are judged, so neither direction of link can launder the answer.
//
// It exists so a caller can refuse BEFORE the fact rather than only at the read: the hub uses it to
// reject a session cwd at session.create, which stops the protected directory from ever becoming an
// allowed root in the first place. Guard.Resolve enforces the same rule per operation and is the
// backstop that holds when some future caller adds a root nobody checked.
func ProtectedPath(p string) string {
	if strings.TrimSpace(p) == "" {
		return ""
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return ""
	}
	abs = filepath.Clean(abs)
	for _, cand := range [2]string{resolveExisting(abs), abs} {
		if label := protectedLabel(cand); label != "" {
			return label
		}
	}
	return ""
}

// Guard validates that paths stay inside a set of allowed roots.
type Guard struct{ roots []string }

// New builds a Guard from roots, resolving each to an absolute, symlink-free, cleaned path
// and de-duplicating. Empty/invalid roots are dropped, as is any root that is itself protected.
//
// Dropping such a root is belt-and-braces — Resolve refuses those paths per operation whatever the
// roots say — but it matters for the one caller that reads Roots() directly instead of resolving:
// fs.search hands roots straight to ripgrep, which would otherwise happily grep ~/.oculus/daemon.key
// and stream the match text back to the client. A worktree root under ~/.oculus/worktrees survives,
// because the carve-out is part of the same judgement.
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
		if protectedLabel(abs) != "" {
			continue
		}
		if !seen[abs] {
			seen[abs] = true
			out = append(out, abs)
		}
	}
	return &Guard{roots: out}
}

// Roots returns the allowed roots (absolute, cleaned).
func (g *Guard) Roots() []string { return append([]string(nil), g.roots...) }

// Resolve cleans p to an absolute path and confirms it lies within an allowed root and outside any
// repository-metadata directory, resolving symlinks on the longest existing prefix so a symlink
// can't escape the sandbox. The returned path is the unresolved absolute path (safe to open); the
// containment check uses the resolved one. The target need not exist yet (so writes to new files
// pass).
//
// This is the single choke point for both rules, which is why the metadata check lives here and not
// in Write: applyRename in the hub resolves and then calls os.WriteFile itself, so a check bolted
// onto Guard.Write would have covered fs.write and missed lsp.rename.
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
	// Judged BEFORE the roots check, and on purpose. Being inside a protected directory is not a
	// weaker version of being outside the roots — it is the case where the root itself is the
	// problem, so answering "outside allowed roots" would be wrong as well as unhelpful. Checking it
	// first also means the refusal cannot be turned off by adding a root, which is the whole point.
	for _, cand := range [2]string{real, abs} {
		if label := protectedLabel(cand); label != "" {
			return "", fmt.Errorf("%w: %s is inside %s", ErrProtectedPath, p, label)
		}
	}
	root, ok := g.rootOf(real)
	if !ok {
		return "", fmt.Errorf("path outside allowed roots: %s", p)
	}
	// Checked against both forms on purpose, and they catch opposite tricks. The RESOLVED path
	// catches a link laundering the name out of the request (roots/vcs -> roots/.git); it also
	// covers targets that don't exist yet, since resolveExisting rejoins the non-existent tail, and
	// creating a file is the usual way into .git. The LITERAL path catches the mirror image: a
	// symlink NAMED .git pointing at an ordinary directory would resolve somewhere this guard
	// considers innocent, while git — which resolves that same link when it looks for its gitdir —
	// would still read whatever landed there as its configuration.
	for _, cand := range [2]string{real, abs} {
		if name := vcsMetaComponent(root, cand); name != "" {
			return "", fmt.Errorf("%w: %s is inside %s", ErrVCSMetadata, p, name)
		}
	}
	return abs, nil
}

// rootOf returns the allowed root containing abs.
func (g *Guard) rootOf(abs string) (string, bool) {
	for _, r := range g.roots {
		if abs == r || strings.HasPrefix(abs, r+string(os.PathSeparator)) {
			return r, true
		}
	}
	return "", false
}

// vcsMetaComponent returns the metadata directory name when path descends into one BELOW root, or
// "" otherwise. The walk is root-RELATIVE rather than over the whole absolute path so that a root
// which itself sits under such a directory stays usable — otherwise registering a project that
// happens to live inside a .git subtree would brick the editor for that project entirely, to
// prevent nothing (the owner chose that root; the attack is descending into metadata from a root
// that isn't already there).
func vcsMetaComponent(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return ""
	}
	// A rel that climbs out means path isn't under this root at all. That is routine for the literal
	// candidate — on macOS /var is a symlink to /private/var, so every path under a temp dir differs
	// from its resolved form — and the escape route it produces ("../../..") can easily run through
	// a real ".git" component belonging to some unrelated directory. Judging that would refuse
	// ordinary files for a name that isn't in their path at all.
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return ""
	}
	for _, part := range strings.Split(rel, string(os.PathSeparator)) {
		if vcsMetaDirs[part] {
			return part
		}
	}
	return ""
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

// VCSMetadataComponent returns the repository-metadata component ("​.git"/".hg") a path descends
// into, ROOT-INDEPENDENTLY, or "" if it does not touch one.
//
// This is deliberately a different rule from vcsMetaComponent, which is root-RELATIVE so that a
// project root living inside a .git subtree stays usable. Here there is no root to be relative to:
// the caller is judging a path an AGENT asked to write, and the question is simply "does this reach
// into repository metadata anywhere along the way".
//
// Why it exists: the daemon's own fs RPCs go through Resolve and are already refused, but a
// provider's write tool bypasses all of that — it writes to disk itself, and the approval system
// only ever decided "may this tool run", never "is this target safe". That gap is a deferred shell:
// an agent writes .git/hooks/pre-commit into its worktree, the user approves what looks like an
// ordinary file write, and the daemon's OWN `git commit`/`git merge` at finish time executes it as
// the owner. The relay makes that reachable from anywhere, which is the same standard that got PTY
// sessions parked.
//
// The path is normalized first, so "wt/subdir/../.git/config" and a symlink through an existing
// ancestor are both judged on where they actually land.
func VCSMetadataComponent(path string) string {
	if strings.TrimSpace(path) == "" {
		return ""
	}
	for _, part := range strings.Split(NormalizePath(path), string(os.PathSeparator)) {
		if vcsMetaDirs[part] {
			return part
		}
	}
	return ""
}
