package server_test

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/agent/opencode"
	"github.com/howlerops/oculus/daemon/agent/opencode/octest"
	"github.com/howlerops/oculus/daemon/crypto"
	"github.com/howlerops/oculus/daemon/hub"
	"github.com/howlerops/oculus/daemon/protocol"
	"github.com/howlerops/oculus/daemon/server"
)

// TestE2E_OverWebSocket drives the full stack over a real WebSocket connection:
// WS dial -> encrypted handshake -> hub -> opencode provider -> opencode API,
// including an approval round-trip.
func TestE2E_OverWebSocket(t *testing.T) {
	oc := octest.New()
	ocSrv := httptest.NewServer(oc)
	defer ocSrv.Close()
	defer ocSrv.CloseClientConnections()

	h := hub.New()
	h.Register(opencode.New(ocSrv.URL))

	daemonKP, _ := crypto.GenerateKeyPair()
	clientKP, _ := crypto.GenerateKeyPair()
	s := server.New(h, daemonKP, func([]byte, string) bool { return true })

	httpSrv := httptest.NewServer(s.Handler())
	defer httpSrv.Close()
	defer httpSrv.CloseClientConnections()

	wsURL := "ws" + strings.TrimPrefix(httpSrv.URL, "http")

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	conn, err := server.Dial(ctx, wsURL, clientKP, daemonKP.Public(), "secret")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	send := func(id, typ string, payload any) {
		raw, err := protocol.Encode(id, typ, payload)
		if err != nil {
			t.Fatal(err)
		}
		if err := conn.Send(raw); err != nil {
			t.Fatal(err)
		}
	}
	send("c1", protocol.TypeSessionCreate, protocol.SessionCreate{Provider: "opencode", Prompt: "go"})

	type recvd struct {
		env protocol.Envelope
		err error
	}
	rc := make(chan recvd, 8)
	go func() {
		for {
			raw, err := conn.Recv()
			if err != nil {
				rc <- recvd{err: err}
				return
			}
			env, _ := protocol.Decode(raw)
			rc <- recvd{env: env}
		}
	}()

	var gotOK, gotOutput, gotIdle bool
	deadline := time.After(10 * time.Second)
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
			t.Fatalf("timeout: ok=%v output=%v idle=%v", gotOutput, gotOutput, gotIdle)
		}
	}

	if got := oc.LastPermissionResponse(); got != "once" {
		t.Fatalf("permission response = %q, want once", got)
	}
}
