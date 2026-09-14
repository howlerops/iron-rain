package hub

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// An ordinary failed turn could deadlock the entire daemon.
//
// Two locks, taken in both orders. The event pump held the SESSION lock while recording a
// session.error to telemetry, and tel() takes the HUB lock. Hub.sessionList does the reverse: it
// holds the hub lock and calls m.info(), which wants the session lock. Whichever gets half of what
// it needs first, neither finishes.
//
// The hub lock is the daemon's single global lock — every dispatch case, every broadcast, every
// subscribe and disconnect takes it — so the blast radius is everything: all sessions freeze, every
// connected device sits holding a socket that is still open and will never carry another frame, and
// only killing the process recovers it. Neither side is exotic. session.list is re-broadcast on every
// session create, rename and delete and requested by every client on connect; a failed turn is what a
// rate limit or a bad model name produces on any given day.
//
// This is a STRUCTURAL test on purpose. The obvious version — hammer both paths and assert they
// finish — was written first and its negative control passed: the inversion is real but the window
// is a few instructions wide, so losing the race is a matter of luck and a green run proves nothing.
// The invariant is what actually holds the daemon together, so the invariant is what gets tested:
// nothing reaches for the hub while holding a session lock.
func TestNothingReachesForTheHubUnderTheSessionLock(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var offences []string

	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			// Walk the function in source order, tracking whether m.mu is held. A deferred unlock
			// holds the lock for the remainder of the function.
			depth, deferred := 0, false
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				switch v := n.(type) {
				case *ast.DeferStmt:
					if call, ok := v.Call.Fun.(*ast.SelectorExpr); ok && isSessionMutex(call, "Unlock") {
						deferred = true
					}
					return false
				case *ast.CallExpr:
					sel, ok := v.Fun.(*ast.SelectorExpr)
					if !ok {
						return true
					}
					switch {
					case isSessionMutex(sel, "Lock"):
						depth++
					case isSessionMutex(sel, "Unlock"):
						if depth > 0 {
							depth--
						}
					case (depth > 0 || deferred) && isHubCall(sel):
						offences = append(offences, fset.Position(v.Pos()).String()+
							"  m.hub."+sel.Sel.Name+"()  in "+fn.Name.Name)
					}
				}
				return true
			})
		}
	}

	if len(offences) > 0 {
		t.Errorf("the hub is reached for while a session lock is held, in %d place(s):\n  %s\n\n"+
			"Hub methods take the hub's own lock, and Hub.sessionList already holds it when it calls "+
			"m.info(), which wants this one. That is a lock-order inversion on the daemon's single "+
			"global lock: every session freezes and every connected device goes dead holding a "+
			"healthy socket. Read what you need under m.mu, unlock, then call the hub.",
			len(offences), strings.Join(offences, "\n  "))
	}
}

// isSessionMutex reports whether sel is `m.mu.<name>` — the managed session's own lock.
func isSessionMutex(sel *ast.SelectorExpr, name string) bool {
	if sel.Sel.Name != name {
		return false
	}
	inner, ok := sel.X.(*ast.SelectorExpr)
	return ok && inner.Sel.Name == "mu" && isIdent(inner.X, "m")
}

// isHubCall reports whether sel is `m.hub.<anything>` — a call into the hub, which will take the
// hub's lock for all but a handful of methods. The rule is blanket by design: "don't reach for the
// hub from under a session lock" is checkable, whereas "only reach for the hub methods that happen
// not to lock today" is a rule that quietly stops being true.
func isHubCall(sel *ast.SelectorExpr) bool {
	inner, ok := sel.X.(*ast.SelectorExpr)
	return ok && inner.Sel.Name == "hub" && isIdent(inner.X, "m")
}

func isIdent(e ast.Expr, name string) bool {
	id, ok := e.(*ast.Ident)
	return ok && id.Name == name
}
