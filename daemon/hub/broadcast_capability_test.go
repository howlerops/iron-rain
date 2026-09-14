package hub

import (
	"encoding/json"
	"testing"

	"github.com/howlerops/oculus/daemon/protocol"
	"github.com/howlerops/oculus/daemon/transport"
)

// A push must carry the capability of the request that would have returned it.
//
// Gating a request and then announcing its answer to everyone gates nothing. device.list refuses a
// non-owner with an explicit reason — and seventeen lines below it, device.revoke pushed that same
// list to every connected client, so an invited observer learned every enrolled device's public key,
// label and last-seen time without asking for anything. The same shape held for the MCP server list
// (command lines and endpoint URLs), the language server's diagnostics (a project path and a line of
// its source), and filesystem change notifications (absolute paths on the owner's machine).
//
// This is the structural half of the fix: whatever else changes, an owner-only payload must not leave
// through a fan-out.
func TestAnOwnerOnlyPayloadIsNotBroadcastToEveryone(t *testing.T) {
	h := newRoleTestHub(t)
	owner, observer := h.conns[RoleOwner], h.conns[RoleObserver]

	h.hub.broadcastWithCapability(protocol.TypeDeviceList, protocol.DeviceList{
		Devices: []protocol.DeviceInfo{{Pub: "abc123", Label: "Jacob's iPhone"}},
	}, capOwner)

	if got := h.drain(owner); len(got) != 1 {
		t.Fatalf("the owner received %d frames, want 1 — the fan-out has to still reach the people it is for", len(got))
	}
	if got := h.drain(observer); len(got) != 0 {
		t.Errorf("an observer received the device list: %s\n\nThis is the exact inventory device.list "+
			"refuses them — every enrolled device's public key, label and last-seen time — arriving "+
			"because the owner revoked something.", got[0])
	}
}

// The steer-level version of the same rule, for the three payloads that sit at capSteer: the MCP
// server list, language-server diagnostics, and filesystem change notices.
func TestASteerOnlyPayloadReachesSteerersButNotWatchers(t *testing.T) {
	h := newRoleTestHub(t)

	h.hub.broadcastWithCapability(protocol.TypeLSPDiagnostics, protocol.LSPDiagnostics{
		Path: "/Users/jacob/projects/secret/main.go",
	}, editorReadCap)

	if got := h.drain(h.conns[RoleSteerer]); len(got) != 1 {
		t.Errorf("a steerer got %d diagnostics frames, want 1 — they may open this file in the editor, "+
			"so withholding the diagnostics would break the feature for them", len(got))
	}
	if got := h.drain(h.conns[RoleOwner]); len(got) != 1 {
		t.Errorf("the owner got %d diagnostics frames, want 1", len(got))
	}
	if got := h.drain(h.conns[RoleObserver]); len(got) != 0 {
		t.Errorf("an observer received a diagnostic naming a path on the owner's machine: %s\n\n"+
			"requireEditorRead was added precisely so they could not read project files this way.", got[0])
	}
}

// The promise that makes all of the above free. With sharing off — the default, and the only state a
// solo install is ever in — a capability-filtered broadcast is the unfiltered one it replaced.
func TestASoloUserStillReceivesEveryBroadcast(t *testing.T) {
	h := newRoleTestHub(t)
	h.hub.roles.SetEnabled(false) // as shipped

	for _, c := range []capability{capWatch, capSteer, capApprove, capOwner} {
		h.hub.broadcastWithCapability(protocol.TypeDeviceList, protocol.DeviceList{}, c)
	}
	for role, conn := range h.conns {
		if got := h.drain(conn); len(got) != 4 {
			t.Fatalf("with sharing off, the connection registered as %q received %d of 4 broadcasts — "+
				"every one of these filters must be invisible to someone running this alone", role, len(got))
		}
	}
}

// The tests above prove the FILTER works. This one proves it is actually used.
//
// Without it they are all satisfiable by a helper nobody calls: reverting any of these four call
// sites to a plain h.broadcast leaves every assertion above green while putting the leak straight
// back. The payloads are named individually rather than by a blanket rule because most broadcasts
// SHOULD go to everyone — a session event, a heartbeat, the participant list — and a rule that
// flagged those would be turned off within a week.
func TestTheLeakingBroadcastsActuallyUseTheFilter(t *testing.T) {
	ownerOnly := map[string][]string{
		"hub.go": {"TypeDeviceList", "TypeLoopList", "TypeApprovalRulesChanged"},
	}
	steerOnly := map[string][]string{
		"hub.go":         {"TypeLSPDiagnostics", "TypeFSChange", "TypeIssueList", "TypeIntegrationStatus", "TypeActivityEvent"},
		"mcp.go":         {"TypeMCPChanged"},
		"turn.go":        {"TypeActivityEvent"},
		"heartbeat.go":   {"TypeHandoffList"},
		"preview_dom.go": {"TypePreviewDOMAsk"},
	}

	check := func(file, typ string) {
		t.Helper()
		src, err := readFileString(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range splitLines(src) {
			if !containsBoth(line, "h.broadcast(protocol."+typ, "") {
				continue
			}
			t.Errorf("%s: %s is broadcast unfiltered:\n  %s\n\nThis payload is refused to a "+
				"non-owner when they ASK for it. Sending it to every connected client makes that "+
				"refusal decorative. Use h.broadcastWithCapability.", file, typ, line)
		}
	}
	for file, types := range ownerOnly {
		for _, typ := range types {
			check(file, typ)
		}
	}
	for file, types := range steerOnly {
		for _, typ := range types {
			check(file, typ)
		}
	}
}

func splitLines(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

func containsBoth(s, a, b string) bool {
	return indexOf(s, a) >= 0 && (b == "" || indexOf(s, b) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// --- harness ---

type roleTestHub struct {
	hub   *Hub
	conns map[string]*transport.Conn
}

// newRoleTestHub builds a Hub with one connection per role and sharing ON, so the filters are live.
// The outbound queues are read directly rather than through a writer goroutine: what is being
// asserted is what got ENQUEUED for each connection, which is where the filter applies.
func newRoleTestHub(t *testing.T) *roleTestHub {
	t.Helper()
	h := &Hub{roles: newRoleRegistry(), clients: map[*transport.Conn]*hubClient{}}
	h.roles.SetEnabled(true)
	out := &roleTestHub{hub: h, conns: map[string]*transport.Conn{}}
	for _, role := range []string{RoleOwner, RoleSteerer, RoleObserver} {
		conn := &transport.Conn{}
		h.clients[conn] = &hubClient{conn: conn, ch: make(chan []byte, 8), done: make(chan struct{})}
		h.roles.setRole(conn, role)
		out.conns[role] = conn
	}
	return out
}

// drain returns everything currently queued for a connection.
func (r *roleTestHub) drain(conn *transport.Conn) []string {
	c := r.hub.clients[conn]
	var out []string
	for {
		select {
		case raw := <-c.ch:
			out = append(out, string(raw))
		default:
			return out
		}
	}
}

// The wire shape has to survive the filter — a payload that arrives unparseable is no better than one
// that does not arrive. Cheap to check here and it keeps the tests above honest about what they read.
func TestAFilteredBroadcastIsStillAWellFormedFrame(t *testing.T) {
	h := newRoleTestHub(t)
	h.hub.broadcastWithCapability(protocol.TypeDeviceList, protocol.DeviceList{
		Devices: []protocol.DeviceInfo{{Pub: "abc", Label: "Mac"}},
	}, capOwner)
	got := h.drain(h.conns[RoleOwner])
	if len(got) != 1 {
		t.Fatalf("want 1 frame, got %d", len(got))
	}
	var env struct {
		Type    string          `json:"type"`
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal([]byte(got[0]), &env); err != nil {
		t.Fatal(err)
	}
	if env.Type != protocol.TypeDeviceList {
		t.Errorf("type = %q, want %q", env.Type, protocol.TypeDeviceList)
	}
	var list protocol.DeviceList
	if err := json.Unmarshal(env.Payload, &list); err != nil || len(list.Devices) != 1 {
		t.Errorf("payload did not survive: %v (%s)", err, env.Payload)
	}
}
