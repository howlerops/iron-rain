package agent

import (
	"encoding/json"
	"strings"
)

// DiffStat counts the added and removed lines in a unified diff.
//
// It is deliberately conservative. Harnesses report an edit's result in whatever shape they like —
// a patch, a prose confirmation, a JSON blob with the diff nested somewhere inside — and the counts
// feed a badge on a tool card. A badge that is sometimes absent is fine; a badge that is confidently
// wrong is worse than none, so anything we can't recognise as a diff returns (0, 0), which the
// client reads as "unknown" and renders as nothing.
//
// Rules, in the order they matter:
//   - `+++`/`---` file headers are NOT content lines. Counting them (the obvious bug here) inflates
//     every single-file edit by exactly one add and one delete.
//   - `@@` hunk headers, `diff --git`, `index`, and `\ No newline` markers are skipped.
//   - A body with no `@@` hunk header at all is not treated as a diff. Plenty of tool output is
//     prose that happens to contain lines starting with "-" (bullet lists are the common case), and
//     counting those turns a README edit into "+40 −12" out of nowhere.
func DiffStat(patch string) (adds, dels int) {
	if !strings.Contains(patch, "@@") {
		return 0, 0
	}
	inHunk := false
	for _, line := range strings.Split(patch, "\n") {
		switch {
		case strings.HasPrefix(line, "@@"):
			inHunk = true
		case strings.HasPrefix(line, "diff --git"), strings.HasPrefix(line, "index "):
			inHunk = false
		case strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"):
			// File headers, not content — and they only appear outside a hunk body.
			if !inHunk {
				continue
			}
			// Inside a hunk, "---" can legitimately be a removed line of content (e.g. a markdown
			// horizontal rule or YAML separator), so fall through to the normal counting below.
			if strings.HasPrefix(line, "+") {
				adds++
			} else {
				dels++
			}
		case strings.HasPrefix(line, `\ No newline`):
			// Not a content line.
		case inHunk && strings.HasPrefix(line, "+"):
			adds++
		case inHunk && strings.HasPrefix(line, "-"):
			dels++
		}
	}
	return adds, dels
}

// DiffStatFrom finds a diff in any of the given candidates and counts it, returning the first one
// that looks like a real patch. Candidates are tried in order, so pass the most authoritative source
// first (provider metadata before free-text output).
//
// A candidate may be a raw patch OR a JSON object with the patch nested inside it under a plausible
// key — which is how every provider that reports a diff at all has chosen to do it so far.
func DiffStatFrom(candidates ...string) (adds, dels int) {
	for _, c := range candidates {
		if c == "" {
			continue
		}
		if a, d := DiffStat(c); a > 0 || d > 0 {
			return a, d
		}
		if a, d := DiffStat(diffInJSON(c)); a > 0 || d > 0 {
			return a, d
		}
	}
	return 0, 0
}

// diffKeys are the field names providers use for a patch, cheapest-to-guess first.
var diffKeys = []string{"diff", "patch", "unified_diff", "unifiedDiff", "changes"}

// diffInJSON pulls a patch out of a JSON object, searching nested objects too (opencode nests the
// edit result under `state`/`metadata` depending on version). Returns "" for anything that isn't
// JSON, which DiffStat then rejects.
func diffInJSON(raw string) string {
	var any map[string]json.RawMessage
	if json.Unmarshal([]byte(raw), &any) != nil {
		return ""
	}
	return searchJSON(any, 0)
}

func searchJSON(obj map[string]json.RawMessage, depth int) string {
	if depth > 4 { // provider payloads are shallow; this only bounds a pathological one
		return ""
	}
	for _, k := range diffKeys {
		if v, ok := obj[k]; ok {
			var s string
			if json.Unmarshal(v, &s) == nil && strings.Contains(s, "@@") {
				return s
			}
		}
	}
	for _, v := range obj {
		var nested map[string]json.RawMessage
		if json.Unmarshal(v, &nested) != nil {
			continue
		}
		if found := searchJSON(nested, depth+1); found != "" {
			return found
		}
	}
	return ""
}
