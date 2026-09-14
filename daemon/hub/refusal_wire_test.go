package hub_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/howlerops/oculus/daemon/crypto"
	"github.com/howlerops/oculus/daemon/hub"
	"github.com/howlerops/oculus/daemon/protocol"
	"github.com/howlerops/oculus/daemon/transport"
)

// The refusal seam, driven end to end over a real encrypted connection.
//
// The previous test for this built a protocol.Error{Code: ErrorForbidden} by hand and asserted the
// decoded Code was ErrorForbidden — it supplied the value it checked. Nothing in it reached
// requireCapability or sendErrCode, which is where the daemon actually decides a refusal gets
// classified, so deleting that classification left the whole suite green. The Swift side had the
// mirror-image problem: it started from a hand-written JSON string containing "code":"forbidden".
// Each end tested its own assumption about the other and neither tested the join.
//
// This one sends a real message from a real observer and reads what comes back off the wire.
func TestARefusalArrivesClassifiedOverTheWire(t *testing.T) {
	conn, r := newObserver(t)
	send(t, conn, "req-1", protocol.TypeDeviceList, struct{}{})

	env := waitEnvelope(t, r, "req-1")
	if env.Type != protocol.TypeError {
		t.Fatalf("device.list was ANSWERED for an observer (type %q) — the gate is gone", env.Type)
	}
	var perr protocol.Error
	if err := json.Unmarshal(env.Payload, &perr); err != nil {
		t.Fatal(err)
	}
	if perr.Code != protocol.ErrorForbidden {
		t.Errorf("code = %q, want %q\n\nThe client cannot tell a refusal from a failure without it, "+
			"and every loader that swallows its error renders the screen's empty state for both — "+
			"which is how the Devices screen came to say \"No devices enrolled\" about a Mac with "+
			"devices enrolled.", perr.Code, protocol.ErrorForbidden)
	}
	if !strings.Contains(perr.Message, "list enrolled devices") {
		t.Errorf("message = %q — a refusal has to name what was refused", perr.Message)
	}
}

// The editor gate is the one refusal that carries an extra sentence, because being allowed to read a
// transcript but not hover a symbol is not self-evident. Asserting on a reason string the test itself
// passed in proves nothing; this reads what the daemon actually sends.
func TestTheEditorRefusalCarriesItsReason(t *testing.T) {
	conn, r := newObserver(t)
	send(t, conn, "req-2", protocol.TypeLSPHover, protocol.LSPPosReq{Path: "/tmp/x.go", Line: 1})

	env := waitEnvelope(t, r, "req-2")
	if env.Type != protocol.TypeError {
		t.Fatalf("lsp.hover answered an observer (type %q)", env.Type)
	}
	var perr protocol.Error
	if err := json.Unmarshal(env.Payload, &perr); err != nil {
		t.Fatal(err)
	}
	if perr.Code != protocol.ErrorForbidden {
		t.Errorf("code = %q, want %q", perr.Code, protocol.ErrorForbidden)
	}
	if !strings.Contains(perr.Message, "file browser") {
		t.Errorf("message = %q\n\nrequireEditorRead is supposed to explain WHY the editor is closed to "+
			"someone who can read the transcript. Without the reason this reads as a broken feature, "+
			"which is the honest guess from the other side.", perr.Message)
	}
}

// And the promise that keeps all of it free: with sharing off — the default, and the only state a
// solo install is ever in — the same request is answered, not refused.
func TestTheSameRequestSucceedsForASoloUser(t *testing.T) {
	h := hub.New() // enforcement off, as shipped

	daemonKP, _ := crypto.GenerateKeyPair()
	conn := connectClient(t, h, daemonKP)
	r := newReader(conn)

	send(t, conn, "req-3", protocol.TypeDeviceList, struct{}{})

	env := waitEnvelope(t, r, "req-3")
	if env.Type == protocol.TypeError {
		t.Fatalf("a solo user was refused their own device list: %s\n\nEvery gate in the dispatch "+
			"switch is predicated on this being impossible.", env.Payload)
	}
}

// waitEnvelope returns the first envelope correlated to id, whatever its type. waitOK cannot be used
// here: it fails the test on an error envelope, and an error envelope is the thing under test.
func waitEnvelope(t *testing.T, r *clientReader, id string) protocol.Envelope {
	t.Helper()
	var out protocol.Envelope
	r.waitFor(t, "reply "+id, func(e protocol.Envelope) bool {
		if e.ID != id {
			return false
		}
		out = e
		return true
	})
	return out
}

// newObserver builds a hub with sharing on and returns a SECOND connection that has been demoted to
// observer through the real role.grant path, plus its reader.
//
// The demotion has to be done this way. Every connection the test harness makes authenticates as the
// owner's own device, so roleForConn resolves it to RoleOwner and merely flipping enforcement on
// refuses nothing — the first version of this test asserted a refusal against a connection that was
// the owner, and passed for the wrong reason in the other direction.
func newObserver(t *testing.T) (*transport.Conn, *clientReader) {
	t.Helper()
	h := hub.New()
	h.SetRolesEnabled(true)

	daemonKP, _ := crypto.GenerateKeyPair()
	owner := connectClient(t, h, daemonKP)
	ownerReader := newReader(owner)

	guest := connectClient(t, h, daemonKP)
	guestReader := newReader(guest)
	send(t, guest, "id", protocol.TypeClientIdentify, protocol.ClientIdentify{Name: "Watcher"})
	guestReader.waitOK(t, "id")

	send(t, owner, "grant", protocol.TypeRoleGrant, protocol.RoleGrant{Name: "Watcher", Role: "observer"})
	ownerReader.waitOK(t, "grant")
	return guest, guestReader
}
