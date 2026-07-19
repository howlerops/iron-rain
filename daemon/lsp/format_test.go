package lsp

import "testing"

func edit(sl, sc, el, ec int, text string) textEdit {
	return textEdit{Range: rangeObj{Start: position{sl, sc}, End: position{el, ec}}, NewText: text}
}

func TestApplyTextEdits(t *testing.T) {
	// Replace "x" (line 1, col 0..1) with "  y", and insert "// top\n" at the very start.
	src := "a\nx\n"
	edits := []textEdit{
		edit(1, 0, 1, 1, "  y"),
		edit(0, 0, 0, 0, "// top\n"),
	}
	got := applyTextEdits(src, edits)
	want := "// top\na\n  y\n"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestByteOffsetForUTF16(t *testing.T) {
	// "é" is 2 bytes in UTF-8 but 1 UTF-16 unit; "😀" is 4 bytes / 2 UTF-16 units.
	content := "é😀z\n"
	// char 0 -> byte 0; char 1 (after é) -> byte 2; char 3 (after 😀, 2 units) -> byte 6.
	if got := byteOffsetFor(content, 0, 0); got != 0 {
		t.Errorf("offset(0,0) = %d, want 0", got)
	}
	if got := byteOffsetFor(content, 0, 1); got != 2 {
		t.Errorf("offset(0,1) = %d, want 2 (after é)", got)
	}
	if got := byteOffsetFor(content, 0, 3); got != 6 {
		t.Errorf("offset(0,3) = %d, want 6 (after 😀)", got)
	}
	// Past end of a line clamps at the newline.
	if got := byteOffsetFor(content, 0, 99); got != 7 {
		t.Errorf("offset(0,99) = %d, want 7 (before \\n)", got)
	}
}
