package lsp

import (
	"context"
	"encoding/json"
)

// RenameChange collects the edits a rename produces for a single file.
type RenameChange struct {
	Path  string
	Edits []textEdit
}

// renameParams is textDocument/rename params: the position of the symbol plus the
// new identifier.
type renameParams struct {
	TextDocument textDocumentIdentifier `json:"textDocument"`
	Position     position               `json:"position"`
	NewName      string                 `json:"newName"`
}

// Rename asks the server to rename the symbol at a 0-based position, returning the
// resulting per-file edits. Returns nil (no error) when the file has no server or
// the server produces no edits.
func (m *Manager) Rename(ctx context.Context, path string, line, char int, newName string) ([]RenameChange, error) {
	d, ok := m.lookupDoc(path)
	if !ok {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	res, err := d.srv.call(ctx, "textDocument/rename", renameParams{
		TextDocument: textDocumentIdentifier{URI: d.uri},
		Position:     position{Line: line, Character: char},
		NewName:      newName,
	})
	if err != nil {
		return nil, err
	}
	return parseWorkspaceEdit(res), nil
}

// parseWorkspaceEdit decodes a WorkspaceEdit result into per-file RenameChanges. It
// handles both shapes: the older {"changes": {uri: [TextEdit]}} map and the newer
// {"documentChanges": [{textDocument:{uri}, edits:[TextEdit]}]} array. Returns nil
// on a null/empty result.
func parseWorkspaceEdit(raw json.RawMessage) []RenameChange {
	if isJSONNull(raw) {
		return nil
	}
	var we struct {
		Changes         map[string][]textEdit `json:"changes"`
		DocumentChanges []struct {
			TextDocument textDocumentIdentifier `json:"textDocument"`
			Edits        []textEdit             `json:"edits"`
		} `json:"documentChanges"`
	}
	if json.Unmarshal(raw, &we) != nil {
		return nil
	}

	var out []RenameChange
	// documentChanges is the richer, ordered form — prefer it when present.
	if len(we.DocumentChanges) > 0 {
		for _, dc := range we.DocumentChanges {
			if dc.TextDocument.URI == "" || len(dc.Edits) == 0 {
				continue
			}
			out = append(out, RenameChange{Path: uriToPath(dc.TextDocument.URI), Edits: dc.Edits})
		}
		return out
	}
	for uri, edits := range we.Changes {
		if uri == "" || len(edits) == 0 {
			continue
		}
		out = append(out, RenameChange{Path: uriToPath(uri), Edits: edits})
	}
	return out
}

// Symbol is one node in a document's symbol outline. Line/Char are 0-based and point
// at the symbol's name (selection range). Children is populated only for the
// hierarchical DocumentSymbol response shape.
type Symbol struct {
	Name     string
	Kind     int // LSP SymbolKind (1..26)
	Detail   string
	Line     int // 0-based; the symbol's name/selection start
	Char     int
	Children []Symbol
}

// DocumentSymbols returns the outline for a file. The server may reply with either
// hierarchical DocumentSymbol[] (nested, with selectionRange) or flat
// SymbolInformation[] (with a location); both are normalized to []Symbol. Returns
// nil (no error) when the file has no server or the outline is empty.
func (m *Manager) DocumentSymbols(ctx context.Context, path string) ([]Symbol, error) {
	d, ok := m.lookupDoc(path)
	if !ok {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	res, err := d.srv.call(ctx, "textDocument/documentSymbol", map[string]interface{}{
		"textDocument": textDocumentIdentifier{URI: d.uri},
	})
	if err != nil {
		return nil, err
	}
	return parseDocumentSymbols(res), nil
}

// rawSymbol is a tolerant union of DocumentSymbol and SymbolInformation. Presence of
// "location" marks the flat SymbolInformation shape; selectionRange/range/children
// mark the hierarchical DocumentSymbol shape.
type rawSymbol struct {
	Name           string      `json:"name"`
	Detail         string      `json:"detail"`
	Kind           int         `json:"kind"`
	Range          *rangeObj   `json:"range"`          // DocumentSymbol
	SelectionRange *rangeObj   `json:"selectionRange"` // DocumentSymbol
	Children       []rawSymbol `json:"children"`       // DocumentSymbol
	Location       *struct {   // SymbolInformation
		URI   string   `json:"uri"`
		Range rangeObj `json:"range"`
	} `json:"location"`
}

// parseDocumentSymbols decodes a textDocument/documentSymbol result (either shape)
// into a normalized []Symbol. Returns nil on a null/empty result.
func parseDocumentSymbols(raw json.RawMessage) []Symbol {
	if isJSONNull(raw) {
		return nil
	}
	var items []rawSymbol
	if json.Unmarshal(raw, &items) != nil || len(items) == 0 {
		return nil
	}
	out := make([]Symbol, 0, len(items))
	for _, it := range items {
		out = append(out, it.toSymbol())
	}
	return out
}

// toSymbol converts a rawSymbol to a Symbol, choosing the position source by shape:
// SymbolInformation uses location.range.start; DocumentSymbol prefers
// selectionRange.start and falls back to range.start. Children recurse (hierarchical
// shape only).
func (r rawSymbol) toSymbol() Symbol {
	s := Symbol{Name: r.Name, Kind: r.Kind, Detail: r.Detail}
	switch {
	case r.Location != nil: // flat SymbolInformation
		s.Line = r.Location.Range.Start.Line
		s.Char = r.Location.Range.Start.Character
	case r.SelectionRange != nil: // hierarchical, name range
		s.Line = r.SelectionRange.Start.Line
		s.Char = r.SelectionRange.Start.Character
	case r.Range != nil: // hierarchical fallback
		s.Line = r.Range.Start.Line
		s.Char = r.Range.Start.Character
	}
	if len(r.Children) > 0 {
		s.Children = make([]Symbol, 0, len(r.Children))
		for _, c := range r.Children {
			s.Children = append(s.Children, c.toSymbol())
		}
	}
	return s
}
