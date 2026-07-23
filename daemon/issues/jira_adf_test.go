package issues

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestADFToText(t *testing.T) {
	// A realistic Jira Cloud description: two paragraphs + a bullet list.
	adf := `{"type":"doc","version":1,"content":[
		{"type":"paragraph","content":[{"type":"text","text":"First line."}]},
		{"type":"paragraph","content":[{"type":"text","text":"Second "},{"type":"text","text":"line."}]},
		{"type":"bulletList","content":[
			{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"a"}]}]},
			{"type":"listItem","content":[{"type":"paragraph","content":[{"type":"text","text":"b"}]}]}
		]}
	]}`
	got := adfToText(json.RawMessage(adf))
	for _, want := range []string{"First line.", "Second line.", "a", "b"} {
		if !strings.Contains(got, want) {
			t.Errorf("adfToText missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "\"type\"") {
		t.Errorf("adfToText leaked raw JSON: %s", got)
	}
}

func TestADFToTextEmpty(t *testing.T) {
	for _, in := range []string{"", "null", `"just a string"`, `{}`} {
		if got := adfToText(json.RawMessage(in)); got != "" {
			t.Errorf("adfToText(%q) = %q, want empty", in, got)
		}
	}
}

func TestJiraPriority(t *testing.T) {
	cases := map[string]int{"Highest": 1, "High": 2, "Medium": 3, "Low": 4, "Lowest": 5, "": 0, "weird": 0}
	for name, want := range cases {
		if got := jiraPriority(name); got != want {
			t.Errorf("jiraPriority(%q) = %d, want %d", name, got, want)
		}
	}
}
