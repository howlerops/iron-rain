package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Browsing the public MCP registry.
//
// Adding a server currently means knowing its npm package and argv by heart. The official registry
// (registry.modelcontextprotocol.io) publishes that metadata, so the app can offer search-and-install
// instead of type-it-from-memory.
//
// Two deliberate limits. Nothing here INSTALLS anything on its own — a directory entry becomes a
// pre-filled form the user confirms, because a one-tap "install" of a third-party command that then
// runs with the user's credentials is exactly the kind of thing that should require a look first.
// And the registry is treated as untrusted input: names and descriptions are bounded, and a package
// whose runtime we don't recognize is surfaced as unsupported rather than guessed at.

// directoryURL is the official registry's server listing (API frozen at v0.1).
const directoryURL = "https://registry.modelcontextprotocol.io/v0/servers"

// directoryTimeout bounds a browse.
const directoryTimeout = 15 * time.Second

// maxDirectoryResults caps what one search returns.
const maxDirectoryResults = 50

// DirectoryEntry is one published server, reduced to what an install form needs.
type DirectoryEntry struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Version     string `json:"version,omitempty"`
	// Command/Args are the suggested local invocation, empty when the entry has no runtime we
	// recognize (in which case Unsupported explains why).
	Command     string   `json:"command,omitempty"`
	Args        []string `json:"args,omitempty"`
	URL         string   `json:"url,omitempty"`      // for remotes
	Transport   string   `json:"transport"`          // stdio | http
	EnvKeys     []string `json:"env_keys,omitempty"` // credentials the server expects
	Unsupported string   `json:"unsupported,omitempty"`
}

// BrowseDirectory searches the public registry. query is matched by the registry itself; an empty
// query returns the first page.
func BrowseDirectory(ctx context.Context, query string) ([]DirectoryEntry, error) {
	ctx, cancel := context.WithTimeout(ctx, directoryTimeout)
	defer cancel()

	u := directoryURL + "?limit=" + fmt.Sprint(maxDirectoryResults)
	if q := strings.TrimSpace(query); q != "" {
		u += "&search=" + url.QueryEscape(q)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("the MCP registry returned HTTP %d", resp.StatusCode)
	}

	var body struct {
		Servers []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
			Version     string `json:"version"`
			Packages    []struct {
				RegistryType string `json:"registryType"`
				Identifier   string `json:"identifier"`
				Version      string `json:"version"`
				Transport    struct {
					Type string `json:"type"`
					URL  string `json:"url"`
				} `json:"transport"`
				EnvironmentVariables []struct {
					Name string `json:"name"`
				} `json:"environmentVariables"`
			} `json:"packages"`
			Remotes []struct {
				Type string `json:"type"`
				URL  string `json:"url"`
			} `json:"remotes"`
		} `json:"servers"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("unreadable registry response: %w", err)
	}

	out := make([]DirectoryEntry, 0, len(body.Servers))
	for _, s := range body.Servers {
		e := DirectoryEntry{
			Name:        clip(s.Name, 120),
			Description: clip(s.Description, 400),
			Version:     clip(s.Version, 40),
		}
		switch {
		case len(s.Remotes) > 0:
			e.Transport = "http"
			e.URL = s.Remotes[0].URL
		case len(s.Packages) > 0:
			p := s.Packages[0]
			e.Transport = "stdio"
			cmd, args, ok := runtimeFor(p.RegistryType, p.Identifier, p.Version)
			if !ok {
				e.Unsupported = "packaged for " + p.RegistryType + ", which this host can't launch"
			}
			e.Command, e.Args = cmd, args
			for _, ev := range p.EnvironmentVariables {
				e.EnvKeys = append(e.EnvKeys, ev.Name)
			}
		default:
			e.Unsupported = "no runnable package or remote endpoint published"
		}
		out = append(out, e)
	}
	return out, nil
}

// runtimeFor maps a registry package type onto a local command. Only runtimes whose one-shot runner
// is unambiguous are supported; anything else is reported rather than guessed, because a wrong guess
// here executes an arbitrary third-party command.
func runtimeFor(registryType, identifier, version string) (string, []string, bool) {
	spec := identifier
	if version != "" && !hasVersionSuffix(identifier) {
		spec = identifier + "@" + version
	}
	switch strings.ToLower(registryType) {
	case "npm":
		return "npx", []string{"-y", spec}, true
	case "pypi":
		return "uvx", []string{spec}, true
	case "oci", "docker":
		return "docker", []string{"run", "-i", "--rm", spec}, true
	default:
		return "", nil, false
	}
}

// hasVersionSuffix reports whether an identifier already pins a version. A leading "@" is an npm
// SCOPE (@scope/name), not a version separator — treating it as one silently dropped the version
// from every scoped package, which is most of the official MCP servers.
func hasVersionSuffix(identifier string) bool {
	return strings.Contains(strings.TrimPrefix(identifier, "@"), "@")
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}
