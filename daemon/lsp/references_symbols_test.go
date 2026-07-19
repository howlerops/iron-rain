package lsp

import (
	"encoding/json"
	"testing"
)

func TestParseReferences(t *testing.T) {
	raw := json.RawMessage(`[
		{"uri":"file:///proj/a.go","range":{"start":{"line":3,"character":5},"end":{"line":3,"character":9}}},
		{"uri":"file:///proj/b.go","range":{"start":{"line":10,"character":0},"end":{"line":10,"character":4}}}
	]`)
	got := parseReferences(raw)
	if len(got) != 2 {
		t.Fatalf("got %d locations, want 2", len(got))
	}
	if got[0].Path != "/proj/a.go" || got[0].StartLine != 3 || got[0].StartChar != 5 {
		t.Errorf("loc0 = %+v", got[0])
	}
	if got[1].Path != "/proj/b.go" || got[1].StartLine != 10 || got[1].StartChar != 0 {
		t.Errorf("loc1 = %+v", got[1])
	}
	if parseReferences(json.RawMessage(`null`)) != nil {
		t.Error("null result should parse to nil")
	}
}

func TestParseWorkspaceEditChangesShape(t *testing.T) {
	raw := json.RawMessage(`{"changes":{
		"file:///proj/a.go":[
			{"range":{"start":{"line":1,"character":2},"end":{"line":1,"character":5}},"newText":"foo"},
			{"range":{"start":{"line":4,"character":0},"end":{"line":4,"character":3}},"newText":"foo"}
		]
	}}`)
	got := parseWorkspaceEdit(raw)
	if len(got) != 1 {
		t.Fatalf("got %d changes, want 1", len(got))
	}
	if got[0].Path != "/proj/a.go" {
		t.Errorf("path = %q, want /proj/a.go", got[0].Path)
	}
	if len(got[0].Edits) != 2 {
		t.Errorf("edit count = %d, want 2", len(got[0].Edits))
	}
	if got[0].Edits[0].NewText != "foo" {
		t.Errorf("edit newText = %q, want foo", got[0].Edits[0].NewText)
	}
}

func TestParseWorkspaceEditDocumentChangesShape(t *testing.T) {
	raw := json.RawMessage(`{"documentChanges":[
		{"textDocument":{"uri":"file:///proj/a.go","version":1},"edits":[
			{"range":{"start":{"line":0,"character":0},"end":{"line":0,"character":3}},"newText":"bar"}
		]},
		{"textDocument":{"uri":"file:///proj/b.go","version":2},"edits":[
			{"range":{"start":{"line":2,"character":1},"end":{"line":2,"character":4}},"newText":"bar"},
			{"range":{"start":{"line":9,"character":0},"end":{"line":9,"character":3}},"newText":"bar"}
		]}
	]}`)
	got := parseWorkspaceEdit(raw)
	if len(got) != 2 {
		t.Fatalf("got %d changes, want 2", len(got))
	}
	byPath := map[string]RenameChange{}
	for _, c := range got {
		byPath[c.Path] = c
	}
	a, ok := byPath["/proj/a.go"]
	if !ok || len(a.Edits) != 1 {
		t.Errorf("a.go change = %+v", a)
	}
	b, ok := byPath["/proj/b.go"]
	if !ok || len(b.Edits) != 2 {
		t.Errorf("b.go change = %+v", b)
	}
	if parseWorkspaceEdit(json.RawMessage(`null`)) != nil {
		t.Error("null workspace edit should parse to nil")
	}
}

func TestParseDocumentSymbolsHierarchical(t *testing.T) {
	raw := json.RawMessage(`[
		{"name":"Server","detail":"struct","kind":23,
		 "range":{"start":{"line":5,"character":0},"end":{"line":20,"character":1}},
		 "selectionRange":{"start":{"line":5,"character":5},"end":{"line":5,"character":11}},
		 "children":[
			{"name":"Start","detail":"func()","kind":6,
			 "range":{"start":{"line":8,"character":1},"end":{"line":12,"character":2}},
			 "selectionRange":{"start":{"line":8,"character":4},"end":{"line":8,"character":9}}}
		 ]}
	]`)
	got := parseDocumentSymbols(raw)
	if len(got) != 1 {
		t.Fatalf("got %d top-level symbols, want 1", len(got))
	}
	s := got[0]
	if s.Name != "Server" || s.Kind != 23 || s.Detail != "struct" {
		t.Errorf("symbol = %+v", s)
	}
	// selectionRange.start wins over range.start.
	if s.Line != 5 || s.Char != 5 {
		t.Errorf("symbol pos = (%d,%d), want (5,5)", s.Line, s.Char)
	}
	if len(s.Children) != 1 {
		t.Fatalf("got %d children, want 1", len(s.Children))
	}
	c := s.Children[0]
	if c.Name != "Start" || c.Kind != 6 || c.Line != 8 || c.Char != 4 {
		t.Errorf("child = %+v", c)
	}
}

func TestParseDocumentSymbolsFlat(t *testing.T) {
	raw := json.RawMessage(`[
		{"name":"Alpha","kind":12,"location":{"uri":"file:///proj/a.go",
		 "range":{"start":{"line":1,"character":0},"end":{"line":1,"character":5}}},"containerName":""},
		{"name":"Beta","kind":6,"location":{"uri":"file:///proj/a.go",
		 "range":{"start":{"line":7,"character":2},"end":{"line":7,"character":6}}},"containerName":"Alpha"}
	]`)
	got := parseDocumentSymbols(raw)
	if len(got) != 2 {
		t.Fatalf("got %d symbols, want 2", len(got))
	}
	if got[0].Name != "Alpha" || got[0].Kind != 12 || got[0].Line != 1 || got[0].Char != 0 {
		t.Errorf("sym0 = %+v", got[0])
	}
	if got[1].Name != "Beta" || got[1].Line != 7 || got[1].Char != 2 {
		t.Errorf("sym1 = %+v", got[1])
	}
	// Flat shape yields no nested children.
	if got[0].Children != nil || got[1].Children != nil {
		t.Error("flat symbols should have nil children")
	}
	if parseDocumentSymbols(json.RawMessage(`null`)) != nil {
		t.Error("null result should parse to nil")
	}
}
