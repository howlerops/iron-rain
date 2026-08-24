// Package loghub captures the daemon's log output into a ring buffer and streams each new line to a
// listener, so a connected app can show a live "Developer → Logs" view of the daemon — local OR
// remote — instead of shelling into the machine to grep ~/.oculus/oculusd.log. Only `log` output is
// captured (plugged in via log.SetOutput), so the startup pairing-QR banner (printed to stdout with
// fmt) never pollutes the stream.
package loghub

import (
	"bytes"
	"sync"
)

// Hub is an io.Writer for the standard logger: it splits writes into lines, keeps the last `max` in
// a ring, and notifies the listener (set by the daemon hub) of each new line.
type Hub struct {
	mu      sync.Mutex
	ring    []string
	max     int
	partial []byte
	onLine  func(string)
}

func New(max int) *Hub {
	if max <= 0 {
		max = 1000
	}
	return &Hub{max: max}
}

// Write implements io.Writer — use with io.MultiWriter(os.Stderr, hub) in log.SetOutput.
func (h *Hub) Write(p []byte) (int, error) {
	h.mu.Lock()
	h.partial = append(h.partial, p...)
	var newLines []string
	for {
		i := bytes.IndexByte(h.partial, '\n')
		if i < 0 {
			break
		}
		line := string(h.partial[:i])
		h.partial = h.partial[i+1:]
		h.ring = append(h.ring, line)
		if len(h.ring) > h.max {
			h.ring = h.ring[len(h.ring)-h.max:]
		}
		newLines = append(newLines, line)
	}
	cb := h.onLine
	h.mu.Unlock()
	if cb != nil {
		for _, l := range newLines {
			cb(l)
		}
	}
	return len(p), nil
}

// SetListener registers the callback invoked (off the write path's lock) for each new line.
func (h *Hub) SetListener(f func(string)) {
	h.mu.Lock()
	h.onLine = f
	h.mu.Unlock()
}

// Recent returns a copy of the buffered lines (replayed when a client first subscribes).
func (h *Hub) Recent() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.ring...)
}
