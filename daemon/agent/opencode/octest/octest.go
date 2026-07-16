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

	mu       sync.Mutex
	permResp string
}

// New returns a ready Stub.
func New() *Stub {
	return &Stub{
		events:    make(chan string, 16),
		connected: make(chan struct{}),
		permCh:    make(chan string, 1),
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
		go s.scenario()
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
	s.events <- `{"type":"permission.updated","properties":{"id":"perm_stub","type":"bash","sessionID":"` + SessionID + `","messageID":"m1","title":"run","metadata":{},"time":{"created":0}}}`
	<-s.permCh
	s.events <- `{"type":"message.part.delta","properties":{"sessionID":"` + SessionID + `","field":"text","delta":"done"}}`
	s.events <- `{"type":"session.idle","properties":{"sessionID":"` + SessionID + `"}}`
}
