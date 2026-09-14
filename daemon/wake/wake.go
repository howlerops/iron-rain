// Package wake keeps the machine awake while there is work a remote user is waiting on.
//
// The premise is "swap to my phone from anywhere and continue" — which fails completely if the Mac
// sleeps thirty seconds after you walk away from it. The agent stops mid-turn, the relay registration
// goes stale, and the phone shows a session that is neither running nor reachable, with nothing to
// explain why.
//
// This is a courtesy, not a guarantee: it uses `caffeinate -s`, which holds off IDLE system sleep on
// AC power and does nothing against a closed lid on battery. The app tells the user that honestly
// rather than pretending otherwise.
package wake

import (
	"errors"
	"os/exec"
	"sync"

	"github.com/howlerops/oculus/daemon/procutil"
)

var errFake = errors.New("wake: test")

// Guard is a refcounted sleep assertion. Callers Hold while work is outstanding and Release when it
// finishes; the assertion exists only while the count is above zero.
type Guard struct {
	mu    sync.Mutex
	n     int
	cmd   *exec.Cmd
	start func() error
	stop  func()
}

// New returns a Guard backed by caffeinate.
func New() *Guard {
	g := &Guard{}
	g.start = g.spawn
	g.stop = g.kill
	return g
}

// Hold marks one more piece of outstanding work.
func (g *Guard) Hold() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.n++
	if g.n == 1 && g.start != nil {
		// Best-effort by design. A machine that will not stay awake is a worse experience, not a
		// broken one — refusing to run the turn because of it would be far worse.
		_ = g.start()
	}
}

// Release marks one piece of work finished. Extra releases are ignored rather than driving the count
// negative, which would leave a later Hold unable to re-assert.
func (g *Guard) Release() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.n == 0 {
		return
	}
	g.n--
	if g.n == 0 && g.stop != nil {
		g.stop()
	}
}

// ReleaseAll drops the assertion outright, whatever the count.
//
// For the one case that is not a turn ending: the daemon replacing its own process image. The
// self-updater calls syscall.Exec, which unwinds nothing — no defers, no Shutdown, no turn close —
// so the `caffeinate -s` child survives parentless while the new image starts with a refcount of
// zero and no knowledge of it. The user's Mac then never idle-sleeps again, and gains one more
// orphan on every update that lands mid-turn.
func (g *Guard) ReleaseAll() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.n == 0 {
		return
	}
	g.n = 0
	if g.stop != nil {
		g.stop()
	}
}

func (g *Guard) spawn() error {
	cmd := exec.Command("caffeinate", "-s") // -s: hold off system sleep while this process lives
	procutil.Isolate(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	g.cmd = cmd
	go func() { _ = cmd.Wait() }() // reap, so a long-lived daemon doesn't accumulate zombies
	return nil
}

func (g *Guard) kill() {
	if g.cmd == nil {
		return
	}
	procutil.TerminateGroup(g.cmd)
	g.cmd = nil
}
