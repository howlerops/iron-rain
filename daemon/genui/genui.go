// Package genui turns a harness's assistant-text stream into normalized generative-UI components.
//
// It rides the ONE channel every harness has — assistant text — so it works identically on
// opencode/claude-code/pi and degrades losslessly on a plain text-only CLI. The model emits a
// reserved fenced block:
//
//	```iron:ui
//	{ "component": "table", "id": "t1", "props": { ... } }
//	```
//
// The Segmenter scans the stream incrementally (line-oriented, so a fence split across many deltas is
// handled), forwards the text OUTSIDE fences unchanged for live rendering, and on a fence CLOSE
// validates the JSON against the closed component catalog and returns a protocol.UIComponent. An
// invalid or unknown block is left in the forwarded text as an ordinary code block (never broken).
//
// The model emits DATA, never behavior: components are a fixed catalog with inert props; the client
// owns the native view. All parsing/validation/caps live here, once, not per-harness and not in the
// client.
package genui

import (
	"encoding/json"
	"strings"

	"github.com/howlerops/oculus/daemon/protocol"
)

// fenceInfo is the reserved fence info-string (```iron:ui).
const fenceInfo = "iron:ui"

// Resource caps — a hallucinated or malicious payload can't OOM the client. Enforced before a
// component is emitted; an over-cap block is dropped (left as forwarded text).
const (
	maxPayloadBytes = 64 * 1024
	maxRows         = 500
	maxCols         = 20
	maxOptions      = 50
)

// knownComponents is the closed catalog. An unknown component name is not emitted (degrades to text).
var knownComponents = map[string]bool{
	"table": true, "checklist": true, "plan": true, "callout": true,
	"diff": true, "choice": true, "confirm": true,
}

// Segmenter incrementally splits one session's assistant-text stream into forwardable text and
// generative-UI components. It is NOT safe for concurrent use — drive it from a single goroutine
// (the per-session event pump). Zero value is ready to use.
type Segmenter struct {
	line     strings.Builder // the current, not-yet-newline-terminated line
	inFence  bool
	fenceBuf strings.Builder // accumulated body while inside an iron:ui fence
}

// Feed consumes a chunk of assistant text and returns the text to forward downstream (fenced iron:ui
// blocks removed) plus any components parsed from fences closed by this chunk. Text with no trailing
// newline is held until the line completes, so a fence marker split across deltas is still detected.
func (s *Segmenter) Feed(chunk string) (forward string, comps []protocol.UIComponent) {
	var out strings.Builder
	for i := 0; i < len(chunk); i++ {
		c := chunk[i]
		s.line.WriteByte(c)
		if c == '\n' {
			f, comp, ok := s.consumeLine(s.line.String())
			s.line.Reset()
			out.WriteString(f)
			if ok {
				comps = append(comps, comp)
			}
		}
	}
	return out.String(), comps
}

// Flush finalizes the stream at turn end: it processes any trailing partial line, and if a fence was
// left open (the model never closed it) its buffered text is returned as-is so nothing is lost.
func (s *Segmenter) Flush() (forward string, comps []protocol.UIComponent) {
	var out strings.Builder
	if s.line.Len() > 0 {
		f, comp, ok := s.consumeLine(s.line.String())
		s.line.Reset()
		out.WriteString(f)
		if ok {
			comps = append(comps, comp)
		}
	}
	if s.inFence {
		// Unterminated fence: emit what we buffered as plain text (as a fenced block) so the user
		// still sees the content instead of it vanishing.
		out.WriteString("```" + fenceInfo + "\n")
		out.WriteString(s.fenceBuf.String())
		s.inFence = false
		s.fenceBuf.Reset()
	}
	return out.String(), nil
}

// consumeLine handles one complete line (including its trailing '\n', if any). It returns the text to
// forward, a component if this line CLOSED a valid fence, and whether that component is valid.
func (s *Segmenter) consumeLine(line string) (string, protocol.UIComponent, bool) {
	trimmed := strings.TrimRight(line, "\r\n")
	fence := strings.TrimSpace(trimmed)
	if s.inFence {
		if fence == "```" {
			// Close the fence: validate the buffered JSON.
			body := s.fenceBuf.String()
			s.inFence = false
			s.fenceBuf.Reset()
			if comp, ok := parseComponent(body); ok {
				return "", comp, true
			}
			// Invalid: fall back to a visible code block so nothing is hidden or broken.
			return "```" + fenceInfo + "\n" + body + "```\n", protocol.UIComponent{}, false
		}
		s.fenceBuf.WriteString(line)
		return "", protocol.UIComponent{}, false
	}
	if isFenceOpener(fence) {
		s.inFence = true
		s.fenceBuf.Reset()
		return "", protocol.UIComponent{}, false
	}
	// Lenient catch: a BARE one-line component JSON outside any fence. Models sometimes emit the
	// iron:ui payload without the fence (seen in the wild: a raw {"component":"table",...} line
	// printed as text). If the whole line parses as a valid known component, render it as one —
	// anything that doesn't fully validate falls through untouched as ordinary text.
	if strings.HasPrefix(fence, `{"component"`) && strings.HasSuffix(fence, "}") {
		if comp, ok := parseComponent(fence); ok {
			return "", comp, true
		}
	}
	return line, protocol.UIComponent{}, false
}

// isFenceOpener reports whether a trimmed line opens an iron:ui fence (```iron:ui, tolerating extra
// backticks/whitespace).
func isFenceOpener(fence string) bool {
	if !strings.HasPrefix(fence, "```") {
		return false
	}
	info := strings.TrimSpace(strings.TrimLeft(fence, "`"))
	return info == fenceInfo
}

// fenceComponent is the on-the-wire JSON a model emits inside an iron:ui fence.
type fenceComponent struct {
	Component    string             `json:"component"`
	ID           string             `json:"id"`
	SchemaV      int                `json:"schema_v"`
	Props        json.RawMessage    `json:"props"`
	Actions      []protocol.UIAction `json:"actions"`
	FallbackText string             `json:"fallback_text"`
}

// parseComponent strictly parses + validates a fence body into a normalized UIComponent. It returns
// ok=false (drop, keep as text) for invalid JSON, an unknown component, an over-cap payload, or a
// missing id. SessionID/MessageID are filled by the caller.
func parseComponent(body string) (protocol.UIComponent, bool) {
	if len(body) > maxPayloadBytes {
		return protocol.UIComponent{}, false
	}
	var fc fenceComponent
	if err := json.Unmarshal([]byte(strings.TrimSpace(body)), &fc); err != nil {
		return protocol.UIComponent{}, false
	}
	if !knownComponents[fc.Component] || fc.ID == "" {
		return protocol.UIComponent{}, false
	}
	if !propsWithinCaps(fc.Component, fc.Props) {
		return protocol.UIComponent{}, false
	}
	schemaV := fc.SchemaV
	if schemaV == 0 {
		schemaV = 1
	}
	fallback := fc.FallbackText
	if fallback == "" {
		fallback = "*(this client can't render the “" + fc.Component + "” component)*"
	}
	return protocol.UIComponent{
		ID:           fc.ID,
		Component:    fc.Component,
		SchemaV:      schemaV,
		Status:       "ready",
		Props:        fc.Props,
		Actions:      fc.Actions,
		FallbackText: fallback,
	}, true
}

// propsWithinCaps enforces per-component size caps so a giant hallucinated payload can't overwhelm
// the client. Unknown shapes pass (already gated by knownComponents + byte cap).
func propsWithinCaps(component string, props json.RawMessage) bool {
	switch component {
	case "table":
		var p struct {
			Columns []json.RawMessage `json:"columns"`
			Rows    []json.RawMessage `json:"rows"`
		}
		if err := json.Unmarshal(props, &p); err != nil {
			return false
		}
		return len(p.Columns) <= maxCols && len(p.Rows) <= maxRows
	case "choice", "confirm":
		var p struct {
			Options []json.RawMessage `json:"options"`
		}
		_ = json.Unmarshal(props, &p)
		return len(p.Options) <= maxOptions
	}
	return true
}
