// Package octest provides a stub `opencode serve` HTTP/SSE server for tests, using
// the real event shapes. It runs a default scenario on the first prompt:
// stream output -> request a permission -> (await decision) -> stream output -> idle.
package octest

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
)

const SessionID = "ses_stub"

// Stub is an http.Handler mimicking opencode.
type Stub struct {
	events    chan string
	connected chan struct{}
	permCh    chan string
	turnDone  chan struct{}

	mu       sync.Mutex
	permResp string
}

// New returns a ready Stub.
func New() *Stub {
	return &Stub{
		events:    make(chan string, 16),
		connected: make(chan struct{}),
		permCh:    make(chan string, 1),
		turnDone:  make(chan struct{}, 1),
	}
}

// LastPermissionResponse returns the last response body value posted to the
// permissions endpoint ("once"/"always"/"reject").
func (s *Stub) LastPermissionResponse() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.permResp
}

func (s *Stub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/session":
		_ = json.NewEncoder(w).Encode(map[string]any{"id": SessionID, "title": "stub"})

	case r.Method == http.MethodGet && r.URL.Path == "/event":
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)
		if fl != nil {
			fl.Flush()
		}
		select {
		case <-s.connected:
		default:
			close(s.connected)
		}
		for {
			select {
			case ev := <-s.events:
				fmt.Fprintf(w, "data: %s\n\n", ev)
				if fl != nil {
					fl.Flush()
				}
			case <-r.Context().Done():
				return
			}
		}

	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/message"):
		// Real opencode blocks the message POST until the turn finishes; mirror that so the adapter's
		// POST-return idle backstop fires after the turn, not immediately.
		go s.scenario()
		select {
		case <-s.turnDone:
		case <-r.Context().Done():
		}
		w.WriteHeader(http.StatusOK)

	case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/permissions/"):
		var body struct {
			Response string `json:"response"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		s.mu.Lock()
		s.permResp = body.Response
		s.mu.Unlock()
		select {
		case s.permCh <- body.Response:
		default:
		}
		w.WriteHeader(http.StatusOK)

	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (s *Stub) scenario() {
	<-s.connected
	s.events <- `{"type":"message.part.delta","properties":{"sessionID":"` + SessionID + `","field":"text","delta":"working"}}`
	s.events <- `{"type":"permission.asked","properties":{"id":"perm_stub","permission":"bash","sessionID":"` + SessionID + `","patterns":["run"],"metadata":{"command":"run"},"tool":{"messageID":"m1","callID":"c1"}}}`
	<-s.permCh
	s.events <- `{"type":"message.part.delta","properties":{"sessionID":"` + SessionID + `","field":"text","delta":"done"}}`
	s.events <- `{"type":"session.idle","properties":{"sessionID":"` + SessionID + `"}}`
	s.turnDone <- struct{}{} // let POST /message return (turn complete)
}
