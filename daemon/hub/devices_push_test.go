package hub

import "testing"

// Revoking a device must take its notifications with it.
//
// Hub.Devices() already filters revoked entries out and the live socket is closed, so the device can
// no longer DO anything. But the push token list was anonymous — a []string with no device
// association — so revocation had nothing to match against, and the revoked phone kept receiving
// every push for every session: "Approve <tool>", "<label> finished", tests-failed, PR. Each one
// carries a session_id and an approval_id for a session the device no longer has any access to.
//
// The list was pruned only when APNs reported the token dead, which for a working phone never
// happens. Meanwhile the operator's device list showed the device revoked and the log agreed.
func TestRevokingADeviceDropsItsPushTokens(t *testing.T) {
	h := New()
	const revoked, kept = "aa11", "bb22"
	h.registerDeviceFor("tok-revoked", revoked)
	h.registerDeviceFor("tok-kept", kept)

	if n := h.dropDevicePushTokens(revoked); n != 1 {
		t.Fatalf("dropped %d tokens, want 1", n)
	}

	h.mu.Lock()
	got := append([]string(nil), h.pushTokens...)
	h.mu.Unlock()
	for _, tok := range got {
		if tok == "tok-revoked" {
			t.Fatal("the revoked device keeps its push token, so it goes on receiving every " +
				"approval and finish notification — with session ids — for sessions it cannot open")
		}
	}
	if len(got) != 1 || got[0] != "tok-kept" {
		t.Fatalf("another device's token was collateral: %v", got)
	}
}

// An anonymous registration (no device key) must not be swept by someone else's revocation.
func TestAnUnattributedPushTokenSurvivesAnotherDevicesRevocation(t *testing.T) {
	h := New()
	h.RegisterDevice("tok-anon") // legacy path: no pubkey recorded
	if n := h.dropDevicePushTokens("aa11"); n != 0 {
		t.Fatalf("dropped %d tokens for a device that registered none", n)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.pushTokens) != 1 {
		t.Fatalf("an unattributed token was dropped by an unrelated revocation: %v", h.pushTokens)
	}
}
