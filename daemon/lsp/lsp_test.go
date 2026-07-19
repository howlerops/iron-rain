package lsp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestLanguageID(t *testing.T) {
	cases := map[string]string{
		"main.go":         "go",
		"App.swift":       "swift",
		"index.ts":        "typescript",
		"comp.tsx":        "typescript",
		"app.js":          "javascript",
		"comp.jsx":        "javascript",
		"script.py":       "python",
		"lib.rs":          "rust",
		"main.c":          "c",
		"header.h":        "cpp",
		"impl.hpp":        "cpp",
		"legacy.cc":       "cpp",
		"main.cpp":        "cpp",
		"MAIN.GO":         "go", // case-insensitive extension
		"README.md":       "",
		"noext":           "",
		"archive.tar.gz":  "",
		"dir/nested/x.py": "python",
	}
	for path, want := range cases {
		if got := languageID(path); got != want {
			t.Errorf("languageID(%q) = %q, want %q", path, got, want)
		}
	}
}

func TestPathURIRoundTrip(t *testing.T) {
	paths := []string{
		"/Users/jacob/projects/oculus/daemon/lsp/lsp.go",
		"/Users/jacob/my project/with spaces/main.swift",
		"/tmp/a#b/c?.ts",
		"/root",
	}
	for _, p := range paths {
		uri := pathToURI(p)
		if !bytes.HasPrefix([]byte(uri), []byte("file:///")) {
			t.Errorf("pathToURI(%q) = %q, want file:/// prefix", p, uri)
		}
		if got := uriToPath(uri); got != p {
			t.Errorf("round-trip %q -> %q -> %q", p, uri, got)
		}
	}

	// A URI with percent escapes must decode back to the literal path.
	if got := uriToPath("file:///Users/jacob/my%20project/main.swift"); got != "/Users/jacob/my project/main.swift" {
		t.Errorf("uriToPath escaped = %q", got)
	}
}

func TestFindRoot(t *testing.T) {
	// Layout: <tmp>/proj/go.mod and <tmp>/proj/pkg/sub/file.go
	tmp := t.TempDir()
	proj := filepath.Join(tmp, "proj")
	sub := filepath.Join(proj, "pkg", "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	file := filepath.Join(sub, "file.go")
	if got := findRoot(file, "go"); got != proj {
		t.Errorf("findRoot walked to %q, want %q", got, proj)
	}

	// No marker anywhere: fall back to the file's own directory.
	orphanDir := t.TempDir()
	orphan := filepath.Join(orphanDir, "lone.go")
	if got := findRoot(orphan, "go"); got != orphanDir {
		t.Errorf("findRoot fallback = %q, want %q", got, orphanDir)
	}
}

func TestFindRootStopsAtGit(t *testing.T) {
	// A .git dir bounds the root even without a language marker.
	tmp := t.TempDir()
	repo := filepath.Join(tmp, "repo")
	deep := filepath.Join(repo, "a", "b")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := findRoot(filepath.Join(deep, "x.py"), "python"); got != repo {
		t.Errorf("findRoot stop-at-git = %q, want %q", got, repo)
	}
}

func TestFrameRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	bodies := [][]byte{
		[]byte(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`),
		[]byte(`{}`),
		[]byte(`{"unicode":"café — spaces and \r\n inside"}`),
	}
	for _, b := range bodies {
		if err := writeFrame(&buf, b); err != nil {
			t.Fatalf("writeFrame: %v", err)
		}
	}

	r := bufio.NewReader(&buf)
	for i, want := range bodies {
		got, err := readFrame(r)
		if err != nil {
			t.Fatalf("readFrame[%d]: %v", i, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("frame[%d] = %q, want %q", i, got, want)
		}
	}
}

func TestFrameHeaderVariants(t *testing.T) {
	// Extra headers and lowercased key must still parse; body read must be exact.
	raw := "content-length: 2\r\nContent-Type: application/json\r\n\r\n{}TRAILING"
	r := bufio.NewReader(bytes.NewReader([]byte(raw)))
	got, err := readFrame(r)
	if err != nil {
		t.Fatalf("readFrame: %v", err)
	}
	if string(got) != "{}" {
		t.Errorf("body = %q, want {}", got)
	}
	// The trailing bytes must remain unconsumed.
	rest, _ := r.ReadString(0)
	if rest != "TRAILING" {
		t.Errorf("remaining = %q, want TRAILING", rest)
	}
}

func TestExtractHoverContents(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"string", `"plain hover"`, "plain hover"},
		{"markupContent", `{"kind":"markdown","value":"func F()"}`, "func F()"},
		{"markedStringObject", `{"language":"go","value":"type T struct"}`, "type T struct"},
		{"markedStringArray", `["first",{"language":"go","value":"second"}]`, "first\nsecond"},
		{"null", `null`, ""},
		{"empty", ``, ""},
	}
	for _, c := range cases {
		if got := extractHoverContents(json.RawMessage(c.raw)); got != c.want {
			t.Errorf("%s: extractHoverContents(%s) = %q, want %q", c.name, c.raw, got, c.want)
		}
	}
}

func TestHoverText(t *testing.T) {
	// Full hover results wrapping each of the three content shapes.
	cases := map[string]string{
		`{"contents":"bare string"}`:                                  "bare string",
		`{"contents":{"kind":"plaintext","value":"typed value"}}`:     "typed value",
		`{"contents":["a",{"language":"go","value":"b"}],"range":{}}`: "a\nb",
		`null`: "",
	}
	for raw, want := range cases {
		if got := hoverText(json.RawMessage(raw)); got != want {
			t.Errorf("hoverText(%s) = %q, want %q", raw, got, want)
		}
	}
}

func TestExtractLocation(t *testing.T) {
	// Location (single object)
	loc, ok := extractLocation(json.RawMessage(
		`{"uri":"file:///Users/jacob/pkg/a.go","range":{"start":{"line":10,"character":4},"end":{"line":10,"character":9}}}`))
	if !ok || loc.Path != "/Users/jacob/pkg/a.go" || loc.StartLine != 10 || loc.StartChar != 4 {
		t.Errorf("Location: %+v ok=%v", loc, ok)
	}

	// Location[] — first element wins
	loc, ok = extractLocation(json.RawMessage(
		`[{"uri":"file:///first.rs","range":{"start":{"line":1,"character":2}}},{"uri":"file:///second.rs","range":{"start":{"line":9,"character":9}}}]`))
	if !ok || loc.Path != "/first.rs" || loc.StartLine != 1 || loc.StartChar != 2 {
		t.Errorf("Location[]: %+v ok=%v", loc, ok)
	}

	// LocationLink[] — targetUri / targetRange
	loc, ok = extractLocation(json.RawMessage(
		`[{"targetUri":"file:///Users/jacob/lib.ts","targetRange":{"start":{"line":42,"character":8}},"targetSelectionRange":{"start":{"line":42,"character":8}}}]`))
	if !ok || loc.Path != "/Users/jacob/lib.ts" || loc.StartLine != 42 || loc.StartChar != 8 {
		t.Errorf("LocationLink[]: %+v ok=%v", loc, ok)
	}

	// null / empty array / empty -> not found
	for _, raw := range []string{`null`, `[]`, ``} {
		if _, ok := extractLocation(json.RawMessage(raw)); ok {
			t.Errorf("extractLocation(%q) unexpectedly ok", raw)
		}
	}
}

func TestSupportedUnsupportedExt(t *testing.T) {
	// Unsupported extensions are never supported, regardless of installed binaries.
	for _, p := range []string{"notes.md", "data.json", "noext"} {
		if Supported(p) {
			t.Errorf("Supported(%q) = true, want false", p)
		}
	}
}
