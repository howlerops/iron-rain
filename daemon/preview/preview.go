// Package preview gives every session's dev server a stable NAME instead of a port number.
//
// The problem it solves: several sessions run at once, each in its own worktree, and each starts a
// dev server. They all want :3000. AllocPort already stops them colliding by handing out distinct
// ports — but the result is that a session's preview lives at :4173 today and :4181 tomorrow, and
// nothing on screen says which agent owns which number.
//
// So each session gets a host name and they all share ONE listener:
//
//	http://fix-login.localhost:7777   ->  127.0.0.1:4173
//	http://review-pr-42.localhost:7777 -> 127.0.0.1:4181
//
// Why *.localhost rather than /etc/hosts or a resolver install: macOS resolves ANY *.localhost label
// to 127.0.0.1 with no configuration, no root, and nothing to clean up afterwards. Verified on
// macOS 15 — `foo.localhost`, `myfeature.localhost` and `a-b-c.localhost` all resolve, and a real
// server receives the Host header intact, which is what makes routing possible. That is the whole
// reason this design is a small proxy and not a privileged helper: it needs no system state.
//
// The raw port stays reachable and is deliberately NOT hidden. Anything that hardcodes
// `http://localhost:PORT` — OAuth redirect URIs are the common one — cannot follow a rename, so the
// name is an addition, never a replacement.
package preview

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Router maps a host label to a session's local port and proxies to it.
type Router struct {
	mu     sync.RWMutex
	byName map[string]entry  // label -> entry
	byID   map[string]string // session id -> label, so a session can be renamed or dropped
	port   int               // the single port every named preview is served on
	srv    *http.Server
}

type entry struct {
	sessionID string
	port      int
}

// New creates a Router. It does not listen until Start.
func New() *Router {
	return &Router{byName: map[string]entry{}, byID: map[string]string{}}
}

// Port is the shared listener's port, or 0 before Start.
func (r *Router) Port() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.port
}

// Start binds the shared listener. The port is chosen from `pref` when free, otherwise the OS picks
// one — a fixed port is nicer to remember, but refusing to start because something else already
// holds it would take the whole feature down over a detail.
func (r *Router) Start(pref int) error {
	ln, err := listenPreferred(pref)
	if err != nil {
		return err
	}
	r.mu.Lock()
	r.port = ln.Addr().(*net.TCPAddr).Port
	r.srv = &http.Server{Handler: r, ReadHeaderTimeout: 10 * time.Second}
	srv := r.srv
	r.mu.Unlock()
	go func() { _ = srv.Serve(ln) }()
	return nil
}

func listenPreferred(pref int) (net.Listener, error) {
	if pref > 0 {
		if ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", pref)); err == nil {
			return ln, nil
		}
	}
	return net.Listen("tcp", "127.0.0.1:0")
}

// Close stops the listener.
func (r *Router) Close() error {
	r.mu.Lock()
	srv := r.srv
	r.srv = nil
	r.mu.Unlock()
	if srv == nil {
		return nil
	}
	return srv.Close()
}

// Register gives sessionID a name derived from `desired` and returns the label actually assigned.
//
// The label is made unique rather than rejected on collision: two sessions called "fix login" is
// entirely normal (a retry, a second attempt on the same ticket), and failing the second one would
// make the feature unreliable exactly when someone is working hardest.
func (r *Router) Register(sessionID, desired string, port int) string {
	if sessionID == "" || port <= 0 {
		return ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if old, ok := r.byID[sessionID]; ok {
		delete(r.byName, old) // a re-register (new port after restart) must not strand the old label
	}
	label := uniqueLabel(slug(desired), sessionID, r.byName)
	r.byName[label] = entry{sessionID: sessionID, port: port}
	r.byID[sessionID] = label
	return label
}

// Unregister drops a session's name, so a finished session's label is free to reuse and a stale
// name can't quietly proxy to a port something else has since been given.
func (r *Router) Unregister(sessionID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if label, ok := r.byID[sessionID]; ok {
		delete(r.byName, label)
		delete(r.byID, sessionID)
	}
}

// URL returns the browsable address for a session, or "" if it has no preview.
func (r *Router) URL(sessionID string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	label, ok := r.byID[sessionID]
	if !ok || r.port == 0 {
		return ""
	}
	return fmt.Sprintf("http://%s.localhost:%d", label, r.port)
}

// ServeHTTP routes on the Host header.
func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	label := labelFromHost(req.Host)
	r.mu.RLock()
	e, ok := r.byName[label]
	known := make([]string, 0, len(r.byName))
	if !ok {
		for n := range r.byName {
			known = append(known, n)
		}
	}
	port := r.port
	r.mu.RUnlock()

	if !ok {
		// A miss is far more likely to be a typo or a finished session than a bug, so say which
		// names DO work rather than returning a bare 404 that leaves the user guessing.
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		if label == "" {
			fmt.Fprintf(w, "No preview name in the request.\n\nReach a session at http://<name>.localhost:%d\n", port)
		} else {
			fmt.Fprintf(w, "No session named %q.\n", label)
		}
		if len(known) > 0 {
			fmt.Fprintf(w, "\nCurrently serving:\n")
			for _, n := range known {
				fmt.Fprintf(w, "  http://%s.localhost:%d\n", n, port)
			}
		}
		return
	}

	target := &url.URL{Scheme: "http", Host: fmt.Sprintf("127.0.0.1:%d", e.port)}
	p := httputil.NewSingleHostReverseProxy(target)
	// The dev server is told the name the browser used, not 127.0.0.1. Vite and friends compare the
	// Host against allowedHosts and will refuse a request whose Host they don't recognise, and
	// anything generating absolute URLs would otherwise hand the browser a 127.0.0.1 link that
	// escapes the name entirely.
	p.Director = func(out *http.Request) {
		out.URL.Scheme = target.Scheme
		out.URL.Host = target.Host
		out.Host = req.Host
	}
	p.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, err error) {
		// The overwhelmingly common case: the name is registered but the dev server isn't up yet
		// (or has crashed). "Connection refused" from a proxy is a confusing thing to show, so name
		// the actual situation and the port to look at.
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprintf(w, "%s is registered, but nothing is listening on 127.0.0.1:%d yet.\n\n"+
			"The dev server for this session may still be starting, or may have exited.\n\n(%v)\n",
			label, e.port, err)
	}
	// NOTE: httputil.ReverseProxy handles protocol upgrades, so websockets — and therefore HMR /
	// live reload — work through the name. That is not incidental: a preview URL that breaks hot
	// reload would be strictly worse than the port it replaced.
	p.ServeHTTP(w, req)
}

// labelFromHost pulls "fix-login" out of "fix-login.localhost:7777".
func labelFromHost(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if !strings.HasSuffix(host, ".localhost") {
		return ""
	}
	return strings.TrimSuffix(host, ".localhost")
}

// slug reduces a human name to a DNS label. Kept here rather than reusing worktree.Slug so the
// package has no dependency on worktree, and because the constraint is different: this must be a
// valid DNS label (length-capped, no leading/trailing dash), not just a readable directory name.
func slug(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		default:
			if !dash && b.Len() > 0 {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 40 { // DNS labels cap at 63; 40 keeps the URL readable
		out = strings.Trim(out[:40], "-")
	}
	return out
}

// uniqueLabel returns a free label, falling back to the session id when there is no usable name.
func uniqueLabel(base, sessionID string, taken map[string]entry) string {
	if base == "" {
		base = "session-" + shortID(sessionID)
	}
	if _, clash := taken[base]; !clash {
		return base
	}
	// Suffix with the session id rather than -2, -3: a number is ambiguous across restarts, whereas
	// the id always identifies the same session.
	cand := base + "-" + shortID(sessionID)
	if _, clash := taken[cand]; !clash {
		return cand
	}
	for i := 2; ; i++ {
		n := fmt.Sprintf("%s-%d", cand, i)
		if _, clash := taken[n]; !clash {
			return n
		}
	}
}

func shortID(id string) string {
	id = slug(id)
	if len(id) > 6 {
		return id[len(id)-6:]
	}
	if id == "" {
		return "x"
	}
	return id
}
