package hub

import (
	"context"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/push"
)

// recordingNotifier reports each delivered notification's category on a channel.
type recordingNotifier struct{ got chan string }

func (r *recordingNotifier) Notify(_ context.Context, _ string, n push.Notification) error {
	r.got <- n.Category
	return nil
}

// TestNotifyPrefsGate proves a per-type toggle actually suppresses that push type while others still
// deliver — the backing for the app's Notifications settings.
func TestNotifyPrefsGate(t *testing.T) {
	rec := &recordingNotifier{got: make(chan string, 8)}
	h := New()
	h.notifier = rec
	h.pushTokens = []string{"dev"}
	h.SetNotifyPrefsPath(t.TempDir() + "/notify.json")

	// Default: everything on → a finished push delivers.
	h.pushAgentFinished("s1", "Job", 90*time.Second, 3, 3, 0.42)
	select {
	case cat := <-rec.got:
		if cat != notifyFinished {
			t.Fatalf("got category %q, want %q", cat, notifyFinished)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("finished push was not delivered by default")
	}

	// Turn AGENT_FINISHED off → it must be suppressed…
	h.setNotifyPref(notifyFinished, false)
	h.pushAgentFinished("s1", "Job", time.Minute, 0, 0, 0)
	select {
	case cat := <-rec.got:
		t.Fatalf("finished push delivered (%q) after being disabled", cat)
	case <-time.After(300 * time.Millisecond):
		// good — suppressed
	}

	// …while a DIFFERENT type (error) still delivers.
	h.pushAgentError("s1", "Job", "boom")
	select {
	case cat := <-rec.got:
		if cat != notifyError {
			t.Fatalf("got category %q, want %q", cat, notifyError)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("error push should still deliver when only finished is disabled")
	}

	// The catalog reflects the off state (persisted round-trip).
	h.SetNotifyPrefsPath(h.notifyPrefsPath) // reload from disk
	for _, p := range h.notifyPrefs().Prefs {
		if p.Key == notifyFinished && p.Enabled {
			t.Fatal("AGENT_FINISHED should read disabled after persistence")
		}
	}
}
