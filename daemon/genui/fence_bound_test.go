package genui

import (
	"strings"
	"testing"
)

// An unterminated iron:ui fence must not accumulate without limit.
//
// The size cap lived in parseComponent, which runs only once the whole body is resident — so a fence
// that never closes grew in memory forever, and when it finally ended (or the turn did) the over-cap
// body was handed back as a fallback code block and forwarded to every connected client whole, as a
// single output.delta. An unterminated fence is the ordinary shape of the failure: a model that
// starts a UI block and drifts, or gets interrupted mid-block.
func TestAnUnterminatedFenceIsBounded(t *testing.T) {
	var s Segmenter
	var text strings.Builder

	f, _ := s.Feed("```iron:ui\n")
	text.WriteString(f)
	// A component header, so a skeleton is announced and the placeholder path is exercised too.
	f, _ = s.Feed(`{"component":"table","id":"runaway","props":{"columns":["a"],"rows":[` + "\n")
	text.WriteString(f)

	// Now the model drifts, and never closes the fence.
	const chunk = `["aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"],`
	for i := 0; i < 20000; i++ {
		f, _ := s.Feed(chunk + "\n")
		text.WriteString(f)
		if s.fenceBuf.Len() > maxFenceBytes {
			t.Fatalf("the open fence grew to %d bytes, past the %d cap.\n\n"+
				"Nothing bounded it while it accumulated — the only check ran after the whole body was "+
				"already in memory, and the over-cap body was then forwarded to every client in one "+
				"delta.", s.fenceBuf.Len(), maxFenceBytes)
		}
	}

	// And the content is released as text rather than silently swallowed: the user must still see
	// whatever the agent wrote.
	if !strings.Contains(text.String(), "aaaaaaaa") {
		t.Error("the overrun discarded the agent's content instead of releasing it as text — nothing " +
			"the agent wrote may be hidden, which is why an invalid fence falls back to a code block")
	}
}

// A body that merely exceeds the COMPONENT cap must still take the ordinary invalid-fence path, so
// the overrun guard does not change what a slightly-too-big card does today.
func TestAnOverCapButClosedFenceStillFallsBackToText(t *testing.T) {
	var s Segmenter
	var text strings.Builder
	big := strings.Repeat("x", maxPayloadBytes+1024)
	for _, part := range []string{
		"```iron:ui\n",
		`{"component":"callout","id":"c1","props":{"body":"` + big + `"}}` + "\n",
		"```\n",
	} {
		f, _ := s.Feed(part)
		text.WriteString(f)
	}
	if !strings.Contains(text.String(), "iron:ui") {
		t.Errorf("an over-cap component was not returned as a fallback code block; got %q",
			clip(text.String(), 120))
	}
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
