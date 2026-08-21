// Package textutil holds the string shaping used on text that reaches a UI — truncation,
// clipping, and the like.
//
// It exists because the same bug kept being rewritten. Five call sites across the daemon capped a
// string with a plain s[:n], which slices BYTES: any multi-byte character straddling the limit is
// severed, and the resulting invalid UTF-8 is silently rewritten to replacement characters when the
// event is JSON-encoded. Agent prose is dense with em-dashes, curly quotes and emoji, so the cut
// lands mid-rune often — that is where truncated summaries and tool cards picked up mojibake tails.
package textutil

import (
	"strings"
	"unicode/utf8"
)

// Ellipsis is appended to anything Trunc shortens.
const Ellipsis = "…"

// Trunc caps s at n BYTES without splitting a rune, appending an ellipsis if it shortened anything.
//
// The limit stays a byte count because callers are protecting a wire/storage budget, not a column
// width; backing up to a rune boundary only ever makes the result shorter, never longer.
func Trunc(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n] + Ellipsis
}

// Clip trims surrounding whitespace and then truncates. The trim happens first so a value that is
// only long because of padding is left intact rather than cut.
func Clip(s string, n int) string {
	return Trunc(strings.TrimSpace(s), n)
}

// FirstLine reduces s to its first line, clipped to n bytes — the shape wanted for a one-line
// description or a tool-card label, where a stray newline would break the row it renders into.
func FirstLine(s string, n int) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return Trunc(strings.TrimSpace(s), n)
}
