package hub

import (
	"encoding/json"
	"log"
	"os"
	"sync"

	"github.com/howlerops/oculus/daemon/protocol"
)

// Defaults a new session starts with.
//
// Modes are per session, but nobody wants to set the same one every time they start work. The
// setting that makes this worth having is also the one that makes it worth being careful about: a
// default of yolo means every future session silently starts with approvals off, including sessions
// started by a loop or a fan-out that no one is watching at the time.
//
// So the default is STORED but deliberately not trusted blindly — see defaultMode.

// sessionDefaults is what the client can set globally. Kept as a struct rather than loose keys so
// adding the next default (effort, model) is a field and not another file.
type sessionDefaults struct {
	// Mode is the mode new sessions start in. Empty means code.
	Mode string `json:"mode,omitempty"`
	// AllowYoloDefault must be set separately for Mode=="yolo" to be honoured.
	//
	// Two flags for one behaviour looks redundant and is not: it makes "every session from now on
	// runs without approvals" impossible to arrive at by a single tap, a restored backup, or a
	// config file someone pasted in. The client asks for this explicitly, once, in language that
	// says what it does.
	AllowYoloDefault bool `json:"allow_yolo_default,omitempty"`
}

type defaultsStore struct {
	mu   sync.Mutex
	path string
	cur  sessionDefaults
}

// SetDefaultsPath records ~/.oculus/defaults.json and loads it.
func (h *Hub) SetDefaultsPath(path string) {
	h.defaults.mu.Lock()
	defer h.defaults.mu.Unlock()
	h.defaults.path = path
	h.defaults.cur = sessionDefaults{}
	if data, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(data, &h.defaults.cur); err != nil {
			log.Printf("defaults: %s is unreadable (%v) — starting from the safe defaults", path, err)
			h.defaults.cur = sessionDefaults{}
		}
	}
}

// sessionDefaults returns the stored defaults.
func (h *Hub) sessionDefaults() protocol.SessionDefaults {
	h.defaults.mu.Lock()
	defer h.defaults.mu.Unlock()
	return protocol.SessionDefaults{
		Mode:             h.defaults.cur.Mode,
		AllowYoloDefault: h.defaults.cur.AllowYoloDefault,
		Modes:            protocol.Modes(),
	}
}

// setSessionDefaults stores new defaults and returns what was actually stored — which is not
// necessarily what was asked for, since a yolo default without the explicit acknowledgement is
// downgraded rather than rejected. Returning the stored value means the client's toggle snaps back
// to the truth instead of showing a setting the daemon is not honouring.
func (h *Hub) setSessionDefaults(in protocol.SessionDefaults) protocol.SessionDefaults {
	mode := normalizeMode(in.Mode, false)
	if mode == protocol.ModeYolo && !in.AllowYoloDefault {
		log.Printf("defaults: refusing a yolo default without the explicit acknowledgement — storing %q", protocol.ModeCode)
		mode = protocol.ModeCode
	}
	h.defaults.mu.Lock()
	h.defaults.cur.Mode = mode
	h.defaults.cur.AllowYoloDefault = in.AllowYoloDefault
	path := h.defaults.path
	snapshot := h.defaults.cur
	h.defaults.mu.Unlock()

	if path != "" {
		if data, err := json.MarshalIndent(snapshot, "", "  "); err == nil {
			if err := os.WriteFile(path, data, 0o600); err != nil {
				log.Printf("defaults: could not save %s: %v", path, err)
			}
		}
	}
	if mode == protocol.ModeYolo {
		log.Printf("defaults: NEW SESSIONS WILL START IN YOLO — approvals are off by default")
	}
	return h.sessionDefaults()
}

// defaultMode is the mode a new session starts in when the request does not name one.
//
// A stored yolo default is only honoured when the acknowledgement is also stored. That is checked
// HERE, at the point of use, and not only when it was set: a defaults.json edited by hand, restored
// from a backup, or synced from another machine reaches this function without ever passing through
// setSessionDefaults.
func (h *Hub) defaultMode() string {
	h.defaults.mu.Lock()
	defer h.defaults.mu.Unlock()
	mode := h.defaults.cur.Mode
	if mode == protocol.ModeYolo && !h.defaults.cur.AllowYoloDefault {
		return protocol.ModeCode
	}
	if mode == "" {
		return protocol.ModeCode
	}
	return mode
}
