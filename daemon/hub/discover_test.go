package hub_test

import (
	"context"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/crypto"
	"github.com/howlerops/oculus/daemon/hub"
	"github.com/howlerops/oculus/daemon/protocol"
	"github.com/howlerops/oculus/daemon/transport"
)

// TestDiscover_ReturnsHostArtifacts drives discover.list end-to-end over the
// encrypted transport with an injected host scan.
func TestDiscover_ReturnsHostArtifacts(t *testing.T) {
	h := hub.New()
	h.SetDiscoverer(func(context.Context) ([]protocol.Discovered, error) {
		return []protocol.Discovered{
			{Provider: "opencode", Kind: protocol.KindServer, URL: "http://127.0.0.1:4096"},
			{Provider: "opencode", Kind: protocol.KindSession, URL: "http://127.0.0.1:4096", SessionID: "ses_x", Title: "wip"},
			{Provider: "claude-code", Kind: protocol.KindSession, SessionID: "cc_y", Cwd: "/tmp/proj"},
		}, nil
	})

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
	defer client.Close()

	raw, _ := protocol.Encode("d1", protocol.TypeDiscover, nil)
	if err := client.Send(raw); err != nil {
		t.Fatal(err)
	}

	done := make(chan protocol.DiscoverList, 1)
	go func() {
		for {
			b, err := client.Recv()
			if err != nil {
				return
			}
			env, _ := protocol.Decode(b)
			if env.Type == protocol.TypeOK && env.ID == "d1" {
				var dl protocol.DiscoverList
				_ = env.Unmarshal(&dl)
				done <- dl
				return
			}
		}
	}()

	select {
	case dl := <-done:
		if len(dl.Items) != 3 {
			t.Fatalf("want 3 discovered items, got %d: %+v", len(dl.Items), dl.Items)
		}
		if dl.Items[1].SessionID != "ses_x" || dl.Items[1].Title != "wip" {
			t.Errorf("opencode session item = %+v", dl.Items[1])
		}
		if dl.Items[2].Provider != "claude-code" || dl.Items[2].Cwd != "/tmp/proj" {
			t.Errorf("claude session item = %+v", dl.Items[2])
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for discover.list response")
	}
}

// TestDiscover_NoDiscovererIsEmpty proves the hub answers discover.list even when
// no scan is installed (empty list, not an error).
func TestDiscover_NoDiscovererIsEmpty(t *testing.T) {
	h := hub.New()

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
	defer client.Close()

	raw, _ := protocol.Encode("d1", protocol.TypeDiscover, nil)
	_ = client.Send(raw)

	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("timeout")
		default:
		}
		b, err := client.Recv()
		if err != nil {
			t.Fatal(err)
		}
		env, _ := protocol.Decode(b)
		if env.Type == protocol.TypeOK && env.ID == "d1" {
			var dl protocol.DiscoverList
			_ = env.Unmarshal(&dl)
			if len(dl.Items) != 0 {
				t.Fatalf("want empty, got %+v", dl.Items)
			}
			return
		}
	}
}
