package lockfile

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestSecondAcquireIsRefused(t *testing.T) {
	dir := t.TempDir()
	first, err := Acquire(dir, "daemon.lock")
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	defer first.Release()

	// Same process, second descriptor: flock is per open file description, so this is a real second
	// claim rather than a re-entrant one.
	if _, err := Acquire(dir, "daemon.lock"); err == nil {
		t.Fatal("a second daemon acquired the same state directory")
	}
}

func TestReleaseAllowsTheNextDaemon(t *testing.T) {
	dir := t.TempDir()
	first, err := Acquire(dir, "daemon.lock")
	if err != nil {
		t.Fatal(err)
	}
	first.Release()

	second, err := Acquire(dir, "daemon.lock")
	if err != nil {
		t.Fatalf("an orderly restart must be able to take the lock: %v", err)
	}
	second.Release()
	first.Release() // double release must not panic
}

// Separate homes are separate daemons. Isolated test daemons and a second user account both rely on
// this, so a lock that keyed on anything global would break them.
func TestDifferentDirectoriesAreIndependent(t *testing.T) {
	a, err := Acquire(t.TempDir(), "daemon.lock")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Release()
	b, err := Acquire(t.TempDir(), "daemon.lock")
	if err != nil {
		t.Fatalf("a different state directory must be lockable: %v", err)
	}
	defer b.Release()
}

// The error has to name the holder, because "already running" without a pid leaves someone with no
// way to find what to stop.
func TestRefusalNamesTheHolder(t *testing.T) {
	dir := t.TempDir()
	first, err := Acquire(dir, "daemon.lock")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Release()

	_, err = Acquire(dir, "daemon.lock")
	if err == nil {
		t.Fatal("expected a refusal")
	}
	if !strings.Contains(err.Error(), strconv.Itoa(os.Getpid())) {
		t.Errorf("the refusal should name the holding pid, got %q", err)
	}
}

// A failed attempt must not disturb the holder's recorded pid — the truncate happens only after the
// lock is held, precisely so a rejected daemon cannot blank it.
func TestFailedAcquireLeavesThePIDIntact(t *testing.T) {
	dir := t.TempDir()
	first, err := Acquire(dir, "daemon.lock")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Release()

	_, _ = Acquire(dir, "daemon.lock")

	if got := readPID(filepath.Join(dir, "daemon.lock")); got != os.Getpid() {
		t.Errorf("pid after a failed acquire = %d, want %d", got, os.Getpid())
	}
}

// The lock must die WITH the process, or a crash leaves a machine that refuses to start a daemon
// until someone deletes a file — a worse failure than the one being prevented.
func TestLockIsReleasedWhenTheProcessDies(t *testing.T) {
	dir := t.TempDir()
	// A child that takes the lock and is then killed, exercising the kernel's cleanup rather than
	// any code of ours.
	helper := exec.Command(os.Args[0], "-test.run=TestHelperHoldsLock")
	helper.Env = append(os.Environ(), "LOCK_HELPER_DIR="+dir)
	stdout, err := helper.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := helper.Start(); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 16)
	if _, err := stdout.Read(buf); err != nil { // waits until the child reports it holds the lock
		t.Fatalf("helper never reported: %v", err)
	}
	if _, err := Acquire(dir, "daemon.lock"); err == nil {
		helper.Process.Kill()
		helper.Wait()
		t.Fatal("acquired a lock the live helper was holding")
	}

	_ = helper.Process.Kill()
	_, _ = helper.Process.Wait()

	got, err := Acquire(dir, "daemon.lock")
	if err != nil {
		t.Fatalf("the lock outlived the process that held it: %v", err)
	}
	got.Release()
}

// TestHelperHoldsLock is not a test; it is the child process above.
func TestHelperHoldsLock(t *testing.T) {
	dir := os.Getenv("LOCK_HELPER_DIR")
	if dir == "" {
		t.Skip("helper only")
	}
	if _, err := Acquire(dir, "daemon.lock"); err != nil {
		os.Exit(1)
	}
	os.Stdout.WriteString("held\n")
	select {} // held until killed
}
