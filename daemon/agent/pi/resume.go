package pi

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/howlerops/oculus/daemon/agent"
	"github.com/howlerops/oculus/daemon/protocol"
)

// Everything in this file exists to make a pi conversation survive the daemon that was driving it.
//
// pi is the one "subprocess" harness whose sessions are genuinely recoverable: it writes every
// conversation to ~/.pi/agent/sessions/--<project-path>--/<timestamp>_<uuid>.jsonl and can be
// restarted onto an existing file with `--session <path>` (docs/sessions.md, session-format.md).
// Without this, a daemon restart re-ran `pi --mode rpc` cold: the app still showed the old
// transcript, but the agent behind it had no memory of any of it and the real conversation was
// orphaned on disk. This mirrors the claude-code pattern — resume by id, then replay the on-disk
// transcript, because RPC mode does not re-emit past messages.

// DefaultSessionsRoot is ~/.pi/agent/sessions.
func DefaultSessionsRoot() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".pi", "agent", "sessions")
}

// SetSessionsRoot overrides where pi's session files are looked up (tests; a custom --session-dir).
func (p *Provider) SetSessionsRoot(root string) { p.sessionsRoot = root }

func (p *Provider) root() string {
	if p.sessionsRoot != "" {
		return p.sessionsRoot
	}
	return DefaultSessionsRoot()
}

// findSessionFile resolves a pi session id to its JSONL file. Files are named
// "<timestamp>_<uuid>.jsonl" under a per-project directory, so the id is matched on the suffix.
// An id that is already a path is returned as-is when it exists (pi's own --session accepts both).
func findSessionFile(root, id string) string {
	if id == "" {
		return ""
	}
	if strings.ContainsRune(id, os.PathSeparator) {
		if _, err := os.Stat(id); err == nil {
			return id
		}
		return ""
	}
	matches, _ := filepath.Glob(filepath.Join(root, "*", "*_"+id+".jsonl"))
	if len(matches) == 0 {
		return ""
	}
	sort.Strings(matches)
	return matches[len(matches)-1]
}

// DiscoveredSession is one pi conversation found on disk.
type DiscoveredSession struct {
	ID        string
	Cwd       string
	Path      string
	UpdatedAt time.Time
}

// Discover lists pi sessions modified within `within` of now, newest first — the input a takeover
// UI needs to offer "continue this terminal conversation". A missing root is not an error (pi may
// not be installed).
func Discover(root string, within time.Duration, now time.Time) []DiscoveredSession {
	projects, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var out []DiscoveredSession
	for _, proj := range projects {
		if !proj.IsDir() {
			continue
		}
		files, _ := os.ReadDir(filepath.Join(root, proj.Name()))
		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".jsonl") {
				continue
			}
			info, err := f.Info()
			if err != nil || now.Sub(info.ModTime()) > within {
				continue
			}
			path := filepath.Join(root, proj.Name(), f.Name())
			id, cwd := sessionHeader(path)
			if id == "" {
				continue // not a pi session file
			}
			out = append(out, DiscoveredSession{ID: id, Cwd: cwd, Path: path, UpdatedAt: info.ModTime()})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out
}

// sessionHeader reads the first line of a pi session file, which carries the session id and the
// working directory the conversation belongs to. The directory is read from the FILE rather than
// decoded from the encoded directory name, because that encoding is lossy (every separator becomes
// a dash) and a wrong cwd sends the resumed agent at the wrong repository.
func sessionHeader(path string) (id, cwd string) {
	f, err := os.Open(path)
	if err != nil {
		return "", ""
	}
	defer f.Close()
	line, err := bufio.NewReaderSize(f, 1<<16).ReadBytes('\n')
	if len(line) == 0 && err != nil {
		return "", ""
	}
	var hdr struct {
		Type string `json:"type"`
		ID   string `json:"id"`
		Cwd  string `json:"cwd"`
	}
	if json.Unmarshal(line, &hdr) != nil || hdr.Type != "session" {
		return "", ""
	}
	return hdr.ID, hdr.Cwd
}

// CanResume implements agent.ResumeChecker: a pi session is resumable only when its file is still on
// disk. Without it, restore would revive the id as a fresh, empty session pretending to be the old
// conversation.
func (p *Provider) CanResume(id string) bool {
	return p.resolveSessionPath(id) != ""
}

// resolveSessionPath maps one of OUR session ids (pi_…) or pi's own uuid to a session file. Our
// created sessions learn pi's real file from get_state (see readLoop) and remember it, exactly as
// the claude adapter remembers claude's UUID.
func (p *Provider) resolveSessionPath(id string) string {
	if path := p.resumePath(id); path != "" {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	return findSessionFile(p.root(), id)
}

// Attach resumes an existing pi conversation: pi is restarted onto its own session file, and the
// file's history is replayed so the transcript is there immediately (RPC mode replays nothing).
// Implements agent.Attacher, so hub.RestoreSessions brings pi sessions back after a daemon restart.
func (p *Provider) Attach(ctx context.Context, sessionID, cwd string) (agent.Session, error) {
	path := p.resolveSessionPath(sessionID)
	if path == "" {
		// Attaching without the file would silently start a NEW conversation under the old id — the
		// empty-session-that-claims-to-be-yours failure. Fail instead: the hub keeps the record as
		// stopped/restartable, which is honest and still gives the user a way forward.
		return nil, fmt.Errorf("pi: no session file on disk for %s — can't resume it", sessionID)
	}
	if cwd == "" {
		if _, real := sessionHeader(path); real != "" {
			cwd = real // resume in the project the conversation belongs to, not the daemon's cwd
		}
	}
	s, err := p.spawn(ctx, cwd, sessionID, []string{"--session", path})
	if err != nil {
		return nil, err
	}
	s.resumedPath = path
	go s.replayTranscript(path)
	return s, nil
}

// SelfReplaying implements agent.Replayer: true only for an attach that replays pi's own transcript,
// so the hub doesn't ALSO replay its durable copy and show every message twice. A normal (created)
// pi session replays nothing, and the durable transcript stays its only history source.
func (s *session) SelfReplaying() bool { return s.resumedPath != "" }

// replayTranscriptMax bounds a replay so attaching to a long conversation stays fast and renderable.
const (
	replayTranscriptMax     = 200
	replayTranscriptMsgSize = 20000
)

// replayTranscript emits the tail of a pi session file as SessionMessage events (MsgID = the entry
// id, so the durable transcript dedups a re-attach). Best-effort: a parse hiccup ends the replay.
func (s *session) replayTranscript(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	type msg struct{ role, text, id string }
	var tail []msg
	r := bufio.NewReaderSize(f, 1<<20)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 1 {
			var entry struct {
				Type    string `json:"type"`
				ID      string `json:"id"`
				Message struct {
					Role    string          `json:"role"`
					Content json.RawMessage `json:"content"`
				} `json:"message"`
			}
			if json.Unmarshal(line, &entry) == nil && entry.Type == "message" &&
				(entry.Message.Role == "user" || entry.Message.Role == "assistant") {
				if text := transcriptText(entry.Message.Content); text != "" {
					if len(text) > replayTranscriptMsgSize {
						text = text[:replayTranscriptMsgSize] + "\n… [truncated]"
					}
					tail = append(tail, msg{role: entry.Message.Role, text: text, id: entry.ID})
					if len(tail) > replayTranscriptMax {
						tail = tail[1:]
					}
				}
			}
		}
		if err != nil {
			break
		}
	}
	for _, m := range tail {
		s.emit(agent.Event{Type: protocol.TypeSessionMessage, Payload: protocol.SessionMessage{
			SessionID: s.id, Role: m.role, Text: m.text, MsgID: m.id,
		}})
	}
}

// transcriptText pulls the human-readable text out of a pi content value: a bare string, or the
// concatenated "text" blocks of a content array (thinking/toolCall/image blocks are skipped —
// they're re-rendered from live events, not from history).
func transcriptText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var str string
	if json.Unmarshal(raw, &str) == nil {
		return strings.TrimSpace(str)
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	var out strings.Builder
	for _, b := range blocks {
		if b.Type == "text" && b.Text != "" {
			if out.Len() > 0 {
				out.WriteString("\n")
			}
			out.WriteString(b.Text)
		}
	}
	return strings.TrimSpace(out.String())
}

// resumePath returns the session file we recorded for one of our ids ("" if unknown).
func (p *Provider) resumePath(id string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.resume[id]
}

// setResume records the session file pi actually opened for one of our ids and persists it (0600),
// so a resume after a daemon restart finds the conversation. pi names its files by its own uuid,
// which our pi_… ids don't match — the same impedance mismatch the claude adapter solves.
func (p *Provider) setResume(id, path string) {
	if id == "" || path == "" {
		return
	}
	p.mu.Lock()
	if p.resume == nil {
		p.resume = map[string]string{}
	}
	if p.resume[id] == path {
		p.mu.Unlock()
		return
	}
	p.resume[id] = path
	data, _ := json.MarshalIndent(p.resume, "", "  ")
	dst := p.resumeFile
	p.mu.Unlock()
	if dst != "" && data != nil {
		_ = os.WriteFile(dst, data, 0o600)
	}
}
