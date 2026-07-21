package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The pairing secret must be STABLE across daemon restarts (persisted), or an already-paired
// phone/app gets "unauthorized" after every restart or reinstall.
func TestLoadOrCreateSecret_StableAcrossRestarts(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "secret") // includes a missing dir to create
	s1 := loadOrCreateSecret(p)
	if len(s1) < 16 {
		t.Fatalf("secret too short: %q", s1)
	}
	// Written with 0600 (it's a credential).
	if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("secret file mode = %v (err %v), want 0600", fi.Mode().Perm(), err)
	}
	if s2 := loadOrCreateSecret(p); s2 != s1 {
		t.Errorf("secret changed across calls: %q != %q", s1, s2)
	}
	// A blank/whitespace file regenerates rather than returning empty.
	_ = os.WriteFile(p, []byte("  \n"), 0o600)
	if s3 := loadOrCreateSecret(p); strings.TrimSpace(s3) == "" {
		t.Errorf("blank secret file should regenerate, got empty")
	}
}

func TestBuildPairURL_IncludesName(t *testing.T) {
	u := buildPairURL("wss://x.example/ws", "abcd", "s3cret", "Jacob's MBP")
	for _, want := range []string{"oculus://pair?", "ws=wss", "pub=abcd", "secret=s3cret", "name=Jacob"} {
		if !strings.Contains(u, want) {
			t.Errorf("pair URL %q missing %q", u, want)
		}
	}
	// The name must be URL-escaped (space -> %20, apostrophe encoded).
	if strings.Contains(u, "Jacob's MBP") {
		t.Errorf("name not URL-escaped in %q", u)
	}
	// No name -> no name param.
	if strings.Contains(buildPairURL("ws://x/ws", "p", "s", ""), "name=") {
		t.Error("empty name should not add a name param")
	}
}

func TestPairingJSON_IncludesName(t *testing.T) {
	var m map[string]string
	if err := json.Unmarshal(pairingJSON("ws://local/ws", "wss://pub/ws", "pub", "sec", "Studio"), &m); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]string{"ws": "ws://local/ws", "public": "wss://pub/ws", "pub": "pub", "secret": "sec", "name": "Studio"} {
		if m[k] != want {
			t.Errorf("pairing.json[%q] = %q, want %q", k, m[k], want)
		}
	}
}
