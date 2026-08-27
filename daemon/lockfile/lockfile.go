// Package lockfile enforces one daemon per state directory.
//
// ~/.oculus is single-owner state: the SQLite transcript, the device registry, the project list, and
// the local bootstrap credential in pairing.json. Two daemons sharing it do not merely race — the
// second ROTATES the bootstrap code and rewrites pairing.json while the first keeps its own copy in
// memory, so the file and the running daemon disagree from that moment on. Every local recovery path
// reads that file, including the app's "the daemon forgot me, re-enrol" fallback, so the machine ends
// up with an app that cannot connect and cannot repair itself, and no error anywhere names the cause.
//
// Binding the listener before rotating catches the common case, because the second daemon usually
// wants a port the first already holds. It does not catch a second daemon on a DIFFERENT port, which
// is the same accident with a flag typo. The invariant worth enforcing is the state directory, not
// the address.
package lockfile

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// Lock is a held exclusive lock on a state directory.
type Lock struct {
	f    *os.File
	path string
}

// Acquire takes an exclusive, non-blocking lock on dir/name.
//
// The lock is an flock on an open descriptor, not the existence of a file, so it is released by the
// KERNEL when the process exits — including on a crash or a SIGKILL. A pid file alone would strand a
// stale lock after any hard exit and turn "one daemon per home" into "no daemon until someone
// deletes a file", which is a worse failure than the one it prevents.
//
// The pid is written into the file anyway, purely so the error message can name the process actually
// holding it. It is advisory text, never the lock itself.
func Acquire(dir, name string) (*Lock, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create %s: %w", dir, err)
	}
	path := filepath.Join(dir, name)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		holder := readPID(path)
		f.Close()
		if holder > 0 {
			return nil, fmt.Errorf("another oculusd is already using %s (pid %d)", dir, holder)
		}
		return nil, fmt.Errorf("another oculusd is already using %s", dir)
	}
	// Truncate only AFTER the lock is held, so a failed attempt never disturbs the holder's pid.
	if err := f.Truncate(0); err == nil {
		_, _ = f.WriteAt([]byte(strconv.Itoa(os.Getpid())+"\n"), 0)
	}
	return &Lock{f: f, path: path}, nil
}

// Release drops the lock. Safe to call twice.
func (l *Lock) Release() {
	if l == nil || l.f == nil {
		return
	}
	_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	_ = l.f.Close()
	l.f = nil
}

// readPID reads the advisory pid a holder wrote. 0 when unreadable — the message just gets vaguer.
func readPID(path string) int {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return 0
	}
	return n
}
