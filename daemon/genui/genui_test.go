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
			comps = append(comps, componentResult{c.Component, c.ID, string(c.Props), c.Status})
		}
	}
	f, cs := s.Flush()
	fwd.WriteString(f)
	for _, c := range cs {
		comps = append(comps, componentResult{c.Component, c.ID, string(c.Props), c.Status})
	}
	return fwd.String(), comps
}

type componentResult struct{ component, id, props, status string }

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
	// callout has no props validator, so it is announced early: running then ready, same id.
	if len(comps) != 2 || comps[0].status != "running" || comps[1].status != "ready" {
		t.Fatalf("expected running+ready for c1, got %+v", comps)
	}
	for _, c := range comps {
		if c.component != "callout" || c.id != "c1" {
			t.Fatalf("both frames must describe callout c1, got %+v", comps)
		}
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
	// Each callout is announced then completed, so the sequence is a,a,b,b — and crucially the
	// second fence's placeholder must not reuse the first's id.
	if len(comps) != 4 {
		t.Fatalf("expected running+ready for each of a,b, got %+v", comps)
	}
	got := []string{comps[0].id, comps[1].id, comps[2].id, comps[3].id}
	for i, want := range []string{"a", "a", "b", "b"} {
		if got[i] != want {
			t.Fatalf("frame %d should be id %q, got %+v", i, want, comps)
		}
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
	// No READY component may be produced — but the early `running` placeholder has to be retracted,
	// or it would spin forever on a turn that never finished writing it.
	for _, c := range comps {
		if c.status == "ready" {
			t.Fatalf("unterminated fence must not produce a ready component, got %+v", comps)
		}
	}
	if len(comps) > 0 && comps[len(comps)-1].status != "error" {
		t.Fatalf("an announced placeholder must resolve to error, got %+v", comps)
	}
	if !strings.Contains(fwd, "text\n") || !strings.Contains(fwd, "iron:ui") {
		t.Fatalf("unterminated fence content should be recovered as text: %q", fwd)
	}
}

func TestDefaultSchemaAndFallback(t *testing.T) {
	var s Segmenter
	_, cs := s.Feed("```iron:ui\n{\"component\":\"callout\",\"id\":\"c1\",\"props\":{\"body\":\"x\"}}\n```\n")
	// Two frames now: the component is announced as `running` the moment it can be identified, then
	// replaced in place by the finished `ready` one under the same id. The client has always had a
	// skeleton view for `running`; the daemon simply never emitted it.
	if len(cs) != 2 {
		t.Fatalf("expected running+ready, got %d: %+v", len(cs), cs)
	}
	if cs[0].Status != "running" || cs[0].ID != "c1" || cs[0].Component != "callout" {
		t.Fatalf("first frame should be a running placeholder for c1/callout, got %+v", cs[0])
	}
	final := cs[len(cs)-1]
	if final.ID != cs[0].ID {
		t.Fatalf("the ready frame must reuse the placeholder's id to replace it in place: %q vs %q", final.ID, cs[0].ID)
	}
	if final.SchemaV != 1 {
		t.Fatalf("schema_v should default to 1, got %d", final.SchemaV)
	}
	if final.FallbackText == "" {
		t.Fatalf("fallback_text should be synthesized when omitted")
	}
	if final.Status != "ready" {
		t.Fatalf("status should be ready, got %q", final.Status)
	}
}

// A placeholder must never outlive the fence that created it. If the body turns out to be malformed,
// the skeleton has to resolve to `error` under the same id — otherwise the user is left with a
// component that spins forever, which is strictly worse than never having announced it.
func TestMalformedBodyResolvesThePlaceholder(t *testing.T) {
	var s Segmenter
	_, cs := s.Feed("```iron:ui\n{\"component\":\"callout\",\"id\":\"t1\",\"props\":{oops}\n```\n")
	if len(cs) == 0 {
		t.Fatal("expected the placeholder to be resolved, got no components")
	}
	final := cs[len(cs)-1]
	if final.Status != "error" || final.ID != "t1" {
		t.Fatalf("expected an error frame for t1, got %+v", final)
	}
}

// Same contract when the turn simply ends mid-component (interrupted or truncated answer).
func TestUnfinishedComponentResolvesOnFlush(t *testing.T) {
	var s Segmenter
	_, cs := s.Feed("```iron:ui\n{\"component\":\"checklist\",\"id\":\"k1\",\n")
	if len(cs) != 1 || cs[0].Status != "running" {
		t.Fatalf("expected a running placeholder, got %+v", cs)
	}
	_, tail := s.Flush()
	if len(tail) != 1 || tail[0].Status != "error" || tail[0].ID != "k1" {
		t.Fatalf("flush should resolve the orphaned placeholder, got %+v", tail)
	}
}

// A nested "id" inside props must not be mistaken for the component's own id — that would strand the
// skeleton under an id nothing ever replaces. When the header can't be read confidently, the correct
// behaviour is to announce nothing and fall back to the old all-at-once delivery.
func TestPropsFirstEmitsNoPlaceholder(t *testing.T) {
	var s Segmenter
	_, cs := s.Feed("```iron:ui\n{\"props\":{\"options\":[{\"id\":\"nested\"}]},\"component\":\"choice\",\"id\":\"real\"}\n```\n")
	for _, c := range cs {
		if c.Status == "running" && c.ID != "real" {
			t.Fatalf("announced a placeholder under a nested id: %+v", c)
		}
	}
	if cs[len(cs)-1].ID != "real" {
		t.Fatalf("final component should be the real one, got %+v", cs[len(cs)-1])
	}
}

func TestStripGuideRoundTrip(t *testing.T) {
	pre := Preamble()
	user := "please summarize the failing tests"
	combined := pre + user
	if got := StripGuide(combined); got != user {
		t.Fatalf("StripGuide did not recover the user text: %q", got)
	}
	// No guide → unchanged.
	if got := StripGuide(user); got != user {
		t.Fatalf("StripGuide altered guide-free text: %q", got)
	}
	// The preamble must actually be wrapped in the sentinels the app strips.
	if !strings.Contains(pre, GuideOpen) || !strings.Contains(pre, GuideClose) {
		t.Fatal("preamble missing sentinels")
	}
}
