// Command oculus-relay is the shared, stateless ciphertext-forwarding relay that lets an Iron Rain
// app reach a daemon from anywhere (off-LAN) without opening inbound ports on the Mac. Host (daemon)
// and client (app) both dial the relay outbound and are bridged by server_id. The relay forwards
// opaque, end-to-end-encrypted bytes — it can't read session content. Deploy one publicly (e.g. on
// Fly.io); every daemon connects to it automatically.
package main

import (
	"log"
	"net/http"
	"os"
	"time"

	"github.com/howlerops/oculus/daemon/relay"
)

func main() {
	addr := ":8080"
	if p := os.Getenv("PORT"); p != "" { // Fly (and most PaaS) inject PORT
		addr = ":" + p
	}
	if len(os.Args) > 1 { // explicit override: oculus-relay :9000
		addr = os.Args[1]
	}

	r := relay.New()
	mux := http.NewServeMux()
	mux.Handle("/ws", r.Handler())
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("iron rain relay")) })

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second, // slowloris guard on the public listener
	}
	log.Printf("oculus-relay listening on %s", addr)
	log.Fatal(srv.ListenAndServe())
}
