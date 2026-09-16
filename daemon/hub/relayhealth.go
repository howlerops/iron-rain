package hub

import (
	"sort"
	"sync"
	"time"

	"github.com/howlerops/oculus/daemon/protocol"
)

// Per-relay registration state, so the app can tell "remote access is down" from "the daemon is
// down".
//
// Those are different problems with different fixes, and before this they were indistinguishable
// from the phone: a daemon that failed to register on its relay was silent, the app failed to reach
// it, and the failure surfaced as "daemon not running" — sending someone to restart a process that
// was running perfectly. The daemon logged the real reason to a file nobody reads.

type relayHealth struct {
	mu     sync.Mutex
	states map[string]*protocol.RelayState
	order  []string // insertion order, so the list keeps the operator's stated preference
}

// SetRelayConnected records that a host registration is live on this relay.
func (h *Hub) SetRelayConnected(url string) {
	h.relayHealthUpdate(url, func(s *protocol.RelayState) {
		s.Connected = true
		s.LastOKAt = time.Now().Unix()
		s.Failures = 0
		s.Detail = ""
	})
}

// SetRelayFailed records a failed or lost registration. err may be nil for a clean disconnect.
func (h *Hub) SetRelayFailed(url string, err error) {
	h.relayHealthUpdate(url, func(s *protocol.RelayState) {
		s.Connected = false
		s.Failures++
		if err != nil {
			s.Detail = err.Error()
		}
	})
}

// relayHealthUpdate applies a change and broadcasts ONLY when the result differs.
//
// The registration loop retries on a backoff that starts at one second, so an unreachable relay
// would otherwise emit a frame per attempt, forever, to every connected device — a status display
// that costs more traffic than the sessions it is reporting on. Connected/Detail transitions are
// what a human reacts to; the attempt count behind them is not.
func (h *Hub) relayHealthUpdate(url string, apply func(*protocol.RelayState)) {
	if h == nil {
		return
	}
	h.relays.mu.Lock()
	if h.relays.states == nil {
		h.relays.states = map[string]*protocol.RelayState{}
	}
	st := h.relays.states[url]
	if st == nil {
		st = &protocol.RelayState{URL: url}
		h.relays.states[url] = st
		h.relays.order = append(h.relays.order, url)
	}
	before := *st
	apply(st)
	changed := before.Connected != st.Connected || before.Detail != st.Detail
	h.relays.mu.Unlock()

	if changed {
		h.broadcast(protocol.TypeRelayHealth, h.RelayHealth())
	}
}

// RelayHealth snapshots the current state of every relay, in the order they were configured.
func (h *Hub) RelayHealth() protocol.RelayHealth {
	if h == nil {
		return protocol.RelayHealth{}
	}
	h.relays.mu.Lock()
	defer h.relays.mu.Unlock()
	out := protocol.RelayHealth{Relays: make([]protocol.RelayState, 0, len(h.relays.order))}
	for _, u := range h.relays.order {
		if st := h.relays.states[u]; st != nil {
			out.Relays = append(out.Relays, *st)
		}
	}
	// order is append-only and already deterministic; this only matters if a state was added
	// concurrently by two goroutines racing the same new URL, which sort makes harmless.
	sort.SliceStable(out.Relays, func(i, j int) bool { return false })
	return out
}
