package hub

import (
	"os"
	"strings"
	"testing"
)

// The two fields of sessionMeta that CHANGE after construction may only be read under m.mu.
//
// session.rename writes meta.label on the connection's read loop; resolving a fan-out clears
// meta.fanoutGroup. Both write under m.mu. Several readers took only h.mu — a different lock, so no
// mutual exclusion at all — and one of them, watchPreviewPorts, runs every four seconds on a
// goroutine started with no recover(). Copying a Go string header non-atomically can pair one value's
// data pointer with another's length, so the TrimSpace that follows faults and takes the entire
// daemon with it: every session dies and every device is left holding a healthy socket to a corpse.
//
// This is asserted structurally rather than with a stress test. The window is a few instructions
// wide, so a green race-detector run proves nothing — that lesson was learned the hard way on the
// deadlock test this one sits beside.
func TestMutableSessionMetadataIsOnlyReadUnderItsOwnLock(t *testing.T) {
	// Derived from the struct rather than listed, so a field added later is covered without anyone
	// remembering to come back here. Everything in sessionMeta is set once at construction EXCEPT
	// these two, which are written afterwards under m.mu.
	mutable := map[string]bool{"label": true, "fanoutGroup": true}
	immutable := map[string]bool{}
	for _, f := range sessionMetaFields(t) {
		if !mutable[f] {
			immutable[f] = true
		}
	}

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		// session.go is NOT excluded. It used to be, on the reasoning that "its own reads run on the
		// pump that writes them" — which is false in both halves: neither writer is the pump
		// (session.rename runs on a connection's read loop, resolveFanout on the handler that
		// resolves the race), and info() is called from whichever connection asked for the session
		// list. Excluding the one file that DECLARES the fields is what let two unlocked reads of
		// meta.fanoutGroup sit in onStatus and info() through a whole audit.
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		held := false // whether m.mu is held at this point in the file
		// A switch whose arms each unlock is the shape onStatus uses: ONE Lock at the top, then a
		// separate Unlock inside every case. A purely linear scan goes blind after the first arm's
		// Unlock and reports every later arm as unlocked. So remember what was held when the switch
		// opened and restore it at each `case`, keyed on the switch's indentation to survive nesting.
		type swState struct {
			indent int
			held   bool
		}
		var switches []swState
		indentOf := func(s string) int { return len(s) - len(strings.TrimLeft(s, "\t")) }
		for i, line := range strings.Split(string(src), "\n") {
			trimmed := strings.TrimSpace(line)
			ind := indentOf(line)
			// Leaving a switch's body: anything at or left of its own indent closes it.
			for len(switches) > 0 && trimmed != "" && ind < switches[len(switches)-1].indent {
				switches = switches[:len(switches)-1]
			}
			switch {
			case strings.HasPrefix(trimmed, "func "):
				held = false // a new function starts with nothing held
				switches = switches[:0]
			case strings.HasPrefix(trimmed, "switch ") || trimmed == "switch {":
				switches = append(switches, swState{indent: ind, held: held})
			case strings.HasPrefix(trimmed, "case ") || trimmed == "default:":
				if len(switches) > 0 && ind == switches[len(switches)-1].indent {
					held = switches[len(switches)-1].held // this arm starts where the switch did
				}
			// Any session lock, whatever the receiver is called — thread.go holds `parent.mu`. The
			// hub lock is explicitly NOT one of these: holding it is the mistake being looked for.
			case sessionLock(trimmed, "Lock()"):
				held = true
			case sessionLock(trimmed, "Unlock()"):
				// A deferred unlock releases at return, not here, so it does not end the region.
				if !strings.HasPrefix(trimmed, "defer ") {
					held = false
				}
			}
			if strings.HasPrefix(trimmed, "//") || held {
				continue
			}
			for _, at := range metaReads(line) {
				if immutable[at] {
					continue
				}
				what := "the whole meta struct (copied by value, label included)"
				if at != "" {
					what = "meta." + at
				}
				t.Errorf("%s:%d reads %s with m.mu NOT held:\n  %s\n\nlabel and fanoutGroup are "+
					"written under m.mu after construction. Holding h.mu is not the same lock — and "+
					"copying the struct by value copies the string header, which is the read that "+
					"faults. Use m.snapshotMeta(); h.mu → m.mu is the order the package already uses.",
					name, i+1, what, trimmed)
			}
		}
	}
}

// snapshotMeta has to return a COPY. If it ever returns a pointer or a reference into the live
// struct, every caller above goes straight back to reading unsynchronized memory while believing it
// is safe — and the structural test above would still pass.
func TestSnapshotMetaHandsBackACopy(t *testing.T) {
	m := &managedSession{}
	m.meta.label = "before"

	snap := m.snapshotMeta()
	m.mu.Lock()
	m.meta.label = "after" // a concurrent rename, serialized here for determinism
	m.mu.Unlock()

	if snap.label != "before" {
		t.Errorf("the snapshot changed under the caller: got %q, want %q — snapshotMeta is handing "+
			"back a view of live memory, which is the race it exists to prevent", snap.label, "before")
	}
	if got := m.snapshotMeta().label; got != "after" {
		t.Errorf("a fresh snapshot did not see the write: got %q, want %q", got, "after")
	}
}

// metaReads returns, for each `.meta` access on a line, the field name that follows it — or "" when
// the whole struct is read. Deliberately textual: the bug this guards was a struct copied by value
// into a function argument, which no field-name search would ever have matched.
func metaReads(line string) []string {
	var out []string
	rest := line
	for {
		i := strings.Index(rest, ".meta")
		if i < 0 {
			return out
		}
		rest = rest[i+len(".meta"):]
		if !strings.HasPrefix(rest, ".") {
			out = append(out, "") // whole-struct read
			continue
		}
		field := rest[1:]
		for j, r := range field {
			if !(r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
				field = field[:j]
				break
			}
		}
		out = append(out, field)
	}
}

// sessionMetaFields returns the field names declared on sessionMeta.
func sessionMetaFields(t *testing.T) []string {
	t.Helper()
	src, err := os.ReadFile("session.go")
	if err != nil {
		t.Fatal(err)
	}
	body := string(src)
	i := strings.Index(body, "type sessionMeta struct {")
	if i < 0 {
		t.Fatal("could not find sessionMeta — this test cannot tell mutable fields from immutable ones")
	}
	body = body[i:]
	end := strings.Index(body, "\n}")
	if end < 0 {
		t.Fatal("sessionMeta declaration is not terminated")
	}
	var out []string
	for _, line := range strings.Split(body[:end], "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "//") || strings.HasPrefix(line, "type ") {
			continue
		}
		name := line
		for j, r := range name {
			if !(r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
				name = name[:j]
				break
			}
		}
		if name != "" {
			out = append(out, name)
		}
	}
	if len(out) < 5 {
		t.Fatalf("only parsed %d fields from sessionMeta — the parse is wrong and this test would "+
			"flag everything", len(out))
	}
	return out
}

// sessionLock reports whether a line takes or releases a per-session mutex (any receiver), as
// opposed to the hub's. Holding h.mu is precisely the mistake this test exists to catch, so it must
// not count as protection.
func sessionLock(line, op string) bool {
	for _, verb := range []string{".mu." + op, ".mu.R" + op} {
		i := strings.Index(line, verb)
		if i < 0 {
			continue
		}
		if strings.HasSuffix(line[:i], "h") && (i == 1 || !isIdentByte(line[i-2])) {
			continue // h.mu — the wrong lock
		}
		return true
	}
	return false
}

func isIdentByte(b byte) bool {
	return b == '_' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9'
}
