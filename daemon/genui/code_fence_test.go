package genui

import (
	"strings"
	"testing"
)

// The segmenter must not reach inside somebody else's code block.
//
// It had no concept of any fence but its own, so while "outside a fence" — which included being
// inside a ```json block — it still applied the bare-JSON catch. An agent EXPLAINING the iron:ui
// grammar therefore had its example executed as a live card and the example deleted from its own
// code block, leaving an empty ``` pair in the prose. The 4-backtick documentation idiom (wrapping a
// ```iron:ui block to show it) was worse: the inner block ran.
func TestCodeFencesAreNotOurs(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
	}{
		{
			"a json block containing a component payload",
			"Here is what you would emit:\n```json\n{\"component\":\"table\",\"id\":\"t1\",\"props\":{\"columns\":[\"a\"],\"rows\":[[\"1\"]]}}\n```\nThat's the shape.\n",
		},
		{
			"a plain block containing a component payload",
			"Like so:\n```\n{\"component\":\"table\",\"id\":\"t2\",\"props\":{\"columns\":[\"a\"],\"rows\":[[\"1\"]]}}\n```\ndone\n",
		},
		{
			"a four-backtick wrapper quoting an iron:ui block",
			"To render a table, emit:\n\n````\n```iron:ui\n{\"component\":\"table\",\"id\":\"demo\",\"props\":{\"columns\":[\"a\"],\"rows\":[[\"1\"]]}}\n```\n````\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var s Segmenter
			var text strings.Builder
			var comps int
			for _, line := range strings.SplitAfter(tc.in, "\n") {
				if line == "" {
					continue
				}
				out, _, ok := s.consumeLine(line)
				text.WriteString(out)
				if ok {
					comps++
				}
			}
			flushed, rest := s.Flush()
			text.WriteString(flushed)
			comps += len(rest)

			if comps != 0 {
				t.Errorf("%d component(s) were built from a quoted example — an agent explaining the "+
					"grammar had its example executed", comps)
			}
			if got := text.String(); got != tc.in {
				t.Errorf("the code block was mutilated.\n got: %q\nwant: %q", got, tc.in)
			}
		})
	}
}

// The real thing still works: an unquoted iron:ui fence is still a component.
func TestOurOwnFenceStillBuildsAComponent(t *testing.T) {
	in := "before\n```iron:ui\n{\"component\":\"table\",\"id\":\"t1\",\"props\":{\"columns\":[\"a\"],\"rows\":[[\"1\"]]}}\n```\nafter\n"
	var s Segmenter
	s.noPlaceholders = true
	var comps int
	for _, line := range strings.SplitAfter(in, "\n") {
		if line == "" {
			continue
		}
		if _, _, ok := s.consumeLine(line); ok {
			comps++
		}
	}
	if comps != 1 {
		t.Errorf("built %d components from a real iron:ui fence, want 1", comps)
	}
}
