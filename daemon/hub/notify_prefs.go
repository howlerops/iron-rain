package hub

import (
	"encoding/json"
	"os"

	"github.com/howlerops/oculus/daemon/protocol"
)

// Push categories (also the APNs "category" for actionable buttons). The user can toggle each in the
// app's Notifications settings; everything defaults ON.
const (
	notifyApproval = "APPROVAL"
	notifyFinished = "AGENT_FINISHED"
	notifyError    = "AGENT_ERROR"
	notifyStalled  = "AGENT_STALLED"
	notifyTests    = "TESTS_FAILED"
	notifyPR       = "PR_FINISHED"
	notifyFanout   = "FANOUT_DONE"
	notifyLoop     = "LOOP_DONE"
)

// notifyCatalog is the ordered, labeled set of toggleable notification types shown in settings.
var notifyCatalog = []struct{ Key, Label, Detail string }{
	{notifyApproval, "Approval needed", "The agent is waiting for you to allow/deny a tool"},
	{notifyFinished, "Agent finished", "A turn completed (with a summary of what it did)"},
	{notifyError, "Agent error", "A session hit an error"},
	{notifyStalled, "Agent stalled", "A supervised session needs attention"},
	{notifyTests, "Tests failed", "A test/build run failed"},
	{notifyPR, "PR / worktree finished", "The agent opened a PR or finished a worktree branch"},
	{notifyFanout, "Fan-out finished", "Every agent in a fan-out has completed"},
	{notifyLoop, "Loop run finished", "An autonomous loop run completed"},
}

// SetNotifyPrefsPath records ~/.oculus/notify.json and loads any saved OFF toggles.
func (h *Hub) SetNotifyPrefsPath(path string) {
	h.mu.Lock()
	h.notifyPrefsPath = path
	h.notifyOff = map[string]bool{}
	if data, err := os.ReadFile(path); err == nil {
		var off []string
		if json.Unmarshal(data, &off) == nil {
			for _, c := range off {
				h.notifyOff[c] = true
			}
		}
	}
	h.mu.Unlock()
}

// notifyPrefs returns the full catalog with each type's current enabled state (for the app).
func (h *Hub) notifyPrefs() protocol.NotifyPrefs {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := protocol.NotifyPrefs{Prefs: make([]protocol.NotifyPref, 0, len(notifyCatalog))}
	for _, c := range notifyCatalog {
		out.Prefs = append(out.Prefs, protocol.NotifyPref{
			Key: c.Key, Label: c.Label, Detail: c.Detail, Enabled: !h.notifyOff[c.Key],
		})
	}
	return out
}

// setNotifyPref flips one category on/off and persists the OFF set.
func (h *Hub) setNotifyPref(category string, enabled bool) {
	h.mu.Lock()
	if h.notifyOff == nil {
		h.notifyOff = map[string]bool{}
	}
	if enabled {
		delete(h.notifyOff, category)
	} else {
		h.notifyOff[category] = true
	}
	off := make([]string, 0, len(h.notifyOff))
	for c := range h.notifyOff {
		off = append(off, c)
	}
	path := h.notifyPrefsPath
	h.mu.Unlock()
	if path != "" {
		if data, err := json.Marshal(off); err == nil {
			_ = os.WriteFile(path, data, 0o600)
		}
	}
}
