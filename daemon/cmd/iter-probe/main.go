// Command iter-probe drives ONE session through N sequential prompts, so "one agent iterating with
// its own history" can be compared against "N agents sharing notes". The harness keeps the
// conversation, so each turn already sees every previous attempt this agent made — which is the
// baseline any shared-memory feature has to beat.
package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"time"

	"github.com/howlerops/oculus/daemon/crypto"
	"github.com/howlerops/oculus/daemon/protocol"
	"github.com/howlerops/oculus/daemon/transport"
	"github.com/howlerops/oculus/daemon/wsmsg"
)

func main() {
	ws := flag.String("ws", "ws://127.0.0.1:6000/ws", "daemon websocket")
	pubHex := flag.String("pub", "", "daemon pubkey hex")
	secret := flag.String("secret", "", "pairing secret")
	cwd := flag.String("cwd", "", "working directory")
	provider := flag.String("provider", "claude-code", "provider")
	turns := flag.Int("turns", 3, "how many sequential prompts")
	first := flag.String("first", "", "prompt for turn 1")
	next := flag.String("next", "", "prompt for turns 2..N")
	timeout := flag.Duration("timeout", 1800*time.Second, "overall deadline")
	flag.Parse()

	pub, _ := hex.DecodeString(*pubHex)
	kp, _ := crypto.GenerateKeyPair()
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	mc, err := wsmsg.Dial(ctx, *ws)
	if err != nil {
		log.Fatalf("dial: %v", err)
	}
	conn, err := transport.ClientHandshake(mc, kp, pub, *secret)
	if err != nil {
		log.Fatalf("handshake: %v", err)
	}
	defer conn.Close()

	cr, _ := protocol.Encode("s1", protocol.TypeSessionCreate, protocol.SessionCreate{
		Provider: *provider, Cwd: *cwd, Prompt: *first,
	})
	if err := conn.Send(cr); err != nil {
		log.Fatal(err)
	}
	sid := ""
	dl := time.After(*timeout)

	// Turn 1 rides the create. Each subsequent prompt is sent only after the previous turn closes,
	// so the agent has genuinely finished (and its history contains) the attempt before it.
	sent := 1
	for {
		raw := recv(conn, dl)
		env, err := protocol.Decode(raw)
		if err != nil {
			continue
		}
		if env.Type == protocol.TypeError {
			log.Fatalf("DAEMON ERROR: %s", string(env.Payload))
		}
		if env.ID == "s1" && sid == "" {
			var s protocol.Session
			if json.Unmarshal(env.Payload, &s) == nil && s.ID != "" {
				sid = s.ID
				log.Printf("SESSION %s", sid)
			}
			continue
		}
		if env.Type != protocol.TypeTurnState || sid == "" {
			continue
		}
		var ts protocol.TurnState
		if json.Unmarshal(env.Payload, &ts) != nil || ts.SessionID != sid {
			continue
		}
		if ts.State != protocol.StatusIdle {
			continue
		}
		log.Printf("turn %d finished", sent)
		if sent >= *turns {
			log.Printf("ALL %d TURNS DONE", *turns)
			return
		}
		sent++
		p, _ := protocol.Encode(fmt.Sprintf("p%d", sent), protocol.TypeSessionPrompt,
			protocol.SessionPrompt{SessionID: sid, Text: *next})
		if err := conn.Send(p); err != nil {
			log.Fatal(err)
		}
		log.Printf("sent turn %d", sent)
	}
}

func recv(conn *transport.Conn, dl <-chan time.Time) []byte {
	var raw []byte
	done := make(chan error, 1)
	go func() { var e error; raw, e = conn.Recv(); done <- e }()
	select {
	case e := <-done:
		if e != nil {
			log.Fatalf("recv: %v", e)
		}
	case <-dl:
		log.Fatal("timeout")
	}
	return raw
}
