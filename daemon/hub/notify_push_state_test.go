package hub

import "testing"

// The prefs must say whether a push can actually be delivered.
//
// Push is only on when the daemon was given APNs credentials — and the two ways a real user's daemon
// starts (the app-managed child and the launchd agent) both hardcode their argv with none, and there
// was no file to configure it from either. So for most installs every toggle on the Notifications
// screen was decoration: the setting saved, and nothing could ever arrive. A notification that never
// comes is indistinguishable from an agent that never finished, which is the worst thing for this
// feature to be ambiguous about. The app now says so instead of offering the toggles bare.
func TestNotifyPrefsReportWhetherPushCanFire(t *testing.T) {
	h := New()

	prefs := h.notifyPrefs()
	if prefs.PushEnabled {
		t.Error("a daemon with no APNs credentials must not report push as available")
	}
	if prefs.Devices != 0 {
		t.Errorf("no devices are registered, got %d", prefs.Devices)
	}
	// The catalog is still listed: the choices are saved and they also gate the Slack mirror, so the
	// toggles remain meaningful even with push off. They are simply no longer presented as if a phone
	// were going to buzz.
	if len(prefs.Prefs) == 0 {
		t.Fatal("the notification catalog should still be listed")
	}

	// Registered devices are reported, so "push is on but nobody is listening" is distinguishable
	// from "push is off".
	h.mu.Lock()
	h.pushTokens = []string{"tokenA", "tokenB"}
	h.mu.Unlock()
	if got := h.notifyPrefs().Devices; got != 2 {
		t.Errorf("Devices = %d, want 2", got)
	}
}
