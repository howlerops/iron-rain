package hub

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/howlerops/oculus/daemon/agent"
	"github.com/howlerops/oculus/daemon/protocol"
)

func remoteMeta() sessionMeta {
	return sessionMeta{label: "remote: build-box", cwd: "/srv/app",
		execKind: protocol.ExecKindSSH, execHost: "build-box"}
}

// TestExecHostSurvivesRename is the whole reason execHost exists as its own field. "remote: build-box"
// was only ever the DEFAULT label, and session.rename overwrites the label wholesale — so the moment a
// user gave a remote session a task-shaped name, the app had nothing left telling it the agent was
// editing files on another machine.
func TestExecHostSurvivesRename(t *testing.T) {
	h := New()
	m := h.addSession(&fakeAttachedSess{id: "s_remote", provider: "cli", events: make(chan agent.Event)}, remoteMeta())

	// Exactly what the session.rename handler does to a live session.
	m.mu.Lock()
	m.meta.label = "nightly deploy"
	m.mu.Unlock()

	got := m.info()
	if got.Name != "nightly deploy" {
		t.Fatalf("rename didn't take: Name = %q", got.Name)
	}
	if got.ExecHost != "build-box" || got.ExecKind != protocol.ExecKindSSH {
		t.Errorf("renaming erased the execution location: ExecKind=%q ExecHost=%q, want %q/%q",
			got.ExecKind, got.ExecHost, protocol.ExecKindSSH, "build-box")
	}
}

// TestExecHostSurvivesPersistRoundTrip covers the restart path a remote session actually takes. An
// ssh session runs on the CLI provider, which has nothing to re-attach to, so it comes back through
// the stopped/restartable list on EVERY daemon restart — the one path where a dropped host would make
// a remote session indistinguishable from one running on this Mac.
func TestExecHostSurvivesPersistRoundTrip(t *testing.T) {
	blob, err := json.Marshal(metaToPersisted(remoteMeta()))
	if err != nil {
		t.Fatal(err)
	}
	var pm persistedMeta
	if err := json.Unmarshal(blob, &pm); err != nil {
		t.Fatal(err)
	}
	if got := pm.toMeta(); got.execHost != "build-box" || got.execKind != protocol.ExecKindSSH {
		t.Fatalf("meta round-trip lost the host: execKind=%q execHost=%q", got.execKind, got.execHost)
	}

	// …and the same record, read back the way a restarted daemon reads it: as a stopped row in the
	// session list, with no live session behind it.
	h, db := restoreHub(t)
	saveRecord(t, db, "s_remote", "cli", pm)
	stopped := h.stoppedSessions()
	if len(stopped) != 1 {
		t.Fatalf("expected 1 stopped session, got %d", len(stopped))
	}
	if stopped[0].ExecHost != "build-box" || stopped[0].ExecKind != protocol.ExecKindSSH {
		t.Errorf("a restored remote session looks local: ExecKind=%q ExecHost=%q",
			stopped[0].ExecKind, stopped[0].ExecHost)
	}
}

// TestLocalSessionExecFieldsAbsent pins the compatibility decision: local is the EMPTY value, so a
// local session's wire frame is byte-for-byte what it was before this field existed. An app built
// against the older daemon must not start rendering an execution chip on every row it already shows.
func TestLocalSessionExecFieldsAbsent(t *testing.T) {
	h := New()
	m := h.addSession(&fakeAttachedSess{id: "s_local", provider: "opencode", events: make(chan agent.Event)},
		sessionMeta{label: "local work", cwd: "/repo"})

	got := m.info()
	if got.ExecKind != protocol.ExecKindLocal || got.ExecHost != "" {
		t.Fatalf("a local session reported an execution location: ExecKind=%q ExecHost=%q", got.ExecKind, got.ExecHost)
	}
	blob, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(blob), "exec_") {
		t.Errorf("local session frame carries execution keys it shouldn't: %s", blob)
	}
}

// TestPushLabel: the host has to reach the lock screen, and a local session's push has to be exactly
// the string it was yesterday — the notification body is the surface with no UI to fall back on.
func TestPushLabel(t *testing.T) {
	cases := []struct{ label, host, want string }{
		{"nightly deploy", "", "nightly deploy"}, // local: untouched
		{"", "", ""},                             // local, unnamed: still untouched
		{"nightly deploy", "build-box", "nightly deploy on build-box"},
		{"", "build-box", "build-box"}, // renamed to nothing — the host is all we have, and it beats "Agent finished"
	}
	for _, c := range cases {
		if got := pushLabel(c.label, c.host); got != c.want {
			t.Errorf("pushLabel(%q, %q) = %q, want %q", c.label, c.host, got, c.want)
		}
	}
}
