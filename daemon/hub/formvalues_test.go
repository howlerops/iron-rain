package hub

import (
	"encoding/json"
	"testing"
)

func TestFormValuesText(t *testing.T) {
	// Rendered as the user's own words, sorted for stability.
	raw := json.RawMessage(`{"scope":"backend","urgency":"high","notes":""}`)
	got := formValuesText(raw)
	want := "scope: backend\nurgency: high"
	if got != want {
		t.Errorf("formValuesText = %q, want %q (blank optional fields must be dropped)", got, want)
	}
	// Non-string values render too.
	if got := formValuesText(json.RawMessage(`{"count":3,"enabled":true}`)); got != "count: 3\nenabled: true" {
		t.Errorf("non-string values = %q", got)
	}
	// Degenerate inputs produce nothing rather than junk.
	for _, in := range []string{``, `null`, `{}`, `"nope"`, `[1,2]`, `{bad json`} {
		if got := formValuesText(json.RawMessage(in)); got != "" {
			t.Errorf("formValuesText(%q) = %q, want empty", in, got)
		}
	}
}
