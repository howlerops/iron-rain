package genui

import "strings"

// The generative-UI "skill" is delivered as a compact guide the daemon injects ONCE, on a session's
// first user turn, wrapped in sentinels so the app hides it from the transcript. Real lazy-loaded
// skills only exist in claude-code; injecting on turn 1 is the one mechanism every harness (down to a
// plain CLI reading stdin) supports, so the feature is uniform and non-invasive (no repo mutation,
// one-time token cost). The app strips everything between the sentinels from any displayed message.
const (
	GuideOpen  = "⟦iron:ui-guide⟧"
	GuideClose = "⟦/iron:ui-guide⟧"
)

// guide is the compact grammar taught to the agent — enough to emit any v1 component correctly, kept
// small (prompt-cacheable) and biased toward prose ("use UI only when it's clearer than text").
const guide = "You can render rich NATIVE UI inline by emitting a fenced block:\n" +
	"```iron:ui\n{\"component\":\"<type>\",\"id\":\"<unique>\",\"props\":{...},\"fallback_text\":\"<markdown>\"}\n```\n" +
	"Rules: one valid-JSON object per fence; put `component` and `id` first; ALWAYS include a short " +
	"`fallback_text` (markdown) describing it for clients that can't render the component. Prefer normal " +
	"markdown prose — use a component ONLY when structured/quantitative/selectable data is clearer as UI. " +
	"Catalog:\n" +
	"- table — props {columns:[{label,align?}], rows:[[cell,…]], caption?}\n" +
	"- checklist — props {title?, items:[{text, status:pending|active|done|failed}]}\n" +
	"- callout — props {level:info|warn|error|success, title?, body}\n" +
	"- diff — props {path, patch?}\n" +
	"- choice (interactive) — props {prompt}; actions:[{id, kind:\"prompt\", label, prompt:\"<the message sent AS THE USER'S REPLY when this option is tapped>\"}]\n" +
	"- confirm (interactive) — props {prompt}; actions:[{id, kind:\"prompt\", label, style:default|destructive, prompt:\"<reply text>\"}]\n" +
	"For interactive components each action's `prompt` becomes the user's next turn when tapped, so phrase it from the user's side. Emit at most a few components per message."

// Preamble returns the sentinel-wrapped guide to prepend to a session's first user turn. The trailing
// blank lines keep it visually separated from the user's actual prompt for the model.
func Preamble() string {
	return GuideOpen + "\n" + guide + "\n" + GuideClose + "\n\n"
}

// StripGuide removes any injected guide block from a piece of text (used server-side where useful; the
// app does the same for display). Safe on text with no guide.
func StripGuide(s string) string {
	for {
		i := strings.Index(s, GuideOpen)
		if i < 0 {
			return s
		}
		j := strings.Index(s, GuideClose)
		if j < 0 || j < i {
			return s
		}
		end := j + len(GuideClose)
		// Swallow trailing whitespace/newlines that followed the close sentinel.
		for end < len(s) && (s[end] == '\n' || s[end] == '\r' || s[end] == ' ' || s[end] == '\t') {
			end++
		}
		s = s[:i] + s[end:]
	}
}
