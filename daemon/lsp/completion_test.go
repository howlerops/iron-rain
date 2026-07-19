package lsp

import (
	"encoding/json"
	"testing"
)

func TestCompletionItems(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		want    []CompletionItem
		wantLen int
	}{
		{
			name: "completion list shape",
			raw:  `{"items":[{"label":"foo","detail":"int"},{"label":"bar","insertText":"barX"}]}`,
			want: []CompletionItem{
				{Label: "foo", Insert: "foo", Detail: "int"},
				{Label: "bar", Insert: "barX"},
			},
		},
		{
			name: "array shape",
			raw:  `[{"label":"baz"}]`,
			want: []CompletionItem{{Label: "baz", Insert: "baz"}},
		},
		{
			name: "textEdit newText preferred",
			raw:  `{"items":[{"label":"q","textEdit":{"newText":"qq","range":{"start":{"line":0,"character":0},"end":{"line":0,"character":1}}}}]}`,
			want: []CompletionItem{{Label: "q", Insert: "qq"}},
		},
		{
			name:    "null result",
			raw:     `null`,
			wantLen: 0,
		},
		{
			name:    "item without label is skipped",
			raw:     `{"items":[{"insertText":"x"},{"label":"ok"}]}`,
			wantLen: 1,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := completionItems(json.RawMessage(c.raw))
			if c.want != nil {
				if len(got) != len(c.want) {
					t.Fatalf("got %d items, want %d: %+v", len(got), len(c.want), got)
				}
				for i, w := range c.want {
					if got[i].Label != w.Label || got[i].Insert != w.Insert || got[i].Detail != w.Detail {
						t.Errorf("item %d = %+v, want %+v", i, got[i], w)
					}
				}
			} else if len(got) != c.wantLen {
				t.Fatalf("got %d items, want %d", len(got), c.wantLen)
			}
		})
	}
}

func TestInfoForPath(t *testing.T) {
	// Go is supported and has a scripted recipe; a .txt file is unsupported.
	if info := InfoForPath("/x/main.go"); info.Language != "go" {
		t.Errorf("go language = %q, want go", info.Language)
	}
	if info := InfoForPath("/x/readme.txt"); info.Language != "" {
		t.Errorf("txt language = %q, want empty (unsupported)", info.Language)
	}
	// Swift has a recipe but no scripted install command → not installable via a button.
	sw := InfoForPath("/x/App.swift")
	if sw.Language != "swift" || sw.Installable {
		t.Errorf("swift info = %+v, want language=swift installable=false", sw)
	}
}
