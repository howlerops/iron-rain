// Package agentsim is a scriptable provider session for exercising the Turn Engine.
//
// Every stub that came before it modelled ONE incident — dropStub lost an SSE stream, wedgeStub hung
// a POST, halfOpenStub went half-open — each hand-written next to the test that needed it, in the
// provider package it came from. That is fine for reproducing a bug once and useless for the thing
// stage 5 is for: running the SAME failure through the real hub, repeatedly, in combinations nobody
// hit yet. A scenario here is data — a list of steps — so a new failure mode is a new slice, not a
// new stub.
//
// What it deliberately is NOT: a fake opencode. It speaks agent.Event and the optional capability
// interfaces, which is the whole surface the hub sees. Anything below that line (HTTP framing, SSE
// parsing, JSONL) belongs in the provider packages' own tests, where those stubs still live.
package agentsim

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/howlerops/oculus/daemon/agent"
	"github.com/howlerops/oculus/daemon/protocol"
)

// Step is one scripted action. Exactly one field is meaningful per step; Kind selects which.
type Step struct {
	Kind StepKind

	Text string        // Emit: message text. SpawnChild/EndChild: the child's title.
	ID   string        // SpawnChild/EndChild/Tool: the child or tool id.
	For  time.Duration // Delay: how long. Also the grace before DropStream takes effect.
	// As overrides the session id an event is emitted under. Sub-agent events travel through the
	// PARENT's event stream carrying the child's own id, so this is how a scenario models a child
	// speaking — including a child announcing that IT is idle, which must not end the parent's turn.
	As    string
	State string // Tool: running|completed|error. EndChild: done|error.
}

// StepKind enumerates what a scenario can do. Each one corresponds to something a real provider has
// actually done to this daemon.
type StepKind int

const (
	// Emit sends an assistant message — ordinary forward progress.
	Emit StepKind = iota
	// Running announces the provider's own StatusRunning, the way a provider opens a turn it was
	// not asked to open (a resumed turn, an auto-continue).
	Running
	// Idle announces StatusIdle: the provider says the turn is done. This is the event that gets
	// LOST in most of the interesting scenarios.
	Idle
	// Delay pauses the script. Longer than the hub's quietAfter, it provokes the reconciler.
	Delay
	// DropStream closes Events() without ever sending Idle: the stream died mid-turn. The turn is
	// still running as far as the provider is concerned — only the transport is gone.
	DropStream
	// Exit ends the session the way a subprocess provider dies: stream closed AND not busy.
	Exit
	// SpawnChild / EndChild model fanout. The child's events carry its own session id.
	SpawnChild
	EndChild
	// Tool announces a tool boundary, which is what the stall detector counts as progress.
	Tool
)

// Scenario is a named script plus how the provider answers a probe while it runs.
type Scenario struct {
	Name string
	// Steps run in order on their own goroutine once Events() is first read.
	Steps []Step
	// Busy answers Probe. nil means "busy until the script finishes, then not busy", which is what
	// an honest provider does and what most scenarios want. Return an error to model an agent that
	// cannot be reached at all.
	Busy func(done bool) (bool, error)
	// RecoverEmits is replayed when the hub calls Recover — the lost tail of a dropped turn. A
	// provider that cannot re-fetch its output (pi, cli) leaves this nil.
	RecoverEmits []Step
}

// Session is a scripted agent.Session. It implements Prober, Recoverer and Nudger; a scenario
// chooses which of those actually do anything.
type Session struct {
	id, provider string
	sc           Scenario

	ch   chan agent.Event
	once sync.Once

	done     atomic.Bool  // the script ran to the end
	closed   atomic.Bool  // Close() was called
	probes   atomic.Int32 //
	recovers atomic.Int32
	stops    atomic.Int32
	prompts  atomic.Int32

	nudgeMu sync.Mutex
	nudges  []string

	// emitMu guards the CLOSE of ch against concurrent sends.
	//
	// There are two senders — the script goroutine and Recover — and closing a channel out from under
	// either one is a data race, not merely a panic to recover from. So senders take RLock and
	// re-check chClosed; the closer takes the write lock. A sender may park inside the select while
	// holding RLock, which is why quit MUST be closed before the closer asks for the write lock:
	// closing quit is what releases a parked sender. Same ordering, and the same reason, as the
	// opencode adapter's own emitMu.
	emitMu   sync.RWMutex
	chClosed bool

	// quit is closed exactly once, through quitOnce.
	//
	// Both Stop and Close end the session, the hub calls them from more than one goroutine, and
	// closing a closed channel is an unrecoverable panic — which in a test binary takes every other
	// test down with it, reported as a crash somewhere unrelated. A `select`/`default` guard does not
	// help: two goroutines can both find it open and both proceed to close.
	quitOnce  sync.Once
	closeOnce sync.Once
	quit      chan struct{}
}

// New builds a session that will run sc when its events are first read.
func New(id string, sc Scenario) *Session {
	return &Session{
		id: id, provider: "agentsim", sc: sc,
		ch:   make(chan agent.Event, 256),
		quit: make(chan struct{}),
	}
}

func (s *Session) ID() string       { return s.id }
func (s *Session) Provider() string { return s.provider }

// Events starts the script on first call. Starting lazily rather than in New is deliberate: it means
// a scenario's clock begins when the hub is actually listening, so a Delay measures what the test
// thinks it measures rather than racing harness setup.
func (s *Session) Events() <-chan agent.Event {
	s.once.Do(func() { go s.run() })
	return s.ch
}

func (s *Session) run() {
	defer s.done.Store(true)
	for _, st := range s.sc.Steps {
		select {
		case <-s.quit:
			return
		default:
		}
		if !s.step(st) {
			return // the script closed the stream; nothing after it can be delivered
		}
	}
}

// step performs one step. It returns false once the stream is gone.
func (s *Session) step(st Step) bool {
	id := s.id
	if st.As != "" {
		id = st.As
	}
	switch st.Kind {
	case Delay:
		select {
		case <-time.After(st.For):
		case <-s.quit:
			return false
		}
	case Emit:
		s.send(agent.Event{Type: protocol.TypeSessionMessage, Payload: protocol.SessionMessage{
			SessionID: id, Role: "assistant", Text: st.Text}})
	case Running:
		s.send(agent.Event{Type: protocol.TypeSessionStatus, Payload: protocol.SessionStatus{
			SessionID: id, Status: protocol.StatusRunning, Detail: st.Text}})
	case Idle:
		s.send(agent.Event{Type: protocol.TypeSessionStatus, Payload: protocol.SessionStatus{
			SessionID: id, Status: protocol.StatusIdle, Detail: st.Text}})
	case Tool:
		status := st.State
		if status == "" {
			status = "completed"
		}
		s.send(agent.Event{Type: protocol.TypeSessionTool, Payload: protocol.SessionTool{
			SessionID: id, ID: st.ID, Name: st.Text, Status: status}})
	case SpawnChild:
		s.send(agent.Event{Type: protocol.TypeSessionSubAgent, Payload: protocol.SubAgent{
			ParentID: s.id, ID: st.ID, Title: st.Text, Status: "started"}})
	case EndChild:
		state := st.State
		if state == "" {
			state = "done"
		}
		s.send(agent.Event{Type: protocol.TypeSessionSubAgent, Payload: protocol.SubAgent{
			ParentID: s.id, ID: st.ID, Title: st.Text, Status: state}})
	case DropStream, Exit:
		if st.For > 0 {
			select {
			case <-time.After(st.For):
			case <-s.quit:
			}
		}
		s.closeStream()
		return false
	}
	return true
}

func (s *Session) send(ev agent.Event) {
	s.emitMu.RLock()
	defer s.emitMu.RUnlock()
	if s.chClosed {
		return
	}
	select {
	case s.ch <- ev:
	case <-s.quit:
	}
}

func (s *Session) closeStream() {
	s.closeOnce.Do(func() {
		s.closed.Store(true)
		s.stop() // release any sender parked in send's select BEFORE asking for the write lock
		s.emitMu.Lock()
		s.chClosed = true
		close(s.ch)
		s.emitMu.Unlock()
	})
}

// stop closes quit, at most once.
func (s *Session) stop() { s.quitOnce.Do(func() { close(s.quit) }) }

// Probe answers the reconciler. The default — busy while the script runs, not busy once it has
// finished — is the behaviour of a provider that tells the truth, which is exactly what makes a lost
// Idle recoverable. Scenarios that model a LYING or unreachable provider override it.
func (s *Session) Probe(ctx context.Context) (bool, error) {
	s.probes.Add(1)
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if s.sc.Busy != nil {
		return s.sc.Busy(s.done.Load())
	}
	return !s.done.Load(), nil
}

// Recover re-emits the scenario's RecoverEmits — the output the hub missed when the stream died.
func (s *Session) Recover(ctx context.Context) {
	s.recovers.Add(1)
	if len(s.sc.RecoverEmits) == 0 || s.closed.Load() {
		return
	}
	for _, st := range s.sc.RecoverEmits {
		if ctx.Err() != nil {
			return
		}
		if !s.step(st) {
			return
		}
	}
}

func (s *Session) Prompt(_ context.Context, _ string) error {
	s.prompts.Add(1)
	if s.closed.Load() {
		return errors.New("session closed")
	}
	return nil
}

func (s *Session) Nudge(_ context.Context, text string) error {
	s.nudgeMu.Lock()
	defer s.nudgeMu.Unlock()
	s.nudges = append(s.nudges, text)
	return nil
}

func (s *Session) Respond(context.Context, string, string) error { return nil }

func (s *Session) Stop(context.Context) error {
	s.stops.Add(1)
	s.stop()
	s.closeStream()
	return nil
}

func (s *Session) Close() error {
	s.stop()
	s.closeStream()
	return nil
}

// Probes/Recovers/Stops/Prompts report what the hub did to this session.
func (s *Session) Probes() int   { return int(s.probes.Load()) }
func (s *Session) Recovers() int { return int(s.recovers.Load()) }
func (s *Session) Stops() int    { return int(s.stops.Load()) }
func (s *Session) Prompts() int  { return int(s.prompts.Load()) }

// Nudges returns the nudge texts received so far.
func (s *Session) Nudges() []string {
	s.nudgeMu.Lock()
	defer s.nudgeMu.Unlock()
	return append([]string(nil), s.nudges...)
}

// Done reports whether the script ran to completion.
func (s *Session) Done() bool { return s.done.Load() }

// ---- scenario builders -------------------------------------------------------------------------
//
// Each one is a real failure this daemon has shipped with. The comments say which, because a
// scenario whose provenance is lost gets "simplified" by the next person to read it.

// HappyTurn: work, a tool boundary, more work, then a clean Idle. The control for everything else —
// if this does not close cleanly the rest of the suite proves nothing.
func HappyTurn() Scenario {
	return Scenario{Name: "happy", Steps: []Step{
		{Kind: Running, Text: "thinking"},
		{Kind: Emit, Text: "looking at the file"},
		{Kind: Tool, ID: "t1", Text: "read", State: "completed"},
		{Kind: Emit, Text: "here is the answer"},
		{Kind: Idle},
	}}
}

// LostIdle: the provider finishes but its completion event never arrives (opencode's dropped SSE
// frame). The stream stays OPEN, so nothing about the transport says anything is wrong — the only
// evidence is that the provider is no longer busy. This is the scenario the whole reconciler exists
// for, and the one the client's old watchdog got wrong in both directions.
func LostIdle() Scenario {
	return Scenario{Name: "lost-idle", Steps: []Step{
		{Kind: Running},
		{Kind: Emit, Text: "partial output"},
		// ...and then nothing. The script ends, so Probe reports not-busy: provider truth.
	}, RecoverEmits: []Step{
		{Kind: Emit, Text: "the tail the hub never saw"},
		{Kind: Idle},
	}}
}

// DroppedStream: the SSE socket dies mid-turn while the agent keeps working. Transport failure, NOT
// completion — the distinction the daemon used to get wrong by closing the turn on stream end.
func DroppedStream(busyAfter bool) Scenario {
	sc := Scenario{Name: "dropped-stream", Steps: []Step{
		{Kind: Running},
		{Kind: Emit, Text: "started"},
		{Kind: DropStream},
	}}
	if busyAfter {
		sc.Name = "dropped-stream-still-working"
		sc.Busy = func(bool) (bool, error) { return true, nil }
	}
	return sc
}

// WedgedBusy: the provider swears it is busy forever and nothing progresses — the hung-tool
// signature. opencode reads an incomplete assistant message for a wedged tool exactly as it does for
// one that is thinking, so "busy" here is truthful and useless. Must end as needs_you, never error.
func WedgedBusy() Scenario {
	return Scenario{Name: "wedged-busy", Steps: []Step{
		{Kind: Running, Text: "running tests"},
		{Kind: Delay, For: time.Hour},
	}, Busy: func(bool) (bool, error) { return true, nil }}
}

// Unreachable: the probe is REFUSED. Absence, not slowness — the daemon died, the port is gone.
func Unreachable() Scenario {
	return Scenario{Name: "unreachable", Steps: []Step{
		{Kind: Running},
		{Kind: Delay, For: time.Hour},
	}, Busy: func(bool) (bool, error) {
		return false, errors.New("connect: connection refused")
	}}
}

// Fanout spawns n children and ends them all, then goes idle. The parent must not close while a
// child is still open.
func Fanout(n int) Scenario {
	sc := Scenario{Name: fmt.Sprintf("fanout-%d", n), Steps: []Step{{Kind: Running, Text: "delegating"}}}
	for i := 0; i < n; i++ {
		sc.Steps = append(sc.Steps, Step{Kind: SpawnChild, ID: fmt.Sprintf("kid_%d", i),
			Text: fmt.Sprintf("subtask %d", i)})
	}
	for i := 0; i < n; i++ {
		sc.Steps = append(sc.Steps, Step{Kind: EndChild, ID: fmt.Sprintf("kid_%d", i), State: "done"})
	}
	sc.Steps = append(sc.Steps, Step{Kind: Idle})
	return sc
}

// FanoutChildIdleLost spawns n children and never ends one of them: the child's completion event was
// lost. The parent must still close on provider truth rather than waiting on the orphan forever.
func FanoutChildIdleLost(n int) Scenario {
	sc := Fanout(n)
	sc.Name = fmt.Sprintf("fanout-%d-child-idle-lost", n)
	for i, st := range sc.Steps {
		if st.Kind == EndChild && st.ID == "kid_0" {
			sc.Steps = append(sc.Steps[:i], sc.Steps[i+1:]...)
			break
		}
	}
	return sc
}

// Burst emits n deltas back to back. Used to assert seq stays gap-free under buffer pressure.
func Burst(n int) Scenario {
	sc := Scenario{Name: fmt.Sprintf("burst-%d", n), Steps: []Step{{Kind: Running}}}
	for i := 0; i < n; i++ {
		sc.Steps = append(sc.Steps, Step{Kind: Emit, Text: fmt.Sprintf("delta %d", i)})
	}
	sc.Steps = append(sc.Steps, Step{Kind: Idle})
	return sc
}

// ChildSpeaksIdle: a sub-agent finishes and announces ITS OWN idle through the parent's stream,
// while the parent keeps working.
//
// This is the routing case that decides whether fanout works at all. A child's status event carries
// the child's session id and travels the parent's event stream, so a hub that folds every status it
// sees into its own turn ends the parent the moment the FIRST child finishes — with nine siblings
// still running and the parent's own answer still to come.
func ChildSpeaksIdle() Scenario {
	return Scenario{Name: "child-speaks-idle", Steps: []Step{
		{Kind: Running, Text: "delegating"},
		{Kind: SpawnChild, ID: "kid_0", Text: "subtask"},
		{Kind: Emit, Text: "child output", As: "kid_0"},
		{Kind: Idle, As: "kid_0"}, // the CHILD is done; the parent is not
		{Kind: Delay, For: time.Hour},
	}, Busy: func(bool) (bool, error) { return true, nil }}
}
