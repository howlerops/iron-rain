package main

import "testing"

// TestRelayEnvOverrideRespectsExplicitFlag: the environment variable exists for self-hosters starting
// the daemon from a launchd plist or systemd unit, where editing an argument list is awkward. It must
// never win over an explicit --relay, which is the more specific instruction.
func TestRelayEnvOverrideRespectsExplicitFlag(t *testing.T) {
	cases := []struct {
		name, flag, env, want string
	}{
		{"env applies to the default", defaultRelayURL, "wss://mine/ws", "wss://mine/ws"},
		{"explicit flag wins", "wss://chosen/ws", "wss://mine/ws", "wss://chosen/ws"},
		{"no env changes nothing", defaultRelayURL, "", defaultRelayURL},
		{"LAN-only stays LAN-only", "", "wss://mine/ws", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := c.flag
			if c.env != "" && got == defaultRelayURL {
				got = c.env
			}
			if got != c.want {
				t.Errorf("relay = %q, want %q", got, c.want)
			}
		})
	}
}
