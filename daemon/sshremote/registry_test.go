package sshremote

import (
	"path/filepath"
	"testing"
)

func TestRegistryUpsertGetDeletePersist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "remotes.json")
	r := LoadRegistry(path)
	h := r.Upsert(Host{Name: "build-box", SSHTarget: "jacob@box", RemotePath: "/repo"})
	if h.ID == "" {
		t.Fatal("no id assigned")
	}
	if got, ok := r.Get(h.ID); !ok || got.SSHTarget != "jacob@box" {
		t.Fatalf("Get = %+v ok=%v", got, ok)
	}
	// Update in place.
	h.Name = "renamed"
	r.Upsert(h)
	if got, _ := r.Get(h.ID); got.Name != "renamed" {
		t.Fatalf("update failed: %+v", got)
	}
	// Persist.
	if r2 := LoadRegistry(path); len(r2.List()) != 1 {
		t.Fatalf("persist failed, %d hosts", len(r2.List()))
	}
	r.Delete(h.ID)
	if len(r.List()) != 0 {
		t.Fatal("delete failed")
	}
}
