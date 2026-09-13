package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The updater must authenticate what it downloaded against the release's published checksums.
//
// It did not. The old path downloaded a tarball over TLS and then "verified a code signature" that
// it had applied ITSELF a few lines earlier with `codesign --sign -` — an ad-hoc signature attests
// to nothing about the publisher, so that check could only ever pass. Anything able to put bytes in
// front of the updater was accepted and swapped in as the daemon. On Linux there was no check at all.
func TestUpdaterRejectsBytesThatAreNotThePublishedRelease(t *testing.T) {
	real := []byte("the genuine release archive")
	sum := sha256.Sum256(real)
	asset := "oculusd_darwin_arm64.tar.gz"

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(hex.EncodeToString(sum[:]) + "  " + asset + "\n"))
	}))
	defer srv.Close()

	if err := verifyChecksum(context.Background(), srv.URL, asset, real); err != nil {
		t.Errorf("the genuine archive was rejected: %v", err)
	}

	tampered := []byte("the genuine release archive, plus a backdoor")
	err := verifyChecksum(context.Background(), srv.URL, asset, tampered)
	if err == nil {
		t.Fatal("a tampered archive was accepted and would have been installed as the daemon")
	}
	if !strings.Contains(err.Error(), "checksum mismatch") {
		t.Errorf("unexpected rejection reason: %v", err)
	}
}

// A release that publishes no checksums, or omits this asset, must be refused rather than trusted.
func TestUpdaterRefusesWhenThereIsNothingToVerifyAgainst(t *testing.T) {
	if err := verifyChecksum(context.Background(), "", "oculusd_darwin_arm64.tar.gz", []byte("x")); err == nil {
		t.Error("a release with no checksums.txt was treated as authenticated")
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("deadbeef  some_other_asset.tar.gz\n"))
	}))
	defer srv.Close()
	if err := verifyChecksum(context.Background(), srv.URL, "oculusd_darwin_arm64.tar.gz", []byte("x")); err == nil {
		t.Error("an asset absent from checksums.txt was treated as authenticated")
	}
}
