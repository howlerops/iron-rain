package lsp

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
)

// installRecipe describes how to install a language server on the host. Command is the argv
// to run; Prereq is the tool that must be present to run it (go, npm, rustup, brew). A recipe
// with an empty Command is a known-but-not-scriptable server (e.g. sourcekit-lsp ships with
// Xcode) — surfaced as a requirement, not an install button.
type installRecipe struct {
	Binary  string
	Prereq  string
	Command []string
	Label   string
}

func recipeFor(langID string) (installRecipe, bool) {
	switch langID {
	case "go":
		return installRecipe{Binary: "gopls", Prereq: "go",
			Command: []string{"go", "install", "golang.org/x/tools/gopls@latest"}, Label: "gopls"}, true
	case "typescript", "javascript":
		return installRecipe{Binary: "typescript-language-server", Prereq: "npm",
			Command: []string{"npm", "install", "-g", "typescript-language-server", "typescript"},
			Label:   "typescript-language-server"}, true
	case "python":
		return installRecipe{Binary: "pyright-langserver", Prereq: "npm",
			Command: []string{"npm", "install", "-g", "pyright"}, Label: "pyright"}, true
	case "rust":
		return installRecipe{Binary: "rust-analyzer", Prereq: "rustup",
			Command: []string{"rustup", "component", "add", "rust-analyzer"}, Label: "rust-analyzer"}, true
	case "c", "cpp":
		return installRecipe{Binary: "clangd", Prereq: "brew",
			Command: []string{"brew", "install", "llvm"}, Label: "clangd (llvm)"}, true
	case "swift":
		return installRecipe{Binary: "sourcekit-lsp", Label: "Xcode / Swift toolchain"}, true
	}
	return installRecipe{}, false
}

// ServerInfo reports the language-server situation for a file: what language it is, whether a
// server is installed, and whether we can install one (a scripted recipe whose prerequisite
// tool is present).
type ServerInfo struct {
	Language     string
	Binary       string
	Installed    bool
	Installable  bool
	InstallLabel string
}

// InfoForPath returns the server info for a file (Language "" if the extension is unsupported).
func InfoForPath(path string) ServerInfo {
	langID := languageID(path)
	if langID == "" {
		return ServerInfo{}
	}
	_, _, installed := serverCommand(langID)
	info := ServerInfo{Language: langID, Installed: installed}
	if r, ok := recipeFor(langID); ok {
		info.Binary = r.Binary
		info.InstallLabel = r.Label
		if !installed && len(r.Command) > 0 {
			info.Installable = r.Prereq == "" || onPath(r.Prereq)
		}
	}
	return info
}

// Install runs the scripted installer for a file's language server (blocking — the caller
// runs it off the hot path). Returns combined stdout/stderr. Errors if there's no scripted
// recipe or its prerequisite tool is missing.
func Install(ctx context.Context, path string) (string, error) {
	langID := languageID(path)
	r, ok := recipeFor(langID)
	if !ok || len(r.Command) == 0 {
		return "", fmt.Errorf("no scripted installer for %s (install %s manually)", langID, r.Label)
	}
	if r.Prereq != "" && !onPath(r.Prereq) {
		return "", fmt.Errorf("%s is required to install %s but isn't installed", r.Prereq, r.Label)
	}
	cmd := exec.CommandContext(ctx, r.Command[0], r.Command[1:]...)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Run(); err != nil {
		return tail(buf.String(), 2000), fmt.Errorf("install failed: %w", err)
	}
	return tail(buf.String(), 2000), nil
}

func onPath(bin string) bool {
	_, err := exec.LookPath(bin)
	return err == nil
}

// tail returns the last n bytes of s (install output can be long; we only surface the end).
func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}
