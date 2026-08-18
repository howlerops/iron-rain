// Package opencode implements agent.Provider against a running `opencode serve`
// HTTP/SSE API (endpoints POST /session, POST /session/{id}/message, GET /event,
// POST /session/{id}/permissions/{permissionID}, POST /session/{id}/abort).
package opencode

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/howlerops/oculus/daemon/agent"
	"github.com/howlerops/oculus/daemon/protocol"
)

// unaryTimeout bounds the request/response (non-SSE) calls so a hung opencode server
// can't block a goroutine (e.g. a POST /message fired with the long-lived subscribe
// ctx) indefinitely.
const unaryTimeout = 30 * time.Second

// historyReplayTimeout bounds the one request whose cost scales with the conversation: fetching a
// session's whole message list on attach. It is deliberately far larger than unaryTimeout — a long
// agentic session's history is megabytes, and timing it out produces an EMPTY transcript, which is
// a much worse outcome than waiting.
const historyReplayTimeout = 3 * time.Minute

// sseIdleTimeout bounds how long the SSE /event stream may go SILENT before we treat the
// connection as DEAD and force a reconnect. This is the fix for the "opencode gets stuck on long
// tasks and never catches up" bug: a half-open TCP socket (laptop sleep/wake, Wi-Fi roam, NAT idle
// drop, an opencode hang — none of which send a FIN/RST) leaves the blocking Read() with no data and
// no error, so the scanner blocks FOREVER and the transparent reconnect below never fires. An idle
// read-deadline turns that silent hang into a timeout error, which unwinds the scan loop and triggers
// a reconnect (the opencode session lives server-side, so reconnecting is cheap + non-destructive).
// Generous enough that a legitimately quiet stream (a long, output-less tool call) reconnects
// harmlessly rather than being mistaken for a dead socket.
const sseIdleTimeout = 120 * time.Second

// Provider talks to one opencode server.
type Provider struct {
	baseURL string
	sse     *http.Client // no request Timeout, but an idle READ-deadline per conn: the /event stream only
	http    *http.Client // no Timeout: for the turn-long blocking POST /message (bounded by a 3h ctx)
	unary   *http.Client // bounded Timeout: for request/response List/postJSON/replayHistory
}

// New returns a Provider for the given opencode base URL (e.g. http://127.0.0.1:4096).
func New(baseURL string) *Provider { return newProvider(baseURL, sseIdleTimeout) }

// BaseURL reports the exact opencode server this provider talks to. The hub persists it per session
// so a daemon restart/self-update re-attaches to the server that owns that conversation, not whichever
// opencode happens to be registered next.
func (p *Provider) BaseURL() string { return p.baseURL }

// newProvider is New with an injectable SSE idle timeout (tests use a short one to exercise the
// half-open reconnect without waiting the full production window).
func newProvider(baseURL string, sseIdle time.Duration) *Provider {
	return &Provider{
		baseURL: strings.TrimRight(baseURL, "/"),
		sse:     &http.Client{Transport: newSSETransport(sseIdle)},
		http:    &http.Client{},
		unary:   &http.Client{Timeout: unaryTimeout},
	}
}

// newSSETransport builds a transport whose connections carry an idle READ-deadline (see
// sseIdleTimeout). It's used ONLY for the /event SSE stream — never for the blocking POST /message,
// which legitimately sends no bytes for the whole (possibly multi-hour) turn and must not be killed.
func newSSETransport(idle time.Duration) *http.Transport {
	d := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	return &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			c, err := d.DialContext(ctx, network, addr)
			if err != nil {
				return nil, err
			}
			return &idleTimeoutConn{Conn: c, idle: idle}, nil
		},
	}
}

// idleTimeoutConn resets a read deadline on every Read, so a stream that stops delivering bytes for
// longer than `idle` makes the next Read return a timeout error (instead of blocking forever on a
// half-open socket). Writes and everything else are untouched.
type idleTimeoutConn struct {
	net.Conn
	idle time.Duration
}

func (c *idleTimeoutConn) Read(b []byte) (int, error) {
	_ = c.Conn.SetReadDeadline(time.Now().Add(c.idle))
	return c.Conn.Read(b)
}

func (p *Provider) Name() string { return "opencode" }

// List returns current sessions.
func (p *Provider) List(ctx context.Context) ([]protocol.Session, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/session", nil)
	if err != nil {
		return nil, err
	}
	resp, err := p.unary.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var raw []struct {
		ID       string `json:"id"`
		Title    string `json:"title"`
		ParentID string `json:"parentID"`
		// Each session carries its OWN directory, and GET /session reports every session the
		// server knows regardless of ?directory= — so one server routinely lists sessions from
		// several unrelated folders/worktrees. Dropping this field made every listed session look
		// like it lived wherever `opencode serve` was launched: discovery showed pathless rows and
		// attaching sent the wrong ?directory= (which opencode partitions on), i.e. the silent-send
		// failure attach() fights above. Older opencode builds omit it — absent must decode to ""
		// (never an error) so callers fall back to the server's cwd instead of losing the session.
		Directory string `json:"directory"`
		Time      struct {
			Updated int64 `json:"updated"` // opencode reports millis
		} `json:"time"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}
	out := make([]protocol.Session, 0, len(raw))
	for _, s := range raw {
		if s.ParentID != "" {
			continue
		}
		out = append(out, protocol.Session{
			ID: s.ID, Provider: "opencode", Status: protocol.StatusIdle, Title: s.Title,
			Cwd:       s.Directory,
			UpdatedAt: s.Time.Updated / 1000, // millis -> seconds
		})
	}
	return out, nil
}

// Models lists every model opencode has configured, across its providers (GET /config/providers),
// so the app can offer a picker. The model id must be paired with its providerID when sent.
func (p *Provider) Models(ctx context.Context) ([]protocol.ModelInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/config/providers", nil)
	if err != nil {
		return nil, err
	}
	resp, err := p.unary.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var raw struct {
		Providers []struct {
			ID     string `json:"id"`
			Name   string `json:"name"`
			Models map[string]struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"models"`
		} `json:"providers"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}
	var out []protocol.ModelInfo
	for _, pr := range raw.Providers {
		for _, m := range pr.Models {
			name := m.Name
			if name == "" {
				name = m.ID
			}
			out = append(out, protocol.ModelInfo{ID: m.ID, Name: pr.Name + " · " + name, Provider: pr.ID})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Create starts a session and (if prompt != "") kicks it off.
func (p *Provider) Create(ctx context.Context, cwd, prompt string) (agent.Session, error) {
	return p.create(ctx, cwd, prompt, "")
}

// CreatePlan starts a session that runs turns as opencode's "plan" agent — edits and bash are
// gated on approval, so the agent proposes/plans and nothing changes until you allow it.
func (p *Provider) CreatePlan(ctx context.Context, cwd, prompt string) (agent.Session, error) {
	return p.create(ctx, cwd, prompt, "plan")
}

func (p *Provider) create(ctx context.Context, cwd, prompt, agentName string) (agent.Session, error) {
	var created struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	if err := p.postJSON(ctx, withDir("/session", cwd), map[string]any{}, &created); err != nil {
		return nil, err
	}
	if created.ID == "" {
		return nil, fmt.Errorf("opencode: create returned empty session id")
	}

	s := &session{p: p, id: created.ID, dir: cwd, agent: agentName, events: make(chan agent.Event, 32), done: make(chan struct{})}
	if err := s.subscribe(); err != nil {
		return nil, err
	}
	if prompt != "" {
		if err := s.Prompt(ctx, prompt); err != nil {
			_ = s.Close()
			return nil, err
		}
	}
	return s, nil
}

// Attach connects to an existing session (discovered on the host): it subscribes to
// live events and replays the session's message history so the app shows the
// conversation and can continue it.
func (p *Provider) Attach(ctx context.Context, sessionID, cwd string) (agent.Session, error) {
	return p.attach(ctx, sessionID, cwd, false)
}

// AttachVerified is Attach for callers that cannot afford a blind attach: it fails unless this
// server actually HOLDS the session (proved by resolving its directory).
//
// The distinction exists because opencode's /event stream accepts any subscriber — it is a
// server-wide bus, not a session handle. So attaching to a server that has never heard of the
// session still "succeeds": the app gets a live-looking row with no history, and every send goes to
// a session id the server can't find and is silently dropped. That is exactly the state a daemon
// restart produces when the persisted URL is missing/stale or the terminal's opencode was restarted.
// The restore path (hub.RestoreSessions) uses this so such a record stays stopped + RESTARTABLE —
// visibly recoverable — instead of pretending to be attached.
func (p *Provider) AttachVerified(ctx context.Context, sessionID, cwd string) (agent.Session, error) {
	return p.attach(ctx, sessionID, cwd, true)
}

func (p *Provider) attach(ctx context.Context, sessionID, cwd string, requireDir bool) (agent.Session, error) {
	// cwd scopes the /event subscription + message writes to the session's directory
	// (opencode partitions both by ?directory=). A WRONG/stale cwd is worse than none: the
	// session opens (history may replay) but every message write goes to a directory where
	// opencode can't find the session, so sends silently fail — the "session is broken, no
	// message works" bug. opencode's own GET /session/:id reports the session's real directory
	// regardless of the directory param, so re-derive it and trust that over the stored cwd.
	dir := cwd
	if real := p.resolveDir(ctx, sessionID); real != "" {
		if real != cwd {
			log.Printf("opencode: attach %s — resolved real directory %q (stored cwd was %q)", sessionID, real, cwd)
		}
		dir = real
	} else if requireDir {
		return nil, fmt.Errorf("opencode at %s doesn't hold session %s (no directory) — not attaching blind", p.baseURL, sessionID)
	} else if cwd != "" {
		// Couldn't verify the directory (opencode unreachable/slow, or the session is unknown to it).
		// We fall back to the stored cwd — but if that cwd is stale, sends will silently fail, so make
		// the un-healed attach VISIBLE instead of re-arming the "broken session" bug quietly.
		log.Printf("opencode: attach %s — could NOT resolve real directory (opencode unreachable?); using stored cwd %q — sends may fail if it's stale, try Recover", sessionID, cwd)
	}
	s := &session{p: p, id: sessionID, dir: dir, events: make(chan agent.Event, 64), done: make(chan struct{})}
	if err := s.subscribe(); err != nil {
		return nil, err
	}
	s.replayHistory(ctx)
	return s, nil
}

// Dir reports the session's opencode directory (authoritative after Attach resolves it), so the hub
// can heal a stale persisted cwd. Implements agent.DirReporter.
func (s *session) Dir() string { return s.dir }

// resolveDir asks opencode for a session's real working directory via GET /session/:id, which returns
// the directory field no matter which (or no) ?directory= is passed. Empty string on any failure so
// callers fall back to the cwd they already have.
func (p *Provider) resolveDir(ctx context.Context, sessionID string) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/session/"+sessionID, nil)
	if err != nil {
		return ""
	}
	resp, err := p.unary.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return ""
	}
	var info struct {
		Directory string `json:"directory"`
	}
	if json.NewDecoder(resp.Body).Decode(&info) != nil {
		return ""
	}
	return info.Directory
}

// replayHistory fetches the session's messages and emits them as SessionMessage
// events (oldest first) so the client can render the existing conversation.
func (s *session) replayHistory(ctx context.Context) {
	// This request grows with the conversation — it returns EVERY message with full tool output —
	// so it gets its own generous budget instead of the 30s unary client. A long session's history
	// legitimately takes a while to serialise, and the old timeout turned that into an empty
	// transcript: the failure below returned silently, emitting nothing and logging nothing, so
	// "I restarted and lost all my history" arrived with no trace anywhere of what went wrong.
	//
	// The hub's durable transcript is the fallback and does not need us to do anything — but it can
	// only cover what it has already recorded, so a failure here is still worth shouting about.
	hctx, cancel := context.WithTimeout(ctx, historyReplayTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(hctx, http.MethodGet, s.p.baseURL+withDir("/session/"+s.id+"/message", s.dir), nil)
	if err != nil {
		log.Printf("opencode: sid=%s history replay could not be built: %v — the durable transcript is now the only history source", s.id, err)
		return
	}
	resp, err := s.p.http.Do(req) // no client-level Timeout; hctx bounds it
	if err != nil {
		log.Printf("opencode: sid=%s HISTORY REPLAY FAILED after %s: %v — falling back to the daemon's durable transcript; "+
			"anything older than that will not appear", s.id, historyReplayTimeout, err)
		return
	}
	defer resp.Body.Close()
	var msgs []struct {
		Info struct {
			ID         string `json:"id"`
			Role       string `json:"role"`
			ModelID    string `json:"modelID"`
			ProviderID string `json:"providerID"`
		} `json:"info"`
		Parts []struct {
			Type string `json:"type"`
			Text string `json:"text"`
			Tool string `json:"tool"`
		} `json:"parts"`
	}
	if json.NewDecoder(resp.Body).Decode(&msgs) != nil {
		return
	}
	// TAKEOVER MUST NOT CHANGE THE MODEL. opencode takes the model per MESSAGE, so an attached
	// session starts with none — and sendParts then omits it, running the user's next turn on
	// whatever the server defaults to. Mid-task, silently. Adopt the model of the last assistant
	// message instead: that is provably what this conversation was being run with.
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Info.Role == "assistant" && msgs[i].Info.ModelID != "" {
			s.adoptModel(msgs[i].Info.ProviderID, msgs[i].Info.ModelID)
			break
		}
	}
	for _, m := range msgs {
		var text string
		var tool string
		for _, part := range m.Parts {
			switch part.Type {
			case "text":
				text += part.Text
			case "tool":
				if tool == "" {
					tool = part.Tool
				}
			}
		}
		// MsgID carries opencode's stable message id so the durable transcript dedups this message
		// when opencode re-replays its history on a later re-attach.
		if text != "" {
			s.emit(agent.Event{Type: protocol.TypeSessionMessage, Payload: protocol.SessionMessage{SessionID: s.id, Role: m.Info.Role, Text: text, MsgID: m.Info.ID}})
		} else if tool != "" {
			s.emit(agent.Event{Type: protocol.TypeSessionMessage, Payload: protocol.SessionMessage{SessionID: s.id, Role: "tool", Text: tool, MsgID: m.Info.ID}})
		}
	}
}

// Probe reports whether opencode is still actively working this session's turn — authoritative
// provider truth for the hub's reconciler (used when the SSE stream goes quiet). Decided from the
// session's message tail: a final assistant message with time.completed set means the turn is done;
// a trailing user message (or an incomplete assistant one) means opencode is still working.
func (s *session) Probe(ctx context.Context) (bool, error) {
	// Reachability FIRST, and cheaply.
	//
	// This used to go straight to /message, which returns the session's ENTIRE history — hundreds of
	// messages with full tool output on a long agentic session. That is megabytes to serialise, so
	// the probe's cost grows with the conversation until it exceeds its own timeout, and the daemon
	// reports "agent unreachable: context deadline exceeded" about a server that is answering
	// perfectly well. The agent got declared dead for the crime of having been used a lot.
	//
	// /session/{id} is a fixed-size object, so this half stays fast no matter how long the
	// conversation runs, and it is the ONLY half allowed to conclude "unreachable".
	rctx, rcancel := context.WithTimeout(ctx, 8*time.Second)
	defer rcancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodGet, s.p.baseURL+withDir("/session/"+s.id, s.dir), nil)
	if err != nil {
		return false, err
	}
	resp, err := s.p.unary.Do(req)
	if err != nil {
		return false, err // the server is genuinely not answering — the caller's outage clock starts
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		// The server is up but has never heard of this session: a real, permanent condition (the
		// session was deleted, or we are pointed at the wrong directory partition). Say so plainly
		// rather than letting it read as a transient outage that might heal.
		return false, fmt.Errorf("opencode: session %s not found (wrong directory partition, or deleted)", s.id)
	}

	// The server is reachable. From here on, ANY failure means "we could not determine the turn's
	// state", which is not the same as "the agent is gone" — so report busy and let the caller keep
	// waiting. Being patient about a live-but-slow agent is always cheaper than killing a live one.
	req, err = http.NewRequestWithContext(ctx, http.MethodGet, s.p.baseURL+withDir("/session/"+s.id+"/message", s.dir), nil)
	if err != nil {
		return true, nil
	}
	resp, err = s.p.unary.Do(req)
	if err != nil {
		return true, nil // slow history read; the server answered a moment ago, so it is alive
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return true, nil // reachable but couldn't read the tail — assume still working, never abandon
	}
	var msgs []struct {
		Info struct {
			Role string `json:"role"`
			Time struct {
				Completed int64 `json:"completed"`
			} `json:"time"`
		} `json:"info"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&msgs); err != nil {
		return true, nil // a truncated/slow body is not evidence the turn finished
	}
	if len(msgs) == 0 {
		return false, nil // nothing ever sent — idle
	}
	last := msgs[len(msgs)-1]
	if last.Info.Role == "assistant" && last.Info.Time.Completed != 0 {
		return false, nil // the turn's reply is complete — idle
	}
	return true, nil // a user turn is pending or the assistant reply is still being produced
}

// SelfReplaying implements agent.Replayer: opencode re-streams history on every attach, so the hub
// must not double it with the durable transcript.
func (s *session) SelfReplaying() bool { return true }

// Recover implements agent.Recoverer: re-fetch + re-emit the last assistant message (the turn's
// result) when its streamed completion was lost.
func (s *session) Recover(ctx context.Context) { s.resyncLast(ctx) }

// Revive implements agent.Reviver: prove the session is usable again, without disturbing it.
//
// opencode's conversation lives SERVER-SIDE, which makes recovery unusually safe here — there is no
// local process to restart and no history to rebuild. If the server is answering for this session
// id, the session is genuinely fine and the outage was in transit (a slept laptop, a wifi handover,
// a server briefly too busy to answer). The SSE stream repairs itself on its own read-deadline, so
// this deliberately does NOT tear anything down: reconnecting a stream that was about to recover is
// how you turn a blip into lost events.
//
// It returns an error when the session cannot be served — which the caller treats as "not repaired"
// and eventually reports, rather than retrying a session that no longer exists.
func (s *session) Revive(ctx context.Context) error {
	rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodGet, s.p.baseURL+withDir("/session/"+s.id, s.dir), nil)
	if err != nil {
		return err
	}
	resp, err := s.p.unary.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("opencode: session %s not served (%s)", s.id, resp.Status)
	}
	return nil
}

// resyncLast fetches the session's LAST assistant message and emits its full text, so a turn whose
// streaming deltas were missed (the SSE stream dropped/stalled while opencode kept working) still
// shows its result. The app replaces the partial streamed message with this authoritative text.
func (s *session) resyncLast(ctx context.Context) {
	// Same scaling problem as replayHistory (this reads the whole message list to take its tail) and
	// the same reason to be loud: this is the RECOVERY path for a turn whose result was lost in
	// transit, so failing it silently loses the output twice and says nothing either time.
	rctx, cancel := context.WithTimeout(ctx, historyReplayTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(rctx, http.MethodGet, s.p.baseURL+withDir("/session/"+s.id+"/message", s.dir), nil)
	if err != nil {
		return
	}
	resp, err := s.p.http.Do(req) // no client-level Timeout; rctx bounds it
	if err != nil {
		log.Printf("opencode: sid=%s could not re-fetch the turn's final output: %v — if its streaming was also missed, the result is not on screen", s.id, err)
		return
	}
	defer resp.Body.Close()
	var msgs []struct {
		Info struct {
			ID   string `json:"id"`
			Role string `json:"role"`
		} `json:"info"`
		Parts []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"parts"`
	}
	if json.NewDecoder(resp.Body).Decode(&msgs) != nil {
		return
	}
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Info.Role != "assistant" {
			continue
		}
		var text string
		for _, part := range msgs[i].Parts {
			if part.Type == "text" {
				text += part.Text
			}
		}
		if text != "" {
			// Same opencode message id as replayHistory emits → the durable transcript stores this
			// turn once and dedups it when history is replayed on a later re-attach.
			s.emit(agent.Event{Type: protocol.TypeSessionMessage, Payload: protocol.SessionMessage{SessionID: s.id, Role: "assistant", Text: text, MsgID: msgs[i].Info.ID}})
		}
		return
	}
}

func (p *Provider) postJSON(ctx context.Context, path string, body, out any) error {
	return p.doPost(ctx, path, body, out, p.unary)
}

// postJSONLong is postJSON on the UN-TIMED client, for requests opencode intentionally blocks on for
// the entire agent turn (the message POST returns only when the turn yields — minutes for a big
// plan/multi-agent run). The 30s unary bound would spuriously fail those with "context deadline
// exceeded"; the session's own ctx still cancels this when the session closes.
func (p *Provider) postJSONLong(ctx context.Context, path string, body, out any) error {
	return p.doPost(ctx, path, body, out, p.http)
}

func (p *Provider) doPost(ctx context.Context, path string, body, out any, client *http.Client) error {
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+path, &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("opencode POST %s: %s", path, resp.Status)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

type session struct {
	p      *Provider
	id     string
	dir    string // working directory; forwarded to opencode as ?directory= (scopes the session)
	agent  string // opencode agent to run turns as ("plan" = gate edits/bash on approval); "" = default. Guarded by modelMu.
	events chan agent.Event

	ctx       context.Context
	cancel    context.CancelFunc
	closeOnce sync.Once
	done      chan struct{}

	// Guards the CLOSE of `events` against concurrent sends.
	//
	// `readLoop` used to `defer close(s.events)` on the theory that it was the only sender. It is
	// not: `emit` also sends, and `sendParts` calls it from the goroutine that POSTs a turn, so a
	// stream ending while a turn is still emitting had one goroutine closing the channel and another
	// sending on it. Go's race detector caught it in CI; the production symptom is worse than a
	// flaky test, because a send on a closed channel is an unrecoverable `panic`, not an error.
	//
	// A `select` with `<-s.done` does NOT prevent this. Sending on a closed channel is immediately
	// "ready", so select can choose that case and panic — closing a channel makes sends fault, it
	// does not make them block.
	//
	// Readers take RLock and re-check `eventsClosed`; the closer takes the write lock. Senders can
	// hold RLock while parked in the select, so the closer MUST first close `done` (see the deferred
	// close in readLoop) or it would wait on a sender that is itself waiting forever.
	emitMu       sync.RWMutex
	eventsClosed bool

	modelMu       sync.Mutex // guards the selected model (set from the hub, read in sendParts)
	modelID       string
	modelProvider string

	statusMu   sync.Mutex // guards lastStatus (written by the SSE loop, read by the POST goroutine)
	lastStatus string     // last session.status emitted — lets the POST-return idle backstop skip when awaiting approval

	// approvalSession maps an opencode permission id -> the session that raised it. A `task` sub-agent
	// raises permissions under its OWN (child) session id, but the hub records + routes the answer
	// through the PARENT adapter — so Respond must POST the answer to the CHILD's session path, not the
	// parent's, or the sub-agent blocks forever server-side (the whole fanout then never completes).
	approvalMu      sync.Mutex
	approvalSession map[string]string

	// True while a turn's POST /message is in flight. Set by sendParts, read by readLoop so that a
	// mid-turn SSE reconnect resyncs the latest assistant text (recovering anything produced during
	// the silent gap) instead of only resuming live from the reconnect point.
	turnActive atomic.Bool

	// True from when a turn is SENT until it reaches idle — NOT cleared when the POST returns (unlike
	// turnActive). So if opencode wedges a turn server-side (e.g. an agent bash step like `git merge`
	// hangs on $EDITOR), this stays true, and the next prompt aborts the stuck turn first instead of
	// queuing behind it forever (opencode processes a session serially). The exact "I sent
	// continue?/status? and got nothing" pile-up.
	turnPending atomic.Bool

	// populated in the (single) readEvents goroutine — no mutex needed.
	msgRoles    map[string]string // messageID -> role (from message.updated)
	emittedUser map[string]bool   // messageIDs already forwarded as a user turn
	usageDone   map[string]bool   // messageIDs whose usage was already emitted (once per turn)
	childIDs    map[string]bool   // opencode sub-session ids whose parentID == s.id (sub-agents)
	subStarted  map[string]bool   // sub-agent ids already announced (dedup the started card)
}

func (s *session) ID() string                 { return s.id }
func (s *session) Provider() string           { return "opencode" }
func (s *session) Events() <-chan agent.Event { return s.events }

func (s *session) subscribe() error {
	ctx, cancel := context.WithCancel(context.Background())
	s.ctx = ctx
	s.cancel = cancel
	body, err := s.openEvents(ctx)
	if err != nil {
		cancel()
		return err
	}
	go s.readLoop(body)
	return nil
}

// openEvents opens the SSE /event stream, scoped to this session's directory. opencode
// partitions /event by ?directory= (exactly like POST /session and POST /message): a
// session in a project folder or worktree emits its events ONLY on /event?directory=<dir>,
// so a bare /event (the server's default directory) would silently miss them.
func (s *session) openEvents(ctx context.Context) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.p.baseURL+withDir("/event", s.dir), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")
	resp, err := s.p.sse.Do(req) // idle-read-deadline client → a half-open stream reconnects, not hangs
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("opencode /event: %s", resp.Status)
	}
	return resp.Body, nil
}

// readLoop drains the event stream, and — because an opencode session lives on the server
// independent of any one SSE connection — transparently reconnects if the stream drops
// (opencode restart, network blip, idle timeout) instead of ending the session. It only
// stops (and closes s.events, ending the session) when the session is Closed/Stopped.
func (s *session) readLoop(body io.ReadCloser) {
	// Closing `events` ends the session in the hub's run(). It must happen exactly once, and never
	// while another goroutine is mid-send — see the note on emitMu.
	//
	// `s.Close()` first, and unconditionally: it is idempotent via closeOnce, and closing `done` is
	// what releases any sender parked in emit's select. Without it the failed-reconnect exit below
	// (which returns WITHOUT anyone having called Close) could leave a sender blocked on a full
	// channel while this goroutine waits for the write lock — a deadlock instead of a panic, which
	// is quieter but no better.
	defer func() {
		_ = s.Close()
		s.emitMu.Lock()
		s.eventsClosed = true
		close(s.events)
		s.emitMu.Unlock()
	}()
	for {
		s.scanEvents(body)
		body.Close()
		if s.ctx.Err() != nil {
			return // Close()/Stop() was called — the session is really done
		}
		// The stream dropped but the session still lives server-side: reconnect with a
		// capped backoff, then resume. (Any turn in flight keeps running server-side.)
		nb, ok := s.reconnectEvents()
		if !ok {
			return
		}
		log.Printf("opencode: /event reconnected sid=%s", s.id)
		// opencode's /event is live pub/sub with NO replay, so events produced during the drop are gone.
		// If a turn is in flight, pull the latest assistant text so anything the agent finished writing
		// while we were disconnected shows up — the app REPLACES the still-streaming message with it, so
		// this recovers a stalled turn instead of leaving it frozen mid-sentence. Cheap + idempotent.
		if s.turnActive.Load() {
			go s.resyncLast(s.ctx)
		}
		body = nb
	}
}

// reconnectEvents retries openEvents with exponential backoff until it succeeds or the
// session is closed. Returns (stream, true) on success, (nil, false) if the session ended.
func (s *session) reconnectEvents() (io.ReadCloser, bool) {
	backoff := 500 * time.Millisecond
	start := time.Now()
	warned := false
	for {
		if s.ctx.Err() != nil {
			return nil, false
		}
		if body, err := s.openEvents(s.ctx); err == nil {
			if warned { // recovered — clear the error state back to running
				s.emit(agent.Event{Type: protocol.TypeSessionStatus, Payload: protocol.SessionStatus{
					SessionID: s.id, Status: protocol.StatusRunning, Detail: "opencode reconnected"}})
			}
			return body, true
		}
		// Don't retry forever in silence: if the backend stays unreachable, surface the session as
		// errored once (the app/session.list then shows "error" instead of a phantom "running"),
		// while still retrying so it self-heals if opencode comes back.
		if !warned && time.Since(start) > 20*time.Second {
			warned = true
			s.emit(agent.Event{Type: protocol.TypeSessionStatus, Payload: protocol.SessionStatus{
				SessionID: s.id, Status: protocol.StatusError, Detail: "opencode backend unreachable"}})
		}
		select {
		case <-s.ctx.Done():
			return nil, false
		case <-time.After(backoff):
		}
		if backoff < 15*time.Second {
			backoff *= 2
		}
	}
}

func (s *session) scanEvents(body io.ReadCloser) {
	sc := bufio.NewScanner(body)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	dataPrefix := []byte("data:")
	for sc.Scan() {
		// Work in bytes to avoid a string alloc + a []byte copy per streamed event.
		// sc.Bytes() is only valid until the next Scan(), which is fine because handle()
		// consumes it synchronously (json.Unmarshal does not retain the slice).
		line := sc.Bytes()
		if !bytes.HasPrefix(line, dataPrefix) {
			continue
		}
		payload := bytes.TrimSpace(bytes.TrimPrefix(line, dataPrefix))
		if len(payload) != 0 {
			s.handle(payload)
		}
	}
}

// handle translates one opencode SSE event into protocol events for this session.
func (s *session) handle(raw []byte) {
	var e struct {
		Type       string          `json:"type"`
		Properties json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(raw, &e); err != nil {
		return
	}
	switch e.Type {
	case "session.created", "session.updated":
		// A sub-session whose parentID is us is a sub-agent (opencode's `task` tool). Track it so its
		// events can be forwarded, and announce it once as an inline card in the parent transcript.
		var se struct {
			Info struct {
				ID       string `json:"id"`
				ParentID string `json:"parentID"`
				Title    string `json:"title"`
			} `json:"info"`
		}
		if json.Unmarshal(e.Properties, &se) != nil || se.Info.ParentID != s.id || se.Info.ID == "" {
			return
		}
		if s.childIDs == nil {
			s.childIDs = map[string]bool{}
			s.subStarted = map[string]bool{}
		}
		s.childIDs[se.Info.ID] = true
		if !s.subStarted[se.Info.ID] {
			s.subStarted[se.Info.ID] = true
			s.emit(agent.Event{Type: protocol.TypeSessionSubAgent, Payload: protocol.SubAgent{
				ParentID: s.id, ID: se.Info.ID, Title: se.Info.Title, Status: "started"}})
		}

	case "message.updated":
		// Record messageID -> role so we can tell user turns from assistant turns, and — for a
		// completed assistant message — emit token/cost usage (opencode carries it on info).
		var mu struct {
			Info struct {
				ID     string  `json:"id"`
				Role   string  `json:"role"`
				Cost   float64 `json:"cost"`
				Tokens struct {
					Input  int `json:"input"`
					Output int `json:"output"`
					Cache  struct {
						Read  int `json:"read"`
						Write int `json:"write"`
					} `json:"cache"`
				} `json:"tokens"`
				Time struct {
					Completed int64 `json:"completed"`
				} `json:"time"`
			} `json:"info"`
		}
		if json.Unmarshal(e.Properties, &mu) == nil && mu.Info.ID != "" {
			if s.msgRoles == nil {
				s.msgRoles = map[string]string{}
			}
			s.msgRoles[mu.Info.ID] = mu.Info.Role
			// One clean usage number per assistant turn, once the turn has completed. Guard
			// against re-emitting for the same message id (message.updated fires repeatedly).
			if mu.Info.Role == "assistant" && mu.Info.Time.Completed != 0 &&
				(mu.Info.Cost > 0 || mu.Info.Tokens.Input > 0 || mu.Info.Tokens.Output > 0) {
				if s.usageDone == nil {
					s.usageDone = map[string]bool{}
				}
				if !s.usageDone[mu.Info.ID] {
					s.usageDone[mu.Info.ID] = true
					in := mu.Info.Tokens.Input + mu.Info.Tokens.Cache.Read + mu.Info.Tokens.Cache.Write
					s.emit(agent.Event{Type: protocol.TypeSessionUsage, Payload: protocol.SessionUsage{
						SessionID: s.id, InputTokens: in, OutputTokens: mu.Info.Tokens.Output, CostUSD: mu.Info.Cost}})
				}
			}
		}

	case "message.part.updated":
		var pu struct {
			Part struct {
				ID        string          `json:"id"`
				Type      string          `json:"type"`
				Text      string          `json:"text"`
				Tool      string          `json:"tool"`
				Name      string          `json:"name"`
				ToolName  string          `json:"toolName"`
				Title     string          `json:"title"`
				Status    string          `json:"status"`
				Output    string          `json:"output"`
				Error     string          `json:"error"`
				Input     json.RawMessage `json:"input"`
				Args      json.RawMessage `json:"args"`
				Metadata  json.RawMessage `json:"metadata"`
				MessageID string          `json:"messageID"`
				SessionID string          `json:"sessionID"`
				State     struct {
					Status string `json:"status"`
					Title  string `json:"title"`
					Output string `json:"output"`
					Error  string `json:"error"`
				} `json:"state"`
			} `json:"part"`
		}
		if json.Unmarshal(e.Properties, &pu) != nil {
			return
		}
		isParent := pu.Part.SessionID == s.id
		if !isParent && !s.childIDs[pu.Part.SessionID] {
			return // not our session and not one of our sub-agents
		}
		target := pu.Part.SessionID // s.id for the parent turn; the child id for a sub-agent
		switch pu.Part.Type {
		case "tool":
			toolName := firstNonEmpty(pu.Part.Tool, pu.Part.Name, pu.Part.ToolName, "tool")
			st := firstNonEmpty(pu.Part.State.Status, pu.Part.Status)
			if st == "running" || st == "completed" || st == "error" {
				// Keep the top activity chip for the parent's current tool.
				if st != "error" {
					s.emit(agent.Event{Type: protocol.TypeSessionStatus, Payload: protocol.SessionStatus{
						SessionID: target, Status: protocol.StatusRunning, Detail: "running " + toolName,
					}})
				}
				// Rich inline tool card, updated IN PLACE by part id (running → completed+output). The
				// PARENT skips `task` — the sub-agent gets its own card (from session.created) instead.
				if pu.Part.ID != "" && !(isParent && toolName == "task") {
					title := firstNonEmpty(
						pu.Part.State.Title,
						pu.Part.Title,
						toolInputSummary(pu.Part.Input),
						toolInputSummary(pu.Part.Args),
						toolInputSummary(pu.Part.Metadata),
					)
					output := firstNonEmpty(pu.Part.State.Output, pu.Part.Output)
					if st == "error" {
						output = firstNonEmpty(pu.Part.State.Error, pu.Part.Error, output)
					}
					// The edit's diff, when opencode gave us one. It rides in the part metadata (which
					// the client never receives), so the counting has to happen here; free-text output
					// is the fallback. Unknown stays 0/0 and the client shows no badge.
					adds, dels := agent.DiffStatFrom(string(pu.Part.Metadata), output)
					s.emit(agent.Event{Type: protocol.TypeSessionTool, Payload: protocol.SessionTool{
						SessionID: target, ID: pu.Part.ID, Name: toolName,
						Title: title, Output: output, Status: st,
						Additions: adds, Deletions: dels,
					}})
				}
			}
		case "text":
			// Forward USER turns (parent only; assistant text streams via message.part.delta). Once per message.
			if !isParent || pu.Part.Text == "" || s.msgRoles[pu.Part.MessageID] != "user" {
				return
			}
			if s.emittedUser == nil {
				s.emittedUser = map[string]bool{}
			}
			if s.emittedUser[pu.Part.MessageID] {
				return
			}
			s.emittedUser[pu.Part.MessageID] = true
			s.emit(agent.Event{Type: protocol.TypeSessionMessage, Payload: protocol.SessionMessage{SessionID: s.id, Role: "user", Text: pu.Part.Text, MsgID: pu.Part.MessageID}})
		}

	case "message.part.delta":
		// opencode streams assistant output as {sessionID, field, delta}. field "text"
		// is the answer; field "reasoning" is the thinking ("it's working").
		var pr struct {
			SessionID string `json:"sessionID"`
			Field     string `json:"field"`
			Delta     string `json:"delta"`
		}
		if json.Unmarshal(e.Properties, &pr) != nil || pr.Delta == "" {
			return
		}
		// Parent text streams under s.id; a sub-agent's text streams under its own session id and is
		// forwarded tagged to that id so the app routes it into the inline sub-agent card.
		target := pr.SessionID
		if pr.SessionID != s.id {
			if !s.childIDs[pr.SessionID] {
				return
			}
		} else {
			target = s.id
		}
		switch pr.Field {
		case "text":
			s.emit(agent.Event{Type: protocol.TypeOutputDelta, Payload: protocol.OutputDelta{SessionID: target, Text: pr.Delta}})
		case "reasoning":
			if target == s.id { // thinking only surfaced for the parent turn
				s.emit(agent.Event{Type: protocol.TypeThinking, Payload: protocol.Thinking{SessionID: s.id, Text: pr.Delta}})
			}
		}

	case "todo.updated":
		// opencode's todowrite tool publishes a dedicated todo.updated bus event with the
		// full list — map it to the normalized session.todos.
		var tu struct {
			SessionID string `json:"sessionID"`
			Todos     []struct {
				Content string `json:"content"`
				Status  string `json:"status"`
			} `json:"todos"`
		}
		if json.Unmarshal(e.Properties, &tu) != nil || tu.SessionID != s.id {
			return
		}
		todos := make([]protocol.Todo, len(tu.Todos))
		for i, td := range tu.Todos {
			todos[i] = protocol.Todo{Content: td.Content, Status: td.Status}
		}
		s.emit(agent.Event{Type: protocol.TypeSessionTodos, Payload: protocol.SessionTodos{SessionID: s.id, Todos: todos}})

	case "permission.asked", "permission.updated":
		// opencode 1.17.x emits `permission.asked` with properties.permission (the tool);
		// older builds used `permission.updated` with properties.type/title. Handle both.
		// The reply endpoint (POST /session/{id}/permissions/{permID} once|reject) is
		// unchanged — verified live vs 1.17.19.
		var perm struct {
			ID         string          `json:"id"`
			SessionID  string          `json:"sessionID"`
			Permission string          `json:"permission"`
			Type       string          `json:"type"`
			Title      string          `json:"title"`
			Patterns   []string        `json:"patterns"`
			Metadata   json.RawMessage `json:"metadata"`
		}
		// Accept permissions for THIS session AND for our `task` sub-agents (their sessionID is a child
		// id). Dropping a sub-agent's permission (the old `!= s.id` reject) left the sub-agent blocked
		// server-side forever → the parent's task tool never returned → the fanout never completed and
		// the session was wedged with no restart able to clear it.
		if json.Unmarshal(e.Properties, &perm) != nil {
			return
		}
		isParentPerm := perm.SessionID == s.id
		if !isParentPerm && !s.childIDs[perm.SessionID] {
			return
		}
		// Remember which session this approval belongs to so Respond answers the RIGHT one (a sub-agent's
		// answer must POST to the child's session path, not the parent's).
		if perm.ID != "" {
			s.approvalMu.Lock()
			if s.approvalSession == nil {
				s.approvalSession = map[string]string{}
			}
			s.approvalSession[perm.ID] = perm.SessionID
			s.approvalMu.Unlock()
		}
		tool := perm.Permission
		if tool == "" {
			tool = perm.Type
		}
		if tool == "" {
			tool = perm.Title
		}
		// todowrite/todoread are bookkeeping, not code changes — don't surface them as
		// approvals (they'd pop a spurious card); the list arrives via todo.updated.
		if tool == "todowrite" || tool == "todoread" {
			if perm.ID != "" {
				go func() { _ = s.Respond(context.Background(), perm.ID, protocol.DecisionAllow) }()
			}
			return
		}
		// Detail: the concrete command/args to show inline (e.g. the bash command).
		detail := ""
		if len(perm.Patterns) > 0 {
			detail = perm.Patterns[0]
		}
		if detail == "" {
			var md struct {
				Command string `json:"command"`
			}
			if json.Unmarshal(perm.Metadata, &md) == nil {
				detail = md.Command
			}
		}
		// A sub-agent approval is prefixed so the user knows which lane is asking (it's shown in the
		// parent transcript; the client's approval UI keys on the parent session id).
		if !isParentPerm {
			if detail != "" {
				detail = "[sub-agent] " + detail
			} else {
				detail = "[sub-agent]"
			}
		}
		s.emit(agent.Event{Type: protocol.TypeSessionStatus, Payload: protocol.SessionStatus{SessionID: s.id, Status: protocol.StatusAwaitingApproval}})
		// Forward opencode's OWN patterns: they're the authoritative globs for what this permission
		// covers, and they make far better "always allow …" suggestions than re-parsing the command.
		s.emit(agent.Event{Type: protocol.TypeApprovalRequest, Payload: protocol.ApprovalRequest{ApprovalID: perm.ID, SessionID: s.id, Tool: tool, Detail: detail, Input: perm.Metadata, Patterns: perm.Patterns}})

	case "session.idle":
		var pr struct {
			SessionID string `json:"sessionID"`
		}
		if json.Unmarshal(e.Properties, &pr) != nil {
			return
		}
		// A sub-agent going idle → close its inline card (its own turn finished).
		if pr.SessionID != s.id {
			if s.childIDs[pr.SessionID] {
				s.emit(agent.Event{Type: protocol.TypeSessionSubAgent, Payload: protocol.SubAgent{
					ParentID: s.id, ID: pr.SessionID, Status: "done"}})
			}
			return
		}
		// The PARENT turn is done → every `task` sub-agent it spawned is necessarily finished too. Seal
		// them (idempotent) BEFORE emitting idle, so a sub-agent whose own session.idle was missed can't
		// leave the app's "sub-agents still active" state wedged (which suppresses the no-response
		// watchdog and keeps the lane spinning forever).
		for childID := range s.childIDs {
			s.emit(agent.Event{Type: protocol.TypeSessionSubAgent, Payload: protocol.SubAgent{
				ParentID: s.id, ID: childID, Status: "done"}})
		}
		// The turn is done: no message is streaming, so the per-message role/dedup bookkeeping is no
		// longer needed. Dropping it (plus the sub-agent tracking + any dangling approval routes) bounds
		// these maps to one turn and prevents a NEXT turn from mis-routing on stale sub-agent ids.
		s.msgRoles = nil
		s.emittedUser = nil
		s.usageDone = nil
		s.childIDs = nil
		s.subStarted = nil
		s.turnActive.Store(false)  // the turn is authoritatively done (covers the post-approval continuation)
		s.turnPending.Store(false) // reached idle → not wedged; a later prompt won't force an abort
		s.approvalMu.Lock()
		s.approvalSession = nil
		s.approvalMu.Unlock()
		s.emit(agent.Event{Type: protocol.TypeSessionStatus, Payload: protocol.SessionStatus{SessionID: s.id, Status: protocol.StatusIdle}})
	}
}

func (s *session) emit(ev agent.Event) {
	// Track the last status so the POST-return idle backstop knows whether the turn actually
	// completed vs. parked on an approval.
	if ev.Type == protocol.TypeSessionStatus {
		if ss, ok := ev.Payload.(protocol.SessionStatus); ok && ss.SessionID == s.id {
			// Only the PARENT's own status gates the POST-return idle backstop. A sub-agent's status
			// (running/awaiting) must NOT poison lastStatus, or the parent's completion check misfires.
			s.statusMu.Lock()
			s.lastStatus = ss.Status
			s.statusMu.Unlock()
		}
	}
	// RLock lets concurrent emitters through while still excluding the close in readLoop. The
	// re-check inside the lock is the part that matters: without it, this goroutine could pass the
	// check, lose the CPU, and wake up to a channel that has since been closed.
	s.emitMu.RLock()
	defer s.emitMu.RUnlock()
	if s.eventsClosed {
		return // the session is over; dropping the event is correct, panicking is not
	}
	select {
	case s.events <- ev:
	case <-s.done:
	}
}

// Prompt sends a message. opencode's POST /message blocks server-side until the
// turn yields (e.g. it parks on a tool-permission ask), so we fire it async and let
// progress arrive over SSE — otherwise the caller would deadlock, unable to answer
// the very approval the turn is waiting on. Errors surface as an error status event.
// v0 sends a single text part, sufficient for a default-configured server.
func (s *session) Prompt(_ context.Context, text string) error {
	return s.sendParts([]map[string]any{{"type": "text", "text": text}}, false)
}

// Nudge implements agent.Nudger. It is a plain queued send: opencode runs a session serially, so a
// message posted mid-turn is delivered when the current turn yields — never by interrupting it.
//
// The honest limitation: a turn that is truly wedged never yields, so a nudge to one may never be
// consumed. That is why the turn engine treats a nudge as best-effort and escalates to needs_you
// when nothing moves, instead of reaching for the abort itself. Killing a user's agent on a
// heuristic is the failure mode this whole path exists to avoid.
func (s *session) Nudge(_ context.Context, text string) error {
	return s.sendParts([]map[string]any{{"type": "text", "text": text}}, false)
}

// PromptUnsticking is Prompt for a turn the hub's turn engine has DECLARED stalled: it aborts the
// wedged turn first so this message actually runs. Implements agent.Unsticker.
func (s *session) PromptUnsticking(_ context.Context, text string) error {
	return s.sendParts([]map[string]any{{"type": "text", "text": text}}, true)
}

// PromptImages sends a multimodal turn: a text part + opencode "file" parts carrying each
// image as a base64 data URL (opencode decodes data: URLs directly).
func (s *session) PromptImages(_ context.Context, text string, images []protocol.ImageAttachment) error {
	parts := []map[string]any{}
	if text != "" {
		parts = append(parts, map[string]any{"type": "text", "text": text})
	}
	for i, im := range images {
		parts = append(parts, map[string]any{
			"type":     "file",
			"mime":     im.Mime,
			"filename": fmt.Sprintf("image-%d%s", i+1, extForMime(im.Mime)),
			"url":      "data:" + im.Mime + ";base64," + im.Data,
		})
	}
	return s.sendParts(parts, false)
}

// sendParts fires a message with the given parts asynchronously (opencode's POST blocks
// until the turn yields, so we drive progress from SSE — see the note above).
// SetModel selects the model for subsequent turns (opencode takes it per message).
func (s *session) SetModel(provider, model string) error {
	s.modelMu.Lock()
	s.modelProvider, s.modelID = provider, model
	s.modelMu.Unlock()
	return nil
}

// adoptModel takes the model a session was ALREADY running with (from its own history), without
// overriding a model the user picked explicitly — SetModel wins, because it's a stated intent while
// this is an inference.
func (s *session) adoptModel(provider, model string) {
	if model == "" {
		return
	}
	s.modelMu.Lock()
	defer s.modelMu.Unlock()
	if s.modelID != "" {
		return
	}
	s.modelProvider, s.modelID = provider, model
	log.Printf("opencode: attach %s — continuing with the session's own model %s/%s", s.id, provider, model)
}

// Model reports the model this session runs turns with ("" when opencode's default is in force), so
// the hub can persist it and the app can show "continuing with model X" after a takeover.
func (s *session) Model() (provider, model string) {
	s.modelMu.Lock()
	defer s.modelMu.Unlock()
	return s.modelProvider, s.modelID
}

// sendParts posts a message. abortStuck asks it to kill an unfinished prior turn first.
//
// That abort used to be unconditional on `turnPending`, which is set for ANY turn that hasn't
// reached idle — including one that is perfectly healthy and three hours into a migration. So a
// user typing a follow-up while their agent worked silently destroyed the work, and it looked for
// all the world like the message itself had crashed the agent. turnPending cannot tell wedged from
// busy; only the hub's turn engine can (it has probes and a tool-progress clock), so the decision
// now lives THERE and arrives here as an explicit argument. See hub.unstickIfStalled.
func (s *session) sendParts(parts []map[string]any, abortStuck bool) error {
	body := map[string]any{"parts": parts}
	s.modelMu.Lock()
	if s.agent != "" {
		body["agent"] = s.agent // e.g. "plan" — gate edits/bash on approval
	}
	if s.modelID != "" {
		m := map[string]any{"modelID": s.modelID}
		if s.modelProvider != "" {
			m["providerID"] = s.modelProvider
		}
		body["model"] = m
	}
	s.modelMu.Unlock()
	ctx := s.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	// opencode runs a session serially, so a message sent while a turn is genuinely wedged would queue
	// behind the hang and never run — the "I sent continue?/status? and got nothing back" pile-up.
	// Aborting first is the cure for that, but ONLY when the caller has evidence of a wedge; without
	// it we queue, which is the right thing to do to an agent that is actually working.
	priorUnfinished := s.turnPending.Swap(true)
	go func() {
		if priorUnfinished && abortStuck {
			actx, acancel := context.WithTimeout(ctx, 15*time.Second)
			_ = s.p.postJSON(actx, withDir("/session/"+s.id+"/abort", s.dir), map[string]any{}, nil)
			acancel()
			log.Printf("opencode: sid=%s aborted the stalled prior turn before sending (caller declared it wedged)", s.id)
		}
		// opencode's message POST blocks server-side for the WHOLE turn. We bound it only to prevent a
		// leaked goroutine — NOT to bound the turn (huge migrations legitimately run for hours). The
		// real progress + completion channel is the SSE stream; this POST is fire-and-forget.
		pctx, cancel := context.WithTimeout(ctx, 3*time.Hour)
		defer cancel()
		s.turnActive.Store(true)
		defer s.turnActive.Store(false)
		start := time.Now()
		log.Printf("opencode: POST message sid=%s (turn start)", s.id)
		err := s.p.doPost(pctx, withDir("/session/"+s.id+"/message", s.dir), body, nil, s.p.http) // pctx bounds it
		if ctx.Err() != nil {
			return // the session was closed/stopped — nothing to report
		}
		switch {
		case err == nil:
			log.Printf("opencode: POST message sid=%s turn returned after %s", s.id, time.Since(start).Round(time.Second))
			// The POST returning cleanly is a RELIABLE end-of-turn signal. opencode's /event stream is
			// live pub/sub with NO replay, so a mid-turn reconnect (network blip, opencode idle timeout,
			// a long turn) can MISS the session.idle — leaving the app stuck "working" forever. Emit idle
			// here as a backstop, UNLESS the turn parked on an approval (POST can return on a yield), in
			// which case awaiting-approval must stand until it's answered.
			s.statusMu.Lock()
			parked := s.lastStatus == protocol.StatusAwaitingApproval
			s.statusMu.Unlock()
			if !parked {
				s.turnPending.Store(false) // turn completed cleanly → not wedged
				s.resyncLast(ctx)          // recover the turn's result if its streaming was missed, then seal it
				s.emit(agent.Event{Type: protocol.TypeSessionStatus, Payload: protocol.SessionStatus{SessionID: s.id, Status: protocol.StatusIdle}})
			}
		case pctx.Err() == context.DeadlineExceeded:
			// Our local leak-bound elapsed with the POST STILL blocking — the turn is wedged server-side
			// (a hung interactive command, etc.). Deliberately LEAVE turnPending set so the user's NEXT
			// prompt aborts this stuck turn instead of queuing behind it. We don't declare an error (a
			// legit long migration also keeps this POST open while streaming over SSE).
			log.Printf("opencode: POST message sid=%s stopped waiting after %s (turn continues on the server)", s.id, time.Since(start).Round(time.Second))
		default:
			// A real transport failure (opencode died / connection refused) — surface it.
			s.turnPending.Store(false)
			log.Printf("opencode: POST message sid=%s FAILED after %s: %v", s.id, time.Since(start).Round(time.Second), err)
			s.emit(agent.Event{Type: protocol.TypeSessionStatus, Payload: protocol.SessionStatus{SessionID: s.id, Status: protocol.StatusError, Detail: "opencode: " + err.Error()}})
		}
	}()
	return nil
}

func extForMime(mime string) string {
	switch mime {
	case "image/png":
		return ".png"
	case "image/jpeg", "image/jpg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	default:
		return ""
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func toolInputSummary(raw json.RawMessage) string {
	if len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return ""
	}
	var obj map[string]any
	if err := json.Unmarshal(raw, &obj); err != nil {
		return ""
	}
	for _, key := range []string{"command", "file_path", "path", "pattern", "url", "query", "prompt"} {
		if s, ok := obj[key].(string); ok && strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

// Respond maps allow->"once", always->"always", deny->"reject".
func (s *session) Respond(ctx context.Context, approvalID, decision string) error {
	resp := "reject"
	switch decision {
	case protocol.DecisionAllow:
		resp = "once"
	case protocol.DecisionAlways:
		resp = "always"
	}
	// Answer the session that actually raised this permission — a `task` sub-agent's approval must go
	// to the CHILD session path, or it stays blocked server-side and the whole turn hangs. Sub-agents
	// share the parent's directory, so s.dir is the correct ?directory= for both.
	sid := s.id
	s.approvalMu.Lock()
	if s.approvalSession != nil {
		if owner, ok := s.approvalSession[approvalID]; ok && owner != "" {
			sid = owner
		}
		delete(s.approvalSession, approvalID)
	}
	s.approvalMu.Unlock()
	// Answering an approval RESUMES the turn. If the parent's POST already returned at the yield (so
	// turnActive was cleared), re-arm it so a mid-turn SSE reconnect during the continuation still
	// resyncs the latest output. It's cleared again on the parent's session.idle.
	s.turnActive.Store(true)
	return s.p.postJSON(ctx, withDir(fmt.Sprintf("/session/%s/permissions/%s", sid, approvalID), s.dir), map[string]string{"response": resp}, nil)
}

func (s *session) Stop(ctx context.Context) error {
	return s.p.postJSON(ctx, withDir("/session/"+s.id+"/abort", s.dir), map[string]any{}, nil)
}

// Delete permanently removes the session from the opencode server (DELETE /session/:id), so a
// user-initiated delete truly deletes it — otherwise the session lingers server-side and reappears
// when the app re-attaches on reconnect or rediscovers it. Implements agent.Deleter.
func (s *session) Delete(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, s.p.baseURL+withDir("/session/"+s.id, s.dir), nil)
	if err != nil {
		return err
	}
	resp, err := s.p.unary.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// withDir appends opencode's ?directory= query param (which scopes a call to a project
// folder / worktree) when dir is non-empty; empty dir → the server's default directory.
func withDir(path, dir string) string {
	if dir == "" {
		return path
	}
	return path + "?directory=" + url.QueryEscape(dir)
}

func (s *session) Close() error {
	s.closeOnce.Do(func() {
		close(s.done)
		if s.cancel != nil {
			s.cancel()
		}
	})
	return nil
}

// SetMode implements agent.ModeSetter. opencode carries its agent/mode on EVERY message, so a switch
// takes effect on the next turn with no restart and no lost context — architect maps to opencode's
// own "plan" agent, code to its default. Ask has no opencode equivalent; the daemon enforces it.
func (s *session) SetMode(_ context.Context, mode string) error {
	s.modelMu.Lock() // same lock as the other per-message config read in sendParts
	defer s.modelMu.Unlock()
	switch mode {
	case protocol.ModeArchitect:
		s.agent = "plan"
	case protocol.ModeAsk:
		s.agent = "plan" // closest native behavior: propose rather than edit
	default:
		s.agent = ""
	}
	return nil
}
