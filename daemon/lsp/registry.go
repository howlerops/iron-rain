package lsp

import (
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// languageID maps a file extension to its LSP languageId, or "" if unsupported.
func languageID(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go":
		return "go"
	case ".swift":
		return "swift"
	case ".ts", ".tsx":
		return "typescript"
	case ".js", ".jsx":
		return "javascript"
	case ".py":
		return "python"
	case ".rs":
		return "rust"
	case ".c":
		return "c"
	case ".h", ".hpp", ".cc", ".cpp":
		return "cpp"
	}
	return ""
}

// serverCommand resolves the installed language server for a languageId. It returns
// the resolved binary path, its args, and ok=false when no server is installed
// (graceful degrade). Special cases: swift falls back to `xcrun --find sourcekit-lsp`
// and python falls back to pylsp.
func serverCommand(langID string) (command string, args []string, ok bool) {
	switch langID {
	case "go":
		return lookPath("gopls", nil)
	case "swift":
		if p, a, found := lookPath("sourcekit-lsp", nil); found {
			return p, a, true
		}
		if p := xcrunFind("sourcekit-lsp"); p != "" {
			return p, nil, true
		}
		return "", nil, false
	case "typescript", "javascript":
		return lookPath("typescript-language-server", []string{"--stdio"})
	case "python":
		if p, a, found := lookPath("pyright-langserver", []string{"--stdio"}); found {
			return p, a, true
		}
		return lookPath("pylsp", nil)
	case "rust":
		return lookPath("rust-analyzer", nil)
	case "c", "cpp":
		return lookPath("clangd", nil)
	}
	return "", nil, false
}

// lookPath resolves bin on PATH, returning it with the given args.
func lookPath(bin string, args []string) (string, []string, bool) {
	p, err := exec.LookPath(bin)
	if err != nil {
		return "", nil, false
	}
	return p, args, true
}

// xcrunFind resolves a tool via Xcode's `xcrun --find`, used for sourcekit-lsp when
// it isn't directly on PATH. Returns "" if xcrun or the tool is unavailable.
func xcrunFind(tool string) string {
	if _, err := exec.LookPath("xcrun"); err != nil {
		return ""
	}
	out, err := exec.Command("xcrun", "--find", tool).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// Supported reports whether a file has a known and installed language server.
func Supported(path string) bool {
	langID := languageID(path)
	if langID == "" {
		return false
	}
	_, _, ok := serverCommand(langID)
	return ok
}

// rootMarkers lists the project-root marker files for a languageId, checked in order.
func rootMarkers(langID string) []string {
	switch langID {
	case "go":
		return []string{"go.mod"}
	case "swift":
		return []string{"Package.swift"}
	case "typescript", "javascript":
		return []string{"package.json", "tsconfig.json"}
	case "python":
		return []string{"pyproject.toml", "setup.py"}
	case "rust":
		return []string{"Cargo.toml"}
	case "c", "cpp":
		return []string{"compile_commands.json", ".git"}
	}
	return nil
}

// findRoot walks up from the file's directory to the nearest language-specific
// marker (or a .git directory), falling back to the file's own directory.
func findRoot(path, langID string) string {
	start := filepath.Dir(path)
	markers := rootMarkers(langID)
	cur := start
	for {
		for _, m := range markers {
			if exists(filepath.Join(cur, m)) {
				return cur
			}
		}
		// A .git directory always bounds a project, regardless of language.
		if exists(filepath.Join(cur, ".git")) {
			return cur
		}
		parent := filepath.Dir(cur)
		if parent == cur { // reached filesystem root
			break
		}
		cur = parent
	}
	return start
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// pathToURI converts an absolute POSIX path to a file:// URI, escaping as needed
// (e.g. spaces -> %20) while preserving path separators.
func pathToURI(path string) string {
	p := filepath.ToSlash(path)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	u := url.URL{Scheme: "file", Path: p}
	return u.String()
}

// uriToPath converts a file:// URI back to a filesystem path, undoing escaping.
// Non-file or unparseable URIs are returned with the scheme prefix stripped.
func uriToPath(uri string) string {
	u, err := url.Parse(uri)
	if err == nil && u.Scheme == "file" {
		return u.Path
	}
	return strings.TrimPrefix(uri, "file://")
}
