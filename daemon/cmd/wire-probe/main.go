// Command wire-probe sends ONE protocol message to a live daemon and prints the reply.
//
// A generic form of the feature-specific probes: most "does this actually work end to end" questions are
// one request and one answer, and writing a new binary for each of them is how a check gets skipped.
//
//	go run ./cmd/wire-probe -pub <hex> -secret <s> -type github.repos
//	go run ./cmd/wire-probe -pub <hex> -secret <s> -type github.clone -payload '{"name_with_owner":"o/r","parent":"/tmp"}'
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
	ws := flag.String("ws", "ws://127.0.0.1:6000/ws", "daemon websocket URL")
	pubHex := flag.String("pub", "", "daemon public key (hex)")
	secret := flag.String("secret", "", "pairing secret")
	typ := flag.String("type", "", "message type to send")
	payload := flag.String("payload", "{}", "JSON payload")
	timeout := flag.Duration("timeout", 60*time.Second, "deadline")
	flag.Parse()

	pub, err := hex.DecodeString(*pubHex)
	if err != nil || len(pub) == 0 {
		log.Fatalf("bad -pub: %v", err)
	}
	var body json.RawMessage
	if err := json.Unmarshal([]byte(*payload), &body); err != nil {
		log.Fatalf("bad -payload: %v", err)
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

	raw, err := protocol.Encode("w1", *typ, body)
	if err != nil {
		log.Fatal(err)
	}
	if err := conn.Send(raw); err != nil {
		log.Fatal(err)
	}

	dl := time.After(*timeout)
	for {
		var in []byte
		done := make(chan error, 1)
		go func() { var e error; in, e = conn.Recv(); done <- e }()
		select {
		case e := <-done:
			if e != nil {
				log.Fatalf("recv: %v", e)
			}
		case <-dl:
			log.Fatal("timed out with no reply")
		}
		env, err := protocol.Decode(in)
		if err != nil || env.ID != "w1" {
			continue // broadcast traffic on the way past
		}
		var pretty any
		if json.Unmarshal(env.Payload, &pretty) == nil {
			out, _ := json.MarshalIndent(pretty, "", "  ")
			fmt.Printf("%s\n%s\n", env.Type, out)
		} else {
			fmt.Printf("%s\n%s\n", env.Type, env.Payload)
		}
		return
	}
}
