package lsp

import (
	"context"
	"encoding/json"
)

// maxReferences caps the number of reference locations returned so a symbol with
// thousands of uses can't flood the editor.
const maxReferences = 500

// referenceContext is the LSP ReferenceContext; includeDeclaration asks the server
// to include the symbol's own declaration among the results.
type referenceContext struct {
	IncludeDeclaration bool `json:"includeDeclaration"`
}

// referenceParams is textDocument/references params: a position plus a context.
type referenceParams struct {
	TextDocument textDocumentIdentifier `json:"textDocument"`
	Position     position               `json:"position"`
	Context      referenceContext       `json:"context"`
}

// References returns every reference to the symbol at a 0-based position, including
// its declaration. Returns nil (no error) when the file has no server or the server
// reports no references.
func (m *Manager) References(ctx context.Context, path string, line, char int) ([]Location, error) {
	d, ok := m.lookupDoc(path)
	if !ok {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	res, err := d.srv.call(ctx, "textDocument/references", referenceParams{
		TextDocument: textDocumentIdentifier{URI: d.uri},
		Position:     position{Line: line, Character: char},
		Context:      referenceContext{IncludeDeclaration: true},
	})
	if err != nil {
		return nil, err
	}
	return parseReferences(res), nil
}

// parseReferences decodes a textDocument/references result (Location[] or null) into
// a capped slice of lsp.Location. Positions come from each Location's range.start.
func parseReferences(raw json.RawMessage) []Location {
	if isJSONNull(raw) {
		return nil
	}
	var locs []struct {
		URI   string   `json:"uri"`
		Range rangeObj `json:"range"`
	}
	if json.Unmarshal(raw, &locs) != nil || len(locs) == 0 {
		return nil
	}
	out := make([]Location, 0, len(locs))
	for _, l := range locs {
		if l.URI == "" {
			continue
		}
		out = append(out, Location{
			Path:      uriToPath(l.URI),
			StartLine: l.Range.Start.Line,
			StartChar: l.Range.Start.Character,
		})
		if len(out) >= maxReferences {
			break
		}
	}
	return out
}
