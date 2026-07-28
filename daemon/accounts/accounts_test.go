package accounts

import (
	"path/filepath"
	"testing"
)

func TestUpsertActivateEnvAndPersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "accounts.json")
	r := Load(path)

	// First account for a provider becomes active automatically.
	work := r.Upsert(Account{Provider: "codex", Name: "work", Env: map[string]string{"OPENAI_API_KEY": "work-key"}})
	if work.ID == "" {
		t.Fatal("Upsert didn't assign an ID")
	}
	if r.ActiveID("codex") != work.ID {
		t.Fatal("first account should be active by default")
	}
	if env := r.EnvFor("codex"); env["OPENAI_API_KEY"] != "work-key" {
		t.Fatalf("EnvFor(codex) = %v, want work-key", env)
	}

	// A second account does NOT steal active.
	personal := r.Upsert(Account{Provider: "codex", Name: "personal", Env: map[string]string{"OPENAI_API_KEY": "personal-key"}})
	if r.ActiveID("codex") != work.ID {
		t.Fatal("second account must not become active")
	}

	// Switch active → env follows.
	if !r.SetActive("codex", personal.ID) {
		t.Fatal("SetActive failed")
	}
	if env := r.EnvFor("codex"); env["OPENAI_API_KEY"] != "personal-key" {
		t.Fatalf("after switch, EnvFor = %v, want personal-key", env)
	}

	// SetActive rejects a mismatched provider.
	other := r.Upsert(Account{Provider: "gemini", Name: "g", Env: map[string]string{"K": "v"}})
	if r.SetActive("codex", other.ID) {
		t.Fatal("SetActive should reject an account from a different provider")
	}

	// Persistence: a fresh Load sees the same active + env.
	r2 := Load(path)
	if r2.ActiveID("codex") != personal.ID {
		t.Fatal("active selection didn't persist")
	}
	if env := r2.EnvFor("codex"); env["OPENAI_API_KEY"] != "personal-key" {
		t.Fatalf("persisted EnvFor = %v", env)
	}
}

func TestDeleteFallsBackActive(t *testing.T) {
	r := Load(filepath.Join(t.TempDir(), "a.json"))
	a := r.Upsert(Account{Provider: "codex", Name: "a"})
	b := r.Upsert(Account{Provider: "codex", Name: "b"})
	r.SetActive("codex", a.ID)

	r.Delete(a.ID) // deleting the active one falls back to the other
	if r.ActiveID("codex") != b.ID {
		t.Fatalf("after deleting active, active = %q, want %q", r.ActiveID("codex"), b.ID)
	}

	r.Delete(b.ID) // deleting the last clears active
	if r.ActiveID("codex") != "" {
		t.Fatal("active should clear when no accounts remain")
	}
}

func TestEnvForNoAccounts(t *testing.T) {
	r := Load(filepath.Join(t.TempDir(), "a.json"))
	if r.EnvFor("codex") != nil {
		t.Fatal("EnvFor with no accounts must be nil")
	}
}
