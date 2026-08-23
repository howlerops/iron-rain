// Command fanout-probe drives a LIVE daemon's fan-out over the real wire protocol, so a dead
// button in the app can be attributed to the client or the daemon instead of guessed at.
//
//	go run ./cmd/fanout-probe -pub <hex> -secret <s> -provider claude-code -project <id> -count 3
package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"flag"
	"log"
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
	provider := flag.String("provider", "claude-code", "agent provider")
	project := flag.String("project", "", "project id")
	prompt := flag.String("prompt", "Write a one-paragraph note in NOTE.md about token buckets.", "task")
	count := flag.Int("count", 3, "agents")
	listProjects := flag.Bool("projects", false, "list projects and exit")
	addPath := flag.String("add", "", "register a project folder and exit")
	timeout := flag.Duration("timeout", 120*time.Second, "deadline")
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
	log.Printf("connected")

	var req []byte
	if *addPath != "" {
		req, _ = protocol.Encode("a1", protocol.TypeProjectAdd, protocol.ProjectAdd{Path: *addPath})
	} else if *listProjects {
		req, _ = protocol.Encode("p1", protocol.TypeProjectList, struct{}{})
	} else {
		req, _ = protocol.Encode("f1", protocol.TypeFanoutCreate, protocol.FanoutCreate{
			Provider: *provider, ProjectID: *project, Prompt: *prompt, Count: *count, Judge: true,
		})
	}
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
			log.Fatal("timeout with no answer")
		}
		env, err := protocol.Decode(raw)
		if err != nil {
			continue
		}
		switch env.Type {
		case protocol.TypeError:
			log.Fatalf("DAEMON ERROR: %s", string(env.Payload))
		case protocol.TypeProjectList:
			var pl struct {
				Projects []struct {
					ID   string `json:"id"`
					Name string `json:"name"`
					Path string `json:"path"`
				} `json:"projects"`
			}
			_ = json.Unmarshal(env.Payload, &pl)
			for _, p := range pl.Projects {
				log.Printf("PROJECT %s  %s  %s", p.ID, p.Name, p.Path)
			}
			return
		}
		// The reply echoes the request type, so match on the request ID instead.
		if env.ID == "a1" {
			log.Printf("ADDED: %s", string(env.Payload))
			return
		}
		if env.ID == "f1" {
			var res protocol.FanoutResult
			if json.Unmarshal(env.Payload, &res) == nil && res.Group != "" {
				log.Printf("OK group=%s sessions=%d %v", res.Group, len(res.SessionIDs), res.SessionIDs)
				return
			}
		}
	}
}
