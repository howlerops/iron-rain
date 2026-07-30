// Package transcript is the daemon's own durable, append-only record of every session — the
// "never lose work" backstop. Providers own the live conversation (opencode's server, claude's
// jsonl), but if a send silently fails, a provider drops its copy, or the daemon restarts, that
// was the ONLY copy. Here the daemon write-AHEADs the user's prompt to disk BEFORE it's sent (so a
// failed turn can never vaporize the text) and mirrors assistant/tool/status events, one JSON line
// per event, under ~/.oculus/transcripts/<session>.jsonl. This is the same append-only-JSONL
// primitive Claude Code, Codex, and Omnara converge on, and it's what makes recovery possible.
package transcript

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Entry is one recorded event. Kind: "user" (write-ahead prompt), "assistant", "tool", "status".
type Entry struct {
	TS     int64  `json:"ts"`               // unix seconds
	Kind   string `json:"kind"`             // user | assistant | tool | status
	Text   string `json:"text"`             // prompt text, message text, tool name, or status+detail
	Detail string `json:"detail,omitempty"` // extra context (e.g. status detail)
	Author string `json:"author,omitempty"` // who sent a user entry (device/person name); "" = unattributed
}

// Store appends entries to per-session files. Safe for concurrent use; one mutex per session file so
// the write-ahead of a user prompt is durable (flushed) before the caller proceeds to send.
type Store struct {
	dir string
	mu  sync.Mutex
	fhs map[string]*os.File
}

// New opens (creating if needed) the transcripts directory; nil Store if it can't be created (the
// daemon still runs, just without the durable backstop).
func New(dir string) *Store {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil
	}
	return &Store{dir: dir, fhs: map[string]*os.File{}}
}

func (s *Store) path(sessionID string) string {
	return filepath.Join(s.dir, sessionID+".jsonl")
}

func (s *Store) file(sessionID string) (*os.File, error) {
	if f := s.fhs[sessionID]; f != nil {
		return f, nil
	}
	f, err := os.OpenFile(s.path(sessionID), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	s.fhs[sessionID] = f
	return f, nil
}

// Append durably writes one entry (open→append→flush under the lock). Returns nil on a nil Store so
// callers don't have to branch. Volume is low by design (messages/status, not raw deltas).
func (s *Store) Append(sessionID string, e Entry) error {
	if s == nil {
		return nil
	}
	if e.TS == 0 {
		e.TS = time.Now().Unix()
	}
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f, err := s.file(sessionID)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		return err
	}
	return f.Sync() // durable before we return — the write-ahead guarantee
}

// Read returns every recorded entry for a session (oldest first) — used to recover a conversation
// whose provider lost it, or to resurface prompts a failed send never delivered.
func (s *Store) Read(sessionID string) ([]Entry, error) {
	if s == nil {
		return nil, nil
	}
	f, err := os.Open(s.path(sessionID))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	var out []Entry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		var e Entry
		if json.Unmarshal(sc.Bytes(), &e) == nil {
			out = append(out, e)
		}
	}
	return out, sc.Err()
}

// Close releases open file handles (best-effort, on shutdown).
func (s *Store) Close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, f := range s.fhs {
		f.Close()
		delete(s.fhs, id)
	}
}
