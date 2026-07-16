package push_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/push"
)

// TestLive_RealAPNs sends one push to APNs (sandbox) to diagnose provider auth.
// A 403 is returned on the provider JWT alone (before the device token is checked),
// so a dummy token still surfaces InvalidProviderToken / TopicDisallowed. Opt-in:
//
//	OCULUS_APNS_KEY=~/.oculus/AuthKey.p8 OCULUS_APNS_KEY_ID=... OCULUS_APNS_TEAM_ID=... \
//	  go test ./push/ -run TestLive_RealAPNs -v
func TestLive_RealAPNs(t *testing.T) {
	keyPath := os.Getenv("OCULUS_APNS_KEY")
	if keyPath == "" {
		t.Skip("set OCULUS_APNS_KEY/OCULUS_APNS_KEY_ID/OCULUS_APNS_TEAM_ID to run")
	}
	pem, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatal(err)
	}
	key, err := push.ParseP8(pem)
	if err != nil {
		t.Fatal(err)
	}
	n, err := push.NewAPNs(push.APNsConfig{
		KeyID:    os.Getenv("OCULUS_APNS_KEY_ID"),
		TeamID:   os.Getenv("OCULUS_APNS_TEAM_ID"),
		BundleID: "com.howlerops.oculus",
		Key:      key,
		BaseURL:  "https://api.sandbox.push.apple.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	dummy := "0000000000000000000000000000000000000000000000000000000000000000"
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	err = n.Notify(ctx, dummy, push.Notification{Title: "diag", Body: "diag"})
	t.Logf("APNs sandbox response for dummy token: %v", err)
	// BadDeviceToken => provider auth OK (JWT/team/key valid). 403 => provider auth wrong.
}
