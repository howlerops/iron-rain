package protocol

import (
	"encoding/json"
	"testing"
)

func TestStampSeq(t *testing.T) {
	got := string(StampSeq([]byte(`{"type":"session.message","payload":{"a":1}}`), 7))
	want := `{"seq":7,"type":"session.message","payload":{"a":1}}`
	if got != want {
		t.Fatalf("got %s want %s", got, want)
	}
	if s := string(StampSeq([]byte(`{}`), 3)); s != `{"seq":3}` {
		t.Fatalf("empty object: %s", s)
	}
	if s := string(StampSeq([]byte(`{"type":"x"}`), 0)); s != `{"type":"x"}` {
		t.Fatalf("zero seq must not stamp: %s", s)
	}
	var env Envelope
	if err := json.Unmarshal([]byte(got), &env); err != nil || env.Seq != 7 || env.Type != "session.message" {
		t.Fatalf("stamped frame did not decode: %v seq=%d type=%s", err, env.Seq, env.Type)
	}
}

func TestStripSeqIsTheInverseOfStamp(t *testing.T) {
	for _, frame := range []string{
		`{"type":"session.message","payload":{"a":1}}`,
		`{}`,
		`{"type":"x"}`,
	} {
		stamped := StampSeq([]byte(frame), 42)
		if got := string(StripSeq(stamped)); got != frame {
			t.Errorf("round trip: %s -> %s -> %s", frame, stamped, got)
		}
	}
	// A frame that was never stamped is returned untouched, and one whose first member merely looks
	// similar must not be mangled.
	for _, untouched := range []string{
		`{"type":"session.message"}`,
		`{"seqno":3,"type":"x"}`,
		`{"sequence":"abc"}`,
	} {
		if got := string(StripSeq([]byte(untouched))); got != untouched {
			t.Errorf("StripSeq altered an unstamped frame: %s -> %s", untouched, got)
		}
	}
}
