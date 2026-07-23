// Package telemetry ships anonymized diagnostic events to a Cloudflare Worker so failures in the
// wild (e.g. a session-create hang) can be traced without asking the user to fish logs out by hand.
//
// Privacy contract — this package sends ONLY:
//   - a random install id (generated locally, not tied to the user or machine identity),
//   - the daemon version, OS, and arch,
//   - per-event: an event name, the agent provider, a duration, an ok flag, and a SCRUBBED error
//     class (home dir + absolute paths redacted, truncated).
//
// It NEVER sends file paths, repo/branch names, prompts, tokens, or message content. Enabled by
// default with a persisted toggle (SetEnabled); a disabled client drops every Record cheaply.
package telemetry

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
)

// DefaultEndpoint is the telemetry Worker's ingest URL (see cloudflare/telemetry-worker).
const DefaultEndpoint = "https://oculus-telemetry.jacobbeck-dev.workers.dev/ingest"

const (
	flushInterval = 20 * time.Second
	maxBuffer     = 200 // hard cap so a long offline stretch can't grow memory unbounded
	batchMax      = 50
)

// Event is one anonymized record. See the package doc for the privacy contract.
type Event struct {
	Event      string `json:"event"`
	Provider   string `json:"provider,omitempty"`
	DurationMS int64  `json:"dur_ms,omitempty"`
	Error      string `json:"error,omitempty"` // scrubbed class, "" on success
	OK         bool   `json:"ok"`
	TS         int64  `json:"ts"`
}

// state is the persisted toggle + install id (~/.oculus/telemetry.json).
type state struct {
	Enabled   *bool  `json:"enabled,omitempty"` // pointer so a missing file defaults to on
	InstallID string `json:"install_id"`
}

// Client batches events and POSTs them to the endpoint. Safe for concurrent use; all methods are
// no-ops-cheap when disabled.
type Client struct {
	mu        sync.Mutex
	enabled   bool
	endpoint  string
	installID string
	version   string
	statePath string
	buf       []Event
	http      *http.Client
}

// New loads (or initializes) telemetry state from statePath and returns a client. Missing/unreadable
// state defaults to ENABLED with a freshly minted install id. version is stamped on every batch.
func New(statePath, version string) *Client {
	c := &Client{
		enabled:   true,
		endpoint:  DefaultEndpoint,
		version:   version,
		statePath: statePath,
		http:      &http.Client{Timeout: 8 * time.Second},
	}
	st := loadState(statePath)
	if st.Enabled != nil {
		c.enabled = *st.Enabled
	}
	c.installID = st.InstallID
	if c.installID == "" {
		c.installID = randomID()
		c.persist() // stamp the install id (+ default enabled) on first run
	}
	return c
}

// Enabled reports whether events are being sent.
func (c *Client) Enabled() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.enabled
}

// SetEnabled flips telemetry on/off and persists the choice.
func (c *Client) SetEnabled(on bool) {
	c.mu.Lock()
	c.enabled = on
	if !on {
		c.buf = nil // drop anything queued the moment the user opts out
	}
	c.mu.Unlock()
	c.persist()
}

// Record queues an anonymized event. Cheap no-op when disabled. err is scrubbed to a class; pass nil
// for success.
func (c *Client) Record(event, provider string, dur time.Duration, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.enabled {
		return
	}
	e := Event{Event: event, Provider: provider, OK: err == nil, TS: time.Now().Unix()}
	if dur > 0 {
		e.DurationMS = dur.Milliseconds()
	}
	if err != nil {
		e.Error = scrub(err.Error())
	}
	c.buf = append(c.buf, e)
	if len(c.buf) > maxBuffer {
		c.buf = c.buf[len(c.buf)-maxBuffer:] // keep the most recent
	}
}

// Run flushes on a ticker until ctx is done. Call once in a goroutine.
func (c *Client) Run(ctx context.Context) {
	t := time.NewTicker(flushInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			c.flush(context.Background())
			return
		case <-t.C:
			c.flush(ctx)
		}
	}
}

func (c *Client) flush(ctx context.Context) {
	c.mu.Lock()
	if !c.enabled || len(c.buf) == 0 {
		c.mu.Unlock()
		return
	}
	batch := c.buf
	if len(batch) > batchMax {
		batch = batch[:batchMax]
	}
	sending := append([]Event(nil), batch...)
	c.buf = c.buf[len(sending):]
	endpoint, installID, version := c.endpoint, c.installID, c.version
	c.mu.Unlock()

	body, err := json.Marshal(map[string]any{
		"install_id": installID,
		"version":    version,
		"os":         runtime.GOOS,
		"arch":       runtime.GOARCH,
		"events":     sending,
	})
	if err != nil {
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		// Re-queue on network failure so a transient outage doesn't lose the trace.
		c.mu.Lock()
		if c.enabled {
			c.buf = append(sending, c.buf...)
			if len(c.buf) > maxBuffer {
				c.buf = c.buf[len(c.buf)-maxBuffer:]
			}
		}
		c.mu.Unlock()
		return
	}
	_ = resp.Body.Close()
}

func (c *Client) persist() {
	c.mu.Lock()
	st := state{Enabled: &c.enabled, InstallID: c.installID}
	path := c.statePath
	c.mu.Unlock()
	if path == "" {
		return
	}
	if dir := filepath.Dir(path); dir != "" {
		_ = os.MkdirAll(dir, 0o700)
	}
	if data, err := json.MarshalIndent(st, "", "  "); err == nil {
		_ = os.WriteFile(path, data, 0o600)
	}
}

func loadState(path string) state {
	var st state
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &st)
	}
	return st
}

func randomID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "unknown"
	}
	return hex.EncodeToString(b)
}

var (
	homeDir     = func() string { h, _ := os.UserHomeDir(); return h }()
	absPathRe   = regexp.MustCompile(`(/[^\s"']+)+`)
	whitespaceR = regexp.MustCompile(`\s+`)
)

// scrub reduces an error message to a shareable class: redact the home dir and any absolute path,
// collapse whitespace, and truncate — so no repo/user/path detail leaks while the failure shape
// (e.g. "git worktree add timed out …", "connection refused") is preserved.
func scrub(msg string) string {
	if homeDir != "" {
		msg = strings.ReplaceAll(msg, homeDir, "~")
	}
	msg = absPathRe.ReplaceAllString(msg, "[path]")
	msg = whitespaceR.ReplaceAllString(msg, " ")
	msg = strings.TrimSpace(msg)
	if len(msg) > 200 {
		msg = msg[:200] + "…"
	}
	return msg
}
