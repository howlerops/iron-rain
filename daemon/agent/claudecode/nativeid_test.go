package claudecode

import "testing"

// TestNativeSessionID covers the three states the take-over dedupe depends on. The Provider is built
// by hand rather than with New(): New() loads (and setResume would REWRITE) the real
// ~/.oculus/claude-resume.json, and a test must not touch the user's live resume map.
func TestNativeSessionID(t *testing.T) {
	const uuid = "11111111-2222-3333-4444-555555555555"

	// A session we created: our id is cc_…, claude's UUID arrived on the sidecar's init message.
	mapped := &session{id: "cc_1", p: &Provider{resume: map[string]string{"cc_1": uuid}}}
	if got := mapped.NativeSessionID(); got != uuid {
		t.Errorf("mapped session reported %q, want claude's uuid %q — discovery would offer it for take-over again", got, uuid)
	}

	// A discovered session we took over: our id already IS claude's uuid, and no resume entry exists
	// for it (setResume skips id == uuid), so the mapping alone would report nothing.
	taken := &session{id: uuid, p: &Provider{resume: map[string]string{}}}
	if got := taken.NativeSessionID(); got != uuid {
		t.Errorf("taken-over session reported %q, want %q", got, uuid)
	}

	// A restart that lost the map (or a session whose sidecar hasn't reported yet): UNKNOWN. It must
	// report empty so the hub filters nothing, not a wrong id that would hide real candidates.
	unmapped := &session{id: "cc_2", p: &Provider{resume: map[string]string{}}}
	if got := unmapped.NativeSessionID(); got != "" {
		t.Errorf("session with no known uuid reported %q, want empty", got)
	}
}
