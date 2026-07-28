package genui

import (
	"strings"
	"testing"
)

// feedAll drives a Segmenter with the given chunks then flushes, returning the concatenated forwarded
// text and all components. It's the workhorse for the streaming-split tests.
func feedAll(chunks []string) (string, []componentResult) {
	var s Segmenter
	var fwd strings.Builder
	var comps []componentResult
	for _, ch := range chunks {
		f, cs := s.Feed(ch)
		fwd.WriteString(f)
		for _, c := range cs {
			comps = append(comps, componentResult{c.Component, c.ID, string(c.Props)})
		}
	}
	f, cs := s.Flush()
	fwd.WriteString(f)
	for _, c := range cs {
		comps = append(comps, componentResult{c.Component, c.ID, string(c.Props)})
	}
	return fwd.String(), comps
}

type componentResult struct{ component, id, props string }

func TestPlainTextPassesThrough(t *testing.T) {
	fwd, comps := feedAll([]string{"hello ", "world\n", "how are you\n"})
	if fwd != "hello world\nhow are you\n" {
		t.Fatalf("plain text altered: %q", fwd)
	}
	if len(comps) != 0 {
		t.Fatalf("expected no components, got %d", len(comps))
	}
}

func TestSingleFenceOneChunk(t *testing.T) {
	in := "before\n```iron:ui\n{\"component\":\"callout\",\"id\":\"c1\",\"props\":{\"level\":\"info\",\"body\":\"hi\"}}\n```\nafter\n"
	fwd, comps := feedAll([]string{in})
	if strings.Contains(fwd, "iron:ui") || strings.Contains(fwd, "callout") {
		t.Fatalf("fence not stripped from forwarded text: %q", fwd)
	}
	if fwd != "before\nafter\n" {
		t.Fatalf("forwarded text wrong: %q", fwd)
	}
	if len(comps) != 1 || comps[0].component != "callout" || comps[0].id != "c1" {
		t.Fatalf("expected 1 callout c1, got %+v", comps)
	}
}

// The critical property: a fence split across MANY tiny deltas (token-by-token) is still detected and
// stripped, and its component parsed exactly once.
func TestFenceSplitAcrossManyDeltas(t *testing.T) {
	full := "intro\n```iron:ui\n{\"component\":\"table\",\"id\":\"t1\",\"props\":{\"columns\":[{\"label\":\"A\"}],\"rows\":[[\"1\"]]}}\n```\nend\n"
	var chunks []string
	for _, r := range full { // one rune per delta — worst case
		chunks = append(chunks, string(r))
	}
	fwd, comps := feedAll(chunks)
	if fwd != "intro\nend\n" {
		t.Fatalf("forwarded text wrong under token split: %q", fwd)
	}
	if len(comps) != 1 || comps[0].component != "table" || comps[0].id != "t1" {
		t.Fatalf("expected 1 table t1, got %+v", comps)
	}
}

func TestMultipleFences(t *testing.T) {
	in := "```iron:ui\n{\"component\":\"callout\",\"id\":\"a\",\"props\":{\"body\":\"x\"}}\n```\nmid\n" +
		"```iron:ui\n{\"component\":\"callout\",\"id\":\"b\",\"props\":{\"body\":\"y\"}}\n```\n"
	fwd, comps := feedAll([]string{in})
	if fwd != "mid\n" {
		t.Fatalf("forwarded text wrong: %q", fwd)
	}
	if len(comps) != 2 || comps[0].id != "a" || comps[1].id != "b" {
		t.Fatalf("expected 2 callouts a,b, got %+v", comps)
	}
}

func TestMalformedJSONFallsBackToCodeBlock(t *testing.T) {
	in := "```iron:ui\n{not json}\n```\n"
	fwd, comps := feedAll([]string{in})
	if len(comps) != 0 {
		t.Fatalf("malformed block must not emit a component, got %+v", comps)
	}
	if !strings.Contains(fwd, "iron:ui") || !strings.Contains(fwd, "{not json}") {
		t.Fatalf("malformed block should survive as a visible code block: %q", fwd)
	}
}

func TestUnknownComponentDropped(t *testing.T) {
	in := "```iron:ui\n{\"component\":\"nope\",\"id\":\"z\",\"props\":{}}\n```\n"
	_, comps := feedAll([]string{in})
	if len(comps) != 0 {
		t.Fatalf("unknown component must not be emitted, got %+v", comps)
	}
}

func TestMissingIDDropped(t *testing.T) {
	in := "```iron:ui\n{\"component\":\"callout\",\"props\":{\"body\":\"x\"}}\n```\n"
	_, comps := feedAll([]string{in})
	if len(comps) != 0 {
		t.Fatalf("component without id must not be emitted, got %+v", comps)
	}
}

func TestOverCapTableDropped(t *testing.T) {
	var rows strings.Builder
	for i := 0; i < maxRows+1; i++ {
		if i > 0 {
			rows.WriteString(",")
		}
		rows.WriteString("[\"x\"]")
	}
	in := "```iron:ui\n{\"component\":\"table\",\"id\":\"big\",\"props\":{\"columns\":[{\"label\":\"A\"}],\"rows\":[" + rows.String() + "]}}\n```\n"
	_, comps := feedAll([]string{in})
	if len(comps) != 0 {
		t.Fatalf("over-cap table must be dropped, got %d", len(comps))
	}
}

func TestUnterminatedFenceRecoveredOnFlush(t *testing.T) {
	in := "text\n```iron:ui\n{\"component\":\"callout\",\"id\":\"c\"" // never closed
	fwd, comps := feedAll([]string{in})
	if len(comps) != 0 {
		t.Fatalf("unterminated fence should emit no component, got %+v", comps)
	}
	if !strings.Contains(fwd, "text\n") || !strings.Contains(fwd, "iron:ui") {
		t.Fatalf("unterminated fence content should be recovered as text: %q", fwd)
	}
}

func TestDefaultSchemaAndFallback(t *testing.T) {
	var s Segmenter
	_, cs := s.Feed("```iron:ui\n{\"component\":\"callout\",\"id\":\"c1\",\"props\":{\"body\":\"x\"}}\n```\n")
	if len(cs) != 1 {
		t.Fatalf("expected 1 component, got %d", len(cs))
	}
	if cs[0].SchemaV != 1 {
		t.Fatalf("schema_v should default to 1, got %d", cs[0].SchemaV)
	}
	if cs[0].FallbackText == "" {
		t.Fatalf("fallback_text should be synthesized when omitted")
	}
	if cs[0].Status != "ready" {
		t.Fatalf("status should be ready, got %q", cs[0].Status)
	}
}
