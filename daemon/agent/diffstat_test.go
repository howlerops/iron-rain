package agent

import "testing"

func TestDiffStatCountsHunkBodyNotFileHeaders(t *testing.T) {
	patch := `diff --git a/main.go b/main.go
index 1234567..89abcde 100644
--- a/main.go
+++ b/main.go
@@ -1,5 +1,6 @@
 package main
-import "fmt"
+import (
+	"fmt"
+	"os"
+)

 func main() {
`
	adds, dels := DiffStat(patch)
	// The +++/--- headers are NOT content. Counting them is the classic off-by-one here and it
	// inflates every single-file edit by exactly one add and one delete.
	if adds != 4 || dels != 1 {
		t.Fatalf("DiffStat = +%d −%d; want +4 −1", adds, dels)
	}
}

func TestDiffStatIgnoresProseThatIsNotADiff(t *testing.T) {
	// Tool output is frequently prose, and a bullet list is full of lines starting with "-".
	// Counting those turns "I updated the README" into a confident, invented "+0 −12".
	prose := `The file has been updated.

- removed the old section
- rewrote the intro
+ added a note
`
	if adds, dels := DiffStat(prose); adds != 0 || dels != 0 {
		t.Fatalf("prose counted as a diff: +%d −%d", adds, dels)
	}
}

func TestDiffStatHandlesRemovedContentThatLooksLikeAHeader(t *testing.T) {
	// A markdown horizontal rule / YAML separator removed inside a hunk really is a deleted line.
	patch := `--- a/doc.md
+++ b/doc.md
@@ -1,4 +1,3 @@
 title
----
+++
 body
`
	adds, dels := DiffStat(patch)
	if adds != 1 || dels != 1 {
		t.Fatalf("DiffStat = +%d −%d; want +1 −1", adds, dels)
	}
}

func TestDiffStatFromFindsANestedPatch(t *testing.T) {
	// How providers actually ship a diff: nested in a JSON payload the client never sees.
	meta := `{"tool":"edit","state":{"status":"completed","metadata":{"diff":"@@ -1,2 +1,3 @@\n a\n-b\n+c\n+d\n"}}}`
	adds, dels := DiffStatFrom(meta)
	if adds != 2 || dels != 1 {
		t.Fatalf("DiffStatFrom = +%d −%d; want +2 −1", adds, dels)
	}
}

func TestDiffStatFromPrefersTheFirstRealPatch(t *testing.T) {
	adds, dels := DiffStatFrom("", "no diff here", "@@ -1 +1,2 @@\n a\n+b\n")
	if adds != 1 || dels != 0 {
		t.Fatalf("DiffStatFrom = +%d −%d; want +1 −0", adds, dels)
	}
}

func TestDiffStatFromReportsUnknownAsZero(t *testing.T) {
	// The contract the client depends on: zero/zero means "couldn't tell", and it renders nothing.
	if adds, dels := DiffStatFrom("The file has been updated.", `{"ok":true}`); adds != 0 || dels != 0 {
		t.Fatalf("expected unknown (0,0), got +%d −%d", adds, dels)
	}
}
