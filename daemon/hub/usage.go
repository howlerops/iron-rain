package hub

import (
	"sort"
	"time"

	"github.com/howlerops/oculus/daemon/protocol"
)

// usageWindowHours is the rolling window a Claude subscription meters on. Usage inside one window
// counts against the same allowance, and the window starts with your FIRST activity rather than on
// the clock — so the reset is derived from when usage began, not from midnight.
const usageWindowHours = 5

// usageReport assembles the spend view: fixed calendar periods, the rolling window, and breakdowns.
func (h *Hub) usageReport() protocol.UsageReport {
	h.mu.Lock()
	db := h.db
	h.mu.Unlock()
	out := protocol.UsageReport{
		Providers: []protocol.UsageSlice{}, Models: []protocol.UsageSlice{}, Sessions: []protocol.UsageSlice{},
	}
	if db == nil {
		return out
	}
	now := time.Now()
	// Calendar periods in LOCAL time — "today" means the user's today, not UTC's.
	startOfDay := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	startOfWeek := startOfDay.AddDate(0, 0, -int((now.Weekday()+6)%7)) // Monday
	startOfMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())

	if t, err := db.UsageTotal(startOfDay.Unix()); err == nil {
		out.Today = protocol.UsageSlice{Key: "today", InputTokens: t.InTokens, OutputTokens: t.OutTokens, CostUSD: t.CostUSD}
	}
	if t, err := db.UsageTotal(startOfWeek.Unix()); err == nil {
		out.Week = protocol.UsageSlice{Key: "week", InputTokens: t.InTokens, OutputTokens: t.OutTokens, CostUSD: t.CostUSD}
	}
	if t, err := db.UsageTotal(startOfMonth.Unix()); err == nil {
		out.Month = protocol.UsageSlice{Key: "month", InputTokens: t.InTokens, OutputTokens: t.OutTokens, CostUSD: t.CostUSD}
	}

	out.Window = h.usageWindow(now)

	for _, g := range []struct {
		col  string
		dest *[]protocol.UsageSlice
	}{{"provider", &out.Providers}, {"model", &out.Models}, {"session_id", &out.Sessions}} {
		buckets, err := db.UsageSince(startOfWeek.Unix(), g.col)
		if err != nil {
			continue
		}
		for _, b := range buckets {
			if b.Key == "" {
				continue
			}
			*g.dest = append(*g.dest, protocol.UsageSlice{
				Key: b.Key, InputTokens: b.InTokens, OutputTokens: b.OutTokens, CostUSD: b.CostUSD,
			})
		}
	}
	// Name the sessions, so the biggest spender is identifiable rather than an opaque id.
	h.mu.Lock()
	names := map[string]string{}
	for id, m := range h.sessions {
		m.mu.Lock()
		n := m.meta.label
		if n == "" {
			n = m.meta.workspaceName
		}
		m.mu.Unlock()
		names[id] = n
	}
	h.mu.Unlock()
	for i := range out.Sessions {
		if n := names[out.Sessions[i].Key]; n != "" {
			out.Sessions[i].Label = n
		}
	}
	// Only the heaviest few are worth showing.
	if len(out.Sessions) > 10 {
		out.Sessions = out.Sessions[:10]
	}
	sort.SliceStable(out.Models, func(i, j int) bool { return out.Models[i].CostUSD > out.Models[j].CostUSD })

	// Any subscription-backed provider means the dollar figures are notional, not billed.
	h.mu.Lock()
	for _, m := range h.sessions {
		if p := m.sess.Provider(); p == "claude-code" || p == "opencode" {
			out.Subscription = true
			break
		}
	}
	h.mu.Unlock()
	return out
}

// usageWindow derives the current rolling window from when usage actually started.
func (h *Hub) usageWindow(now time.Time) protocol.UsageWindow {
	w := protocol.UsageWindow{Hours: usageWindowHours}
	db := h.db
	if db == nil {
		return w
	}
	lookback := now.Add(-usageWindowHours * time.Hour).Unix()
	first, err := db.FirstUsageSince(lookback)
	if err != nil || first == 0 {
		return w // nothing in the window: no allowance is being consumed, so there's nothing to reset
	}
	w.StartedAt = first
	w.ResetsAt = first + int64(usageWindowHours*time.Hour/time.Second)
	w.Active = true
	if t, err := db.UsageTotal(first); err == nil {
		w.CostUSD = t.CostUSD
		w.Tokens = t.InTokens + t.OutTokens
	}
	return w
}
