// Command replay-probe answers one question against a LIVE daemon: if a client opens this session
// right now, what history does it actually get?
//
// It exists because that question has been answered wrongly three separate times, each time producing
// a conversation that rendered blank or doubled, and each time diagnosed by reading code rather than
// by looking. This subscribes over the real wire protocol and reports what comes back — frames by
// type, how many would render as visible rows, and whether any frame arrived twice.
//
//	go run ./cmd/replay-probe -pub <daemonPubHex> -secret <secret> [-session <id>]
//
// With no -session it probes every session the daemon lists. Exit 1 if any would render empty.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"sort"
	"time"

	"github.com/howlerops/oculus/daemon/crypto"
	"github.com/howlerops/oculus/daemon/protocol"
	"github.com/howlerops/oculus/daemon/transport"
	"github.com/howlerops/oculus/daemon/wsmsg"
)

type frame struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

func main() {
	ws := flag.String("ws", "ws://127.0.0.1:6000/ws", "daemon websocket URL")
	pubHex := flag.String("pub", "", "daemon public key (hex)")
	secret := flag.String("secret", "", "pairing secret")
	session := flag.String("session", "", "session id (default: probe all)")
	quiet := flag.Duration("quiet", 3*time.Second, "treat the replay as finished after this much silence")
	flag.Parse()

	pub, err := hex.DecodeString(*pubHex)
	if err != nil || len(pub) == 0 {
		log.Fatalf("bad -pub: %v", err)
	}
	kp, err := crypto.GenerateKeyPair()
	if err != nil {
		log.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
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

	rd := newReader(conn)
	ids := []string{*session}
	if *session == "" {
		ids = listSessions(conn, rd, *quiet)
		if len(ids) == 0 {
			log.Fatal("daemon listed no sessions")
		}
	}

	failures := 0
	for _, id := range ids {
		if !probe(conn, rd, id, *quiet) {
			failures++
		}
	}
	if failures > 0 {
		fmt.Printf("\nFAIL: %d/%d session(s) would open with no visible history\n", failures, len(ids))
		os.Exit(1)
	}
	fmt.Printf("\nPASS: all %d session(s) replay visible history\n", len(ids))
}

func listSessions(conn *transport.Conn, rd *reader, quiet time.Duration) []string {
	req, _ := protocol.Encode("l1", protocol.TypeSessionList, struct{}{})
	if err := conn.Send(req); err != nil {
		log.Fatal(err)
	}
	deadline := time.Now().Add(quiet + 2*time.Second)
	for time.Now().Before(deadline) {
		raw, err := rd.next(time.Until(deadline))
		if err != nil {
			break
		}
		var env struct {
			Payload struct {
				Sessions []struct {
					ID       string `json:"id"`
					Provider string `json:"provider"`
					Status   string `json:"status"`
				} `json:"sessions"`
			} `json:"payload"`
		}
		if json.Unmarshal(raw, &env) != nil || len(env.Payload.Sessions) == 0 {
			continue
		}
		var out []string
		for _, s := range env.Payload.Sessions {
			fmt.Printf("session %s  provider=%s status=%s\n", s.ID, s.Provider, s.Status)
			out = append(out, s.ID)
		}
		return out
	}
	return nil
}

// probe subscribes and reports what the daemon actually sends back.
func probe(conn *transport.Conn, rd *reader, id string, quiet time.Duration) bool {
	req, _ := protocol.Encode("s-"+id, protocol.TypeSessionSubscribe, protocol.SessionRef{SessionID: id})
	if err := conn.Send(req); err != nil {
		log.Fatal(err)
	}
	byType := map[string]int{}
	seen := map[string]int{}             // frame hash -> count, to catch a doubled replay
	kindOf := map[string]string{}        // frame hash -> its type, so repeats can be judged by what they draw
	sample := map[string][]byte{}        // frame hash -> the raw frame, for reporting a real duplicate
	var dupSamples [][]byte
	visible, total := 0, 0
	last := time.Now()
	for time.Since(last) < quiet {
		raw, err := rd.next(quiet)
		if err != nil {
			break
		}
		var f frame
		if json.Unmarshal(raw, &f) != nil {
			continue
		}
		// Only count this session's frames: the list broadcast and other sessions share the socket.
		if f.Payload != nil {
			var sref struct {
				SessionID string `json:"session_id"`
			}
			_ = json.Unmarshal(f.Payload, &sref)
			if sref.SessionID != "" && sref.SessionID != id {
				continue
			}
		}
		last = time.Now()
		total++
		byType[f.Type]++
		h := sha256.Sum256(raw)
		key := hex.EncodeToString(h[:8])
		seen[key]++
		kindOf[key] = f.Type
		if n := seen[key]; n == 2 {
			sample[key] = raw // keep the first repeat for the report
		}
		// What the user would actually SEE. Status and turn frames render nothing, which is exactly
		// how a "connected but empty" conversation looks from the inside.
		switch f.Type {
		case protocol.TypeSessionMessage, protocol.TypeSessionTool, protocol.TypeUIComponent:
			visible++
		}
	}
	// Duplicates that MATTER are duplicates of frames that render. Status and turn frames repeat
	// byte-for-byte all the time — several idles in a turn are normal and draw nothing — so counting
	// them as "the transcript would double" produced a standing false alarm that buried the signal
	// this probe exists to give.
	dupes, visibleDupes := 0, 0
	dupTypes := map[string]int{}
	for k, n := range seen {
		if n <= 1 {
			continue
		}
		dupes += n - 1
		dupTypes[kindOf[k]] += n - 1
		switch kindOf[k] {
		case protocol.TypeSessionMessage, protocol.TypeSessionTool, protocol.TypeUIComponent:
			visibleDupes += n - 1
			if len(dupSamples) < 4 {
				dupSamples = append(dupSamples, sample[k])
			}
		}
	}
	kinds := make([]string, 0, len(byType))
	for k := range byType {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	fmt.Printf("\n%s\n  frames=%d visible=%d duplicated=%d\n", id, total, visible, dupes)
	for _, k := range kinds {
		fmt.Printf("    %-28s %d\n", k, byType[k])
	}
	if visible == 0 {
		fmt.Printf("  >> would render EMPTY\n")
		return false
	}
	if dupes > 0 {
		for k, n := range dupTypes {
			fmt.Printf("    repeated: %-18s %d\n", k, n)
		}
	}
	if visibleDupes > 0 {
		fmt.Printf("  >> %d RENDERING frame(s) sent twice — the transcript would double\n", visibleDupes)
		for _, raw := range dupSamples {
			one := string(raw)
			if len(one) > 220 {
				one = one[:220] + "…"
			}
			fmt.Printf("     %s\n", one)
		}
	} else if dupes > 0 {
		fmt.Printf("  (%d non-rendering repeat(s) — status/turn frames, which draw nothing)\n", dupes)
	}
	return true
}

// reader pumps the connection on ONE goroutine.
//
// The obvious shape — spawn a goroutine per Recv and race it against a timer — silently loses a
// frame on every timeout: the abandoned goroutine stays blocked in Recv and swallows the next frame
// into a channel nobody reads. A probe that eats evidence is worse than no probe.
type reader struct{ ch chan []byte }

func newReader(conn *transport.Conn) *reader {
	r := &reader{ch: make(chan []byte, 4096)}
	go func() {
		for {
			raw, err := conn.Recv()
			if err != nil {
				close(r.ch)
				return
			}
			r.ch <- raw
		}
	}()
	return r
}

func (r *reader) next(d time.Duration) ([]byte, error) {
	select {
	case raw, ok := <-r.ch:
		if !ok {
			return nil, fmt.Errorf("closed")
		}
		return raw, nil
	case <-time.After(d):
		return nil, fmt.Errorf("quiet")
	}
}
