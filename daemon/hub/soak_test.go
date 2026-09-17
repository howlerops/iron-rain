package hub_test

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/agent/opencode"
	"github.com/howlerops/oculus/daemon/crypto"
	"github.com/howlerops/oculus/daemon/hub"
	"github.com/howlerops/oculus/daemon/protocol"
	"github.com/howlerops/oculus/daemon/transport"
)

// Turn Engine stage 5, part D: the soak, with the network cut underneath a REAL provider adapter and
// a REAL encrypted client.
//
// The chaos suite (turnengine_e2e_test.go) scripts the provider directly, which is what makes it
// fast and deterministic — and also means it never touches HTTP, SSE framing, or the adapter's own
// reconnect path. Those are where the field failures live. This test puts a severable TCP proxy
// between the opencode adapter and its backend, so the stream dies the way it dies on a wifi
// handover: mid-frame, socket gone, server none the wiser.
//
// WHAT THIS IS NOT, because the plan asked for something larger. Part D as written calls for a real
// `opencode serve` on a big prompt, ten times a night in CI. That spends model credits on a
// schedule — not a thing to switch on unilaterally — and a stock GitHub runner has neither the
// binary nor a key. What is here is the half that costs nothing and catches the same class of bug:
// the chaos is in the socket, and the model is the only part a live opencode would add. Pointing it
// at a real server is a one-line change of backend address.

// severableProxy forwards TCP to a backend and can drop every open connection on demand.
//
// Cutting at the TCP layer rather than closing an HTTP response is the whole point: an SSE reader
// then sees a truncated frame and a dead socket, which is what a real network failure looks like and
// what the adapter's reconnect logic is written against. Closing the response body politely is a
// different event that the adapter handles a different way.
type severableProxy struct {
	ln      net.Listener
	backend string

	mu     sync.Mutex
	conns  []net.Conn
	opens  int
	refuse bool // once set, every new connection is dropped immediately: the backend is GONE
}

func newSeverableProxy(t *testing.T, backend string) *severableProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	p := &severableProxy{ln: ln, backend: backend}
	go p.serve()
	t.Cleanup(func() { _ = ln.Close(); p.Sever() })
	return p
}

func (p *severableProxy) URL() string { return "http://" + p.ln.Addr().String() }

func (p *severableProxy) serve() {
	for {
		down, err := p.ln.Accept()
		if err != nil {
			return
		}
		p.mu.Lock()
		refusing := p.refuse
		p.mu.Unlock()
		if refusing {
			_ = down.Close()
			continue
		}
		up, err := net.Dial("tcp", p.backend)
		if err != nil {
			_ = down.Close()
			continue
		}
		p.mu.Lock()
		p.conns = append(p.conns, down, up)
		p.opens++
		p.mu.Unlock()
		go func() { _, _ = io.Copy(up, down); _ = up.Close() }()
		go func() { _, _ = io.Copy(down, up); _ = down.Close() }()
	}
}

// Sever drops every connection currently open through the proxy and reports how many it closed.
func (p *severableProxy) Sever() int {
	p.mu.Lock()
	conns := p.conns
	p.conns = nil
	p.mu.Unlock()
	for _, c := range conns {
		_ = c.Close()
	}
	return len(conns)
}

// Refuse makes the proxy drop every future connection — the backend is not coming back. Used to
// prove the daemon can tell a severed stream (recoverable) from an agent that is actually gone.
func (p *severableProxy) Refuse() {
	p.mu.Lock()
	p.refuse = true
	p.mu.Unlock()
	p.Sever()
}

// Opens reports how many connections have been made through the proxy since it started — the
// evidence that a reconnect actually happened rather than the adapter quietly giving up.
func (p *severableProxy) Opens() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.opens
}

// soakStub is an opencode backend that models a LONG turn: the message POST blocks for the whole
// turn (which is what opencode really does), and the event stream emits a delta and then stays open.
//
// The package's other stub returns from POST /message immediately and scripts a turn that completes
// on its own. That is right for the full-stack test it was written for and useless here: the turn is
// over before a network cut can land on it, so severing the socket proved nothing. A soak needs a
// turn that is still in flight when the wire goes away.
type soakStub struct {
	mu        sync.Mutex
	connected bool
	release   chan struct{} // closed to let a blocked POST /message return
}

func newSoakStub() *soakStub { return &soakStub{release: make(chan struct{})} }

func (s *soakStub) Connected() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.connected
}

func (s *soakStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/session":
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "ses_soak", "title": "soak"})

	case r.Method == http.MethodGet && r.URL.Path == "/event":
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)
		s.mu.Lock()
		s.connected = true
		s.mu.Unlock()
		// One delta so the turn has visibly started, then hold the stream open the way a real
		// long-running turn does: quiet, but alive.
		_, _ = w.Write([]byte(`data: {"type":"message.part.delta","properties":{"sessionID":"ses_soak","field":"text","delta":"working"}}` + "\n\n"))
		if fl != nil {
			fl.Flush()
		}
		<-r.Context().Done()

	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/message"):
		// Blocks for the whole turn, as opencode's does. This is what makes the turn still be in
		// flight when the test cuts the wire.
		select {
		case <-s.release:
		case <-r.Context().Done():
		}
		w.WriteHeader(http.StatusOK)

	case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/message"):
		// The probe's history read: an incomplete assistant message means "still working".
		_, _ = w.Write([]byte(`[{"info":{"id":"m1","role":"assistant","sessionID":"ses_soak"},"parts":[{"type":"text","text":"working"}]}]`))

	default:
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("{}"))
	}
}

// soakClient is a real encrypted client attached to a real hub, over a real opencode adapter whose
// backend sits behind a severable proxy.
type soakClient struct {
	conn   *transport.Conn
	proxy  *severableProxy
	stub   *soakStub
	frames chan protocol.Envelope
}

func newSoakClient(t *testing.T) *soakClient {
	t.Helper()
	oc := newSoakStub()
	backendLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	hsrv := &http.Server{Handler: oc}
	go func() { _ = hsrv.Serve(backendLn) }()
	t.Cleanup(func() { _ = hsrv.Close() })

	proxy := newSeverableProxy(t, backendLn.Addr().String())

	h := hub.New()
	h.Register(opencode.New(proxy.URL()))

	clientKP, _ := crypto.GenerateKeyPair()
	daemonKP, _ := crypto.GenerateKeyPair()
	cPipe, sPipe := newPipePair()
	go func() {
		conn, err := transport.ServerHandshake(sPipe, daemonKP, func([]byte, string) bool { return true })
		if err != nil {
			return
		}
		_ = h.Serve(context.Background(), conn)
	}()
	client, err := transport.ClientHandshake(cPipe, clientKP, daemonKP.Public(), "secret")
	if err != nil {
		t.Fatalf("client handshake: %v", err)
	}

	sc := &soakClient{conn: client, proxy: proxy, stub: oc, frames: make(chan protocol.Envelope, 4096)}
	go func() {
		for {
			raw, err := client.Recv()
			if err != nil {
				close(sc.frames)
				return
			}
			if env, err := protocol.Decode(raw); err == nil {
				select {
				case sc.frames <- env:
				default: // a full buffer means the test stopped reading; dropping is correct here
				}
			}
		}
	}()
	return sc
}

func (sc *soakClient) send(t *testing.T, id, typ string, payload any) {
	t.Helper()
	raw, err := protocol.Encode(id, typ, payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := sc.conn.Send(raw); err != nil {
		t.Fatalf("send %s: %v", typ, err)
	}
}

// turnStateOf decodes a turn.state payload, or reports that the frame was something else.
func turnStateOf(env protocol.Envelope) (protocol.TurnState, bool) {
	if env.Type != protocol.TypeTurnState {
		return protocol.TurnState{}, false
	}
	var ts protocol.TurnState
	if json.Unmarshal(env.Payload, &ts) != nil {
		return protocol.TurnState{}, false
	}
	return ts, true
}

// openTurn creates a session, prompts it, and waits until the daemon reports a turn as RUNNING.
//
// Waiting for the turn is not ceremony. Both soaks below cut the network "mid-turn", and a cut that
// lands before a turn exists severs nothing that matters — the test would then pass by never having
// put anything at risk. The session id is returned so the caller can keep prompting it.
func (sc *soakClient) openTurn(t *testing.T) string {
	t.Helper()
	sc.send(t, "c1", protocol.TypeSessionCreate, protocol.SessionCreate{Provider: "opencode", Prompt: "go"})
	waitFor(t, 3*time.Second, func() bool { return sc.proxy.Opens() > 0 })

	deadline := time.After(5 * time.Second)
	for {
		select {
		case env, ok := <-sc.frames:
			if !ok {
				t.Fatal("the connection closed before a turn ever opened")
			}
			if ts, isTurn := turnStateOf(env); isTurn && ts.State == protocol.StatusRunning {
				return ts.SessionID
			}
		case <-deadline:
			t.Fatal("no turn ever reached running: the cut below would sever a session with nothing " +
				"in flight, and every assertion after it would be vacuous")
		}
	}
}

// TestSoakSeveredStreamNeverProducesAFalseTimeout cuts the SSE socket mid-turn, round after round,
// and requires that no turn ever ends by declaring a reachable agent unreachable.
//
// This is the expensive failure: "No response from the agent" because the phone changed networks. It
// was the most common complaint before the Turn Engine and the reason the client's timers were
// deleted rather than tuned. The backend here is answering the whole time — only the proxy is cut —
// so an "unreachable" verdict is provably wrong rather than merely suspicious.
func TestSoakSeveredStreamNeverProducesAFalseTimeout(t *testing.T) {
	if testing.Short() {
		t.Skip("soak: severs live TCP connections and runs for several seconds")
	}
	// Compress the verdict windows, or this test cannot fail.
	//
	// The production unreachable window is two minutes and each round waits four seconds, so the
	// daemon could not have declared an agent unreachable inside the observation window however
	// broken it was — the headline assertion ("a severed stream never produces a false timeout")
	// was asserting something physically unable to occur. Compressed, a wrong verdict has time to
	// appear, which is what makes the absence of one evidence.
	hub.CompressTurnWindowsForTest(t, 800*time.Millisecond, 2*time.Second, 200*time.Millisecond,
		100*time.Millisecond)

	const rounds = 6
	var severed, reconnected int

	for round := 0; round < rounds; round++ {
		sc := newSoakClient(t)
		sc.openTurn(t)
		before := sc.proxy.Opens()
		if n := sc.proxy.Sever(); n == 0 {
			t.Fatalf("round %d: nothing to sever — no network path was exercised", round)
		}
		severed++

		// Whatever happens next, the turn must not be reported as an unreachable agent.
		deadline := time.After(4 * time.Second)
		for done := false; !done; {
			select {
			case env, ok := <-sc.frames:
				if !ok {
					done = true
					break
				}
				ts, isTurn := turnStateOf(env)
				if !isTurn {
					continue
				}
				if ts.State == protocol.StatusAbandoned && (strings.Contains(ts.Reason, "unreachable") || strings.Contains(ts.Reason, "refused")) {
					t.Fatalf("round %d: the turn was abandoned as %q while the backend was serving "+
						"normally. Only the proxy was cut — this is the false timeout the engine "+
						"exists to prevent, and the user is told their agent died.", round, ts.Reason)
				}
				if isTerminalTurn(ts.State) {
					done = true
				}
			case <-deadline:
				done = true // a turn still open after a cut is correct; it is not evidence of failure
			}
		}
		if sc.proxy.Opens() > before {
			reconnected++
		}
	}

	if severed != rounds {
		t.Fatalf("only %d of %d rounds actually severed a connection", severed, rounds)
	}
	// Reconnects are the mechanism that makes the absence of false timeouts meaningful. Zero of them
	// would mean the turns survived because nothing was watching, not because anything recovered.
	if reconnected == 0 {
		t.Fatal("not one round reconnected through the proxy after its stream was cut. The turns " +
			"did not survive the outage — nothing tried to reach the agent again, so 'no false " +
			"timeout' here says only that nothing looked.")
	}
	t.Logf("soak: %d rounds severed, %d reconnected, 0 false timeouts", severed, reconnected)
}

func isTerminalTurn(state string) bool {
	switch state {
	case protocol.StatusIdle, protocol.StatusError, protocol.StatusNeedsYou, protocol.StatusAbandoned:
		return true
	}
	return false
}

// waitFor polls cond until it holds or the budget runs out.
func waitFor(t *testing.T, within time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("condition never held within %v", within)
}

// A note on what is NOT here: a soak for "a backend that is really gone is still reported gone".
//
// It was written, and removed, because it could not be staged honestly in this harness — the POST
// broke at t=0 before the turn was properly under way, so the test was measuring its own setup. The
// property itself is covered, with a negative control, by TestSimUnreachableProviderIsAbandonedWithAReason
// in turnengine_e2e_test.go, which drives a provider whose probe is REFUSED and requires abandoned
// specifically. Duplicating it here with a flakier rig would have added noise, not coverage.
//
// It matters that something covers it: without it, "no false timeout" could be satisfied by a daemon
// that never reports unreachability at all, which is not patience but a spinner outliving its agent.
