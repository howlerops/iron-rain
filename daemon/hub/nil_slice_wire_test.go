package hub_test

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/howlerops/oculus/daemon/crypto"
	"github.com/howlerops/oculus/daemon/hub"
	"github.com/howlerops/oculus/daemon/issues"
	"github.com/howlerops/oculus/daemon/protocol"
)

// A nil slice is NOT an omitted key — it is `null` on the wire.
//
// The client declares these as non-optional Swift arrays, and `null` into a non-optional array
// throws valueNotFound, which unwinds the decode of the WHOLE message. Every one of these loaders
// swallows that with `try?`, so the failure is silent: the screen renders its empty state and the
// user is told there is nothing there.
//
// All three producers reach their empty case on ordinary paths — no tracker connected, an empty log
// ring, a rename that matched nothing — so this is the everyday behaviour, not an edge.
//
// Driven over the real connection with an empty hub, because the bug is in what the PRODUCER emits;
// asserting on a hand-built struct would only prove that Go marshals a nil slice as null, which
// nobody doubted.
func TestListRepliesNeverSendNullForAnEmptyList(t *testing.T) {
	h := hub.New()
	// A manager with NO tracker connected — the exact state the empty reply comes from.
	h.SetIssues(issues.NewManager(filepath.Join(t.TempDir(), "issues.json"), nil))
	daemonKP, _ := crypto.GenerateKeyPair()
	conn := connectClient(t, h, daemonKP)
	r := newReader(conn)

	cases := []struct {
		what    string
		id      string
		msgType string
		payload any
		field   string
		why     string
	}{
		{
			what: "the ticket-board project picker", id: "n1",
			msgType: protocol.TypeIssueProjects, payload: struct{}{}, field: "projects",
			why: "reached whenever no tracker is connected, or every connected tracker's Projects() " +
				"call failed — the very case the code comments say must not blank the picker",
		},
		{
			what: "the daemon log panel", id: "n2",
			msgType: protocol.TypeLogSubscribe, payload: struct{}{}, field: "lines",
			why: "reached on an empty ring, and its failure path also clears the client's subscribed " +
				"flag while the daemon keeps streaming to it",
		},
	}

	for _, tc := range cases {
		send(t, conn, tc.id, tc.msgType, tc.payload)
		env := waitEnvelope(t, r, tc.id)
		if env.Type == protocol.TypeError {
			t.Fatalf("%s: %s", tc.what, env.Payload)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(env.Payload, &fields); err != nil {
			t.Fatalf("%s: %v", tc.what, err)
		}
		raw, ok := fields[tc.field]
		if !ok {
			t.Errorf("%s: the reply has no %q key at all", tc.what, tc.field)
			continue
		}
		if strings.TrimSpace(string(raw)) == "null" {
			t.Errorf("%s sent %q: null.\n\nThe client declares this as a non-optional array, so null "+
				"throws and unwinds the decode of the whole message — and the loader swallows that, "+
				"so the screen just renders empty with no error. This case is %s.",
				tc.what, tc.field, tc.why)
		}
	}
}
