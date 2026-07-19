package lsp

import (
	"context"
	"encoding/json"
	"sort"
	"unicode/utf8"
)

type textEdit struct {
	Range   rangeObj `json:"range"`
	NewText string   `json:"newText"`
}

// Format runs textDocument/formatting and applies the returned edits to content, returning the
// formatted text and whether anything changed. content should match what the server last saw
// (the caller keeps the doc in sync via Change). No server / no edits → content unchanged.
func (m *Manager) Format(ctx context.Context, path, content string) (string, bool, error) {
	d, ok := m.lookupDoc(path)
	if !ok {
		return content, false, nil
	}
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	insertSpaces := d.langID != "go" // gofmt uses tabs; most others prefer spaces
	res, err := d.srv.call(ctx, "textDocument/formatting", map[string]interface{}{
		"textDocument": map[string]string{"uri": d.uri},
		"options":      map[string]interface{}{"tabSize": 4, "insertSpaces": insertSpaces},
	})
	if err != nil {
		return content, false, err
	}
	if isJSONNull(res) {
		return content, false, nil
	}
	var edits []textEdit
	if json.Unmarshal(res, &edits) != nil || len(edits) == 0 {
		return content, false, nil
	}
	out := applyTextEdits(content, edits)
	return out, out != content, nil
}

// applyTextEdits applies LSP TextEdits to content. Edits are applied from the end of the
// document backwards so earlier offsets stay valid. LSP positions are 0-based line + UTF-16
// character; content is UTF-8, so offsets are converted accordingly.
func applyTextEdits(content string, edits []textEdit) string {
	sort.SliceStable(edits, func(a, b int) bool {
		ea, eb := edits[a].Range.Start, edits[b].Range.Start
		if ea.Line != eb.Line {
			return ea.Line > eb.Line
		}
		return ea.Character > eb.Character
	})
	for _, e := range edits {
		s := byteOffsetFor(content, e.Range.Start.Line, e.Range.Start.Character)
		end := byteOffsetFor(content, e.Range.End.Line, e.Range.End.Character)
		if s < 0 || end < 0 || s > len(content) || end > len(content) || s > end {
			continue
		}
		content = content[:s] + e.NewText + content[end:]
	}
	return content
}

// byteOffsetFor converts a 0-based (line, UTF-16 character) position to a byte index in a
// UTF-8 string. Past-end positions clamp to len(content).
func byteOffsetFor(content string, line, char int) int {
	lineStart, curLine := 0, 0
	for i := 0; i < len(content) && curLine < line; i++ {
		if content[i] == '\n' {
			curLine++
			lineStart = i + 1
		}
	}
	if curLine < line {
		return len(content)
	}
	i, u16 := lineStart, 0
	for i < len(content) && u16 < char {
		r, size := utf8.DecodeRuneInString(content[i:])
		if r == '\n' {
			break
		}
		if r > 0xFFFF {
			u16 += 2 // surrogate pair in UTF-16
		} else {
			u16++
		}
		i += size
	}
	return i
}
