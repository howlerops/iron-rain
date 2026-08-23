package hub_test

import (
	"encoding/json"
	"testing"

	"github.com/howlerops/oculus/daemon/crypto"
	"github.com/howlerops/oculus/daemon/hub"
	"github.com/howlerops/oculus/daemon/protocol"
)

// An approval raised by a session a client has never OPENED must still reach that client.
//
// Clients subscribe to a session when they open it, so the request used to go only to that
// session's subscribers. A session nobody was looking at could therefore block on an approval in
// complete silence — and that is exactly the session you need to be told about. The Fleet renders
// approval controls per card precisely so you can answer without opening anything, which it could
// never do for an unopened session.
//
// Observed live: three fan-out agents sat blocked on a Write for thirteen minutes while their fleet
// cards read "On track". Opening one made its approval appear instantly; the other two stayed
// silent. The asymmetry gave it away — RESOLUTION was already broadcast hub-wide, so clients were
// told an approval had been ANSWERED while never being told it had been ASKED.
//
// Note this is deliberately NOT the same guarantee as TestMultiClient_SharedSession, which covers a
// client that subscribes and gets the approval by transcript REPLAY. Here the watcher never
// subscribes at all, which is the state every client is in for every session it hasn't opened.
func TestApprovalReachesAClientThatNeverOpenedTheSession(t *testing.T) {
	h := hub.New()
	h.Register(&sharedProvider{sess: newSharedSession()})

	daemonKP, _ := crypto.GenerateKeyPair()
	creator := connectClient(t, h, daemonKP)
	watcher := connectClient(t, h, daemonKP) // connects, never subscribes to the session
	defer creator.Close()
	defer watcher.Close()
	readerCreator := newReader(creator)
	readerWatcher := newReader(watcher)

	raw, _ := protocol.Encode("c1", protocol.TypeSessionCreate, protocol.SessionCreate{Provider: "fake"})
	if err := creator.Send(raw); err != nil {
		t.Fatal(err)
	}
	readerCreator.waitFor(t, "create ok", func(e protocol.Envelope) bool {
		return e.Type == protocol.TypeOK && e.ID == "c1"
	})

	readerWatcher.waitFor(t, "approval reached a client that never opened the session", func(e protocol.Envelope) bool {
		if e.Type != protocol.TypeApprovalRequest {
			return false
		}
		var ar protocol.ApprovalRequest
		if err := json.Unmarshal(e.Payload, &ar); err != nil {
			return false
		}
		// The enriched request, not a bare id: the card needs the tool and the session to render.
		return ar.ApprovalID == "ap_shared" && ar.SessionID == "shared_sess" && ar.Tool == "bash"
	})
}
