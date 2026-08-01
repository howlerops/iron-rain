// Package push delivers notifications to Apple devices (APNs) — the daemon side of
// actionable lock-screen approvals. It signs a provider JWT (ES256) with a BYO
// APNs auth key (.p8) and POSTs to APNs over HTTP/2.
//
// Real delivery needs an Apple Developer APNs key + a registered device token; the
// sender + auth here are exercised against a mock APNs in push_test.go. Device-token
// registration (client -> daemon) is the remaining device-gated wiring.
//
// See ../../skills/oculus-push.
package push

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Notification is a high-level push; it maps to an APNs `aps` payload plus any
// custom top-level keys (e.g. approval_id) the app reads from the notification.
type Notification struct {
	// Silent sends a BACKGROUND push: no alert, content-available set, so iOS wakes the app to pull
	// the transcript delta before the user opens it. Sent as an alert instead, this would buzz the
	// user's pocket on every finished turn.
	Silent bool
	// Wake sets content-available ALONGSIDE an alert, so one push both notifies the user and wakes
	// the app to refresh its cache. Tapping then opens an already-painted conversation instead of
	// staring at a skeleton through a relay round trip. One push rather than two: iOS budgets
	// background wakes, and a second delivery would race the first.
	Wake     bool
	Title    string
	Body     string
	Category string         // APNs category for actionable buttons (e.g. "APPROVAL")
	ThreadID string         // groups related notifications
	Custom   map[string]any // extra top-level keys alongside "aps"
}

// Notifier delivers a notification to one device token.
type Notifier interface {
	Notify(ctx context.Context, deviceToken string, n Notification) error
}

// APNsConfig configures the APNs sender (token-based auth, BYO .p8 key).
type APNsConfig struct {
	KeyID    string            // Apple Key ID (the .p8's ID)
	TeamID   string            // Apple Developer Team ID
	BundleID string            // app bundle id — the APNs topic
	Key      *ecdsa.PrivateKey // parsed from the .p8 (see ParseP8)
	BaseURL  string            // default https://api.push.apple.com (sandbox/mock override)
	Now      func() time.Time  // for tests; defaults to time.Now
	Client   *http.Client      // defaults to a dedicated client with a 10s timeout (HTTP/2 via ALPN)
	// SignJWT overrides provider-token minting. Only for tests — it lets them exercise the request
	// shape (headers, payload) without carrying a real signing key.
	SignJWT func() (string, error)
}

// ParseP8 parses an Apple .p8 (PKCS#8 EC) auth key.
func ParseP8(pemBytes []byte) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode(pemBytes)
	if block == nil {
		return nil, errors.New("push: no PEM block in .p8")
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	ec, ok := k.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errors.New("push: .p8 is not an EC private key")
	}
	return ec, nil
}

type apnsNotifier struct {
	cfg APNsConfig

	// cached provider JWT. Apple rejects tokens regenerated more than once per
	// 20 min (403 TooManyProviderTokenUpdates), so we reuse a signed token and
	// only re-sign once it is older than jwtMaxAge.
	mu       sync.Mutex
	token    string
	issuedAt time.Time
}

// jwtMaxAge is how long a signed provider JWT is reused before re-signing.
// Apple's acceptance window is 20-60 min; ~45 min stays safely inside it.
const jwtMaxAge = 45 * time.Minute

// NewAPNs returns a Notifier that sends via APNs.
func NewAPNs(cfg APNsConfig) (Notifier, error) {
	if cfg.Key == nil && cfg.SignJWT == nil {
		return nil, errors.New("push: nil APNs key")
	}
	if cfg.KeyID == "" || cfg.TeamID == "" || cfg.BundleID == "" {
		return nil, errors.New("push: keyID, teamID and bundleID are required")
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api.push.apple.com"
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Client == nil {
		// Dedicated client (not http.DefaultClient) so a stuck HTTP/2 dial to
		// api.push.apple.com can't hang a send forever, and so push doesn't
		// share a connection pool with other DefaultClient users in-process.
		// https default transport still negotiates HTTP/2 via ALPN.
		cfg.Client = &http.Client{Timeout: 10 * time.Second}
	}
	return &apnsNotifier{cfg: cfg}, nil
}

func (a *apnsNotifier) Notify(ctx context.Context, deviceToken string, n Notification) error {
	aps := map[string]any{}
	pushType, priority := "alert", "10"
	if n.Silent {
		// APNs REJECTS a background push sent at priority 10, and ignores one with an alert body.
		aps["content-available"] = 1
		pushType, priority = "background", "5"
	} else {
		aps["alert"] = map[string]any{"title": n.Title, "body": n.Body}
		if n.Wake {
			aps["content-available"] = 1
		}
	}
	if n.Category != "" {
		aps["category"] = n.Category
	}
	if n.ThreadID != "" {
		aps["thread-id"] = n.ThreadID
	}
	payload := map[string]any{"aps": aps}
	for k, v := range n.Custom {
		payload[k] = v
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	url := strings.TrimRight(a.cfg.BaseURL, "/") + "/3/device/" + deviceToken
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	jwt, err := a.providerToken()
	if err != nil {
		return err
	}
	req.Header.Set("authorization", "bearer "+jwt)
	req.Header.Set("apns-topic", a.cfg.BundleID)
	req.Header.Set("apns-push-type", pushType)
	req.Header.Set("apns-priority", priority)
	req.Header.Set("content-type", "application/json")

	resp, err := a.cfg.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("push: APNs returned %s: %s", resp.Status, string(body))
	}
	return nil
}

// providerToken returns a signed APNs provider JWT (ES256), reusing a cached
// token until it is older than jwtMaxAge. Apple rejects tokens regenerated more
// than once per 20 min (403 TooManyProviderTokenUpdates), so caching also avoids
// an ECDSA sign + JSON marshalling on every send.
func (a *apnsNotifier) providerToken() (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.cfg.SignJWT != nil {
		return a.cfg.SignJWT()
	}
	if a.token != "" && a.cfg.Now().Sub(a.issuedAt) <= jwtMaxAge {
		return a.token, nil
	}
	now := a.cfg.Now()
	tok, err := a.signToken(now)
	if err != nil {
		return "", err
	}
	a.token = tok
	a.issuedAt = now
	return tok, nil
}

// signToken mints a fresh signed provider JWT with iat=now.
func (a *apnsNotifier) signToken(now time.Time) (string, error) {
	header, _ := json.Marshal(map[string]any{"alg": "ES256", "kid": a.cfg.KeyID})
	claims, _ := json.Marshal(map[string]any{"iss": a.cfg.TeamID, "iat": now.Unix()})
	signing := seg(header) + "." + seg(claims)

	digest := sha256.Sum256([]byte(signing))
	r, s, err := ecdsa.Sign(rand.Reader, a.cfg.Key, digest[:])
	if err != nil {
		return "", err
	}
	// ES256 signature is R||S, each left-padded to 32 bytes big-endian.
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	return signing + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

func seg(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
