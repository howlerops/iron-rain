// Package claudecode drives claude-code as a PERSISTENT streaming session through a
// small Node sidecar (see sidecar/) built on the Claude Agent SDK. The daemon and the
// sidecar speak a line-delimited JSON protocol over stdio:
//
//	daemon -> sidecar : {"t":"prompt","text":"..."}          send a user turn
//	                    {"t":"approval","id":"..","decision":"allow|deny"}  answer a tool approval
//	                    {"t":"stop"}                          interrupt the current turn
//	sidecar -> daemon : {"t":"session","id":".."}             the session id
//	                    {"t":"text","text":".."}              assistant answer delta
//	                    {"t":"thinking","text":".."}          reasoning delta
//	                    {"t":"tool","tool":"..","detail":".."} a tool started running
//	                    {"t":"approval","id":"..","tool":"..","detail":".."}  approval REQUIRED (blocks in the sidecar)
//	                    {"t":"idle"}                          turn finished
//	                    {"t":"error","message":".."}
//
// This replaces the old single-shot `claude -p` + PreToolUse-HTTP-hook design, whose
// hook does NOT block in -p mode (anthropics/claude-code#36071) — tools could run
// unapproved. In streaming mode the sidecar's canUseTool callback genuinely blocks the
// tool until the daemon answers, so approvals are enforced.
//
// See ../../skills/oculus-... and sidecar/README.md.
package claudecode

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"

	"github.com/howlerops/oculus/daemon/agent"
	"github.com/howlerops/oculus/daemon/discovery"
	"github.com/howlerops/oculus/daemon/mcp"
	"github.com/howlerops/oculus/daemon/procutil"
	"github.com/howlerops/oculus/daemon/protocol"
	"github.com/howlerops/oculus/daemon/textutil"
)

// Provider spawns claude-code sessions via the sidecar.
type Provider struct {
	sidecar     []string // command to run the sidecar, e.g. ["node", "/path/sidecar.mjs"]
	mu          sync.Mutex
	resume      map[string]string // our stable session id (cc_…) -> claude's real session UUID
	resumePath  string            // where the map persists (survives restart), 0600
	projectsDir string            // override for ~/.claude/projects (tests)
}

// SetProjectsDir overrides where claude's on-disk transcripts are looked up (tests).
func (p *Provider) SetProjectsDir(dir string) { p.projectsDir = dir }

// projects is the session-side accessor for the provider's transcript directory.
func (s *session) projects() string {
	if s.p != nil {
		return s.p.projects()
	}
	return discovery.DefaultClaudeProjectsDir()
}

func (p *Provider) projects() string {
	if p.projectsDir != "" {
		return p.projectsDir
	}
	return discovery.DefaultClaudeProjectsDir()
}

// New returns a Provider that runs the given sidecar command (argv). For tests,
// point it at a fake sidecar script that speaks the stdio protocol.
func New(sidecar []string) *Provider {
	p := &Provider{sidecar: sidecar, resume: map[string]string{}}
	if home, err := os.UserHomeDir(); err == nil {
		p.resumePath = filepath.Join(home, ".oculus", "claude-resume.json")
		if data, err := os.ReadFile(p.resumePath); err == nil {
			_ = json.Unmarshal(data, &p.resume)
		}
	}
	// A daemon that was SIGKILLed ran no cleanup at all, so its sidecars are still out there holding
	// SDK connections and `claude` children. This is the one moment we can tell "ours, abandoned"
	// apart from "someone else's, still live" without guessing: we have not spawned any child yet, so
	// nothing with our sidecar path can legitimately be an orphan of ours. See sweep.go for exactly
	// how narrowly that call is made — getting it wrong would kill a second daemon's running work.
	if len(sidecar) == 2 {
		if pids := SweepOrphanSidecars(sidecar[1]); len(pids) > 0 {
			log.Printf("claude-code: reaped %d sidecar(s) orphaned by a previous daemon: %v", len(pids), pids)
		}
	}
	return p
}

// resumeID returns claude's real session UUID for one of our session ids (empty if unknown).
func (p *Provider) resumeID(id string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.resume[id]
}

// setResume records claude's real session UUID for our session id and persists it, so a resume
// after restart uses the UUID claude expects — not our cc_… id (which claude rejects as "not a UUID").
func (p *Provider) setResume(id, uuid string) {
	if id == "" || uuid == "" || id == uuid {
		return
	}
	p.mu.Lock()
	if p.resume[id] == uuid {
		p.mu.Unlock()
		return
	}
	p.resume[id] = uuid
	data, _ := json.MarshalIndent(p.resume, "", "  ")
	path := p.resumePath
	p.mu.Unlock()
	if path != "" && data != nil {
		_ = os.WriteFile(path, data, 0o600)
	}
}

// CanResume implements agent.ResumeChecker: a claude-code session is only resumable when we know
// claude's real UUID for it (recorded on its first turn) or the id itself is that UUID. Without it,
// "re-attaching" starts a FRESH empty session that lies about being the old one.
func (p *Provider) CanResume(id string) bool {
	return p.resumeID(id) != "" || looksLikeUUID(id)
}

func (p *Provider) Name() string { return "claude-code" }

// List returns no live sessions (claude-code sessions are the daemon's child
// processes; discovery of on-disk transcripts is handled by daemon/discovery).
func (p *Provider) List(context.Context) ([]protocol.Session, error) { return nil, nil }

// Create starts a new streaming claude-code session and kicks it off with prompt.
func (p *Provider) Create(ctx context.Context, cwd, prompt string) (agent.Session, error) {
	return p.start(ctx, cwd, "cc_"+randID(), "create", prompt, false)
}

// CreatePlan starts a session in plan mode: the agent proposes a plan and requests approval
// (via ExitPlanMode → the normal approval channel) before making changes.
func (p *Provider) CreatePlan(ctx context.Context, cwd, prompt string) (agent.Session, error) {
	return p.start(ctx, cwd, "cc_"+randID(), "create", prompt, true)
}

// Models offers the Claude model aliases the Agent SDK accepts. Aliases resolve to the latest of
// each tier, so they stay valid across model refreshes without hardcoding dated ids.
func (p *Provider) Models(ctx context.Context) ([]protocol.ModelInfo, error) {
	return []protocol.ModelInfo{
		{ID: "opus", Name: "Claude Opus"},
		{ID: "sonnet", Name: "Claude Sonnet"},
		{ID: "haiku", Name: "Claude Haiku"},
	}, nil
}

// Attach resumes an existing claude-code session by id, running in its original cwd so the
// resumed session's tool calls (edits, bash) target the right project (the SDK's resume runs
// as a fresh process in the given directory, not the session's recorded one).
func (p *Provider) Attach(ctx context.Context, sessionID, cwd string) (agent.Session, error) {
	if cwd == "" {
		// No recorded directory (a pre-Phase-0 record, or a take-over discovered by transcript). The
		// sidecar would then inherit the DAEMON's directory and start editing an unrelated repository.
		// claude stamps the project on every transcript entry, so the conversation itself knows.
		if real := p.transcriptCwd(sessionID); real != "" {
			log.Printf("claude-code: attach %s — no stored cwd; resuming in the project its transcript records (%s)", sessionID, real)
			cwd = real
		}
	}
	sess, err := p.start(ctx, cwd, sessionID, "attach", "", false)
	if err != nil {
		return nil, err
	}
	// The Agent SDK resume does NOT re-emit past messages, so without this the pane is EMPTY after
	// attaching — for a discovered take-over AND for the daemon's own cc_ sessions restored after a
	// restart (their uuid comes from the resume map). Replay the tail of the on-disk transcript
	// (~/.claude/projects/…/<uuid>.jsonl) as history so the conversation shows immediately.
	if cs, ok := sess.(*session); ok && cs.replayUUID != "" {
		go cs.replayTranscript(cs.replayUUID)
	}
	return sess, nil
}

// SelfReplaying implements agent.Replayer: true only when this attach can actually replay its JSONL
// transcript (uuid known). When it can't, the hub's durable transcript is the history source —
// claiming self-replay unconditionally left restored cc_ sessions with NO history at all.
func (s *session) SelfReplaying() bool { return s.replayUUID != "" }

// NativeSessionID reports the id CLAUDE knows this session by — the UUID it names the on-disk
// transcript after (~/.claude/projects/…/<uuid>.jsonl), which is also the only id a host scan can see.
// For every session we created it differs from s.id (ours is cc_…), and that mismatch is what let a
// session the daemon already drives keep appearing in the take-over list as if it were an untouched
// terminal session: discovery reported the UUID, the hub only knew the cc_ id, and "taking it over"
// a second time resumed the same conversation into a SECOND writer, forking it.
//
// Empty means UNKNOWN, not "no id": the sidecar hasn't reported the UUID yet (a brand-new session
// before its first init), or the resume map was lost. Callers must treat empty as "don't dedupe" —
// matching on it would drop unrelated rows and hide real take-over candidates.
func (s *session) NativeSessionID() string {
	if s.p != nil {
		if uuid := s.p.resumeID(s.id); uuid != "" {
			return uuid
		}
	}
	if looksLikeUUID(s.id) {
		// A discovered session we took over: our id came from the transcript filename, so it already
		// IS claude's uuid and no resume-map entry is ever recorded for it (setResume skips id==uuid).
		return s.id
	}
	return ""
}

// looksLikeUUID reports whether id has the 8-4-4-4-12 hex shape of a claude session UUID.
func looksLikeUUID(id string) bool {
	if len(id) != 36 {
		return false
	}
	for i, c := range id {
		switch i {
		case 8, 13, 18, 23:
			if c != '-' {
				return false
			}
		default:
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
				return false
			}
		}
	}
	return true
}

// replayTranscriptMax bounds how many trailing messages a take-over replays (and how large any one
// message may be) so attaching to a huge session stays fast and the transcript stays renderable.
const (
	replayTranscriptMax     = 200
	replayTranscriptMsgSize = 20000
	// replayToolOutputSize caps one replayed tool result, matching the sidecar's live cap so a card
	// doesn't grow when it comes back from history.
	replayToolOutputSize = 8000
)

// transcriptCwd reads the working directory claude recorded for a session, by scanning the head of
// its JSONL transcript for the first entry carrying a cwd. Empty when the transcript is unknown.
func (p *Provider) transcriptCwd(sessionID string) string {
	uuid := p.resumeID(sessionID)
	if uuid == "" && looksLikeUUID(sessionID) {
		uuid = sessionID
	}
	if uuid == "" {
		return ""
	}
	matches, _ := filepath.Glob(filepath.Join(p.projects(), "*", uuid+".jsonl"))
	if len(matches) == 0 {
		return ""
	}
	f, err := os.Open(matches[0])
	if err != nil {
		return ""
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 1<<20)
	// Bounded scan: the header lines (summary/mode/permission-mode) carry no cwd, the first real
	// entry does. Reading the whole file to find it would be pointless work on a huge transcript.
	for i := 0; i < 50; i++ {
		line, err := r.ReadBytes('\n')
		if len(line) > 1 {
			var entry struct {
				Cwd string `json:"cwd"`
			}
			if json.Unmarshal(line, &entry) == nil && entry.Cwd != "" {
				return entry.Cwd
			}
		}
		if err != nil {
			break
		}
	}
	return ""
}

// replayTranscript reads the session's on-disk JSONL transcript and emits its trailing user/assistant
// messages as SessionMessage events (MsgID = the line's uuid, so the durable transcript dedups a
// re-attach). Best-effort: any parse hiccup just ends the replay.
func (s *session) replayTranscript(uuid string) {
	matches, _ := filepath.Glob(filepath.Join(s.projects(), "*", uuid+".jsonl"))
	if len(matches) == 0 {
		return
	}
	f, err := os.Open(matches[0])
	if err != nil {
		return
	}
	defer f.Close()
	// Messages and tool cards share ONE ordered slice. They have to: a take-over that replayed the
	// text and then all the tool calls would render a transcript where every command appears after
	// the conversation that followed it. The trailing window therefore bounds the two together.
	type row struct {
		tool                       bool
		role, text, id             string // message
		name, title, output, state string // tool card
	}
	var tail []row
	// tool_use id -> the identity half of its card. claude writes the tool_use block before the
	// tool_result that answers it, so one pass is enough to pair them. Entries are deleted on pairing;
	// what remains at EOF are tools that never reported, which are deliberately not emitted (below).
	pending := map[string]struct{ name, title string }{}
	r := bufio.NewReaderSize(f, 1<<20)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 1 {
			var entry struct {
				Type    string `json:"type"`
				UUID    string `json:"uuid"`
				Message struct {
					Role    string          `json:"role"`
					Content json.RawMessage `json:"content"`
				} `json:"message"`
			}
			if json.Unmarshal(line, &entry) == nil && (entry.Type == "user" || entry.Type == "assistant") {
				if text := transcriptText(entry.Message.Content); text != "" {
					if len(text) > replayTranscriptMsgSize {
						text = text[:replayTranscriptMsgSize] + "\n… [truncated]"
					}
					tail = append(tail, row{role: entry.Message.Role, text: text, id: entry.UUID})
					if len(tail) > replayTranscriptMax {
						tail = tail[1:]
					}
				}
				// Tool blocks live in the SAME content array the text came from, and transcriptText
				// skips them by design. Without this a taken-over session replays as pure conversation:
				// the agent appears to have talked its way through the work and never run anything.
				for _, b := range transcriptToolBlocks(entry.Message.Content) {
					switch b.Type {
					case "tool_use":
						if b.ID != "" {
							pending[b.ID] = struct{ name, title string }{b.Name, replayToolTitle(b.Input)}
						}
					case "tool_result":
						// A result whose tool_use we never saw would produce exactly the anonymous,
						// untitled card this adapter was just fixed to stop persisting, so skip it.
						u, ok := pending[b.ToolUseID]
						if !ok {
							continue
						}
						delete(pending, b.ToolUseID)
						state := "completed"
						if b.IsError {
							state = "error"
						}
						tail = append(tail, row{tool: true, id: b.ToolUseID, name: u.name, title: u.title,
							output: replayToolOutput(b.Content), state: state})
						if len(tail) > replayTranscriptMax {
							tail = tail[1:]
						}
					}
				}
			}
		}
		if err != nil {
			break
		}
	}
	// A tool_use still pending at EOF never reported a result — the turn was interrupted, or the
	// transcript ends mid-tool. It is NOT emitted: the only states worth replaying are terminal ones,
	// and a card left "running" in replayed history is a spinner that can never resolve, while
	// inventing "completed" would claim a result nobody recorded.
	for _, m := range tail {
		if m.tool {
			// ID is claude's tool_use id, which the hub persists as "tool:"+ID — so replaying a
			// take-over twice dedups against the durable transcript instead of doubling every card.
			adds, dels := agent.DiffStatFrom(m.output)
			s.emit(agent.Event{Type: protocol.TypeSessionTool, Payload: protocol.SessionTool{
				SessionID: s.id, ID: m.id, Name: m.name, Title: m.title, Output: m.output, Status: m.state,
				Additions: adds, Deletions: dels,
			}})
			continue
		}
		role := m.role
		if role == "" {
			role = "assistant"
		}
		s.emit(agent.Event{Type: protocol.TypeSessionMessage, Payload: protocol.SessionMessage{
			SessionID: s.id, Role: role, Text: m.text, MsgID: m.id,
		}})
	}
}

// transcriptBlock is the union of the content-array block shapes a replay cares about: tool_use
// (id/name/input) and tool_result (tool_use_id/content/is_error). Text blocks fall through with a
// Type this caller ignores — transcriptText already handled those.
type transcriptBlock struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

// transcriptToolBlocks decodes a message's content array. Plain-string content (the common shape for
// a typed user prompt) has no blocks and decodes to nothing rather than an error.
func transcriptToolBlocks(raw json.RawMessage) []transcriptBlock {
	if len(raw) == 0 {
		return nil
	}
	var blocks []transcriptBlock
	if json.Unmarshal(raw, &blocks) != nil {
		return nil
	}
	return blocks
}

// replayToolTitle rebuilds the one-line command summary the sidecar's toolSummary produces live, so
// a replayed card reads the same as one watched in real time — the same keys in the same order,
// falling back to the encoded input. Divergence here would be visible as history that doesn't match
// what the user remembers seeing.
func replayToolTitle(input json.RawMessage) string {
	if len(input) == 0 {
		return ""
	}
	var obj map[string]any
	if json.Unmarshal(input, &obj) != nil {
		return ""
	}
	for _, k := range []string{"command", "file_path", "path", "pattern", "url", "query", "prompt"} {
		if s, ok := obj[k].(string); ok && strings.TrimSpace(s) != "" {
			return truncRunes(s, 200)
		}
	}
	if b, err := json.Marshal(obj); err == nil {
		return truncRunes(string(b), 160)
	}
	return ""
}

// replayToolOutput flattens a tool_result's content — a string, or an array of text blocks — and
// caps it, mirroring the sidecar's toolResultText so a replayed card carries the same output a live
// one did.
func replayToolOutput(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var str string
	if json.Unmarshal(raw, &str) == nil {
		return truncRunes(str, replayToolOutputSize)
	}
	var blocks []struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	var out strings.Builder
	for _, b := range blocks {
		out.WriteString(b.Text)
	}
	return truncRunes(out.String(), replayToolOutputSize)
}

// truncRunes caps s at n BYTES without splitting a rune. A plain s[:n] can cut a multi-byte
// character in half, and the resulting invalid UTF-8 is silently rewritten to replacement
// characters when the event is encoded — turning the tail of a truncated command into mojibake.
func truncRunes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + "…"
}

// transcriptText extracts the human-readable text from a transcript message's content: a plain
// string, or the concatenated text blocks of a content array (tool_use/tool_result blocks skipped).
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

func (p *Provider) start(ctx context.Context, cwd, id, mode, prompt string, plan bool) (agent.Session, error) {
	if len(p.sidecar) == 0 {
		return nil, fmt.Errorf("claude-code: no sidecar configured")
	}
	runCtx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(runCtx, p.sidecar[0], p.sidecar[1:]...)
	if cwd != "" {
		cmd.Dir = cwd
	}
	cmd.Env = append(os.Environ(), "OCULUS_SESSION_ID="+id, "OCULUS_MODE="+mode)
	// Daemon-owned MCP servers, rendered into the Agent SDK's mcpServers shape. Passed by env rather
	// than a file so credentials never touch disk in a location the user's other tools can read.
	if cfg, ok := mcp.FromContext(ctx); ok {
		if js := cfg.Claude(); js != "" {
			cmd.Env = append(cmd.Env, "OCULUS_MCP_CONFIG="+js)
			if cfg.Exclusive {
				// The daemon owns MCP for this session: the harness must not also start its own copies.
				cmd.Env = append(cmd.Env, "OCULUS_MCP_EXCLUSIVE=1")
			}
		}
	}
	if plan {
		cmd.Env = append(cmd.Env, "OCULUS_PLAN=1")
	}
	// Resume with claude's real session UUID (captured on create), not our cc_… id which it rejects.
	replayUUID := ""
	if mode == "attach" {
		rid := p.resumeID(id)
		if rid == "" && looksLikeUUID(id) {
			// A DISCOVERED session (take-over): its id came from the on-disk transcript filename and
			// IS claude's real session UUID — resume it directly. Without this, take-over silently
			// started a FRESH sidecar session with none of the conversation.
			rid = id
		}
		if rid != "" {
			cmd.Env = append(cmd.Env, "OCULUS_CLAUDE_RESUME="+rid)
			replayUUID = rid // the JSONL transcript lives under claude's REAL uuid, whatever our id is
		}
	}
	// Sidecar diagnostics go through the daemon log (and so into loghub / the app's log panel).
	// Writing to the inherited os.Stderr FD bypassed loghub entirely, so a crashing sidecar was
	// invisible to anyone not tailing the daemon's own output.
	cmd.Stderr = procutil.LogWriter("claude-sidecar[" + id + "]")
	procutil.Isolate(cmd) // sidecar spawns node children — kill the tree, not just the wrapper
	// Isolate is what makes the sidecar a process-group LEADER (pgid == its own pid). Tell it so:
	// on the path where it has to force-exit (see sidecar.mjs) it kills its own group to take the
	// `claude` child with it, and a negative pid is only safe to signal when you lead that group.
	// Node has no getpgrp(), so this flag is the sidecar's only way to know — and it is set here,
	// on the one line that makes it true, so the two can never drift apart.
	cmd.Env = append(cmd.Env, "OCULUS_SIDECAR_PGLEADER=1")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, err
	}
	s := &session{
		id:         id,
		replayUUID: replayUUID,
		p:          p,
		events:     make(chan agent.Event, 32),
		stdin:      stdin,
		cmd:        cmd,
		cancel:     cancel,
		done:       make(chan struct{}),
	}
	// readLoop returns on stdout EOF (which the ctx-cancel kill triggers); Wait() after
	// it returns reaps the child and releases the stdin/stdout pipe fds without racing
	// the scanner. Without this the process lingers as a zombie and its fds/goroutine leak.
	go func() {
		s.readLoop(stdout)
		_ = cmd.Wait()
	}()
	if prompt != "" {
		_ = s.Prompt(ctx, prompt)
	}
	return s, nil
}

type session struct {
	id     string
	p      *Provider // for recording claude's real session UUID (resume map)
	events chan agent.Event
	stdin  io.WriteCloser
	cmd    *exec.Cmd
	cancel context.CancelFunc

	writeMu   sync.Mutex
	closeOnce sync.Once
	done      chan struct{}

	// events now has TWO senders (readLoop + the take-over transcript replay), so closing it must be
	// mutually exclusive with sends: sendMu + closed make "send on closed channel" impossible.
	sendMu sync.Mutex
	closed bool

	// replayUUID is claude's real session uuid when this attach can replay its JSONL transcript
	// ("" = it can't — the hub's durable transcript is then the history source, see SelfReplaying).
	replayUUID string

	// Liveness probes (Probe/deliverPong): one channel per outstanding ping, keyed by its id so a
	// slow answer can never be mistaken for a newer probe's.
	pingMu  sync.Mutex
	pings   map[string]chan bool
	pingSeq atomic.Uint64

	// toolCards remembers a running tool card's identity (name + command summary) until its terminal
	// frame arrives. The sidecar splits ONE card across TWO frames: the tool_use frame carries
	// {tool, detail} and the matching tool_result frame carries only {id, output, status} — no name,
	// no detail. The hub persists ONLY the terminal state of a tool card, so without re-attaching
	// here the durable row is written with Name:"" and Title:"" and every bash card comes back from
	// history as an anonymous, untitled box ("bash commands with no title/description"). It looks
	// correct while the turn is live purely because the app merges the terminal frame onto the card
	// already on screen and keeps the old fields — which is what hides the bug until a reload,
	// reconnect, or history replay reads the persisted row instead of the live one.
	toolMu    sync.Mutex
	toolCards map[string]toolCard

	// Live ambient state, accumulated from "facts" frames. Held so a client attaching mid-session
	// gets the whole picture at once (Facts) instead of only the fields that happen to change
	// afterwards — a status bar that fills in one field per event is worse than no status bar.
	factsMu sync.Mutex
	facts   protocol.SessionFacts
	// commands is claude's slash-command list, reported once at init.
	commands []string
}

// mergeFacts folds a partial "facts" frame onto the running picture and returns the whole of it.
// Empty fields do not overwrite — a mode-change frame carries only `mode`, and must not blank the
// model and cwd learned at init.
func (s *session) mergeFacts(m outMsg) protocol.SessionFacts {
	s.factsMu.Lock()
	defer s.factsMu.Unlock()
	s.facts.SessionID = s.id
	if m.Model != "" {
		s.facts.Model = m.Model
	}
	if m.Mode != "" {
		s.facts.Mode = m.Mode
	}
	if m.CWD != "" {
		s.facts.CWD = m.CWD
	}
	if len(m.Commands) > 0 {
		s.commands = m.Commands
	}
	if s.facts.Branch == "" && s.facts.CWD != "" {
		s.facts.Branch = agent.GitBranch(s.facts.CWD)
	}
	return s.facts
}

// Facts reports the current ambient state (agent.Factual).
func (s *session) Facts(context.Context) protocol.SessionFacts {
	s.factsMu.Lock()
	defer s.factsMu.Unlock()
	return s.facts
}

// Capabilities declares what claude-code can do (agent.Capable).
//
// Modes come from protocol.Modes() rather than claude's own permissionMode names. Modes are enforced
// daemon-side against the approval layer, so they are a property of the SYSTEM, not of this
// provider; listing claude's native vocabulary here would create a second set of mode ids that means
// almost-but-not-quite the same thing, and every client would need a translation table. The native
// value is a hint we forward in SetMode, not the contract.
func (s *session) Capabilities() protocol.SessionCapabilities {
	return protocol.SessionCapabilities{
		SessionID: s.id,
		Provider:  "claude-code",
		Modes:     protocol.Modes(),
		Commands:  true,
		Agents:    true,
		Models:    true,
		// Thread is deliberately EMPTY, and this is a correction of my own mistake rather than a
		// statement about claude-code.
		//
		// It was declared Fork+Tree+Compact on the strength of the SDK having forkSession and the
		// transcripts being a parent-linked tree (uuid/parentUuid, leafUuid, isSidechain — all really
		// there). But this session type does not implement agent.ThreadOps, so every one of those
		// controls would have appeared in the UI and failed on use: the daemon answers "claude-code
		// sessions cannot branch their history".
		//
		// That is the precise failure the manifest exists to prevent, committed in the manifest
		// itself. Declaring a capability is a promise about THIS ADAPTER, not about what the product
		// could theoretically do — so it stays empty until the operations are implemented and
		// verified against a real session, the way opencode's and pi's were.
	}
}

// toolCard is the identity half of an inline tool card, held only between the sidecar's tool_use
// frame and its matching tool_result frame.
type toolCard struct{ name, title string }

// rememberToolCard records a running card's identity. A frame carrying neither field stores
// nothing, so it can never overwrite a real identity with blanks.
func (s *session) rememberToolCard(id, name, title string) {
	if id == "" || (name == "" && title == "") {
		return
	}
	s.toolMu.Lock()
	defer s.toolMu.Unlock()
	if s.toolCards == nil {
		s.toolCards = map[string]toolCard{}
	}
	s.toolCards[id] = toolCard{name: name, title: title}
}

// takeToolCard returns and DELETES a remembered card. Delete-on-read is what bounds the map: the
// entry is dead the instant its terminal frame is emitted, and a session that stays up for days
// runs thousands of tools, so retaining them would be a genuine unbounded leak in a long-lived
// daemon rather than a theoretical one. A miss — a terminal frame with no prior running frame,
// which happens when the daemon restarted mid-turn or when frames are replayed — returns the zero
// value, leaving the fields empty. We never invent a name for a tool we did not watch start.
func (s *session) takeToolCard(id string) toolCard {
	if id == "" {
		return toolCard{}
	}
	s.toolMu.Lock()
	defer s.toolMu.Unlock()
	c, ok := s.toolCards[id]
	if ok {
		delete(s.toolCards, id)
	}
	return c
}

// peekToolCard reads a card WITHOUT consuming it — for the sub-agent announcement, which borrows the
// Task tool's description for a title while that tool call is still running and still owes us its
// terminal frame. Taking it here would blank the card's identity in the durable record.
func (s *session) peekToolCard(id string) toolCard {
	if id == "" {
		return toolCard{}
	}
	s.toolMu.Lock()
	defer s.toolMu.Unlock()
	return s.toolCards[id]
}

// forgetToolCards drops every still-pending card at teardown. Tools interrupted by a stop or a
// crashed sidecar never get a terminal frame, so delete-on-read alone would strand their entries
// for as long as the session object is reachable.
func (s *session) forgetToolCards() {
	s.toolMu.Lock()
	defer s.toolMu.Unlock()
	s.toolCards = nil
}

func (s *session) ID() string                 { return s.id }
func (s *session) Provider() string           { return "claude-code" }
func (s *session) Events() <-chan agent.Event { return s.events }

// inMsg / outMsg are the stdio protocol frames.
type inMsg struct {
	T        string   `json:"t"`
	Text     string   `json:"text,omitempty"`
	ID       string   `json:"id,omitempty"`
	Decision string   `json:"decision,omitempty"`
	Images   []imgAtt `json:"images,omitempty"`
}
type imgAtt struct {
	Mime string `json:"mime"`
	Data string `json:"data"`
}

// maxLoggedBadFrames caps the unparseable-frame log per session.
const maxLoggedBadFrames = 5

type outMsg struct {
	T                string        `json:"t"`
	ID               string        `json:"id,omitempty"`
	Text             string        `json:"text,omitempty"`
	Tool             string        `json:"tool,omitempty"`
	Detail           string        `json:"detail,omitempty"`
	Output           string        `json:"output,omitempty"`
	Status           string        `json:"status,omitempty"`
	Message          string        `json:"message,omitempty"`
	InputTokens      int           `json:"input_tokens,omitempty"`
	OutputTokens     int           `json:"output_tokens,omitempty"`
	CacheReadTokens  int           `json:"cache_read_tokens,omitempty"`
	CacheWriteTokens int           `json:"cache_write_tokens,omitempty"`
	CostUSD          float64       `json:"cost_usd,omitempty"`
	CostReported     bool          `json:"cost_reported,omitempty"`
	Todos            []sidecarTodo `json:"todos,omitempty"`
	// Input is the approved tool's raw arguments (approval messages only) — what the daemon's rule
	// engine needs to scope an "always allow" to a path or command shape.
	Input json.RawMessage `json:"input,omitempty"`
	// Busy answers a ping: whether the sidecar has a turn in flight (pong messages only).
	Busy bool `json:"busy,omitempty"`
	// Ambient state from the SDK's init message and from mode switches ("facts" frames). Each is
	// optional: a facts frame carrying only `mode` updates only the mode, so a partial report can
	// never blank out a field it says nothing about.
	Model    string   `json:"model,omitempty"`
	Mode     string   `json:"mode,omitempty"`
	CWD      string   `json:"cwd,omitempty"`
	Commands []string `json:"commands,omitempty"`
	MCP      int      `json:"mcp,omitempty"`
}

type sidecarTodo struct {
	Content string `json:"content"`
	Status  string `json:"status"`
}

func (s *session) send(m inMsg) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err = s.stdin.Write(b)
	return err
}

// Prompt sends a follow-up user turn into the SAME running session.
func (s *session) Prompt(_ context.Context, text string) error {
	return s.send(inMsg{T: "prompt", Text: text})
}

// PromptImages sends a multimodal turn; the sidecar builds Anthropic image content blocks.
func (s *session) PromptImages(_ context.Context, text string, images []protocol.ImageAttachment) error {
	ims := make([]imgAtt, len(images))
	for i, im := range images {
		ims[i] = imgAtt{Mime: im.Mime, Data: im.Data}
	}
	return s.send(inMsg{T: "prompt", Text: text, Images: ims})
}

// Respond answers a tool approval; the sidecar's canUseTool unblocks. allow/always→allow.
func (s *session) Respond(_ context.Context, approvalID, decision string) error {
	d := "deny"
	if decision == protocol.DecisionAllow || decision == protocol.DecisionAlways {
		d = "allow"
	}
	return s.send(inMsg{T: "approval", ID: approvalID, Decision: d})
}

func (s *session) Stop(_ context.Context) error { return s.send(inMsg{T: "stop"}) }

// Probe implements agent.Prober: ask the sidecar directly whether a turn is in flight.
//
// Until this existed the hub's reconciler skipped claude-code entirely (it only probes sessions that
// implement Prober), so a wedged claude-code turn heartbeated "working" forever with nothing in the
// system able to contradict it. The round-trip is answered from the sidecar's stdin handler rather
// than its query loop, so a turn stuck inside a tool still replies — which is the whole point: we
// need to distinguish "busy" from "dead", and those two look identical from the event stream.
func (s *session) Probe(ctx context.Context) (bool, error) {
	id := fmt.Sprintf("pg_%d", s.pingSeq.Add(1))
	ch := make(chan bool, 1)
	s.pingMu.Lock()
	if s.pings == nil {
		s.pings = map[string]chan bool{}
	}
	s.pings[id] = ch
	s.pingMu.Unlock()
	defer func() {
		s.pingMu.Lock()
		delete(s.pings, id)
		s.pingMu.Unlock()
	}()

	if err := s.send(inMsg{T: "ping", ID: id}); err != nil {
		return false, err
	}
	select {
	case busy := <-ch:
		return busy, nil
	case <-s.done:
		return false, errors.New("claude-code: session closed")
	case <-ctx.Done():
		// No answer at all: the sidecar is wedged in a way that blocks even its stdin loop, or it is
		// gone. Either way it is unreachable, and the reconciler counts these toward abandonment.
		return false, fmt.Errorf("claude-code: sidecar did not answer a ping: %w", ctx.Err())
	}
}

// deliverPong hands a ping's answer to the waiting Probe. A pong with no waiter (a probe that timed
// out just before the answer arrived) is dropped, never blocked on.
func (s *session) deliverPong(id string, busy bool) {
	s.pingMu.Lock()
	ch := s.pings[id]
	s.pingMu.Unlock()
	if ch == nil {
		return
	}
	select {
	case ch <- busy:
	default:
	}
}

// Nudge implements agent.Nudger: deliver a message into a turn that is already running, without
// disturbing it. This is safe here because the sidecar drives the SDK with a STREAMING input
// generator (sidecar.mjs inputGen/pushInput): a pushed message joins the input stream the running
// query is already consuming. It is the same wire message as Prompt — the distinct method exists
// because the CALLER must not have to know which providers can be nudged and which would abort.
func (s *session) Nudge(_ context.Context, text string) error {
	return s.send(inMsg{T: "prompt", Text: text})
}

// SetModel switches the model via the SDK's setModel (provider is unused — Claude ids stand alone).
func (s *session) SetModel(_, model string) error { return s.send(inMsg{T: "model", Text: model}) }

// SetMode implements agent.ModeSetter: forward the mode to the sidecar, which maps ask/architect onto
// the SDK's "plan" permission mode for subsequent turns. The daemon enforces the mode itself either
// way — this only makes the model aware of the intent.
func (s *session) SetMode(_ context.Context, mode string) error {
	return s.send(inMsg{T: "mode", Text: mode})
}

// Close ends the session and REAPS the sidecar's whole process tree. Every step below is
// load-bearing and the order matters.
//
// Closing stdin first is the cooperative path: the sidecar sees EOF, interrupts its turn and lets the
// Agent SDK shut down the `claude` child it spawned through the SDK's own cleanup, so the common case
// tears down with no signals at all and no truncated events (see sidecar.mjs's EOF handler).
//
// TerminateGroup is what makes the reap UNCONDITIONAL, and it has to be here precisely BECAUSE
// procutil.Isolate put the sidecar in its own process group. That isolation is deliberate — it is how
// a runaway tool tree gets killed as a unit — but it also means the sidecar never receives the
// daemon's process-group signals, so nothing ends it implicitly and explicit reaping is mandatory.
// cancel() alone was never enough either: exec.CommandContext's cancellation kills only the DIRECT
// child, so it would leave the `claude` process the sidecar spawned running, reparented to launchd,
// invisible to everything. Skipping this is how 143 orphaned sidecars — 284 processes counting their
// `claude` children, 12.7 GB resident, the oldest a week old — accumulated on one machine.
func (s *session) Close() error {
	s.closeOnce.Do(func() {
		close(s.done)
		if s.stdin != nil {
			s.writeMu.Lock()
			_ = s.stdin.Close()
			s.writeMu.Unlock()
		}
		// SIGTERM the whole group, a short grace for the cooperative exit above to land, then SIGKILL.
		// Safe on a sidecar that already exited, and safe to reach twice (closeOnce makes it once).
		procutil.TerminateGroup(s.cmd)
		// Release the CommandContext watcher goroutine. The process is already gone by here; this is
		// bookkeeping, not the kill it used to be mistaken for.
		if s.cancel != nil {
			s.cancel()
		}
		// readLoop drains stdout to EOF either way and the reaping goroutine then Wait()s the child.
	})
	return nil
}

func (s *session) emit(ev agent.Event) {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	if s.closed {
		return
	}
	select {
	case s.events <- ev:
	case <-s.done:
	}
}

// closeEvents ends the event stream exactly once, excluding concurrent emitters — the readLoop is no
// longer the only sender (replayTranscript emits from its own goroutine), so a bare close(s.events)
// could panic a concurrent send and take the whole daemon down (it did).
func (s *session) closeEvents() {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	close(s.events)
}

func (s *session) readLoop(stdout io.ReadCloser) {
	defer s.closeEvents()
	// stdout EOF means the sidecar is gone (normal exit, stop, kill, crash), so this is the one
	// teardown path every session takes — release any card that never got its terminal frame here.
	defer s.forgetToolCards()
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	idle := false
	badFrames := 0
	for sc.Scan() {
		var m outMsg
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			// Skipping a malformed frame is right — one bad line must not kill the stream — but doing
			// it silently is not. The pi adapter had the identical pattern and a field typed too
			// narrowly (string where the agent sent an object), so every assistant delta failed to
			// decode and was dropped here without a trace: the agent accepted prompts, closed its
			// turns idle, and never said a word. Rate-limited so a systematically bad frame shows up
			// without flooding the log.
			if badFrames < maxLoggedBadFrames {
				badFrames++
				log.Printf("claude-code: dropping an unparseable frame: %v (%s)", err, textutil.FirstLine(string(sc.Bytes()), 160))
			}
			continue
		}
		switch m.T {
		case "pong":
			// A liveness probe's answer. Route it to whoever is waiting and DON'T let it touch `idle`
			// or emit anything: a probe must observe the turn, never perturb it.
			s.deliverPong(m.ID, m.Busy)
		case "session":
			// The sidecar echoes our id, then reports claude's REAL session UUID on init. Record
			// the UUID so a later --resume uses it (claude rejects our cc_… id as "not a UUID").
			if s.p != nil {
				s.p.setResume(s.id, m.ID)
			}
		case "text":
			if m.Text != "" {
				idle = false
				s.emit(agent.Event{Type: protocol.TypeOutputDelta, Payload: protocol.OutputDelta{SessionID: s.id, Text: m.Text}})
			}
		case "thinking":
			if m.Text != "" {
				idle = false
				s.emit(agent.Event{Type: protocol.TypeThinking, Payload: protocol.Thinking{SessionID: s.id, Text: m.Text}})
			}
		case "tool":
			s.emit(agent.Event{Type: protocol.TypeSessionStatus, Payload: protocol.SessionStatus{SessionID: s.id, Status: protocol.StatusRunning, Detail: "running " + m.Tool}})
		case "toolcall":
			// Rich inline tool card: running carries the command (Detail), the later result carries
			// Output. Same event the app renders for opencode, so claude-code gets card parity.
			name, title := m.Tool, m.Detail
			if m.Status != "" && m.Status != "running" {
				s.emit(agent.Event{Type: protocol.TypeSessionStatus, Payload: protocol.SessionStatus{SessionID: s.id, Status: protocol.StatusRunning, Detail: ""}})
				// This is the frame the hub PERSISTS, and the sidecar sends it without tool/detail —
				// so put the identity back before it becomes the only durable record of the card.
				// Anything the frame did carry wins; the cache only fills genuine blanks.
				c := s.takeToolCard(m.ID)
				if name == "" {
					name = c.name
				}
				if title == "" {
					title = c.title
				}
			} else {
				s.rememberToolCard(m.ID, name, title)
			}
			adds, dels := agent.DiffStatFrom(m.Output, string(m.Input))
			s.emit(agent.Event{Type: protocol.TypeSessionTool, Payload: protocol.SessionTool{
				SessionID: s.id, ID: m.ID, Name: name, Title: title, Output: m.Output, Status: m.Status,
				Additions: adds, Deletions: dels,
			}})
		case "approval":
			s.emit(agent.Event{Type: protocol.TypeSessionStatus, Payload: protocol.SessionStatus{SessionID: s.id, Status: protocol.StatusAwaitingApproval}})
			s.emit(agent.Event{Type: protocol.TypeApprovalRequest, Payload: protocol.ApprovalRequest{ApprovalID: m.ID, SessionID: s.id, Tool: m.Tool, Detail: m.Detail, Input: m.Input}})
		case "idle":
			idle = true
			s.emit(agent.Event{Type: protocol.TypeSessionStatus, Payload: protocol.SessionStatus{SessionID: s.id, Status: protocol.StatusIdle}})
		case "error":
			s.emit(agent.Event{Type: protocol.TypeSessionStatus, Payload: protocol.SessionStatus{SessionID: s.id, Status: protocol.StatusError, Detail: m.Message}})
		case "usage":
			s.emit(agent.Event{Type: protocol.TypeSessionUsage, Payload: protocol.SessionUsage{
				SessionID:        s.id,
				InputTokens:      m.InputTokens,
				OutputTokens:     m.OutputTokens,
				CacheReadTokens:  m.CacheReadTokens,
				CacheWriteTokens: m.CacheWriteTokens,
				CostUSD:          m.CostUSD,
				CostReported:     m.CostReported,
			}})
		case "todos":
			todos := make([]protocol.Todo, len(m.Todos))
			for i, td := range m.Todos {
				todos[i] = protocol.Todo{Content: td.Content, Status: td.Status}
			}
			s.emit(agent.Event{Type: protocol.TypeSessionTodos, Payload: protocol.SessionTodos{SessionID: s.id, Todos: todos}})
		case "facts":
			s.emit(agent.Event{Type: protocol.TypeSessionFacts, Payload: s.mergeFacts(m)})
		case "subagent":
			// parent_tool_use_id IS the Task tool call's id, so the card we already remembered for
			// that tool carries the human description ("Review the auth flow") the model gave it.
			// Peek rather than take: the tool card still needs its own terminal frame.
			title := s.peekToolCard(m.ID).title
			s.emit(agent.Event{Type: protocol.TypeSessionSubAgent, Payload: protocol.SubAgent{
				ParentID: s.id, ID: m.ID, Title: title, Status: "started"}})
		case "compacted":
			// The context figure the client is showing is now wrong. Zero the used side rather than
			// leaving a stale number that only corrects itself on the next turn's usage report.
			s.factsMu.Lock()
			s.facts.ContextUsed = 0
			snapshot := s.facts
			s.factsMu.Unlock()
			s.emit(agent.Event{Type: protocol.TypeSessionFacts, Payload: snapshot})
		}
	}
	if !idle {
		s.emit(agent.Event{Type: protocol.TypeSessionStatus, Payload: protocol.SessionStatus{SessionID: s.id, Status: protocol.StatusIdle}})
	}
}

func randID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
