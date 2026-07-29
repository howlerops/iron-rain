package genui

import "testing"

// TestBareComponentLine: a model that forgets the ```iron:ui fence and emits the component JSON as a
// bare line still gets a rendered component — and near-miss text is left alone.
func TestBareComponentLine(t *testing.T) {
	var s Segmenter
	fwd, comps := s.Feed(`before` + "\n" + `{"component":"table","id":"t1","props":{"columns":[{"label":"A"}],"rows":[["x"]]}}` + "\n" + `after` + "\n")
	if len(comps) != 1 || comps[0].Component != "table" {
		t.Fatalf("bare component line not parsed: comps=%v", comps)
	}
	if want := "before\nafter\n"; fwd != want {
		t.Fatalf("forwarded text = %q, want %q", fwd, want)
	}

	// Invalid / unknown component JSON stays as visible text (never swallowed).
	var s2 Segmenter
	fwd2, comps2 := s2.Feed(`{"component":"nope","id":"x","props":{}}` + "\n")
	if len(comps2) != 0 || fwd2 == "" {
		t.Fatalf("unknown component should stay text: fwd=%q comps=%v", fwd2, comps2)
	}
}

// TestExtractFromFinalizedText: replayed-history text (arrives whole, not as deltas) gets the same
// component extraction as the streaming path — fenced AND bare forms.
func TestExtractFromFinalizedText(t *testing.T) {
	in := "intro\n```iron:ui\n{\"component\":\"callout\",\"id\":\"c1\",\"props\":{\"text\":\"hi\"}}\n```\nmiddle\n" +
		`{"component":"table","id":"t2","props":{"columns":[{"label":"A"}],"rows":[["x"]]}}` + "\n" + "outro"
	cleaned, comps := Extract(in)
	if len(comps) != 2 {
		t.Fatalf("want 2 components, got %d", len(comps))
	}
	if comps[0].Component != "callout" || comps[1].Component != "table" {
		t.Fatalf("wrong components: %s, %s", comps[0].Component, comps[1].Component)
	}
	if want := "intro\nmiddle\noutro\n"; cleaned != want {
		t.Fatalf("cleaned = %q, want %q", cleaned, want)
	}
	// Text without any payload passes through untouched (fast path).
	if out, comps := Extract("plain text"); out != "plain text" || comps != nil {
		t.Fatalf("plain text mangled: %q %v", out, comps)
	}
}
