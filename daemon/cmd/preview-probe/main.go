// Command preview-probe fetches a live session's dev server through a running daemon, over the real
// wire protocol — the same path a phone's web view takes.
//
// It exists because the interesting failure is not in any unit: it is whether the daemon resolves a
// session to its own preview, sets a Host the preview router will route on, and returns something a
// WKWebView can render. That crosses three components and a socket.
//
//	go run ./cmd/preview-probe -pub <hex> -secret <s> -session <id> -path /
//	go run ./cmd/preview-probe -pub <hex> -secret <s> -sessions   # list sessions with previews
package main

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"log"
	"strings"
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
	session := flag.String("session", "", "session id to fetch from")
	path := flag.String("path", "/", "path within that session's dev server")
	list := flag.Bool("sessions", false, "list sessions and their preview URLs, then exit")
	timeout := flag.Duration("timeout", 30*time.Second, "deadline")
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
	if *list {
		req, _ = protocol.Encode("s1", protocol.TypeSessionList, struct{}{})
	} else {
		if *session == "" {
			log.Fatal("-session is required (or use -sessions to list them)")
		}
		req, _ = protocol.Encode("pf1", protocol.TypePreviewFetch, protocol.PreviewFetchReq{
			SessionID: *session,
			Path:      *path,
			Headers:   map[string]string{"Accept": "text/html,*/*"},
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
		case protocol.TypeSessionList:
			var sl struct {
				Sessions []struct {
					ID         string `json:"id"`
					Title      string `json:"title"`
					PreviewURL string `json:"preview_url"`
				} `json:"sessions"`
			}
			if json.Unmarshal(env.Payload, &sl) != nil {
				continue
			}
			n := 0
			for _, s := range sl.Sessions {
				if s.PreviewURL == "" {
					continue
				}
				n++
				log.Printf("  %s  %s  %s", s.ID, s.PreviewURL, s.Title)
			}
			log.Printf("%d session(s) with a preview, of %d", n, len(sl.Sessions))
			return
		case protocol.TypeOK, protocol.TypePreviewFetch:
			var resp protocol.PreviewFetchResp
			if json.Unmarshal(env.Payload, &resp) != nil || resp.Status == 0 {
				continue
			}
			body, derr := base64.StdEncoding.DecodeString(resp.Body)
			if derr != nil {
				log.Fatalf("body is not valid base64: %v", derr)
			}
			log.Printf("HTTP %d, %d bytes, content-type=%q",
				resp.Status, len(body), resp.Headers["Content-Type"])
			preview := strings.TrimSpace(string(body))
			if len(preview) > 300 {
				preview = preview[:300] + "…"
			}
			log.Printf("body: %s", preview)
			return
		}
	}
}
