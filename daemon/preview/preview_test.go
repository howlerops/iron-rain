package preview

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The point of the feature: two sessions' dev servers, one listener, told apart by name.
func TestRoutesByNameToTheRightServer(t *testing.T) {
	a := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "server-A")
	}))
	defer a.Close()
	b := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "server-B")
	}))
	defer b.Close()

	r := New()
	r.Register("sess-a", "Fix login", portOf(t, a.URL))
	r.Register("sess-b", "Review PR 42", portOf(t, b.URL))

	if got := get(t, r, "fix-login.localhost:7777"); got != "server-A" {
		t.Fatalf("fix-login routed to %q", got)
	}
	if got := get(t, r, "review-pr-42.localhost:7777"); got != "server-B" {
		t.Fatalf("review-pr-42 routed to %q", got)
	}
}

// The dev server must be told the NAME the browser used. Vite compares Host against allowedHosts and
// rejects what it doesn't recognise, and anything building absolute URLs would otherwise hand the
// browser a 127.0.0.1 link that escapes the name entirely.
func TestForwardsTheBrowsersHostNotTheLoopbackOne(t *testing.T) {
	var seen string
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		seen = req.Host
		fmt.Fprint(w, "ok")
	}))
	defer up.Close()

	r := New()
	r.Register("s1", "my app", portOf(t, up.URL))
	get(t, r, "my-app.localhost:7777")
	if !strings.HasPrefix(seen, "my-app.localhost") {
		t.Fatalf("upstream saw Host %q, want the browser's name", seen)
	}
}

// Two sessions can legitimately share a name — a retry, a second go at the same ticket. Refusing the
// second would break the feature exactly when someone is working hardest, so it is made unique.
func TestDuplicateNamesBothGetAWorkingURL(t *testing.T) {
	r := started(t)
	one := r.Register("sess-aaaaaa", "fix login", 1111)
	two := r.Register("sess-bbbbbb", "fix login", 2222)
	if one == two {
		t.Fatalf("both sessions got the same label %q — one would shadow the other", one)
	}
	if one == "" || two == "" {
		t.Fatalf("a session was refused a name: %q %q", one, two)
	}
	if r.URL("sess-aaaaaa") == r.URL("sess-bbbbbb") {
		t.Fatal("two sessions resolved to the same URL")
	}
}

// Re-registering (a new port after a daemon restart) must not strand the old label pointing at a
// port that something else may now own.
func TestReRegisterDoesNotStrandTheOldName(t *testing.T) {
	r := New()
	first := r.Register("s1", "checkout flow", 3000)
	second := r.Register("s1", "checkout flow", 4000)
	r.mu.RLock()
	defer r.mu.RUnlock()
	if len(r.byName) != 1 {
		t.Fatalf("%d labels registered after re-register, want 1: %v", len(r.byName), r.byName)
	}
	if r.byName[second].port != 4000 {
		t.Fatalf("label %q points at port %d, want 4000", second, r.byName[second].port)
	}
	_ = first
}

// A finished session's name must stop resolving, or it proxies to a port that has been handed to
// someone else.
func TestUnregisterStopsServingTheName(t *testing.T) {
	r := started(t)
	r.Register("s1", "temp", 1234)
	if r.URL("s1") == "" {
		t.Fatal("expected a URL before unregister")
	}
	r.Unregister("s1")
	if r.URL("s1") != "" {
		t.Fatal("URL survived unregister")
	}
	if body := get(t, r, "temp.localhost:7777"); !strings.Contains(body, "No session named") {
		t.Fatalf("unregistered name still served: %q", body)
	}
}

// A miss must NOT disclose the other sessions' names.
//
// This asserts the reverse of what it originally did. Listing the live labels was a genuine
// convenience for someone who mistyped their own URL, but a label is derived from a session title —
// "fix-customer-billing" — and the 404 is reachable by anything that can open a loopback socket,
// which on this machine means every agent. One wrong guess returned the whole roster of what was
// being worked on, and handed a would-be cross-session caller its target list for free.
func TestUnknownNameDoesNotDiscloseOtherSessions(t *testing.T) {
	r := New()
	r.Register("s1", "acquisition-due-diligence", 1111)
	r.Register("s2", "beta", 2222)
	body := get(t, r, "gamma.localhost:7777")

	if !strings.Contains(body, "gamma") {
		t.Errorf("the 404 should echo the name that missed:\n%s", body)
	}
	for _, secret := range []string{"acquisition-due-diligence", "beta"} {
		if strings.Contains(body, secret) {
			t.Errorf("404 leaked another session's label %q:\n%s", secret, body)
		}
	}
	// The count still distinguishes "nothing is running" from "wrong name", which is the question
	// the person at the keyboard actually has.
	if !strings.Contains(body, "2 sessions") {
		t.Errorf("404 should say how many previews are live, without naming them:\n%s", body)
	}
}

// With nothing running at all, the answer should say so rather than implying a typo.
func TestUnknownNameWithNoPreviewsSaysSo(t *testing.T) {
	r := New()
	body := get(t, r, "anything.localhost:7777")
	if !strings.Contains(body, "No sessions are serving") {
		t.Errorf("expected an explicit empty state:\n%s", body)
	}
}

// The commonest real failure: the name is registered but the dev server hasn't started. A bare
// "connection refused" from a proxy is a confusing thing to show a user.
func TestDeadUpstreamExplainsItself(t *testing.T) {
	r := New()
	r.Register("s1", "not up yet", 1) // nothing listens on port 1
	body := get(t, r, "not-up-yet.localhost:7777")
	for _, want := range []string{"not-up-yet", "nothing is listening", "starting"} {
		if !strings.Contains(strings.ToLower(body), strings.ToLower(want)) {
			t.Fatalf("502 body missing %q:\n%s", want, body)
		}
	}
}

func TestLabelParsing(t *testing.T) {
	cases := map[string]string{
		"fix-login.localhost:7777": "fix-login",
		"fix-login.localhost":      "fix-login",
		"FIX-Login.localhost:80":   "fix-login",
		"fix-login.localhost.:80":  "fix-login", // trailing dot is a legal FQDN
		"localhost:7777":           "",
		"example.com":              "",
		"":                         "",
	}
	for in, want := range cases {
		if got := labelFromHost(in); got != want {
			t.Errorf("labelFromHost(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSlugMakesValidDNSLabels(t *testing.T) {
	cases := map[string]string{
		"Fix login":                   "fix-login",
		"  Review PR #42  ":           "review-pr-42",
		"feature/add-auth":            "feature-add-auth",
		"!!!":                         "",
		"a" + strings.Repeat("b", 60): "a" + strings.Repeat("b", 39),
	}
	for in, want := range cases {
		got := slug(in)
		if got != want {
			t.Errorf("slug(%q) = %q, want %q", in, got, want)
		}
		if strings.HasPrefix(got, "-") || strings.HasSuffix(got, "-") {
			t.Errorf("slug(%q) = %q has a leading/trailing dash — not a valid DNS label", in, got)
		}
		if len(got) > 63 {
			t.Errorf("slug(%q) is %d chars, over the DNS label limit", in, len(got))
		}
	}
}

// A session with no usable name still needs a working URL.
func TestNamelessSessionStillGetsALabel(t *testing.T) {
	r := New()
	label := r.Register("cc_abc123def", "", 1234)
	if label == "" {
		t.Fatal("nameless session got no label")
	}
	if strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
		t.Fatalf("label %q is not a valid DNS label", label)
	}
}

func TestRegisterRejectsUnusableInput(t *testing.T) {
	r := New()
	if l := r.Register("", "name", 1234); l != "" {
		t.Fatalf("registered a session with no id: %q", l)
	}
	if l := r.Register("s1", "name", 0); l != "" {
		t.Fatalf("registered a session with no port: %q", l)
	}
}

// started returns a Router that is actually listening. URL() reports "" until Start binds a port —
// correctly, since an unstarted router serves nothing — so any test that asks for a URL needs one.
func started(t *testing.T) *Router {
	t.Helper()
	r := New()
	if err := r.Start(0); err != nil { // 0 = let the OS pick, so tests never fight for a fixed port
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r
}

func get(t *testing.T, r *Router, host string) string {
	t.Helper()
	req := httptest.NewRequest("GET", "http://"+host+"/", nil)
	req.Host = host
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	b, _ := io.ReadAll(w.Result().Body)
	return string(b)
}

func portOf(t *testing.T, rawURL string) int {
	t.Helper()
	var p int
	if _, err := fmt.Sscanf(rawURL[strings.LastIndex(rawURL, ":")+1:], "%d", &p); err != nil {
		t.Fatalf("port from %q: %v", rawURL, err)
	}
	return p
}
