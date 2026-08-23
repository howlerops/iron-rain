// Command child-probe delegates a subtask over the real wire protocol, so concurrent delegation can
// be exercised end to end without driving the UI N times.
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
	parent := flag.String("parent", "", "parent session id")
	subtask := flag.String("subtask", "", "the subtask")
	provider := flag.String("provider", "", "provider for the child (empty = parent's)")
	model := flag.String("model", "", "model for the child")
	worktree := flag.Bool("worktree", true, "give the child its own worktree")
	count := flag.Int("count", 1, "how many children to delegate CONCURRENTLY")
	parentProvider := flag.String("parent-provider", "claude-code", "provider for the parent session")
	project := flag.String("project", "", "project id for the parent")
	timeout := flag.Duration("timeout", 120*time.Second, "deadline")
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

	// Create the parent here rather than reusing one, so it lives on THIS connection for as long as
	// the delegations need it. A parent created by another client and then disconnected is gone.
	if *parent == "" {
		cr, _ := protocol.Encode("p1", protocol.TypeSessionCreate, protocol.SessionCreate{
			Provider: *parentProvider, ProjectID: *project, Prompt: "Say ready.",
		})
		if err := conn.Send(cr); err != nil {
			log.Fatal(err)
		}
		*parent = awaitID(conn, "p1", *timeout)
		log.Printf("PARENT %s", *parent)
	}

	// Fire every delegation BEFORE reading any reply, so they are genuinely in flight together —
	// sending one and waiting for it would test sequential delegation and prove nothing about
	// collisions.
	for i := 0; i < *count; i++ {
		id := fmt.Sprintf("c%d", i+1)
		req, _ := protocol.Encode(id, protocol.TypeSessionChild, protocol.SessionChild{
			ParentSessionID: *parent,
			Subtask:         fmt.Sprintf("%s (agent %d)", *subtask, i+1),
			Provider:        *provider, Model: *model, Worktree: *worktree,
		})
		if err := conn.Send(req); err != nil {
			log.Fatal(err)
		}
	}
	for i := 0; i < *count; i++ {
		log.Printf("CHILD %d -> %s", i+1, awaitChild(conn, *timeout))
	}
	return
}

// awaitID waits for the reply to one request id and returns the session id it carries.
func awaitID(conn *transport.Conn, id string, timeout time.Duration) string {
	dl := time.After(timeout)
	for {
		raw := recvOrDie(conn, dl)
		env, err := protocol.Decode(raw)
		if err != nil {
			continue
		}
		if env.Type == protocol.TypeError {
			log.Fatalf("DAEMON ERROR: %s", string(env.Payload))
		}
		if env.ID == id {
			var s protocol.Session
			if json.Unmarshal(env.Payload, &s) == nil && s.ID != "" {
				return s.ID
			}
		}
	}
}

// awaitChild waits for the next child reply, whichever delegation answers first.
func awaitChild(conn *transport.Conn, timeout time.Duration) string {
	dl := time.After(timeout)
	for {
		raw := recvOrDie(conn, dl)
		env, err := protocol.Decode(raw)
		if err != nil {
			continue
		}
		if env.Type == protocol.TypeError {
			log.Fatalf("DAEMON ERROR: %s", string(env.Payload))
		}
		if len(env.ID) > 1 && env.ID[0] == 'c' {
			var s protocol.Session
			if json.Unmarshal(env.Payload, &s) == nil && s.ID != "" {
				return fmt.Sprintf("%s provider=%s cwd=%s", s.ID, s.Provider, s.Cwd)
			}
		}
	}
}

func recvOrDie(conn *transport.Conn, dl <-chan time.Time) []byte {
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
