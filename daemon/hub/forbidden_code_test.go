package hub

import (
	"encoding/json"
	"testing"

	"github.com/howlerops/oculus/daemon/protocol"
)

// An ordinary failure must NOT be marked forbidden — otherwise every screen starts claiming a
// permissions problem for a disk error.
//
// This is the only half of the refusal contract worth asserting on a struct literal, because it is a
// claim about the ENCODING (Code is omitempty, so an unclassified error puts no key on the wire).
// The other half — that the daemon actually classifies a refusal — moved to refusal_wire_test.go,
// which drives a real demoted connection and reads the bytes that come back. The version that used
// to live here built a protocol.Error{Code: ErrorForbidden} by hand and then asserted the decoded
// code was ErrorForbidden: it supplied the value it checked, so deleting the one production line
// that emits the classification left it green.
func TestAnOrdinaryFailureCarriesNoCode(t *testing.T) {
	raw, err := protocol.Encode("req-2", protocol.TypeError, protocol.Error{Message: "no such session"})
	if err != nil {
		t.Fatal(err)
	}
	var env struct {
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(env.Payload, &fields); err != nil {
		t.Fatal(err)
	}
	if _, present := fields["code"]; present {
		t.Errorf("an unclassified error put a code on the wire: %s\n\nEvery screen that branches on "+
			"isForbidden would start telling the user they lack permission for what is actually a "+
			"disk error or a dropped socket.", env.Payload)
	}
}
