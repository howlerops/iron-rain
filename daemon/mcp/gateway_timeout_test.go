package mcp

import "testing"

// The approval step must outlast the proxied-call timeout.
//
// ServeHTTP used to hand the authorizer the request context, bounded by requestTimeout (2 minutes) —
// so an MCP approval was force-denied at two minutes however long the authorizer was willing to
// wait. Its own window is ten minutes. The card vanished from every device, the transcript recorded
// "the request was cancelled before anyone answered" (the wrong cause), and tapping Allow afterwards
// returned "no such approval".
//
// hub.mcpApprovalTimeout is 10m. This asserts the relationship rather than the number, so moving
// either one keeps the invariant or fails here.
func TestTheApprovalWindowOutlastsTheCallTimeout(t *testing.T) {
	if authorizeTimeout <= requestTimeout {
		t.Fatalf("authorizeTimeout (%s) must exceed requestTimeout (%s), or every MCP approval is "+
			"force-denied when the proxied-call timer fires and the user is told the wrong reason",
			authorizeTimeout, requestTimeout)
	}
	const hubApprovalWindow = 10 // minutes; hub.mcpApprovalTimeout
	if authorizeTimeout.Minutes() <= hubApprovalWindow {
		t.Fatalf("authorizeTimeout (%s) must exceed the hub's %d-minute approval window, or the "+
			"hub's own timer can never be the one that fires", authorizeTimeout, hubApprovalWindow)
	}
}
