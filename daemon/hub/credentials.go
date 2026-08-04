package hub

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// The pairing credential lifecycle.
//
// What this replaces, and why it had to change. Pairing used to be ONE permanent string. It was
// printed to stdout on every daemon start, written into a `oculus://pair?…&secret=…` URL, rendered
// as a QR, stored plaintext in the client's UserDefaults, and never expired. Presenting it yields
// RoleOwner, and an owner-authenticated connection reaches arbitrary shell on the user's Mac. So a
// single screenshot, a synced clipboard, an unencrypted device backup, or a line of CI scrollback
// was a permanent shell on the machine — and there was no way to take it back, because "revoke
// device" keyed on a client public key the app regenerated on every launch.
//
// The model here separates the credential that ONBOARDS a device from the credential a device
// KEEPS, so the fragile one can be short-lived:
//
//   pairing code    single-use, minutes-long. This is the only thing that ever travels in a URL, a
//                   QR, or a terminal. Spending it enrolls exactly one device; a screenshot of it is
//                   worthless once used or once it lapses.
//   device credential  256 bits, minted at enrollment, bound to the enrolling device's static public
//                   key, stored only as a SHA-256 hash here. It is what the device presents from then
//                   on. Because it is bound to the pubkey, stealing the string alone is not enough:
//                   the thief must also hold the X25519 private key that opens the channel, which
//                   lives in the Keychain with ThisDeviceOnly. Revoking the device kills it.
//   local bootstrap code  the credential in ~/.oculus/pairing.json, for the app running on THIS Mac.
//                   Rotated on every daemon start. Deliberately NOT single-use — see localCode below.
//   legacy secret   the pre-upgrade permanent secret, accepted only long enough to migrate the
//                   devices that still hold it, then retired. See the migration notes on legacy*.
//
// All four arrive in the same handshake slot (a string), so none of this needs a wire-format change.
// The daemon simply knows more than one kind of credential now, and answers "which device is this?"
// from the one it matched.

const (
	// defaultPairCodeTTL is how long a freshly minted pairing code stays redeemable. Minutes, not
	// hours: the code exists only to bridge the gap between "the owner opened the pair sheet" and
	// "the phone scanned it", and every second past that is a screenshot someone can still use.
	defaultPairCodeTTL = 10 * time.Minute

	// legacySecretGrace is how long the old permanent secret keeps working after the FIRST device
	// successfully migrates to a per-device credential.
	//
	// Zero grace would be safer and would lock out the second device you own until you re-paired it,
	// on a build you may not have updated yet. A week is long enough to cover an App Store update
	// landing on a phone, and short enough that a secret already sitting in someone's screenshot
	// library stops being a key to the machine.
	legacySecretGrace = 7 * 24 * time.Hour
)

// credState is the small amount of credential bookkeeping that has to outlive a daemon restart.
//
// Pairing codes are NOT in here on purpose: a code is a minutes-long credential, and persisting one
// across a restart would silently extend it past the expiry the owner was shown. The same reasoning
// the invite registry uses (invites.go).
type credState struct {
	// LegacyRetireAt is when the pre-upgrade permanent secret stops being accepted. Zero means no
	// device has migrated yet, so the clock hasn't started.
	LegacyRetireAt int64 `json:"legacy_retire_at,omitempty"`
	// LegacyRetired records that it is already dead, so a restart can't resurrect it.
	LegacyRetired bool `json:"legacy_retired,omitempty"`
}

// pairCode is one outstanding single-use enrollment credential.
type pairCode struct {
	code      string
	expiresAt time.Time
}

// credentials owns every credential the daemon will accept other than invites.
type credentials struct {
	mu   sync.Mutex
	path string

	// codes are outstanding single-use pairing codes, keyed by the code itself.
	codes map[string]*pairCode

	// localCode is the credential written into ~/.oculus/pairing.json for the app on this same Mac.
	//
	// It is the one credential here that is not single-use, and that is deliberate. pairing.json is
	// mode 0600 in the user's own home directory; anyone who can read it can also read ~/.oculus/key,
	// which is game over by construction and explicitly out of the threat model. Meanwhile a
	// single-use local code would break the zero-config local app the moment it reconnected — it
	// would have spent its only credential on the first connect. So: long-lived within a daemon run,
	// rotated on every start, and never rendered into a QR or a URL. The QR gets a real pairing code.
	localCode string

	// legacySecret is the permanent pre-upgrade owner secret (~/.oculus/secret), kept only to migrate
	// existing pairings. configured is set when the operator passed --secret explicitly, which means
	// it is a deliberate, scripted credential and must never be auto-retired out from under them.
	legacySecret string
	configured   bool
	state        credState

	// pending holds device credentials that have been minted but not yet handed to the device, keyed
	// by client pubkey hex. The handshake has no room for one (and must not grow a field), so the
	// credential is delivered as the first protocol frame after the connection is up; this is where
	// it waits in between. See Hub.Serve.
	pending map[string]string

	// onLocalRotate rewrites ~/.oculus/pairing.json when the local bootstrap code changes.
	onLocalRotate func(code string)
}

func newCredentials() *credentials {
	return &credentials{codes: map[string]*pairCode{}, pending: map[string]string{}}
}

// creds returns the hub's credential store, creating it on first use so a Hub built by a test that
// never calls SetCredentialsPath still authenticates normally (in memory).
func (h *Hub) creds() *credentials {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.credentials == nil {
		h.credentials = newCredentials()
	}
	return h.credentials
}

// SetCredentialsPath points the credential store at its file and loads what's there. Without a path
// the retirement clock still runs, it just doesn't outlive the process.
func (h *Hub) SetCredentialsPath(path string) {
	c := h.creds()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.path = path
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var st credState
	if json.Unmarshal(data, &st) == nil {
		c.state = st
	}
}

// SetLocalPairingRotator installs the callback that rewrites ~/.oculus/pairing.json whenever the
// local bootstrap code is rotated, and rotates it once immediately. Called by main after it knows
// the reachable addresses.
func (h *Hub) SetLocalPairingRotator(f func(code string)) {
	c := h.creds()
	c.mu.Lock()
	c.onLocalRotate = f
	c.localCode = "lc_" + randomSecret(24)
	code, cb := c.localCode, c.onLocalRotate
	c.mu.Unlock()
	if cb != nil {
		cb(code)
	}
}

// LocalPairingCode is the credential the app on this Mac should present. "" until the rotator is
// installed.
func (h *Hub) LocalPairingCode() string {
	c := h.creds()
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.localCode
}

// MintPairCode issues a fresh single-use pairing code. This is the owner's re-pair path: adding a
// second phone, or recovering one that was wiped, is "open the pair sheet, scan, done" — no
// permanent secret has to exist for that to work.
func (h *Hub) MintPairCode(ttl time.Duration) (string, time.Time) {
	if ttl <= 0 || ttl > time.Hour {
		ttl = defaultPairCodeTTL // an hour is already generous for a credential in a photo roll
	}
	c := h.creds()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pruneCodesLocked()
	pc := &pairCode{code: "pc_" + randomSecret(16), expiresAt: time.Now().Add(ttl)}
	c.codes[pc.code] = pc
	return pc.code, pc.expiresAt
}

// redeemPairCode spends a pairing code. It is removed on success, which is what makes it single-use:
// a second presentation of the same code — the screenshot, the second scan, the replayed frame —
// finds nothing and is refused.
func (c *credentials) redeemPairCode(presented string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pruneCodesLocked()
	pc, ok := c.codes[presented]
	if !ok {
		return false
	}
	if time.Now().After(pc.expiresAt) {
		delete(c.codes, presented)
		return false
	}
	delete(c.codes, presented)
	return true
}

// isLocalCode reports whether this is the same-Mac bootstrap credential.
func (c *credentials) isLocalCode(presented string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.localCode != "" && subtle.ConstantTimeCompare([]byte(c.localCode), []byte(presented)) == 1
}

func (c *credentials) pruneCodesLocked() {
	now := time.Now()
	for code, pc := range c.codes {
		if now.After(pc.expiresAt) {
			delete(c.codes, code)
		}
	}
}

// pairCodeCount reports how many codes are outstanding (tests + diagnostics).
func (c *credentials) pairCodeCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pruneCodesLocked()
	return len(c.codes)
}

// adoptLegacySecret records the pre-upgrade permanent secret. configured=true means the operator
// passed --secret on purpose; that one is never auto-retired, because retiring a credential someone
// deliberately configured would break their scripted setup on the next restart.
func (c *credentials) adoptLegacySecret(secret string, configured bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.legacySecret = secret
	c.configured = configured
}

// acceptLegacy reports whether the legacy permanent secret is still a credential.
func (c *credentials) acceptLegacy(presented string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.legacySecret == "" || subtle.ConstantTimeCompare([]byte(c.legacySecret), []byte(presented)) != 1 {
		return false
	}
	if c.configured {
		return true // explicitly configured with --secret: the operator owns this decision
	}
	if c.state.LegacyRetired {
		return false
	}
	if c.state.LegacyRetireAt != 0 && time.Now().Unix() > c.state.LegacyRetireAt {
		c.state.LegacyRetired = true
		c.saveLocked()
		log.Printf("pairing: the old permanent pairing secret has expired; pair devices with a fresh code")
		return false
	}
	return true
}

// noteMigrated starts the retirement clock the first time a device confirms it holds a per-device
// credential. The clock starts on CONFIRMATION rather than on mint so a delivery that never landed
// (client crashed mid-frame, old build that ignores the message) can't strand the user with a
// retired secret and no replacement.
func (c *credentials) noteMigrated() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.configured || c.state.LegacyRetired || c.state.LegacyRetireAt != 0 || c.legacySecret == "" {
		return
	}
	c.state.LegacyRetireAt = time.Now().Add(legacySecretGrace).Unix()
	c.saveLocked()
	log.Printf("pairing: a device migrated to its own credential; the old permanent secret stops working %s",
		time.Unix(c.state.LegacyRetireAt, 0).Format(time.RFC3339))
}

// RetireLegacySecret kills the old permanent secret now, without waiting out the grace period. The
// owner reaches this after they've re-paired everything they own and want the leaked-screenshot
// window closed today rather than next week.
func (h *Hub) RetireLegacySecret() {
	c := h.creds()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.configured {
		log.Printf("pairing: --secret was set explicitly; leaving it in place")
		return
	}
	c.state.LegacyRetired = true
	c.state.LegacyRetireAt = 0
	c.saveLocked()
	log.Printf("pairing: the old permanent pairing secret has been retired")
}

// LegacySecretStatus reports whether the old permanent secret still works, and when it stops.
func (h *Hub) LegacySecretStatus() (live bool, retireAt int64) {
	c := h.creds()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.legacySecret == "" || c.state.LegacyRetired {
		return false, 0
	}
	return true, c.state.LegacyRetireAt
}

// stashPending parks a freshly minted device credential until the connection is up to receive it.
func (c *credentials) stashPending(pubHex, cred string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pending[pubHex] = cred
}

// takePending removes and returns a credential awaiting delivery.
func (c *credentials) takePending(pubHex string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cred, ok := c.pending[pubHex]
	delete(c.pending, pubHex)
	return cred, ok
}

func (c *credentials) saveLocked() {
	if c.path == "" {
		return
	}
	data, err := json.Marshal(c.state)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o700); err != nil {
		return
	}
	tmp := c.path + ".tmp"
	if os.WriteFile(tmp, data, 0o600) != nil {
		return
	}
	_ = os.Rename(tmp, c.path)
}

// StartCredentialSweep expires lapsed pairing codes and retires the legacy secret on time, without
// needing a connection to come in first. Call once at startup, alongside StartHeartbeat.
//
// Expiry that only happens when someone tries to use the credential is expiry the owner can't see:
// the pair sheet would keep showing a code that is already dead, and the "your old secret stops
// working on <date>" promise would only come true if a device happened to connect that day.
func (h *Hub) StartCredentialSweep(ctx context.Context) {
	go func() {
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				h.credentialSweep()
			}
		}
	}()
}

func (h *Hub) credentialSweep() {
	c := h.creds()
	c.mu.Lock()
	c.pruneCodesLocked()
	retire := !c.configured && !c.state.LegacyRetired && c.state.LegacyRetireAt != 0 &&
		time.Now().Unix() > c.state.LegacyRetireAt
	if retire {
		c.state.LegacyRetired = true
		c.saveLocked()
	}
	c.mu.Unlock()
	if retire {
		log.Printf("pairing: the old permanent pairing secret has expired; pair devices with a fresh code")
	}
	// An invite that lapsed mid-session must not leave the guest connected: their access was granted
	// until a time that has now passed, and a socket that outlives it is exactly the hole the owner
	// thought the expiry closed.
	for _, pub := range h.invites.sweepExpired() {
		h.closeDeviceConns(pub, "invite expired")
	}
}

// randomSecret returns n bytes of cryptographic randomness, hex encoded.
//
// Deliberately not randToken(): that is 4 bytes, which is fine for a session id and nowhere near
// enough for something that authorizes shell access.
func randomSecret(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failing is not survivable for a credential — better to produce nothing usable
		// than a predictable string that authorizes the user's machine.
		return ""
	}
	return hex.EncodeToString(b)
}

// credHash is what the device registry stores instead of the credential itself, so a readable
// devices.json is a list of who may connect rather than a set of keys to the machine.
func credHash(cred string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(cred)))
	return hex.EncodeToString(sum[:])
}
