package pi

import "testing"

// TestGetStateRecordsTheResumeHandle covers the one link between our session ids and pi's own files.
//
// This branch had NO coverage: neutering it left the package fully green. That matters more than it
// sounds, because it is the only handle that outlives the process — pi names its files
// `<ts>_<uuid>.jsonl`, so a lookup by our `pi_…` id can never find one. If pi's response frame ever
// changes shape, daemon-created pi sessions become silently unresumable and nothing fails. This
// project has already shipped one entirely inert feature; this is the same failure mode.
func TestGetStateRecordsTheResumeHandle(t *testing.T) {
	p := &Provider{}
	s := &session{id: "pi_abc", p: p}

	// The exact frame shape pi documents for get_state.
	line := []byte(`{"type":"response","command":"get_state","data":{"sessionFile":"/home/u/.pi/sessions/1712000000_9f3a.jsonl"}}`)
	s.recordResumeHandle("get_state", line)

	got := p.resumePath("pi_abc")
	if got != "/home/u/.pi/sessions/1712000000_9f3a.jsonl" {
		t.Fatalf("resume path = %q, want pi's own session file — without it a restart cannot resume this conversation", got)
	}
}

// A response for a DIFFERENT command must not be mistaken for the state report.
func TestOtherResponsesDoNotSetTheResumeHandle(t *testing.T) {
	p := &Provider{}
	s := &session{id: "pi_abc", p: p}
	s.recordResumeHandle("send_message", []byte(`{"type":"response","command":"send_message","data":{"sessionFile":"/wrong.jsonl"}}`))
	if got := p.resumePath("pi_abc"); got != "" {
		t.Errorf("resume path = %q, want empty — only get_state reports the session file", got)
	}
}

// A malformed or empty payload must leave the handle alone rather than recording nonsense that a
// later restore would try to open.
func TestMalformedStateLeavesTheHandleUnset(t *testing.T) {
	p := &Provider{}
	s := &session{id: "pi_abc", p: p}
	s.recordResumeHandle("get_state", []byte(`{"type":"response","command":"get_state","data":{}}`))
	if got := p.resumePath("pi_abc"); got != "" {
		t.Errorf("resume path = %q, want empty for a payload carrying no file", got)
	}
}
