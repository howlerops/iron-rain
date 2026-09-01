package genui

import (
	"strings"
	"testing"
)

// The daemon's recognizer and the client's must accept exactly the same payloads.
//
// Two independent recognizers with different rules is not redundancy, it is a disagreement. This one
// is deliberately strict — closed catalog, size and shape caps — and forwards everything it refuses
// as ordinary text, because an over-cap payload is meant to be dropped SILENTLY and left as plain
// text. The client's accepted ANY object carrying component/id/props: no catalog, no caps, no size
// limit. So a payload judged unsafe here and deliberately passed through as prose was picked up there
// and rendered as a live card, and the closed-catalog guarantee — that a model can only emit UI the
// client has vetted — did not hold.
//
// Every case below is asserted with the SAME payload and the SAME expected verdict in
// app/OculusKit/Tests/OculusUITests/CatalogAgreementTests.swift. If you change one, change both.
func TestCatalogAgreesWithTheClient(t *testing.T) {
	overCapTable := `{"component":"table","id":"t2","props":{"columns":[` +
		strings.Join(quoted("c", 21), ",") + `],"rows":[]}}`
	overCapChoice := `{"component":"choice","id":"ch1","props":{"options":[` +
		strings.Join(quoted("o", 51), ",") + `]}}`

	for _, tc := range []struct {
		name     string
		accepted bool
		payload  string
	}{
		{"a valid table", true,
			`{"component":"table","id":"t1","props":{"columns":["a"],"rows":[["1"]]}}`},
		{"a valid callout (no validator)", true,
			`{"component":"callout","id":"c1","props":{"body":"hi"}}`},
		{"a component outside the catalog", false,
			`{"component":"iframe","id":"x1","props":{"src":"http://evil"}}`},
		{"an empty id", false,
			`{"component":"table","id":"","props":{"columns":["a"],"rows":[["1"]]}}`},
		{"a table over the column cap", false, overCapTable},
		{"a choice over the option cap", false, overCapChoice},
		{"a form with an invented field type", false,
			`{"component":"form","id":"f1","props":{"fields":[{"id":"a","type":"password"}]}}`},
		{"a form with no fields", false,
			`{"component":"form","id":"f2","props":{"fields":[]}}`},
		{"a valid form", true,
			`{"component":"form","id":"f3","props":{"fields":[{"id":"a","type":"text"}]}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, ok := parseComponent(tc.payload)
			if ok != tc.accepted {
				t.Errorf("daemon %s; the client's mirror expects %s",
					verdict(ok), verdict(tc.accepted))
			}
		})
	}
}

func verdict(ok bool) string {
	if ok {
		return "builds a component"
	}
	return "forwards it as text"
}

func quoted(prefix string, n int) []string {
	out := make([]string, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, `"`+prefix+string(rune('0'+i%10))+string(rune('a'+i/10))+`"`)
	}
	return out
}
