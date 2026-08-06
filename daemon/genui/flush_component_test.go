package genui

import (
	"strings"
	"testing"
)

// TestFlushKeepsComponentOnFinalLine pins the case that shipped broken: a component that is the LAST
// thing in a message, with no trailing newline after it.
//
// Feed only consumes COMPLETE lines, so the final one lands in Flush. Flush parsed it correctly and
// appended the component — then returned `nil` instead of the slice it had just built, so the
// component was silently dropped and the payload rendered as raw JSON.
//
// It hid because it is the only path that loses anything: every component followed by a newline goes
// through Feed and works, so this only ever affected the last component in a turn — which, for a
// model that ends by emitting the thing it was building toward, is the one that matters most.
func TestFlushKeepsComponentOnFinalLine(t *testing.T) {
	const payload = `{"component":"callout","id":"verdict","props":{"level":"warn","title":"Conditional go-ahead"}}`

	var s Segmenter
	fwd, comps := s.Feed(payload) // no trailing newline: nothing is a complete line yet
	tail, more := s.Flush()
	comps = append(comps, more...)
	fwd += tail

	if len(comps) != 1 {
		t.Fatalf("expected the trailing component to survive Flush, got %d", len(comps))
	}
	if comps[0].Component != "callout" || comps[0].ID != "verdict" {
		t.Fatalf("wrong component: %q/%q", comps[0].Component, comps[0].ID)
	}
	// The raw JSON must not ALSO be forwarded as text, or the user sees both.
	if strings.Contains(fwd, `"component"`) {
		t.Fatalf("raw JSON leaked into the forwarded text: %q", fwd)
	}
}

// TestExtractHandlesComponentAtEndOfText is the same defect through the public entry point used for
// replayed history — which is what makes the raw JSON persist across app restarts rather than
// being a one-off streaming glitch.
func TestExtractHandlesComponentAtEndOfText(t *testing.T) {
	text := "Decision\n\nKeep the app in Go.\n\n" +
		`{"component":"callout","id":"verdict","props":{"level":"warn","title":"Conditional go-ahead"}}`

	clean, comps := Extract(text)
	if len(comps) != 1 {
		t.Fatalf("expected 1 component from text ending in a payload, got %d", len(comps))
	}
	if strings.Contains(clean, `"component"`) {
		t.Fatalf("raw JSON survived in the cleaned text: %q", clean)
	}
	if !strings.Contains(clean, "Keep the app in Go.") {
		t.Fatalf("surrounding prose was lost: %q", clean)
	}
}
