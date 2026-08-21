package textutil

import (
	"testing"
	"unicode/utf8"
)

func TestTruncDoesNotSplitRunes(t *testing.T) {
	// "…" and "—" are 3 bytes; cutting at a limit that lands inside one used to yield invalid UTF-8.
	for _, tc := range []struct {
		in   string
		n    int
		want string
	}{
		{"abc", 10, "abc"},
		{"a—b", 2, "a…"}, // limit lands mid em-dash: back up to the boundary
		{"a—b", 3, "a…"},
		{"a—b", 4, "a—…"},
		{"🙂🙂", 5, "🙂…"}, // 4-byte runes
		{"abc", 0, ""},
	} {
		if got := Trunc(tc.in, tc.n); got != tc.want {
			t.Errorf("Trunc(%q,%d) = %q, want %q", tc.in, tc.n, got, tc.want)
		}
	}
}

func TestTruncAlwaysValidUTF8(t *testing.T) {
	s := "fixed the parser — added tests 🙂 and updated the docs"
	for n := 1; n <= len(s)+2; n++ {
		if got := Trunc(s, n); !utf8ValidStr(got) {
			t.Fatalf("Trunc(s,%d) produced invalid UTF-8: %q", n, got)
		}
	}
}

func TestFirstLine(t *testing.T) {
	if got := FirstLine("  hello\nworld  ", 50); got != "hello" {
		t.Errorf("FirstLine = %q", got)
	}
}

func utf8ValidStr(s string) bool { return utf8.ValidString(s) }
