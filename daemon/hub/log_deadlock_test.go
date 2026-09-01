package hub

import (
	"log"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/loghub"
)

// Logging while the hub's own mutex is held must not block.
//
// The log listener runs on the goroutine that called log.Printf, and it used to take h.mu. So any
// code that logged while holding or contending that mutex deadlocked against itself. Shutdown did
// exactly that — closeSessions logs "shutdown closed N agent session(s)" while teardown goroutines
// are in detachSession competing for the same lock — and the daemon then stopped accepting, never
// exited, and held its state lock until SIGKILL. A goroutine dump put main in broadcastLogLine's
// h.mu.Lock, underneath log.Printf, underneath closeSessions.
//
// Holding h.mu here reproduces that exact condition. Without the fix this test hangs rather than
// failing, so it is run with an explicit deadline.
func TestLoggingDoesNotBlockOnHubMutex(t *testing.T) {
	h := &Hub{sessions: map[string]*managedSession{}}
	lh := loghub.New(64)
	h.SetLogHub(lh)

	prevOut := log.Writer()
	prevFlags := log.Flags()
	log.SetOutput(lh)
	log.SetFlags(0)
	defer func() { log.SetOutput(prevOut); log.SetFlags(prevFlags) }()

	done := make(chan struct{})
	go func() {
		defer close(done)
		// The shutdown shape: hold the hub lock, then log.
		h.mu.Lock()
		defer h.mu.Unlock()
		log.Printf("hub: shutdown closed %d agent session(s)", 60)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("logging blocked while h.mu was held — this is the shutdown deadlock: the daemon " +
			"stops accepting, never exits, and holds its state lock until SIGKILL")
	}
}

// And the line must still reach subscribers — a fix that simply dropped every log line would pass
// the test above while making the Developer log panel useless.
func TestLogLineStillReachesTheFanout(t *testing.T) {
	h := &Hub{sessions: map[string]*managedSession{}}
	lh := loghub.New(64)
	h.SetLogHub(lh)

	if h.logLines.Load() == nil {
		t.Fatal("no fan-out channel was installed, so no client could ever tail the log")
	}
	h.enqueueLogLine("hello")

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(*h.logLines.Load()) == 0 { // drained by the fan-out goroutine
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the fan-out goroutine never consumed the line")
}
