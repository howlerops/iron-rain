package hub

import (
	"encoding/json"
	"testing"

	"github.com/howlerops/oculus/daemon/protocol"
)

// A refusal has to be distinguishable from a failure ON THE WIRE.
//
// Every caller in the client that swallows its error — which is most of them, because a bootstrap
// request that fails must not block the connection — renders the screen's empty state. So without a
// classification, "you may not see this" and "that request went wrong" both come out as "there is
// nothing here": the Devices screen said "No devices enrolled" about a Mac with devices enrolled,
// and the Notifications section span a loading spinner forever waiting for a list that was refused.
//
// Matching on the message text would work right up until someone improved the wording.
func TestARefusalIsMarkedForbiddenOnTheWire(t *testing.T) {
	raw, err := protocol.Encode("req-1", protocol.TypeError,
		protocol.Error{Message: refusalMessage(capOwner, "list enrolled devices", ""), Code: protocol.ErrorForbidden})
	if err != nil {
		t.Fatal(err)
	}
	var env struct {
		Type    string          `json:"type"`
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	var perr protocol.Error
	if err := json.Unmarshal(env.Payload, &perr); err != nil {
		t.Fatal(err)
	}
	if perr.Code != protocol.ErrorForbidden {
		t.Errorf("code = %q, want %q — the client cannot tell a refusal from a failure and will "+
			"render its empty state for both", perr.Code, protocol.ErrorForbidden)
	}
	if perr.Message == "" {
		t.Error("a refusal with no message tells the user nothing")
	}
}

// An ordinary failure must NOT be marked forbidden — otherwise every screen starts claiming a
// permissions problem for a disk error.
func TestAnOrdinaryFailureCarriesNoCode(t *testing.T) {
	raw, _ := protocol.Encode("req-2", protocol.TypeError, protocol.Error{Message: "no such session"})
	if want := `"code"`; containsJSONKey(raw, want) {
		t.Errorf("an unclassified error put %s on the wire: %s", want, raw)
	}
}

func containsJSONKey(raw []byte, key string) bool {
	return json.Valid(raw) && bytesContains(raw, key)
}

func bytesContains(h []byte, needle string) bool {
	n := []byte(needle)
	for i := 0; i+len(n) <= len(h); i++ {
		if string(h[i:i+len(n)]) == needle {
			return true
		}
	}
	_ = n
	return false
}
