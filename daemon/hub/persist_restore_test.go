package hub

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/agent"
	"github.com/howlerops/oculus/daemon/agent/opencode"
	"github.com/howlerops/oculus/daemon/protocol"
	"github.com/howlerops/oculus/daemon/store"
)

// ocStub is a minimal `opencode serve` good enough for an attach: an open (silent) /event stream,
// a session lookup that reports the session's real directory, and an empty message history.
// knownSession == "" makes every session lookup 404 — the shape of "this server has never heard of
// that session", which is what a restore hits when the wrong opencode is asked.
type ocStub struct {
	knownSession string
	dir          string
	hits         atomic.Int64
}

func (s *ocStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.hits.Add(1)
	switch {
	case r.URL.Path == "/event":
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if fl, ok := w.(http.Flusher); ok {
			fl.Flush()
		}
		<-r.Context().Done()
	case r.URL.Path == "/session/"+s.knownSession && s.knownSession != "":
		_ = json.NewEncoder(w).Encode(map[string]any{"id": s.knownSession, "directory": s.dir})
	case r.URL.Path == "/session/"+s.knownSession+"/message" && s.knownSession != "":
		_, _ = w.Write([]byte(`[]`))
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// restoreHub returns a hub backed by a fresh store, wired with main.go's real attacher factory
// (opencode + a URL), so the restore path under test is the one that ships.
func restoreHub(t *testing.T) (*Hub, *store.Store) {
	t.Helper()
	db, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	h := New()
	h.SetStore(db)
	h.SetAttacherFactory(func(provider, url string) agent.Attacher {
		if provider == "opencode" && url != "" {
			return opencode.New(url)
		}
		return nil
	})
	return h, db
}

func saveRecord(t *testing.T, db *store.Store, id, provider string, pm persistedMeta) {
	t.Helper()
	blob, err := json.Marshal(pm)
	if err != nil {
		t.Fatal(err)
	}
	rec := store.SessionRecord{ID: id, Provider: provider, Cwd: pm.Cwd, Meta: string(blob)}
	if err := db.SaveSession(rec, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
}

// TestRestoreAttachesToPersistedProviderURL: a session taken over from a terminal lives on the
// opencode server that terminal started — NOT necessarily the one the daemon was configured with.
// The daemon self-updates every ~6 hours, so a restart is routine; if the restore re-attaches to
// the daemon's default server the session opens against a server that never heard of it, and every
// send silently goes nowhere. The persisted URL must decide which server we re-attach to.
func TestRestoreAttachesToPersistedProviderURL(t *testing.T) {
	const sid = "ses_takenover"
	right := &ocStub{knownSession: sid, dir: "/Users/x/proj"}
	rightSrv := httptest.NewServer(right)
	t.Cleanup(rightSrv.Close) // registered FIRST so it runs LAST: the session must close before the server waits on its /event connection
	wrong := &ocStub{knownSession: sid, dir: "/Users/x/proj"}
	wrongSrv := httptest.NewServer(wrong)
	t.Cleanup(wrongSrv.Close)

	h, db := restoreHub(t)
	// The daemon's OWN configured opencode is the wrong one — only the persisted URL knows better.
	h.Register(opencode.New(wrongSrv.URL))
	saveRecord(t, db, sid, "opencode", persistedMeta{Cwd: "/Users/x/proj", ProviderURL: rightSrv.URL})

	h.RestoreSessions(context.Background(), 7*24*time.Hour)
	t.Cleanup(func() {
		if m := h.managed(sid); m != nil {
			_ = m.sess.Close()
		}
	})

	if m := h.managed(sid); m == nil {
		t.Fatalf("session %s was not restored at all", sid)
	}
	if right.hits.Load() == 0 {
		t.Errorf("the persisted opencode server saw no requests — the restore attached somewhere else")
	}
	if n := wrong.hits.Load(); n != 0 {
		t.Errorf("the daemon's default opencode server saw %d request(s) — the restore ignored the persisted URL and re-attached to the wrong server", n)
	}
}

type urlProvider struct{ url string }

func (p *urlProvider) Name() string { return "url-agent" }
func (p *urlProvider) BaseURL() string {
	return p.url
}
func (p *urlProvider) Create(context.Context, string, string) (agent.Session, error) {
	return &fakeAttachedSess{id: "url_sess", provider: p.Name(), events: make(chan agent.Event)}, nil
}
func (p *urlProvider) List(context.Context) ([]protocol.Session, error) { return nil, nil }

func TestCreatedSessionPersistsProviderURL(t *testing.T) {
	h, db := restoreHub(t)
	h.Register(&urlProvider{url: "http://127.0.0.1:49001"})

	m, err := h.startSession(context.Background(), protocol.SessionCreate{Provider: "url-agent", Cwd: "/repo"}, sessionMeta{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.sess.Close() })

	recs, err := db.Sessions()
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 1 {
		t.Fatalf("records = %+v, want one", recs)
	}
	var pm persistedMeta
	if err := json.Unmarshal([]byte(recs[0].Meta), &pm); err != nil {
		t.Fatal(err)
	}
	if pm.ProviderURL != "http://127.0.0.1:49001" {
		t.Fatalf("provider_url = %q, want exact server URL for restore", pm.ProviderURL)
	}
}

// TestRestoreAttachFailureYieldsStoppedRestartable: the failure that must never happen quietly.
// opencode's /event stream accepts ANY subscriber, so a restore against a server that doesn't hold
// the session still "succeeds" — producing a live-looking row with no history whose sends vanish.
// Directory resolution is the proof the server actually knows the session; without it the record
// must stay stopped + restartable so the user is told, and offered a way forward.
func TestRestoreAttachFailureYieldsStoppedRestartable(t *testing.T) {
	const sid = "ses_gone"
	stranger := &ocStub{} // /event answers, but no session lookup ever resolves
	srv := httptest.NewServer(stranger)
	t.Cleanup(srv.Close)

	h, db := restoreHub(t)
	// Register it as the daemon's opencode too, so the restore genuinely HAS an attacher for this
	// provider — otherwise the record would fall through to "stopped" for the trivial reason that
	// nothing could attach, and the test would prove nothing.
	h.Register(opencode.New(srv.URL))
	saveRecord(t, db, sid, "opencode", persistedMeta{Cwd: "/Users/x/proj", ProviderURL: srv.URL})

	h.RestoreSessions(context.Background(), 7*24*time.Hour)
	t.Cleanup(func() {
		if m := h.managed(sid); m != nil {
			_ = m.sess.Close()
		}
	})

	if m := h.managed(sid); m != nil {
		t.Fatalf("session %s came back LIVE against a server that can't resolve it — it will look attached and silently swallow every send", sid)
	}
	var found *protocol.Session
	for _, s := range h.stoppedSessions() {
		if s.ID == sid {
			cp := s
			found = &cp
		}
	}
	if found == nil {
		t.Fatalf("session %s vanished entirely; it must remain listed as stopped", sid)
	}
	if found.Status != protocol.StatusStopped || !found.Restartable {
		t.Errorf("status=%q restartable=%v, want stopped+restartable", found.Status, found.Restartable)
	}
}

// cwdRecorder is an Attacher that records the cwd it was handed (the claude-code shape: resume runs
// as a fresh process in whatever directory it's given).
type cwdRecorder struct{ got string }

func (a *cwdRecorder) Attach(_ context.Context, id, cwd string) (agent.Session, error) {
	a.got = cwd
	return &fakeAttachedSess{id: id, provider: "claude-code", events: make(chan agent.Event)}, nil
}

type fakeAttachedSess struct {
	id       string
	provider string
	events   chan agent.Event
	model    string
	mprov    string
	resumed  bool
}

func (s *fakeAttachedSess) ID() string                                    { return s.id }
func (s *fakeAttachedSess) Provider() string                              { return s.provider }
func (s *fakeAttachedSess) Events() <-chan agent.Event                    { return s.events }
func (s *fakeAttachedSess) Prompt(context.Context, string) error          { return nil }
func (s *fakeAttachedSess) Respond(context.Context, string, string) error { return nil }
func (s *fakeAttachedSess) Stop(context.Context) error                    { return nil }
func (s *fakeAttachedSess) Close() error                                  { close(s.events); return nil }
func (s *fakeAttachedSess) SetModel(provider, model string) error {
	s.mprov, s.model = provider, model
	return nil
}
func (s *fakeAttachedSess) MarkResumed() { s.resumed = true }

// TestRestoreResumesWithPersistedCwd: claude-code's resume is a FRESH process — it edits whatever
// directory it is started in, not the one the conversation belongs to. The persisted meta is the
// only record of that directory, so the restore must read it BEFORE attaching. It used to be
// unmarshalled afterwards, so nothing but the (older, coarser) record column reached the sidecar.
func TestRestoreResumesWithPersistedCwd(t *testing.T) {
	h, db := restoreHub(t)
	rec := &cwdRecorder{}
	h.SetAttacherFactory(func(provider, url string) agent.Attacher {
		if provider == "claude-code" {
			return rec
		}
		return nil
	})
	// The meta blob is authoritative; the record column is stale/empty (an older daemon wrote it).
	blob, _ := json.Marshal(persistedMeta{Cwd: "/Users/x/the-right-project"})
	if err := db.SaveSession(store.SessionRecord{
		ID: "cc_1", Provider: "claude-code", Cwd: "", Meta: string(blob),
	}, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}

	h.RestoreSessions(context.Background(), 7*24*time.Hour)
	t.Cleanup(func() {
		if m := h.managed("cc_1"); m != nil {
			_ = m.sess.Close()
		}
	})

	if rec.got != "/Users/x/the-right-project" {
		t.Fatalf("attached with cwd %q, want the persisted project directory — the sidecar would edit the wrong repo", rec.got)
	}
}

// TestRestoreReappliesPersistedModel: a restored session must come back on the model the user chose.
// Nothing re-applied it, so every restart silently moved the conversation onto the provider default.
func TestRestoreReappliesPersistedModel(t *testing.T) {
	h, db := restoreHub(t)
	rec := &cwdRecorder{}
	h.SetAttacherFactory(func(provider, url string) agent.Attacher {
		if provider == "claude-code" {
			return rec
		}
		return nil
	})
	saveRecord(t, db, "cc_2", "claude-code", persistedMeta{Cwd: "/x", Model: "opus", ModelProvider: "anthropic"})

	h.RestoreSessions(context.Background(), 7*24*time.Hour)
	m := h.managed("cc_2")
	if m == nil {
		t.Fatal("session was not restored")
	}
	t.Cleanup(func() { _ = m.sess.Close() })
	fs := m.sess.(*fakeAttachedSess)
	if fs.model != "opus" || fs.mprov != "anthropic" {
		t.Errorf("restored session runs on model %q/%q, want the persisted anthropic/opus", fs.mprov, fs.model)
	}
	m.mu.Lock()
	shown := m.model
	m.mu.Unlock()
	if shown != "opus" {
		t.Errorf("session reports model %q — the app would show the wrong one", shown)
	}
}

func TestRestoreReappliesPersistedModeAndRoots(t *testing.T) {
	h, db := restoreHub(t)
	rec := &cwdRecorder{}
	h.SetAttacherFactory(func(provider, url string) agent.Attacher {
		if provider == "claude-code" {
			return rec
		}
		return nil
	})
	roots := []string{"/repo/api", "/repo/web"}
	saveRecord(t, db, "cc_roots", "claude-code", persistedMeta{
		Cwd: "/repo", Roots: roots, Mode: protocol.ModeAsk,
	})

	h.RestoreSessions(context.Background(), 7*24*time.Hour)
	m := h.managed("cc_roots")
	if m == nil {
		t.Fatal("session was not restored")
	}
	t.Cleanup(func() { _ = m.sess.Close() })
	m.mu.Lock()
	gotMode := m.mode
	gotRoots := append([]string(nil), m.meta.roots...)
	m.mu.Unlock()
	if gotMode != protocol.ModeAsk {
		t.Errorf("restored mode = %q, want %q", gotMode, protocol.ModeAsk)
	}
	if len(gotRoots) != len(roots) || gotRoots[0] != roots[0] || gotRoots[1] != roots[1] {
		t.Errorf("restored roots = %+v, want %+v", gotRoots, roots)
	}
}

func TestSessionListHidesChildSessions(t *testing.T) {
	h := New()
	parent := h.addSession(&fakeAttachedSess{id: "parent", provider: "fake", events: make(chan agent.Event)}, sessionMeta{cwd: "/repo"})
	child := h.addSession(&fakeAttachedSess{id: "child", provider: "fake", events: make(chan agent.Event)}, sessionMeta{cwd: "/repo", parentID: "parent", subtask: "subtask"})
	t.Cleanup(func() {
		_ = parent.sess.Close()
		_ = child.sess.Close()
	})

	list := h.sessionList()
	if len(list) != 1 {
		t.Fatalf("session list = %+v, want only the primary session", list)
	}
	if list[0].ID != "parent" {
		t.Fatalf("listed session = %q, want parent", list[0].ID)
	}
}

// resumeProvider is a Provider whose Create returns a session that records whether the hub told it
// it CONTINUES an earlier conversation (the CLI/pi restart-amnesia signal).
type resumeProvider struct{ last *fakeAttachedSess }

func (p *resumeProvider) Name() string { return "codex" }
func (p *resumeProvider) Create(_ context.Context, cwd, prompt string) (agent.Session, error) {
	p.last = &fakeAttachedSess{id: "codex_new", provider: "codex", events: make(chan agent.Event)}
	return p.last, nil
}
func (p *resumeProvider) List(context.Context) ([]protocol.Session, error) { return nil, nil }

// TestRestartMarksResumedWhenTheSessionHadTurns: a CLI agent has no server-side session, so a
// restart re-runs the command — and with no memory of having talked before it re-runs the agent's
// COLD invocation, dropping the whole conversation. When durable history exists the restart must
// tell the new session it is continuing, so a configured ResumeArgs is used from the first turn.
func TestRestartMarksResumedWhenTheSessionHadTurns(t *testing.T) {
	h, db := restoreHub(t)
	p := &resumeProvider{}
	h.Register(p)
	saveRecord(t, db, "codex_old", "codex", persistedMeta{Cwd: t.TempDir()})
	if _, err := db.AppendTranscript("codex_old", 1, "m1", []byte(`{"type":"session.message"}`)); err != nil {
		t.Fatal(err)
	}

	m, err := h.restartSession(context.Background(), "codex_old")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.sess.Close() })
	if p.last == nil || !p.last.resumed {
		t.Fatal("restarted session was not told it continues a prior conversation — a resume-capable CLI agent starts cold and the history is gone")
	}
}

// TestRestartOfAVirginSessionIsNotMarkedResumed: the mirror image — a session that never produced a
// turn has nothing to resume, and passing an agent's resume flags with no prior session makes it
// fail to start at all.
func TestRestartOfAVirginSessionIsNotMarkedResumed(t *testing.T) {
	h, db := restoreHub(t)
	p := &resumeProvider{}
	h.Register(p)
	saveRecord(t, db, "codex_virgin", "codex", persistedMeta{Cwd: t.TempDir()})

	m, err := h.restartSession(context.Background(), "codex_virgin")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.sess.Close() })
	if p.last != nil && p.last.resumed {
		t.Fatal("a session with no history was restarted in resume mode")
	}
}

// errAttachUnavailable stands in for the live opencode case: the server that owned the session is
// gone, so it cannot be re-attached.
var errAttachUnavailable = errors.New("server no longer holds this session")

// attachResumeProvider can genuinely CONTINUE a conversation (pi, claude-code, opencode all can), and
// records which path restart chose.
type attachResumeProvider struct {
	canResume  bool
	attachErr  error
	attachedID string
	created    bool
}

func (p *attachResumeProvider) Name() string { return "opencode" }
func (p *attachResumeProvider) Create(_ context.Context, cwd, prompt string) (agent.Session, error) {
	p.created = true
	return &fakeAttachedSess{id: "opencode_new", provider: "opencode", events: make(chan agent.Event)}, nil
}
func (p *attachResumeProvider) List(context.Context) ([]protocol.Session, error) { return nil, nil }
func (p *attachResumeProvider) CanResume(string) bool                            { return p.canResume }
func (p *attachResumeProvider) Attach(_ context.Context, id, cwd string) (agent.Session, error) {
	if p.attachErr != nil {
		return nil, p.attachErr
	}
	p.attachedID = id
	// A real resume keeps the ORIGINAL id — that is what leaves the transcript, name and record all
	// still matching.
	return &fakeAttachedSess{id: id, provider: "opencode", events: make(chan agent.Event)}, nil
}

// Restart must prefer a real resume over a fresh session. Before this, restart always called Create
// and leaned on an untyped MarkResumed assertion that only the generic cli adapter implements — so
// opencode, claude-code and pi silently restarted cold, giving the user an agent with no memory of
// the conversation displayed above it.
func TestRestartPrefersResumeOverAFreshSession(t *testing.T) {
	h, db := restoreHub(t)
	p := &attachResumeProvider{canResume: true}
	h.Register(p)
	saveRecord(t, db, "oc_old", "opencode", persistedMeta{Cwd: t.TempDir()})
	if _, err := db.AppendTranscript("oc_old", 1, "m1", []byte(`{"type":"session.message"}`)); err != nil {
		t.Fatal(err)
	}

	m, err := h.restartSession(context.Background(), "oc_old")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.sess.Close() })

	if p.created {
		t.Fatal("restart created a fresh session even though the conversation was resumable")
	}
	if p.attachedID != "oc_old" {
		t.Fatalf("attached %q, want the original id", p.attachedID)
	}
	if m.sess.ID() != "oc_old" {
		t.Fatalf("resumed session id = %q, want the original — history is keyed by it", m.sess.ID())
	}
}

// When the provider says the conversation is NOT recoverable, restart must not attach: doing so
// would produce a brand-new conversation wearing the old id.
func TestRestartDoesNotAttachWhenNotResumable(t *testing.T) {
	h, db := restoreHub(t)
	p := &attachResumeProvider{canResume: false}
	h.Register(p)
	saveRecord(t, db, "oc_gone", "opencode", persistedMeta{Cwd: t.TempDir()})
	if _, err := db.AppendTranscript("oc_gone", 1, "m1", []byte(`{"type":"session.message"}`)); err != nil {
		t.Fatal(err)
	}

	m, err := h.restartSession(context.Background(), "oc_gone")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.sess.Close() })
	if !p.created {
		t.Fatal("expected a fresh session when the provider reports the conversation unrecoverable")
	}
	if p.attachedID != "" {
		t.Fatal("attached despite CanResume being false")
	}
}

// A failing Attach — the live opencode case, where the server that owned the session is gone — must
// fall back to a fresh session rather than failing the restart outright.
func TestRestartFallsBackWhenAttachFails(t *testing.T) {
	h, db := restoreHub(t)
	p := &attachResumeProvider{canResume: true, attachErr: errAttachUnavailable}
	h.Register(p)
	saveRecord(t, db, "oc_dead", "opencode", persistedMeta{Cwd: t.TempDir()})
	if _, err := db.AppendTranscript("oc_dead", 1, "m1", []byte(`{"type":"session.message"}`)); err != nil {
		t.Fatal(err)
	}

	m, err := h.restartSession(context.Background(), "oc_dead")
	if err != nil {
		t.Fatalf("restart must survive an attach failure: %v", err)
	}
	t.Cleanup(func() { _ = m.sess.Close() })
	if !p.created {
		t.Fatal("attach failed but no fresh session was created — the user would be left with nothing")
	}
}

// A session that never produced a turn has nothing to resume, so restart must not attach.
func TestRestartOfAVirginSessionDoesNotAttach(t *testing.T) {
	h, db := restoreHub(t)
	p := &attachResumeProvider{canResume: true}
	h.Register(p)
	saveRecord(t, db, "oc_virgin", "opencode", persistedMeta{Cwd: t.TempDir()})

	m, err := h.restartSession(context.Background(), "oc_virgin")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.sess.Close() })
	if p.attachedID != "" {
		t.Fatal("attached a session that had no prior turns")
	}
}
