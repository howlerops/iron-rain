package genui

import (
	"strings"
	"testing"
)

// A malformed choice block must fall back to prose, not become an unanswerable card.
//
// validateOptions wrote `_ = json.Unmarshal(props, &p)` and then checked len(p.Options) <= maxOptions.
// On a decode failure Options is nil, nil passes the cap, and the block was emitted as a valid
// `choice` carrying the props it could not read. The user got an interactive card with nothing to
// click — and because the fence text is stripped from the stream, the QUESTION the agent was asking
// vanished with it. Nothing to answer, and no prose explaining what was being asked.
//
// Both siblings, validateTable and validateForm, return false on the same condition. The package's
// stated failure mode is "never broken": an unrecognised block stays visible as an ordinary code
// block, which is at least readable.
func TestAMalformedChoiceFallsBackToProse(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"options is a string, not an array", `{"component":"choice","id":"c1","props":{"options":"yes or no"}}`},
		{"options is an object", `{"component":"choice","id":"c2","props":{"options":{"a":1}}}`},
		{"no options at all", `{"component":"choice","id":"c3","props":{"prompt":"pick one"}}`},
		{"options is empty", `{"component":"choice","id":"c4","props":{"options":[]}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var seg Segmenter
			text, comps := seg.Feed("```iron:ui\n" + tc.body + "\n```\n")
			if len(comps) > 0 {
				t.Errorf("emitted a %q component from props it could not read.\n\nThe card renders "+
					"with nothing to click, and the fence text has been stripped from the stream, so "+
					"the question the agent was asking is gone from the transcript entirely.",
					comps[0].Component)
			}
			if !strings.Contains(text, "choice") {
				t.Errorf("the block was dropped AND its text withheld (%q) — the user sees neither a "+
					"card nor the question", text)
			}
		})
	}
}

// A well-formed choice must still become a card, or the fix has simply disabled the feature.
func TestAWellFormedChoiceStillRenders(t *testing.T) {
	var seg Segmenter
	_, comps := seg.Feed("```iron:ui\n" +
		`{"component":"choice","id":"ok1","props":{"options":["yes","no"]}}` + "\n```\n")
	if len(comps) != 1 {
		t.Fatalf("got %d components, want 1 — a valid choice no longer renders", len(comps))
	}
	if comps[0].Component != "choice" {
		t.Errorf("component = %q, want choice", comps[0].Component)
	}
}
