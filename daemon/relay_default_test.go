package main

import (
	"net/url"
	"strings"
	"testing"
)

// The shipped relay list is the one string that can strand every daemon running on defaults, so it
// gets the checks a config file would get if anyone reviewed it.
//
// These are deliberately shape checks, not reachability checks: CI has no business depending on a
// third-party host being up, and a test that did would fail for reasons that are not this repo's.
// What they catch is the class of mistake that has actually happened to config constants here — a
// stray comma, a scheme that silently disables TLS, a path typo — each of which produces a daemon
// that looks configured and is not.
func TestTheDefaultRelayListIsWellFormed(t *testing.T) {
	relays := splitRelays(defaultRelayURL)
	if len(relays) == 0 {
		t.Fatal("the default relay list is empty: every daemon on defaults is LAN-only, and the " +
			"remote access this product is sold on silently does not exist")
	}
	for _, raw := range relays {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatalf("relay %q does not parse: %v", raw, err)
		}
		if u.Scheme != "wss" {
			t.Errorf("relay %q uses scheme %q. Only wss is acceptable: the payload is already "+
				"end-to-end encrypted, but ws exposes the metadata the relay can see (that this "+
				"daemon exists, and when it is busy) to anyone on the path.", raw, u.Scheme)
		}
		if u.Host == "" {
			t.Errorf("relay %q has no host", raw)
		}
		if u.Path != "/ws" {
			t.Errorf("relay %q has path %q, want /ws — both relay implementations serve the socket "+
				"there, and a typo here fails at connect time on the user's machine rather than here", raw, u.Path)
		}
	}
}

// A hostname may appear only once.
//
// Duplicates are not harmless: the daemon opens a host registration per entry, so a repeated URL
// means two sockets to the same relay claiming the same server_id — and the second claim supersedes
// the first, which is the relay's eviction path. The daemon would be quietly evicting itself.
func TestTheDefaultRelayListHasNoDuplicates(t *testing.T) {
	seen := map[string]bool{}
	for _, raw := range splitRelays(defaultRelayURL) {
		if seen[raw] {
			t.Fatalf("relay %q appears twice: the daemon opens one host registration per entry, so "+
				"the second claim supersedes the first and the daemon evicts itself", raw)
		}
		seen[raw] = true
	}
}

// Fly is gone, and must not drift back in without the two things that would make it real.
//
// This is a decision, not a bug, so it is pinned where someone re-adding the URL will read why: a
// second hosted relay needs its deploy config committed and a conformance suite both implementations
// run, or it is a monthly bill for an untested path that cannot be rebuilt. See the long comment on
// defaultRelayURL. Delete this test WITH those two things, not instead of them.
func TestNoRelayIsShippedWithoutADeployConfigInThisRepo(t *testing.T) {
	if strings.Contains(defaultRelayURL, "fly.dev") {
		t.Fatal("a Fly relay is back in the shipped default. If that is deliberate, it needs " +
			"fly.toml committed (the old deployment existed only in Fly's control plane and could " +
			"not be rebuilt from a checkout) and a conformance suite both relay implementations run " +
			"in CI (they are tested separately today, and have already drifted once). Without both, " +
			"this is billing for a fallback nobody can verify or restore.")
	}
}

// splitRelays is the only parser between a user's env var and the addresses a daemon dials.
func TestSplitRelaysHandlesRealInput(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{"single", "wss://a/ws", []string{"wss://a/ws"}},
		{"two", "wss://a/ws,wss://b/ws", []string{"wss://a/ws", "wss://b/ws"}},
		{"spaces around the comma", "wss://a/ws , wss://b/ws", []string{"wss://a/ws", "wss://b/ws"}},
		{"trailing comma", "wss://a/ws,", []string{"wss://a/ws"}},
		{"empty means LAN-only", "", nil},
		{"only commas", ",,,", nil},
		{"only whitespace", "   ", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := splitRelays(c.in)
			if len(got) != len(c.want) {
				t.Fatalf("splitRelays(%q) = %v, want %v", c.in, got, c.want)
			}
			for i := range c.want {
				if got[i] != c.want[i] {
					t.Fatalf("splitRelays(%q)[%d] = %q, want %q. An entry that survives with "+
						"whitespace attached is dialled verbatim and fails to resolve.",
						c.in, i, got[i], c.want[i])
				}
			}
		})
	}
}
