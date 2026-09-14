package main

import (
	"os"
	"strings"
	"testing"
)

// The MCP gateway base must be set BEFORE any provider is started.
//
// gatewayServers rewrites a stdio MCP server into an authenticated gateway URL only when the base is
// known, and otherwise falls back to handing over the raw stdio definition. SetMCPGatewayBase used to
// run ~250 lines below enableProviders, so at the moment opencode's OPENCODE_CONFIG_CONTENT was
// rendered the base was always "" — the fallback was the ONLY branch opencode ever took. Every
// opencode session spawned its own copy of every MCP server, with the real command line and the real
// credentials in its environment.
//
// The consequence is an approval bypass. A tool call that never reaches Gateway.ServeHTTP never
// reaches authorizeMCPTool, so no mode check, no approval rule, no approval card and no .git refusal
// runs for it — while the identical call on a claude-code session is gated, because that adapter
// consumes the per-session config this rewrite produces.
//
// Asserted on source order because that IS the bug: both statements are correct in isolation.
func TestTheMCPGatewayBaseIsSetBeforeProvidersStart(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)

	base := strings.Index(body, "h.SetMCPGatewayBase(")
	if base < 0 {
		t.Fatal("SetMCPGatewayBase is gone — harnesses will be handed raw stdio server definitions, " +
			"credentials included, and their tool calls will bypass every approval rule")
	}
	providers := strings.Index(body, "enableProviders(")
	if providers < 0 {
		t.Fatal("could not find enableProviders")
	}
	if base > providers {
		t.Errorf("SetMCPGatewayBase (offset %d) runs AFTER enableProviders (offset %d).\n\n"+
			"Any harness whose MCP config is rendered during provider start-up gets the raw stdio "+
			"fallback: its own copy of every server, with real credentials, and tool calls that never "+
			"transit the daemon and so are never checked against modes or approval rules.", base, providers)
	}
}
