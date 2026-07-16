package relay_test

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
	"github.com/howlerops/oculus/daemon/relay"
	"github.com/howlerops/oculus/daemon/server"
	"github.com/howlerops/oculus/daemon/transport"
)

// TestE2E_ViaRelay proves "from anywhere": the daemon and client both dial the
// relay outbound (no inbound ports), and the full encrypted session flows through it.
func TestE2E_ViaRelay(t *testing.T) {
	oc := octest.New()
	ocSrv := httptest.NewServer(oc)
	defer ocSrv.Close()
	defer ocSrv.CloseClientConnections()

	h := hub.New()
	h.Register(opencode.New(ocSrv.URL))
	daemonKP, _ := crypto.GenerateKeyPair()
	clientKP, _ := crypto.GenerateKeyPair()
	srv := server.New(h, daemonKP, func([]byte, string) bool { return true })

	relaySrv := httptest.NewServer(relay.New().Handler())
	defer relaySrv.Close()
	defer relaySrv.CloseClientConnections()
	relayWS := "ws" + strings.TrimPrefix(relaySrv.URL, "http")

	const serverID = "srv-1"
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Daemon serves via the relay (registers as host, then serves the bridged client).
	go func() { _ = relay.ServeHost(ctx, relayWS, serverID, srv.ServeConn) }()

	// Let the host register with the relay before the client dials.
	time.Sleep(300 * time.Millisecond)

	mc, err := relay.DialClient(ctx, relayWS, serverID)
	if err != nil {
		t.Fatalf("dial client via relay: %v", err)
	}
	client, err := transport.ClientHandshake(mc, clientKP, daemonKP.Public(), "secret")
	if err != nil {
		t.Fatalf("client handshake via relay: %v", err)
	}
	defer client.Close()

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
			t.Fatalf("timeout via relay: ok=%v output=%v idle=%v", gotOK, gotOutput, gotIdle)
		}
	}

	if got := oc.LastPermissionResponse(); got != "once" {
		t.Fatalf("permission response = %q, want once", got)
	}
}
