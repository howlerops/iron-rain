package hub_test

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/howlerops/oculus/daemon/crypto"
	"github.com/howlerops/oculus/daemon/hub"
	"github.com/howlerops/oculus/daemon/project"
	"github.com/howlerops/oculus/daemon/protocol"
)

// waitEvent waits for the next unsolicited event of a type and returns its payload. Events, unlike
// replies, carry no id, so they are matched on type alone.
func (r *clientReader) waitEvent(t *testing.T, typ string) json.RawMessage {
	t.Helper()
	var out json.RawMessage
	r.waitFor(t, "event "+typ, func(e protocol.Envelope) bool {
		if e.Type == typ {
			out = e.Payload
			return true
		}
		return false
	})
	return out
}

// Subscribing must deliver the capability manifest and the current facts, and deliver them BEFORE
// the transcript — a client that learns them afterwards paints its first screenful with no mode
// indicator and pops one in, which reads as a bug even when the value is right.
//
// This is an e2e test over the real wire rather than a unit test on the encoder because the bug it
// guards against is one of DELIVERY: the events existed and were correct, and the question was
// whether a subscribing client actually received them.
func TestSubscribeDeliversCapabilitiesAndFacts(t *testing.T) {
	reg, _ := project.Load(filepath.Join(t.TempDir(), "projects.json"))
	h := hub.New()
	h.Register(&cwdProvider{})
	h.SetProjects(reg)
	daemonKP, _ := crypto.GenerateKeyPair()
	conn := connectClient(t, h, daemonKP)
	r := newReader(conn)

	send(t, conn, "c1", protocol.TypeSessionCreate, protocol.SessionCreate{Provider: "fake", Cwd: t.TempDir()})
	var sess protocol.Session
	if err := json.Unmarshal(r.waitOK(t, "c1"), &sess); err != nil {
		t.Fatalf("create decode: %v", err)
	}

	send(t, conn, "s1", protocol.TypeSessionSubscribe, protocol.SessionRef{SessionID: sess.ID})
	r.waitOK(t, "s1")

	caps := protocol.SessionCapabilities{}
	if raw := r.waitEvent(t, protocol.TypeSessionCapabilities); raw != nil {
		if err := json.Unmarshal(raw, &caps); err != nil {
			t.Fatalf("capabilities decode: %v", err)
		}
	}
	if caps.SessionID != sess.ID {
		t.Fatalf("capabilities were for %q, want %q", caps.SessionID, sess.ID)
	}

	facts := protocol.SessionFacts{}
	if raw := r.waitEvent(t, protocol.TypeSessionFacts); raw != nil {
		if err := json.Unmarshal(raw, &facts); err != nil {
			t.Fatalf("facts decode: %v", err)
		}
	}
	if facts.SessionID != sess.ID {
		t.Fatalf("facts were for %q, want %q", facts.SessionID, sess.ID)
	}
	// A session that has never had its mode set still reports one. Reporting nothing would leave a
	// client with no mode to display and no way to tell "normal" from "not yet known".
	if facts.Mode == "" {
		t.Error("facts carried no mode")
	}
}

// Switching mode must reach OTHER devices, not just the one that made the change. The mode that
// matters here is yolo: a second device still showing "Normal" would tell someone they are being
// asked before anything runs when they are not.
func TestModeChangeBroadcastsFactsToWatchers(t *testing.T) {
	reg, _ := project.Load(filepath.Join(t.TempDir(), "projects.json"))
	h := hub.New()
	h.Register(&cwdProvider{})
	h.SetProjects(reg)
	daemonKP, _ := crypto.GenerateKeyPair()

	conn := connectClient(t, h, daemonKP)
	r := newReader(conn)
	send(t, conn, "c1", protocol.TypeSessionCreate, protocol.SessionCreate{Provider: "fake", Cwd: t.TempDir()})
	var sess protocol.Session
	if err := json.Unmarshal(r.waitOK(t, "c1"), &sess); err != nil {
		t.Fatalf("create decode: %v", err)
	}
	send(t, conn, "s1", protocol.TypeSessionSubscribe, protocol.SessionRef{SessionID: sess.ID})
	r.waitOK(t, "s1")
	r.waitEvent(t, protocol.TypeSessionCapabilities)
	r.waitEvent(t, protocol.TypeSessionFacts) // the subscribe-time one

	send(t, conn, "m1", protocol.TypeSessionModeSet, protocol.SessionModeSet{SessionID: sess.ID, Mode: protocol.ModeYolo})

	// Wait for the facts event WITHOUT first waiting for the reply.
	//
	// The facts are broadcast from inside setSessionMode, which runs before the handler sends its OK,
	// so the event legitimately arrives first — and waitFor discards every envelope that does not
	// match, so waiting on the OK first would consume the very event this test is here to see. That
	// cost a real debugging detour: the test failed, adding a log line to the daemon made it pass
	// (the delay let the OK overtake the event), and the daemon was never at fault.
	var got protocol.SessionFacts
	if raw := r.waitEvent(t, protocol.TypeSessionFacts); raw != nil {
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("facts decode: %v", err)
		}
	}
	if got.Mode != protocol.ModeYolo {
		t.Errorf("broadcast facts mode = %q, want yolo — other devices would keep showing the old mode", got.Mode)
	}
}

// The session LIST is the other place a client reads mode from, and it overwrites what the client
// believes. If the list omitted the mode after a switch, a client would set itself back to the
// default the instant the next list arrived — silently undoing the change on screen while the
// daemon went on enforcing it.
func TestSessionListReportsTheSwitchedMode(t *testing.T) {
	reg, _ := project.Load(filepath.Join(t.TempDir(), "projects.json"))
	h := hub.New()
	h.Register(&cwdProvider{})
	h.SetProjects(reg)
	daemonKP, _ := crypto.GenerateKeyPair()
	conn := connectClient(t, h, daemonKP)
	r := newReader(conn)

	send(t, conn, "c1", protocol.TypeSessionCreate, protocol.SessionCreate{Provider: "fake", Cwd: t.TempDir()})
	var sess protocol.Session
	if err := json.Unmarshal(r.waitOK(t, "c1"), &sess); err != nil {
		t.Fatalf("create decode: %v", err)
	}
	send(t, conn, "m1", protocol.TypeSessionModeSet, protocol.SessionModeSet{SessionID: sess.ID, Mode: protocol.ModeYolo})
	r.waitOK(t, "m1")

	send(t, conn, "l1", protocol.TypeSessionList, nil)
	var list protocol.SessionList
	if err := json.Unmarshal(r.waitOK(t, "l1"), &list); err != nil {
		t.Fatalf("list decode: %v", err)
	}
	found := false
	for _, s := range list.Sessions {
		if s.ID == sess.ID {
			found = true
			if s.Mode != protocol.ModeYolo {
				t.Errorf("session row mode = %q, want yolo", s.Mode)
			}
		}
	}
	if !found {
		t.Fatal("the session was not in the list")
	}
}

// The global defaults round-trip over the wire, and the daemon's refusal to honour a yolo default
// without its acknowledgement holds at the PROTOCOL boundary too — not just in the store. A client
// that sets yolo and is told "yolo" while the daemon quietly stored something else would show a
// setting that is not in force.
func TestSessionDefaultsRoundTripRefusesUnacknowledgedYolo(t *testing.T) {
	reg, _ := project.Load(filepath.Join(t.TempDir(), "projects.json"))
	h := hub.New()
	h.Register(&cwdProvider{})
	h.SetProjects(reg)
	h.SetDefaultsPath(filepath.Join(t.TempDir(), "defaults.json"))
	daemonKP, _ := crypto.GenerateKeyPair()
	conn := connectClient(t, h, daemonKP)
	r := newReader(conn)

	// A safe default is stored as asked.
	send(t, conn, "d1", protocol.TypeSessionDefaultsSet, protocol.SessionDefaults{Mode: protocol.ModeArchitect})
	var got protocol.SessionDefaults
	if err := json.Unmarshal(r.waitOK(t, "d1"), &got); err != nil {
		t.Fatalf("defaults decode: %v", err)
	}
	if got.Mode != protocol.ModeArchitect {
		t.Errorf("stored mode = %q, want architect", got.Mode)
	}
	// The catalog rides along so a settings screen renders the same copy as the session picker.
	if len(got.Modes) == 0 {
		t.Error("defaults carried no mode catalog for the client to render")
	}

	// yolo WITHOUT the acknowledgement is downgraded, and the reply says so.
	send(t, conn, "d2", protocol.TypeSessionDefaultsSet, protocol.SessionDefaults{Mode: protocol.ModeYolo})
	if err := json.Unmarshal(r.waitOK(t, "d2"), &got); err != nil {
		t.Fatalf("defaults decode: %v", err)
	}
	if got.Mode == protocol.ModeYolo {
		t.Error("an unacknowledged yolo default was accepted over the wire")
	}

	// With it, it sticks — and a fresh get agrees.
	send(t, conn, "d3", protocol.TypeSessionDefaultsSet,
		protocol.SessionDefaults{Mode: protocol.ModeYolo, AllowYoloDefault: true})
	if err := json.Unmarshal(r.waitOK(t, "d3"), &got); err != nil {
		t.Fatalf("defaults decode: %v", err)
	}
	if got.Mode != protocol.ModeYolo {
		t.Fatalf("acknowledged yolo default was not stored (mode=%q)", got.Mode)
	}
	send(t, conn, "d4", protocol.TypeSessionDefaultsGet, nil)
	if err := json.Unmarshal(r.waitOK(t, "d4"), &got); err != nil {
		t.Fatalf("defaults get decode: %v", err)
	}
	if got.Mode != protocol.ModeYolo || got.AllowYoloDefault != true {
		t.Errorf("get returned mode=%q allow=%v, want yolo/true", got.Mode, got.AllowYoloDefault)
	}
}

// A session created WITHOUT a mode takes the stored default. This is the whole point of the setting
// — and the assertion that it does not leak into a request that named its own mode.
func TestNewSessionTakesTheDefaultModeUnlessItNamesOne(t *testing.T) {
	reg, _ := project.Load(filepath.Join(t.TempDir(), "projects.json"))
	h := hub.New()
	h.Register(&cwdProvider{})
	h.SetProjects(reg)
	h.SetDefaultsPath(filepath.Join(t.TempDir(), "defaults.json"))
	daemonKP, _ := crypto.GenerateKeyPair()
	conn := connectClient(t, h, daemonKP)
	r := newReader(conn)

	send(t, conn, "d1", protocol.TypeSessionDefaultsSet, protocol.SessionDefaults{Mode: protocol.ModeAsk})
	r.waitOK(t, "d1")

	send(t, conn, "c1", protocol.TypeSessionCreate, protocol.SessionCreate{Provider: "fake", Cwd: t.TempDir()})
	var sess protocol.Session
	if err := json.Unmarshal(r.waitOK(t, "c1"), &sess); err != nil {
		t.Fatalf("create decode: %v", err)
	}
	if sess.Mode != protocol.ModeAsk {
		t.Errorf("new session mode = %q, want the default (ask)", sess.Mode)
	}

	// An explicit mode always wins over the default.
	send(t, conn, "c2", protocol.TypeSessionCreate,
		protocol.SessionCreate{Provider: "fake", Cwd: t.TempDir(), Mode: protocol.ModeCode})
	var explicit protocol.Session
	if err := json.Unmarshal(r.waitOK(t, "c2"), &explicit); err != nil {
		t.Fatalf("create decode: %v", err)
	}
	if explicit.Mode != protocol.ModeCode {
		t.Errorf("explicit mode = %q, want code — the default must not override a stated choice", explicit.Mode)
	}
}
