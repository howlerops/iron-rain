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
