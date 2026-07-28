package hub_test

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/howlerops/oculus/daemon/accounts"
	"github.com/howlerops/oculus/daemon/crypto"
	"github.com/howlerops/oculus/daemon/hub"
	"github.com/howlerops/oculus/daemon/protocol"
)

// TestAccountE2E drives multi-account management through the real wire: add two accounts, switch the
// active one, and confirm account.list reflects the active flag — the credential hot-swap plumbing.
func TestAccountE2E(t *testing.T) {
	h := hub.New()
	h.Register(&cwdProvider{}) // registers as "fake"
	h.SetAccounts(accounts.Load(filepath.Join(t.TempDir(), "accounts.json")))

	daemonKP, _ := crypto.GenerateKeyPair()
	conn := connectClient(t, h, daemonKP)
	r := newReader(conn)

	// Add two accounts for provider "fake".
	send(t, conn, "a1", protocol.TypeAccountUpsert, protocol.Account{Provider: "fake", Name: "work", Env: map[string]string{"K": "work"}})
	r.waitOK(t, "a1")
	send(t, conn, "a2", protocol.TypeAccountUpsert, protocol.Account{Provider: "fake", Name: "personal", Env: map[string]string{"K": "personal"}})
	var list protocol.AccountList
	if err := json.Unmarshal(r.waitOK(t, "a2"), &list); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(list.Accounts) != 2 {
		t.Fatalf("got %d accounts, want 2", len(list.Accounts))
	}
	// The first-added account is active by default.
	var work, personal protocol.Account
	for _, a := range list.Accounts {
		switch a.Name {
		case "work":
			work = a
		case "personal":
			personal = a
		}
	}
	if !work.Active || personal.Active {
		t.Fatalf("expected 'work' active by default; work.Active=%v personal.Active=%v", work.Active, personal.Active)
	}

	// Hot-swap to personal.
	send(t, conn, "sw", protocol.TypeAccountActivate, protocol.AccountActivate{Provider: "fake", AccountID: personal.ID})
	var after protocol.AccountList
	if err := json.Unmarshal(r.waitOK(t, "sw"), &after); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, a := range after.Accounts {
		if a.Name == "personal" && !a.Active {
			t.Error("personal should be active after switch")
		}
		if a.Name == "work" && a.Active {
			t.Error("work should no longer be active")
		}
	}

	// Activating an account for the wrong provider is a clean error.
	send(t, conn, "bad", protocol.TypeAccountActivate, protocol.AccountActivate{Provider: "nope", AccountID: personal.ID})
	r.waitFor(t, "error bad", func(e protocol.Envelope) bool { return e.Type == protocol.TypeError && e.ID == "bad" })
}
