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
	"net/http"
	"strings"
	"time"
)

// Notification is a high-level push; it maps to an APNs `aps` payload plus any
// custom top-level keys (e.g. approval_id) the app reads from the notification.
type Notification struct {
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
	Client   *http.Client      // defaults to http.DefaultClient (HTTP/2 to real APNs)
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

type apnsNotifier struct{ cfg APNsConfig }

// NewAPNs returns a Notifier that sends via APNs.
func NewAPNs(cfg APNsConfig) (Notifier, error) {
	if cfg.Key == nil {
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
		cfg.Client = http.DefaultClient
	}
	return &apnsNotifier{cfg: cfg}, nil
}

func (a *apnsNotifier) Notify(ctx context.Context, deviceToken string, n Notification) error {
	aps := map[string]any{"alert": map[string]any{"title": n.Title, "body": n.Body}}
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
	req.Header.Set("apns-push-type", "alert")
	req.Header.Set("content-type", "application/json")

	resp, err := a.cfg.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("push: APNs returned %s", resp.Status)
	}
	return nil
}

// providerToken builds a signed APNs provider JWT (ES256). APNs accepts a token
// reusable for up to ~1h; a fresh one per send is simplest and well within limits.
func (a *apnsNotifier) providerToken() (string, error) {
	header, _ := json.Marshal(map[string]any{"alg": "ES256", "kid": a.cfg.KeyID})
	claims, _ := json.Marshal(map[string]any{"iss": a.cfg.TeamID, "iat": a.cfg.Now().Unix()})
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
