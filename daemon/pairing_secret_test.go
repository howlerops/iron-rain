package main

import (
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

// A live pairing secret must not be written to a non-terminal stdout.
//
// Under launchd the plist sends StandardOutPath to ~/.oculus/oculusd.log (LoginItemManager), so the
// pasteable pair URL — which carries `secret=` — was written into a file that is never rotated, on
// every daemon start. Until the log stream was gated to the owner, any connected guest could read it
// back. The code is single-use and expires in ten minutes, which bounds the exposure, but a live
// secret still should not be the thing sitting in a log file.
//
// The camera-less paste flow is preserved for a real terminal; this asserts only the redirected case,
// where nobody is watching stdout and the QR above it is unreadable anyway.
func TestPairingSecretIsWithheldFromRedirectedStdout(t *testing.T) {
	r, w, err := os.Pipe() // a pipe is not a terminal — the same as a log file
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdout
	os.Stdout = w
	printPairing("ws://127.0.0.1:6000/ws", "abcdef123456", "pc_SUPERSECRETCODE", "mac", "", time.Now().Add(10*time.Minute))
	os.Stdout = orig
	_ = w.Close()
	out, _ := io.ReadAll(r)
	got := string(out)

	if strings.Contains(got, "pc_SUPERSECRETCODE") {
		t.Error("the pairing secret was written to a redirected stdout — under launchd that is the log file")
	}
	if strings.Contains(got, "secret=") {
		t.Error("a secret= parameter reached a redirected stdout")
	}
	// The user must still be told what to do instead.
	if !strings.Contains(got, "Pair a phone") {
		t.Error("withholding the link must still point the user at the Mac app")
	}
}

// The daemon log must be bounded. Nothing bounded it: launchd opens it O_APPEND and writes forever,
// so on a machine running agents daily it quietly became the largest file in ~/.oculus — and, until
// the log stream was gated to the owner, one a guest could read back in full.
//
// Truncation rather than rename is the point: launchd holds the descriptor, so a renamed file keeps
// receiving every subsequent write while the "current" log stays empty. This asserts the file is
// rolled in place and that the recent tail survives.
func TestDaemonLogIsRolledInPlaceWhenItGrows(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/oculusd.log"

	// Write past the cap, ending with a line we expect to survive.
	big := make([]byte, maxLogBytes+(2<<20))
	for i := range big {
		big[i] = 'x'
	}
	marker := []byte("\nTHE-LAST-THING-THAT-HAPPENED\n")
	if err := os.WriteFile(path, append(big, marker...), 0o600); err != nil {
		t.Fatal(err)
	}

	// Hold it open in APPEND mode, the way launchd does, for the whole operation.
	held, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()

	rollLogIfLarge(path)

	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Size() > maxLogBytes {
		t.Errorf("the log is still %d bytes — it was not rolled", fi.Size())
	}
	// The holder's next write must land in the CURRENT file, which is what rename would have broken.
	if _, err := held.WriteString("after-roll\n"); err != nil {
		t.Fatalf("the launchd-style holder could not write after the roll: %v", err)
	}
	cur, _ := os.ReadFile(path)
	if !strings.Contains(string(cur), "after-roll") {
		t.Error("writes from the process holding the file no longer reach it — the roll broke logging")
	}
	// And the recent history that makes the file worth reading survived.
	prev, err := os.ReadFile(path + ".1")
	if err != nil {
		t.Fatalf("no tail was preserved: %v", err)
	}
	if !strings.Contains(string(prev), "THE-LAST-THING-THAT-HAPPENED") {
		t.Error("the tail kept aside does not contain the most recent lines")
	}
}

// APNs must be configurable from a file, not only from flags.
//
// Flags were the only way in, and the two ways a real user's daemon actually starts both hardcode
// their argv with no APNs flags — DaemonLauncher for the app-managed child, LoginItemManager for the
// launchd agent. So the core mobile promise ("walk away, your phone tells you when it needs you")
// could never work for anyone who installed via the app. The Slack webhook beside it has read from
// a file all along; this is the same shape.
func TestAPNsCanBeConfiguredFromAFile(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/apns.json"
	body := `{"key_path":"/keys/AuthKey_ABC.p8","key_id":"ABC123","team_id":"TEAM7","bundle":"com.howlerops.oculus","sandbox":true}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	c := loadAPNs(path)
	if c.KeyPath != "/keys/AuthKey_ABC.p8" || c.KeyID != "ABC123" || c.TeamID != "TEAM7" {
		t.Errorf("credentials not read back: %+v", c)
	}
	if c.Bundle != "com.howlerops.oculus" || !c.Sandbox {
		t.Errorf("optional fields not read back: %+v", c)
	}

	// A missing or malformed file must leave push off rather than stop the daemon starting.
	if got := loadAPNs(dir + "/nope.json"); got.KeyPath != "" {
		t.Error("a missing file should yield no credentials")
	}
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := loadAPNs(path); got.KeyPath != "" {
		t.Error("a malformed file should yield no credentials, not a partial config")
	}
}
