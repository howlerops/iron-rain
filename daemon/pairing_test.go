package main

import (
	"encoding/json"
	"strings"
	"testing"
)

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
