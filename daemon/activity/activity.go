// Package activity is the daemon's single source of truth for cross-session "what happened / what
// needs me" events — the backbone of the app's Activity destination, the Needs-You inbox, the
// bottom ticker, and per-row status chips. Every surface reads from here so they can never desync.
// Events are typed, appended to ~/.oculus/activity.jsonl (durable across restarts), kept in a
// bounded in-memory ring for fast replay, and carry unread + needs-you state the UI sorts on.
package activity

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Kind classifies an activity event. needs-you kinds sort to the top and drive the badge.
const (
	KindFinished   = "finished"    // an agent turn completed (was running → idle/done)
	KindNeedsInput = "needs_input" // the agent is asking a question / awaiting approval (NEEDS YOU)
	KindError      = "error"       // a session errored / a send got no response (NEEDS YOU)
	KindLoopRun    = "loop_run"    // a loop started an agent on a ticket
	KindLoopPR     = "loop_pr"     // a loop run opened a PR
	KindStarted    = "started"     // a session started a turn (low-signal; kept for the feed)
	KindFanoutRun  = "fanout_run"  // N agents started racing the same prompt
	KindFanoutDone = "fanout_done" // every variant finished — a comparison is ready
)

// Event is one activity item. ID is stable (dedup + unread tracking).
type Event struct {
	ID        string `json:"id"`
	TS        int64  `json:"ts"`
	Kind      string `json:"kind"`
	SessionID string `json:"session_id,omitempty"`
	Provider  string `json:"provider,omitempty"`
	Project   string `json:"project,omitempty"` // cwd / project for grouping + roll-up
	Title     string `json:"title"`             // human one-liner
	Detail    string `json:"detail,omitempty"`
	NeedsYou  bool   `json:"needs_you"`
	Read      bool   `json:"read"`
}

// Store appends events, keeps a ring, and notifies a listener (the hub) of each new one.
type Store struct {
	mu    sync.Mutex
	path  string
	ring  []Event
	max   int
	seq   int64
	onNew func(Event)
}

// New opens/creates the activity log; nil Store if the dir can't be made (daemon still runs).
func New(path string, max int) *Store {
	if max <= 0 {
		max = 500
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil
	}
	s := &Store{path: path, max: max}
	s.load()
	return s
}

// load replays the on-disk log into the ring so unread/needs-you survive a daemon restart.
func (s *Store) load() {
	f, err := os.Open(s.path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		var e Event
		if json.Unmarshal(sc.Bytes(), &e) == nil {
			s.ring = append(s.ring, e)
		}
	}
	if len(s.ring) > s.max {
		s.ring = s.ring[len(s.ring)-s.max:]
	}
}

// SetListener registers the callback fired (off the lock) for each newly recorded event.
func (s *Store) SetListener(f func(Event)) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.onNew = f
	s.mu.Unlock()
}

// Record stamps, persists, rings, and notifies one event. Returns the stored copy (with ID/TS).
func (s *Store) Record(e Event) Event {
	if s == nil {
		return e
	}
	s.mu.Lock()
	if e.TS == 0 {
		e.TS = time.Now().Unix()
	}
	if e.ID == "" {
		s.seq++
		e.ID = itoa(e.TS) + "-" + itoa(s.seq)
	}
	// A session gets ONE live needs-you at a time. A stuck fan-out once emitted a needs-you per
	// wedged sub-agent event, so the badge read "6" while there was exactly one session to deal
	// with — and answering it cleared one count, leaving five phantom entries pointing at the same
	// place. The newest ask supersedes the older ones (marked read, not deleted, so the feed still
	// shows the history); the badge then counts SESSIONS needing you, which is what it ever meant.
	superseded := false
	if e.NeedsYou && e.SessionID != "" {
		for i := range s.ring {
			if s.ring[i].NeedsYou && !s.ring[i].Read && s.ring[i].SessionID == e.SessionID {
				s.ring[i].Read = true
				superseded = true
			}
		}
	}
	s.ring = append(s.ring, e)
	if len(s.ring) > s.max {
		s.ring = s.ring[len(s.ring)-s.max:]
	}
	snapshot := append([]Event(nil), s.ring...)
	cb := s.onNew
	s.mu.Unlock()
	if superseded {
		// The flips above must survive a restart; an append alone would resurrect the phantoms on
		// next load. Same durability path MarkRead uses.
		s.rewrite(snapshot)
	} else {
		s.appendLine(e)
	}
	if cb != nil {
		cb(e)
	}
	return e
}

func (s *Store) appendLine(e Event) {
	f, err := os.OpenFile(s.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	if line, err := json.Marshal(e); err == nil {
		f.Write(append(line, '\n'))
	}
}

// Recent returns a copy of the buffered events (oldest first) for the initial activity.list reply.
func (s *Store) Recent() []Event {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Event(nil), s.ring...)
}

// MarkRead flips the read flag for the given ids (or all when ids is empty), so the unread/needs-you
// badge clears once the user has seen them. Rewrites the ring; the log is compacted lazily on next
// load (unread state is derived from the ring, which is authoritative in-memory).
func (s *Store) MarkRead(ids []string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	want := map[string]bool{}
	for _, id := range ids {
		want[id] = true
	}
	all := len(ids) == 0
	changed := false
	for i := range s.ring {
		if (all || want[s.ring[i].ID]) && !s.ring[i].Read {
			s.ring[i].Read = true
			changed = true
		}
	}
	snapshot := append([]Event(nil), s.ring...)
	s.mu.Unlock()
	if changed {
		s.rewrite(snapshot)
	}
}

// rewrite atomically replaces the log with the current ring (used after MarkRead so read-state is durable).
func (s *Store) rewrite(events []Event) {
	tmp := s.path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return
	}
	w := bufio.NewWriter(f)
	for _, e := range events {
		if line, err := json.Marshal(e); err == nil {
			w.Write(append(line, '\n'))
		}
	}
	w.Flush()
	f.Close()
	os.Rename(tmp, s.path)
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
