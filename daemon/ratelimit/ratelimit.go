// Package ratelimit detects rate-limit / quota conditions from an agent's own output — the universal
// signal available without provider-specific account APIs. Coding CLIs (Claude, Codex, opencode)
// surface "rate limit", "429", "quota exceeded", and often a reset hint ("try again in 2m", "retry
// after 30s", "resets at 3:45pm") in their output; parsing these lets the app show "rate limited —
// resets in N" and stop hammering, on ANY provider. Live quota RESET times straight from provider
// account APIs (Anthropic/OpenAI usage endpoints) are a separate, credentialed source; this covers
// the common case from data we already stream.
package ratelimit

import (
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Info describes a detected rate-limit condition.
type Info struct {
	Hit        bool          // a rate-limit/quota condition was detected on this line
	RetryAfter time.Duration // parsed "try again in / retry after N" (0 if none found)
	ResetHint  string        // a human reset hint verbatim (e.g. "3:45pm") when no duration parsed
	Raw        string        // the matched line, trimmed
}

var (
	// Trigger phrases. Word-boundary "429" avoids matching e.g. a port number 14290.
	triggerRE = regexp.MustCompile(`(?i)(rate[ -]?limit|quota (?:exceeded|reached)|too many requests|\b429\b|overloaded_error|usage limit|retry[ -]?after)`)
	// "retry after 30s", "try again in 2 minutes", "wait 45 seconds", "retry-after: 60"
	durationRE = regexp.MustCompile(`(?i)(?:retry[ -]?after|try again in|wait|resets? in)\D{0,4}(\d+)\s*(ms|milliseconds?|s|secs?|seconds?|m|mins?|minutes?|h|hours?)?`)
	// A clock/hint like "resets at 3:45pm" or "resets at 14:00 UTC".
	resetAtRE = regexp.MustCompile(`(?i)reset(?:s|ting)?\s+at\s+([0-9:apmAPM \tUTCZ+\-]{3,20})`)
)

// Parse inspects one line of agent output for a rate-limit condition. Returns Info with Hit=false
// when nothing matches.
func Parse(line string) Info {
	l := strings.TrimSpace(line)
	if l == "" || !triggerRE.MatchString(l) {
		return Info{}
	}
	info := Info{Hit: true, Raw: l}
	if m := durationRE.FindStringSubmatch(l); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil {
			info.RetryAfter = scale(n, m[2])
		}
	}
	if info.RetryAfter == 0 {
		if m := resetAtRE.FindStringSubmatch(l); m != nil {
			info.ResetHint = strings.TrimSpace(m[1])
		}
	}
	return info
}

// scale converts a number + unit into a Duration. A bare number (no unit) is treated as seconds
// (the common "retry-after: 60" header convention).
func scale(n int, unit string) time.Duration {
	d := time.Duration(n)
	switch strings.ToLower(strings.TrimSpace(unit)) {
	case "ms", "millisecond", "milliseconds":
		return d * time.Millisecond
	case "m", "min", "mins", "minute", "minutes":
		return d * time.Minute
	case "h", "hour", "hours":
		return d * time.Hour
	default: // s/sec/secs/seconds or empty
		return d * time.Second
	}
}
