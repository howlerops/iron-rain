package mcp

import "context"

// Passing the MCP config to a harness at spawn time.
//
// It rides the CONTEXT rather than a setter on the Provider because providers are daemon-global while
// MCP scoping is per-session (a server can be scoped to one project). A provider-level setter would
// race whenever two sessions in different projects start at once — the second would silently get the
// first's servers.

type configKey struct{}

// WithConfig attaches the rendered MCP configuration for the session being started.
func WithConfig(ctx context.Context, cfg Config) context.Context {
	return context.WithValue(ctx, configKey{}, cfg)
}

// FromContext returns the MCP configuration for this spawn, if any.
func FromContext(ctx context.Context) (Config, bool) {
	cfg, ok := ctx.Value(configKey{}).(Config)
	return cfg, ok
}

// Config carries one spawn's servers, pre-rendered per harness so an adapter doesn't need to know
// the registry type or each other harness's format.
type Config struct {
	Servers []Server
	// Exclusive tells a harness to use ONLY these servers and ignore its own MCP configuration.
	// Off by default: enabling it for someone whose servers we haven't imported would silently
	// remove tools they rely on.
	Exclusive bool
}

// Claude returns the Claude Agent SDK mcpServers JSON ("" = nothing to inject).
func (c Config) Claude() string { return ClaudeConfigJSON(c.Servers) }

// OpenCode returns an OPENCODE_CONFIG_CONTENT document ("" = nothing to inject).
func (c Config) OpenCode() string { return OpenCodeConfigJSON(c.Servers) }

// CLIFile writes a --mcp-config file and returns its path ("" = nothing to inject).
func (c Config) CLIFile() string { return WriteCLIConfig(c.Servers) }
