package hub_test

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/howlerops/oculus/daemon/accounts"
	"github.com/howlerops/oculus/daemon/agent"
	"github.com/howlerops/oculus/daemon/hub"
	"github.com/howlerops/oculus/daemon/protocol"
)

// envAwareProvider is the shape of a CLI-agent provider as far as the account wiring is concerned:
// it accepts a resolver and calls it at spawn time. The real cli.Provider's resolver is unexported,
// so this stands in for it — what is under test is the hub's obligation to install one, which is
// exactly what was missing.
type envAwareProvider struct {
	name string
	mu   sync.Mutex
	f    func() map[string]string
}

func (p *envAwareProvider) Name() string                                     { return p.name }
func (p *envAwareProvider) List(context.Context) ([]protocol.Session, error) { return nil, nil }
func (p *envAwareProvider) Create(context.Context, string, string) (agent.Session, error) {
	return nil, context.Canceled
}
func (p *envAwareProvider) SetAccountEnv(f func() map[string]string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.f = f
}

// resolve is what a spawn would do: ask for the ACTIVE account's env right now.
func (p *envAwareProvider) resolve() map[string]string {
	p.mu.Lock()
	f := p.f
	p.mu.Unlock()
	if f == nil {
		return nil
	}
	return f()
}

// A provider registered AFTER SetAccounts must still resolve the active account's env.
//
// SetAccounts used to wire a one-time snapshot of the providers registered at start-up, and nothing
// re-applied it. Both `agent.upsert` (saving a custom CLI agent) and `provider.refresh` (Re-scan)
// go through Register with a fresh provider object, which therefore had no resolver at all: new
// sessions spawned with the ambient environment — the wrong API key, the wrong config dir — while
// the Accounts screen still showed the account active, because the registry itself was untouched.
func TestAccountEnvSurvivesReRegistration(t *testing.T) {
	h := hub.New()

	reg := accounts.Load(filepath.Join(t.TempDir(), "accounts.json"))
	reg.Upsert(accounts.Account{Provider: "faker", Name: "work", Env: map[string]string{"API_KEY": "from-the-account"}})

	// The start-up ordering: providers first, then SetAccounts.
	atBoot := &envAwareProvider{name: "faker"}
	h.Register(atBoot)
	h.SetAccounts(reg)
	if got := atBoot.resolve()["API_KEY"]; got != "from-the-account" {
		t.Fatalf("the provider present at SetAccounts resolved API_KEY=%q; this test proves nothing "+
			"about re-registration until the base case works", got)
	}

	// Now the case that broke: Re-scan / agent.upsert replaces the provider by name.
	replaced := &envAwareProvider{name: "faker"}
	h.Register(replaced)
	if got := replaced.resolve()["API_KEY"]; got != "from-the-account" {
		t.Errorf("a provider registered after SetAccounts resolved API_KEY=%q, want %q.\n\n"+
			"Re-scan and saving a custom agent both re-Register by name, and the wiring was applied "+
			"once to a snapshot taken at start-up. Sessions spawned from the replacement run with the "+
			"ambient environment while the Accounts screen still reports the account active.",
			got, "from-the-account")
	}
}

// Switching the active account must reach a provider registered at any time, because the resolver is
// called per spawn rather than captured — a provider wired with a snapshot of the env would keep
// serving the old key.
func TestAccountEnvFollowsTheActiveSwitch(t *testing.T) {
	h := hub.New()
	reg := accounts.Load(filepath.Join(t.TempDir(), "accounts.json"))
	work := reg.Upsert(accounts.Account{Provider: "faker", Name: "work", Env: map[string]string{"API_KEY": "work"}})
	personal := reg.Upsert(accounts.Account{Provider: "faker", Name: "personal", Env: map[string]string{"API_KEY": "personal"}})

	h.SetAccounts(reg)
	p := &envAwareProvider{name: "faker"}
	h.Register(p)

	if got := p.resolve()["API_KEY"]; got != "work" {
		t.Fatalf("first account should be active by default, resolved %q", got)
	}
	if !reg.SetActive("faker", personal.ID) {
		t.Fatal("SetActive refused the personal account")
	}
	if got := p.resolve()["API_KEY"]; got != "personal" {
		t.Errorf("after switching the active account the provider still resolves %q", got)
	}
	if !reg.SetActive("faker", work.ID) {
		t.Fatal("SetActive refused the work account")
	}
	if got := p.resolve()["API_KEY"]; got != "work" {
		t.Errorf("after switching back the provider resolves %q", got)
	}
}
