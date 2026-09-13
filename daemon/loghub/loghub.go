// Package loghub captures the daemon's log output into a ring buffer and streams each new line to a
// listener, so a connected app can show a live "Developer → Logs" view of the daemon — local OR
// remote — instead of shelling into the machine to grep ~/.oculus/oculusd.log. Only `log` output is
// captured (plugged in via log.SetOutput), so the startup pairing-QR banner (printed to stdout with
// fmt) never pollutes the stream.
package loghub

import (
	"bytes"
	"io"
	"os"
	"strings"
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

// RestartMarker separates lines recovered from a previous run from this run's own output. The app
// shows it verbatim, so it has to read as an explanation rather than a log line.
const RestartMarker = "——— the daemon restarted here; everything above is from the previous run ———"

// SeedFromFile pre-fills the ring with the tail of the on-disk log, so the panel can show the run
// that BROKE rather than only the one that replaced it.
//
// The ring starts empty on every launch. That is the whole reason the log panel could never answer
// the question people open it for: a daemon that died took its evidence with it, and by the time
// anyone looked, the only lines in memory were from the healthy process that came after. The bytes
// were on disk the entire time.
//
// Reads a bounded tail rather than the file — the log is capped at 8 MiB, not at nothing. Falls back
// to the rolled-aside copy when the live file has just been truncated, which is exactly the case
// where a restart and a roll coincide. Best-effort throughout: a log we cannot read is not a reason
// to fail a daemon start.
func (h *Hub) SeedFromFile(path string, lines int) {
	if lines <= 0 {
		return
	}
	tail := tailLines(path, lines)
	if len(tail) == 0 {
		tail = tailLines(path+".1", lines) // the live file was rolled aside a moment ago
	}
	if len(tail) == 0 {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.ring) > 0 {
		return // this run has already logged; seeding now would report its output as history
	}
	h.ring = append(tail, RestartMarker)
	if len(h.ring) > h.max {
		h.ring = h.ring[len(h.ring)-h.max:]
	}
}

// tailLines returns at most n trailing lines of a file, reading a bounded window from the end.
func tailLines(path string, n int) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.Size() == 0 {
		return nil
	}
	const window = 256 << 10 // enough for n lines of any plausible length, bounded regardless
	start := st.Size() - window
	if start < 0 {
		start = 0
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return nil
	}
	buf, err := io.ReadAll(f)
	if err != nil || len(buf) == 0 {
		return nil
	}
	if start > 0 {
		// The window almost certainly landed mid-line; drop that fragment rather than show it.
		if i := bytes.IndexByte(buf, '\n'); i >= 0 {
			buf = buf[i+1:]
		}
	}
	all := strings.Split(strings.TrimRight(string(buf), "\n"), "\n")
	if len(all) == 1 && all[0] == "" {
		return nil
	}
	if len(all) > n {
		all = all[len(all)-n:]
	}
	return all
}

// Recent returns a copy of the buffered lines (replayed when a client first subscribes).
func (h *Hub) Recent() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]string(nil), h.ring...)
}
