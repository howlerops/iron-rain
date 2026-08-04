package hub

import (
	"context"
	"testing"

	"github.com/howlerops/oculus/daemon/agent"
	"github.com/howlerops/oculus/daemon/protocol"
)

// fakeNativeSess is a managed session that knows the id its PROVIDER uses for it (the claude-code
// shape: ours is cc_…, claude's is the transcript UUID). native == "" models the cases where that id
// is genuinely unknown — the sidecar hasn't reported it yet, or a restart lost the resume map.
type fakeNativeSess struct {
	id       string
	provider string
	native   string
	events   chan agent.Event
}

func (s *fakeNativeSess) ID() string                                    { return s.id }
func (s *fakeNativeSess) Provider() string                              { return s.provider }
func (s *fakeNativeSess) Events() <-chan agent.Event                    { return s.events }
func (s *fakeNativeSess) Prompt(context.Context, string) error          { return nil }
func (s *fakeNativeSess) Respond(context.Context, string, string) error { return nil }
func (s *fakeNativeSess) Stop(context.Context) error                    { return nil }
func (s *fakeNativeSess) Close() error                                  { return nil }
func (s *fakeNativeSess) NativeSessionID() string                       { return s.native }

// manage registers a session with the hub without the persistence/heal side effects of addSession —
// this test is about the discover filter, not about how a session got there.
func manage(t *testing.T, h *Hub, sess agent.Session) {
	t.Helper()
	m := newManagedSession(h, sess, sessionMeta{})
	h.mu.Lock()
	h.sessions[sess.ID()] = m
	h.mu.Unlock()
}

// TestDiscoverDropsManagedClaudeUUID: a claude-code session the daemon already drives is discovered
// under claude's UUID, never under our cc_ id, so it used to come back in the take-over list looking
// like an untouched terminal session — and taking it over a second time forks the conversation into
// two writers. Everything that is NOT that session has to survive, including a row of another provider
// carrying the same id, which is a different session entirely.
func TestDiscoverDropsManagedClaudeUUID(t *testing.T) {
	const uuid = "11111111-2222-3333-4444-555555555555"
	h := New()
	manage(t, h, &fakeNativeSess{id: "cc_1", provider: "claude-code", native: uuid})

	items := []protocol.Discovered{
		{Provider: "claude-code", Kind: protocol.KindSession, SessionID: uuid, Cwd: "/tmp/proj"},
		{Provider: "claude-code", Kind: protocol.KindSession, SessionID: "99999999-8888-7777-6666-555555555555"},
		{Provider: "opencode", Kind: protocol.KindServer, URL: "http://127.0.0.1:4096"},
		{Provider: "opencode", Kind: protocol.KindSession, SessionID: uuid, URL: "http://127.0.0.1:4096"},
	}
	got := h.dropAlreadyManaged(items)

	if len(got) != 3 {
		t.Fatalf("want 3 rows after dedupe, got %d: %+v", len(got), got)
	}
	for _, it := range got {
		if it.Provider == "claude-code" && it.SessionID == uuid {
			t.Fatalf("the managed session was still offered for take-over: %+v", it)
		}
	}
	if got[0].SessionID != "99999999-8888-7777-6666-555555555555" {
		t.Errorf("an unrelated claude session was dropped; rows = %+v", got)
	}
	if got[2].Provider != "opencode" || got[2].SessionID != uuid {
		t.Errorf("a same-id row of ANOTHER provider was dropped; rows = %+v", got)
	}
	if len(items) != 4 || items[0].SessionID != uuid {
		t.Errorf("the scan's own slice was filtered in place: %+v", items)
	}
}

// TestDiscoverKeepsRowsWhenUUIDUnknown: after a restart that lost the resume map (or before a new
// session's sidecar has reported its UUID) the provider cannot name its own id. That must mean "don't
// dedupe" — the pre-existing duplicate — and never "matches the empty id", which would delete every
// take-over candidate the user actually has.
func TestDiscoverKeepsRowsWhenUUIDUnknown(t *testing.T) {
	const uuid = "11111111-2222-3333-4444-555555555555"
	h := New()
	manage(t, h, &fakeNativeSess{id: "cc_1", provider: "claude-code", native: ""})

	items := []protocol.Discovered{
		{Provider: "claude-code", Kind: protocol.KindSession, SessionID: uuid},
		{Provider: "claude-code", Kind: protocol.KindSession, SessionID: ""},
	}
	if got := h.dropAlreadyManaged(items); len(got) != 2 {
		t.Fatalf("an unmapped session filtered rows it cannot possibly identify: %+v", got)
	}
}

// noNativeSess is a provider whose ids the daemon does NOT rewrite (opencode and friends): it has no
// second id to report, so it deliberately does not implement the capability.
type noNativeSess struct {
	id       string
	provider string
	events   chan agent.Event
}

func (s *noNativeSess) ID() string                                    { return s.id }
func (s *noNativeSess) Provider() string                              { return s.provider }
func (s *noNativeSess) Events() <-chan agent.Event                    { return s.events }
func (s *noNativeSess) Prompt(context.Context, string) error          { return nil }
func (s *noNativeSess) Respond(context.Context, string, string) error { return nil }
func (s *noNativeSess) Stop(context.Context) error                    { return nil }
func (s *noNativeSess) Close() error                                  { return nil }

// TestDiscoverIgnoresSessionsWithoutNativeID: asserting the capability on such a provider must be a
// no-op, not a panic — and its row stays, because there the daemon id and the discovered id are the
// same string, which the client's own exact-id match already drops.
func TestDiscoverIgnoresSessionsWithoutNativeID(t *testing.T) {
	h := New()
	manage(t, h, &noNativeSess{id: "ses_x", provider: "opencode"})

	items := []protocol.Discovered{{Provider: "opencode", Kind: protocol.KindSession, SessionID: "ses_x"}}
	if got := h.dropAlreadyManaged(items); len(got) != 1 {
		t.Fatalf("a provider without the capability changed the result: %+v", got)
	}
}
