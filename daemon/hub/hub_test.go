package hub_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/agent/opencode"
	"github.com/howlerops/oculus/daemon/crypto"
	"github.com/howlerops/oculus/daemon/hub"
	"github.com/howlerops/oculus/daemon/protocol"
	"github.com/howlerops/oculus/daemon/transport"
)

// --- minimal opencode stub (same real event shapes as the provider test) ---

type stub struct {
	events    chan string
	connected chan struct{}
	permCh    chan string
}

func newStub() *stub {
	return &stub{events: make(chan string, 16), connected: make(chan struct{}), permCh: make(chan string, 1)}
}

func (s *stub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/session":
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "ses_e2e", "title": "e2e"})
	case r.Method == http.MethodGet && r.URL.Path == "/event":
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)
		if fl != nil {
			fl.Flush()
		}
		select {
		case <-s.connected:
		default:
			close(s.connected)
		}
		for {
			select {
			case ev := <-s.events:
				fmt.Fprintf(w, "data: %s\n\n", ev)
				if fl != nil {
					fl.Flush()
				}
			case <-r.Context().Done():
				return
			}
		}
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/message"):
		go s.scenario()
		w.WriteHeader(http.StatusOK)
	case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/permissions/"):
		var body struct {
			Response string `json:"response"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		select {
		case s.permCh <- body.Response:
		default:
		}
		w.WriteHeader(http.StatusOK)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (s *stub) scenario() {
	<-s.connected
	s.events <- `{"type":"message.part.delta","properties":{"sessionID":"ses_e2e","field":"text","delta":"working"}}`
	s.events <- `{"type":"permission.asked","properties":{"id":"perm_e2e","permission":"bash","sessionID":"ses_e2e","patterns":["run"],"metadata":{"command":"run"},"tool":{"messageID":"m1","callID":"c1"}}}`
	<-s.permCh
	s.events <- `{"type":"message.part.delta","properties":{"sessionID":"ses_e2e","field":"text","delta":"done"}}`
	s.events <- `{"type":"session.idle","properties":{"sessionID":"ses_e2e"}}`
}

// --- in-memory MsgConn pair ---

type pipeConn struct {
	in, out chan []byte
	closed  chan struct{}
}

func newPipePair() (*pipeConn, *pipeConn) {
	a2b := make(chan []byte, 32)
	b2a := make(chan []byte, 32)
	return &pipeConn{in: b2a, out: a2b, closed: make(chan struct{})},
		&pipeConn{in: a2b, out: b2a, closed: make(chan struct{})}
}
func (p *pipeConn) WriteMsg(b []byte) error {
	cp := append([]byte(nil), b...)
	select {
	case p.out <- cp:
		return nil
	case <-p.closed:
		return fmt.Errorf("closed")
	}
}
func (p *pipeConn) ReadMsg() ([]byte, error) {
	select {
	case b := <-p.in:
		return b, nil
	case <-p.closed:
		return nil, fmt.Errorf("closed")
	}
}
func (p *pipeConn) Close() error {
	select {
	case <-p.closed:
	default:
		close(p.closed)
	}
	return nil
}

// TestE2E_FullStack drives the whole spine: encrypted client <-> handshake <-> hub
// <-> opencode provider <-> opencode API, including an approval round-trip.
func TestE2E_FullStack(t *testing.T) {
	oc := newStub()
	srv := httptest.NewServer(oc)
	defer srv.Close()
	// Sessions persist on the daemon across client disconnects, so the provider's
	// SSE stream stays open; force-close it in teardown or srv.Close() blocks.
	defer srv.CloseClientConnections()

	h := hub.New()
	h.Register(opencode.New(srv.URL))

	clientKP, _ := crypto.GenerateKeyPair()
	daemonKP, _ := crypto.GenerateKeyPair()
	cPipe, sPipe := newPipePair()

	go func() {
		conn, err := transport.ServerHandshake(sPipe, daemonKP, func([]byte, string) bool { return true })
		if err != nil {
			t.Errorf("server handshake: %v", err)
			return
		}
		_ = h.Serve(context.Background(), conn)
	}()

	client, err := transport.ClientHandshake(cPipe, clientKP, daemonKP.Public(), "secret")
	if err != nil {
		t.Fatalf("client handshake: %v", err)
	}

	// send session.create
	send := func(id, typ string, payload any) {
		raw, err := protocol.Encode(id, typ, payload)
		if err != nil {
			t.Fatal(err)
		}
		if err := client.Send(raw); err != nil {
			t.Fatal(err)
		}
	}
	send("c1", protocol.TypeSessionCreate, protocol.SessionCreate{Provider: "opencode", Prompt: "go"})

	var gotOK, gotOutput, gotIdle bool
	deadline := time.After(5 * time.Second)
	type recvd struct {
		env protocol.Envelope
		err error
	}
	rc := make(chan recvd, 8)
	go func() {
		for {
			raw, err := client.Recv()
			if err != nil {
				rc <- recvd{err: err}
				return
			}
			env, _ := protocol.Decode(raw)
			rc <- recvd{env: env}
		}
	}()

	for !(gotOK && gotOutput && gotIdle) {
		select {
		case r := <-rc:
			if r.err != nil {
				t.Fatalf("recv: %v", r.err)
			}
			switch r.env.Type {
			case protocol.TypeOK:
				if r.env.ID == "c1" {
					gotOK = true
				}
			case protocol.TypeOutputDelta:
				gotOutput = true
			case protocol.TypeApprovalRequest:
				var ar protocol.ApprovalRequest
				_ = r.env.Unmarshal(&ar)
				send("c2", protocol.TypeApprovalRespond, protocol.ApprovalRespond{ApprovalID: ar.ApprovalID, Decision: protocol.DecisionAllow})
			case protocol.TypeSessionStatus:
				var ss protocol.SessionStatus
				_ = r.env.Unmarshal(&ss)
				if ss.Status == protocol.StatusIdle || ss.Status == protocol.StatusDone {
					gotIdle = true
				}
			case protocol.TypeError:
				var e protocol.Error
				_ = r.env.Unmarshal(&e)
				t.Fatalf("daemon error: %s", e.Message)
			}
		case <-deadline:
			t.Fatalf("timeout: ok=%v output=%v idle=%v", gotOK, gotOutput, gotIdle)
		}
	}

	_ = client.Close()
}
