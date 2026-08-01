package push

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestSilentPushIsBackgroundNotAlert: a pre-warm push exists to wake the app so it can pull the
// transcript delta BEFORE the user opens it — the swap then paints instantly instead of waiting on a
// relay round trip. Sent as an "alert" it would buzz the user's pocket every time an agent finished a
// turn, and iOS would be within its rights to throttle it.
func TestSilentPushIsBackgroundNotAlert(t *testing.T) {
	var gotType, gotPriority string
	var payload map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotType = r.Header.Get("apns-push-type")
		gotPriority = r.Header.Get("apns-priority")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &payload)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n, err := NewAPNs(APNsConfig{
		KeyID: "K", TeamID: "T", BundleID: "com.example", BaseURL: srv.URL,
		SignJWT: func() (string, error) { return "tok", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := n.Notify(context.Background(), "devtoken", Notification{Silent: true, Custom: map[string]any{"session_id": "s1"}}); err != nil {
		t.Fatal(err)
	}

	if gotType != "background" {
		t.Errorf("apns-push-type = %q, want \"background\" — an alert would buzz the user on every turn", gotType)
	}
	if gotPriority != "5" {
		t.Errorf("apns-priority = %q, want \"5\"; APNs REJECTS a background push sent at priority 10", gotPriority)
	}
	aps, _ := payload["aps"].(map[string]any)
	if aps == nil {
		t.Fatal("no aps dictionary")
	}
	if _, hasAlert := aps["alert"]; hasAlert {
		t.Error("a silent push must carry no alert — that is what makes it silent")
	}
	if aps["content-available"] != float64(1) {
		t.Errorf("content-available = %v, want 1 — without it iOS does not wake the app", aps["content-available"])
	}
	if payload["session_id"] != "s1" {
		t.Errorf("custom keys must survive: %v", payload)
	}
}

// TestWakeRidesOnTheAlert: rather than send a second, silent push — which costs the user's background
// budget and races the first delivery — an alert can carry content-available itself. One push both
// notifies and wakes the app, so tapping the notification opens an already-painted conversation.
func TestWakeRidesOnTheAlert(t *testing.T) {
	var payload map[string]any
	var gotType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotType = r.Header.Get("apns-push-type")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &payload)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	n, _ := NewAPNs(APNsConfig{
		KeyID: "K", TeamID: "T", BundleID: "com.example", BaseURL: srv.URL,
		SignJWT: func() (string, error) { return "tok", nil },
	})
	if err := n.Notify(context.Background(), "d", Notification{Title: "Done", Body: "finished", Wake: true}); err != nil {
		t.Fatal(err)
	}
	if gotType != "alert" {
		t.Errorf("apns-push-type = %q, want alert — the user must still be told", gotType)
	}
	aps, _ := payload["aps"].(map[string]any)
	if aps["alert"] == nil {
		t.Error("the alert must survive")
	}
	if aps["content-available"] != float64(1) {
		t.Error("content-available must ride along, or the app is not woken to refresh its cache")
	}
}

// An ordinary notification must be unaffected — no wake unless asked.
func TestAlertPushStillAlerts(t *testing.T) {
	var gotType string
	var payload map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotType = r.Header.Get("apns-push-type")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &payload)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	n, _ := NewAPNs(APNsConfig{
		KeyID: "K", TeamID: "T", BundleID: "com.example", BaseURL: srv.URL,
		SignJWT: func() (string, error) { return "tok", nil },
	})
	if err := n.Notify(context.Background(), "d", Notification{Title: "Done", Body: "agent finished"}); err != nil {
		t.Fatal(err)
	}
	if gotType != "alert" {
		t.Errorf("apns-push-type = %q, want alert", gotType)
	}
	aps, _ := payload["aps"].(map[string]any)
	if aps["alert"] == nil {
		t.Error("a real notification must still carry its alert")
	}
	if _, wakes := aps["content-available"]; wakes {
		t.Error("a plain notification must not wake the app — that spends the background budget for nothing")
	}
}
