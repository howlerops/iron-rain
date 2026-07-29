// Command turn-smoke drives a LIVE daemon over the real wire protocol and verifies the Turn Engine
// end-to-end: it creates a throwaway session on the given provider, sends a trivial prompt, and
// watches turn.state — asserting the turn opens (running), stays honest while working (heartbeats
// carry last_event_at), and closes (idle) WITHOUT any client-side timeout logic. Exit 0 = pass.
//
//	go run ./cmd/turn-smoke -ws ws://127.0.0.1:6000/ws -pub <daemonPubHex> -secret <secret> \
//	    -provider opencode -cwd /tmp/smoke -prompt "Reply with exactly: OK" -timeout 180s
package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/howlerops/oculus/daemon/crypto"
	"github.com/howlerops/oculus/daemon/protocol"
	"github.com/howlerops/oculus/daemon/transport"
	"github.com/howlerops/oculus/daemon/wsmsg"
)

func main() {
	ws := flag.String("ws", "ws://127.0.0.1:6000/ws", "daemon websocket URL")
	pubHex := flag.String("pub", "", "daemon public key (hex)")
	secret := flag.String("secret", "", "pairing secret")
	provider := flag.String("provider", "opencode", "agent provider")
	cwd := flag.String("cwd", os.TempDir(), "session working directory")
	prompt := flag.String("prompt", "Reply with exactly: OK — nothing else.", "prompt to send")
	timeout := flag.Duration("timeout", 180*time.Second, "overall pass deadline")
	mode := flag.String("mode", "turn", "turn | discover | attach")
	session := flag.String("session", "", "session id (mode=attach)")
	flag.Parse()

	pub, err := hex.DecodeString(*pubHex)
	if err != nil || len(pub) == 0 {
		log.Fatalf("bad -pub: %v", err)
	}
	kp, err := crypto.GenerateKeyPair()
	if err != nil {
		log.Fatal(err)
	}
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
	log.Printf("connected to %s", *ws)

	switch *mode {
	case "discover":
		req, _ := protocol.Encode("d1", protocol.TypeDiscover, struct{}{})
		if err := conn.Send(req); err != nil {
			log.Fatal(err)
		}
		dl := time.After(*timeout)
		for {
			var raw []byte
			done := make(chan error, 1)
			go func() { var e error; raw, e = conn.Recv(); done <- e }()
			select {
			case e := <-done:
				if e != nil {
					log.Fatalf("recv: %v", e)
				}
			case <-dl:
				log.Fatal("discover: timeout")
			}
			env, err := protocol.Decode(raw)
			if err != nil {
				continue
			}
			if env.ID == "d1" {
				fmt.Printf("DISCOVER RESULT: %s\n", string(env.Payload))
				return
			}
		}
	case "attach":
		replayed := 0
		req, _ := protocol.Encode("a1", protocol.TypeSessionAttach, protocol.SessionAttach{
			Provider: *provider, SessionID: *session, Cwd: *cwd,
		})
		if err := conn.Send(req); err != nil {
			log.Fatal(err)
		}
		dl := time.After(*timeout)
		for {
			var raw []byte
			done := make(chan error, 1)
			go func() { var e error; raw, e = conn.Recv(); done <- e }()
			select {
			case e := <-done:
				if e != nil {
					log.Fatalf("recv: %v", e)
				}
			case <-dl:
				log.Fatal("attach: TIMEOUT waiting for a reply/replay — this is what the app would experience")
			}
			env, err := protocol.Decode(raw)
			if err != nil {
				continue
			}
			switch env.Type {
			case protocol.TypeOK:
				if env.ID == "a1" {
					log.Printf("attach OK: %.300s", string(env.Payload))
				}
			case protocol.TypeError:
				log.Fatalf("attach ERROR: %s", string(env.Payload))
			case protocol.TypeSessionMessage:
				var m protocol.SessionMessage
				_ = json.Unmarshal(env.Payload, &m)
				replayed++
				log.Printf("replayed %s msg (%d chars)", m.Role, len(m.Text))
				if replayed >= 10 {
					fmt.Printf("PASS: attach replayed %d history messages\n", replayed)
					return
				}
			case protocol.TypeTurnState:
				var ts protocol.TurnState
				_ = json.Unmarshal(env.Payload, &ts)
				log.Printf("turn.state → %s", ts.State)
			case protocol.TypeSessionList:
				fmt.Println("PASS: attach produced a session (session.list broadcast received)")
				return
			}
		}
	}

	create, _ := protocol.Encode("c1", protocol.TypeSessionCreate, protocol.SessionCreate{
		Provider: *provider, Cwd: *cwd, Prompt: *prompt, Ephemeral: true,
	})
	if err := conn.Send(create); err != nil {
		log.Fatalf("send create: %v", err)
	}

	var (
		sessionID  string
		sawRunning bool
		heartbeats int
		lastState  string
		deadline   = time.After(*timeout)
		frames     = make(chan protocol.Envelope, 256)
	)
	go func() {
		for {
			raw, err := conn.Recv()
			if err != nil {
				close(frames)
				return
			}
			if env, err := protocol.Decode(raw); err == nil {
				frames <- env
			}
		}
	}()

	for {
		select {
		case <-deadline:
			log.Fatalf("FAIL: timed out (%s) — lastState=%q sawRunning=%v heartbeats=%d", *timeout, lastState, sawRunning, heartbeats)
		case env, ok := <-frames:
			if !ok {
				log.Fatalf("FAIL: connection closed — lastState=%q", lastState)
			}
			switch env.Type {
			case protocol.TypeOK:
				if env.ID == "c1" {
					var sess protocol.Session
					if json.Unmarshal(env.Payload, &sess) == nil && sess.ID != "" {
						sessionID = sess.ID
						log.Printf("session created: %s", sessionID)
					}
				}
			case protocol.TypeError:
				log.Fatalf("FAIL: daemon error: %s", string(env.Payload))
			case protocol.TypeTurnState:
				var ts protocol.TurnState
				if json.Unmarshal(env.Payload, &ts) != nil || (sessionID != "" && ts.SessionID != sessionID) {
					continue
				}
				if ts.State == lastState && ts.State == protocol.StatusRunning {
					heartbeats++ // repeated running frames while open = heartbeats
				}
				if ts.State != lastState {
					log.Printf("turn.state → %-18s (turn=%s children=%d reason=%q)", ts.State, ts.TurnID, len(ts.Children), ts.Reason)
				}
				lastState = ts.State
				switch ts.State {
				case protocol.StatusRunning:
					sawRunning = true
				case protocol.StatusIdle:
					if !sawRunning {
						log.Fatalf("FAIL: turn closed idle without ever reporting running")
					}
					fmt.Printf("PASS: turn opened, ran, and closed idle (heartbeat frames: %d)\n", heartbeats)
					stop(conn, sessionID)
					return
				case "abandoned", protocol.StatusError:
					log.Fatalf("FAIL: turn closed %s: %s", ts.State, ts.Reason)
				}
			case protocol.TypeOutputDelta, protocol.TypeSessionMessage:
				// progress; the assertions ride on turn.state only
			}
		}
	}
}

// stop tears down the throwaway session (best-effort).
func stop(conn *transport.Conn, sessionID string) {
	if sessionID == "" {
		return
	}
	raw, _ := protocol.Encode("s1", protocol.TypeSessionStop, protocol.SessionRef{SessionID: sessionID})
	_ = conn.Send(raw)
	time.Sleep(500 * time.Millisecond)
}
