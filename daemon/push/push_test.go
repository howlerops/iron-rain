package push

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestParseP8(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	p8 := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})

	got, err := ParseP8(p8)
	if err != nil {
		t.Fatal(err)
	}
	if got.D.Cmp(key.D) != 0 {
		t.Fatal("parsed key does not match original")
	}
}

func TestParseP8_RejectsNonEC(t *testing.T) {
	if _, err := ParseP8([]byte("not a pem")); err == nil {
		t.Fatal("expected error on garbage input")
	}
}

func TestAPNs_Notify(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)

	var gotPath, gotAuth, gotTopic, gotPushType string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("authorization")
		gotTopic = r.Header.Get("apns-topic")
		gotPushType = r.Header.Get("apns-push-type")
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n, err := NewAPNs(APNsConfig{
		KeyID: "KEY123", TeamID: "TEAM456", BundleID: "com.howlerops.oculus",
		Key: key, BaseURL: srv.URL, Client: srv.Client(),
		Now: func() time.Time { return time.Unix(1_700_000_000, 0) },
	})
	if err != nil {
		t.Fatal(err)
	}

	err = n.Notify(context.Background(), "devtoken123", Notification{
		Title: "Approve bash", Body: "run ls", Category: "APPROVAL",
		Custom: map[string]any{"approval_id": "a1", "session_id": "s1"},
	})
	if err != nil {
		t.Fatal(err)
	}

	if gotPath != "/3/device/devtoken123" {
		t.Errorf("path = %q", gotPath)
	}
	if gotTopic != "com.howlerops.oculus" {
		t.Errorf("apns-topic = %q", gotTopic)
	}
	if gotPushType != "alert" {
		t.Errorf("apns-push-type = %q", gotPushType)
	}
	if !strings.HasPrefix(gotAuth, "bearer ") {
		t.Fatalf("authorization = %q", gotAuth)
	}

	// aps payload shape
	aps, _ := gotBody["aps"].(map[string]any)
	alert, _ := aps["alert"].(map[string]any)
	if alert["title"] != "Approve bash" || alert["body"] != "run ls" {
		t.Errorf("alert = %+v", alert)
	}
	if aps["category"] != "APPROVAL" {
		t.Errorf("category = %v", aps["category"])
	}
	if gotBody["approval_id"] != "a1" || gotBody["session_id"] != "s1" {
		t.Errorf("custom keys missing: %+v", gotBody)
	}

	// verify the provider JWT: header kid, claims iss, and ES256 signature over the key
	jwt := strings.TrimPrefix(gotAuth, "bearer ")
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		t.Fatalf("jwt not 3 parts: %q", jwt)
	}
	hdr := decodeSeg(t, parts[0])
	if hdr["alg"] != "ES256" || hdr["kid"] != "KEY123" {
		t.Errorf("jwt header = %+v", hdr)
	}
	claims := decodeSeg(t, parts[1])
	if claims["iss"] != "TEAM456" {
		t.Errorf("jwt iss = %v", claims["iss"])
	}
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	if len(sig) != 64 {
		t.Fatalf("ES256 sig must be 64 bytes, got %d", len(sig))
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	r := new(big.Int).SetBytes(sig[:32])
	s := new(big.Int).SetBytes(sig[32:])
	if !ecdsa.Verify(&key.PublicKey, digest[:], r, s) {
		t.Fatal("jwt signature does not verify against the key")
	}
}

func TestAPNs_NonOKStatusIsError(t *testing.T) {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusGone) // 410 = bad device token
	}))
	defer srv.Close()

	n, _ := NewAPNs(APNsConfig{KeyID: "K", TeamID: "T", BundleID: "b", Key: key, BaseURL: srv.URL, Client: srv.Client()})
	if err := n.Notify(context.Background(), "tok", Notification{Title: "x"}); err == nil {
		t.Fatal("expected error on non-200 APNs response")
	}
}

func decodeSeg(t *testing.T, seg string) map[string]any {
	t.Helper()
	b, err := base64.RawURLEncoding.DecodeString(seg)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}
