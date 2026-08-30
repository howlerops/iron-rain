package hub

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/howlerops/oculus/daemon/protocol"
)

// A yolo DEFAULT turns approvals off for every future session, including ones nobody is watching
// when they start — a loop's, a fan-out's, a scheduled run's. So it takes a second, explicit
// acknowledgement, and asking for it without one must not quietly succeed.
func TestYoloDefaultIsRefusedWithoutTheExplicitAcknowledgement(t *testing.T) {
	h := New()
	h.SetDefaultsPath(filepath.Join(t.TempDir(), "defaults.json"))

	got := h.setSessionDefaults(protocol.SessionDefaults{Mode: protocol.ModeYolo})
	if got.Mode == protocol.ModeYolo {
		t.Fatal("a yolo default was accepted without the acknowledgement")
	}
	if h.defaultMode() != protocol.ModeCode {
		t.Errorf("new sessions would start in %q, want %q", h.defaultMode(), protocol.ModeCode)
	}
}

func TestYoloDefaultIsHonouredWithTheAcknowledgement(t *testing.T) {
	h := New()
	h.SetDefaultsPath(filepath.Join(t.TempDir(), "defaults.json"))

	got := h.setSessionDefaults(protocol.SessionDefaults{Mode: protocol.ModeYolo, AllowYoloDefault: true})
	if got.Mode != protocol.ModeYolo {
		t.Fatalf("stored mode = %q, want yolo", got.Mode)
	}
	if h.defaultMode() != protocol.ModeYolo {
		t.Errorf("defaultMode = %q, want yolo", h.defaultMode())
	}
}

// The acknowledgement is re-checked where the default is USED, not only where it was set. A
// defaults.json can arrive without ever passing through setSessionDefaults — hand-edited, restored
// from a backup, synced from another machine — and must not be able to turn approvals off that way.
func TestAHandEditedYoloDefaultIsNotHonoured(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "defaults.json")
	// Exactly what someone would write by hand, or what a partial backup would restore.
	if err := os.WriteFile(path, []byte(`{"mode":"yolo"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	h := New()
	h.SetDefaultsPath(path)

	if got := h.defaultMode(); got != protocol.ModeCode {
		t.Errorf("a hand-written yolo default was honoured (mode=%q); it must require the acknowledgement", got)
	}
}

// An unreadable or corrupt file must fall back to the SAFE default, not to whatever partially
// decoded.
func TestCorruptDefaultsFallBackToCode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "defaults.json")
	if err := os.WriteFile(path, []byte(`{"mode":"yolo", NOT JSON`), 0o600); err != nil {
		t.Fatal(err)
	}
	h := New()
	h.SetDefaultsPath(path)
	if got := h.defaultMode(); got != protocol.ModeCode {
		t.Errorf("corrupt defaults produced mode %q, want code", got)
	}
}

func TestDefaultsRoundTripToDisk(t *testing.T) {
	path := filepath.Join(t.TempDir(), "defaults.json")
	h := New()
	h.SetDefaultsPath(path)
	h.setSessionDefaults(protocol.SessionDefaults{Mode: protocol.ModeArchitect})

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("defaults were not saved: %v", err)
	}
	var on sessionDefaults
	if err := json.Unmarshal(data, &on); err != nil {
		t.Fatalf("saved defaults are not valid JSON: %v", err)
	}
	if on.Mode != protocol.ModeArchitect {
		t.Errorf("saved mode = %q, want architect", on.Mode)
	}

	// A fresh hub reading the same file must agree.
	h2 := New()
	h2.SetDefaultsPath(path)
	if got := h2.defaultMode(); got != protocol.ModeArchitect {
		t.Errorf("reloaded mode = %q, want architect", got)
	}
}

// An unknown mode string — an older or newer client, or a typo — must land on code. It must
// especially never land on the most permissive mode.
func TestUnknownModeFallsBackToCodeNotYolo(t *testing.T) {
	for _, in := range []string{"", "turbo", "YOLO ", "bypass", "admin"} {
		got := normalizeMode(in, false)
		if got == protocol.ModeYolo && in != "YOLO " {
			t.Errorf("normalizeMode(%q) = yolo — unknown input must not become the permissive mode", in)
		}
		if in == "turbo" || in == "bypass" || in == "admin" {
			if got != protocol.ModeCode {
				t.Errorf("normalizeMode(%q) = %q, want code", in, got)
			}
		}
	}
	// The real value, spelled correctly, still works — including with stray whitespace/case, which
	// is how it arrives from a hand-written config.
	if got := normalizeMode("  YOLO ", false); got != protocol.ModeYolo {
		t.Errorf(`normalizeMode("  YOLO ") = %q, want yolo`, got)
	}
}

// Only yolo skips the prompt. If another mode ever starts auto-approving, that is a silent removal
// of the safety net, so it is asserted rather than assumed.
func TestOnlyYoloAutoApproves(t *testing.T) {
	for _, mode := range []string{protocol.ModeCode, protocol.ModeAsk, protocol.ModeArchitect, ""} {
		if modeAutoApproves(mode) {
			t.Errorf("mode %q auto-approves; only yolo may", mode)
		}
	}
	if !modeAutoApproves(protocol.ModeYolo) {
		t.Error("yolo must auto-approve")
	}
}

// The catalog is what every mode picker and settings screen renders. Exactly one entry may be
// marked unsafe, and it must be the one that turns approvals off.
func TestModeCatalogMarksExactlyYoloUnsafe(t *testing.T) {
	var unsafe []string
	seen := map[string]bool{}
	for _, m := range protocol.Modes() {
		seen[m.ID] = true
		if m.Label == "" || m.Description == "" {
			t.Errorf("mode %q has no label/description to render", m.ID)
		}
		if m.Unsafe {
			unsafe = append(unsafe, m.ID)
		}
	}
	if len(unsafe) != 1 || unsafe[0] != protocol.ModeYolo {
		t.Errorf("modes marked unsafe = %v, want exactly [yolo]", unsafe)
	}
	for _, want := range []string{protocol.ModeCode, protocol.ModeAsk, protocol.ModeArchitect, protocol.ModeYolo} {
		if !seen[want] {
			t.Errorf("the catalog is missing %q", want)
		}
	}
}
