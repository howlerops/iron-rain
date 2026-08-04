package fsaccess

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Hit is one search match: a file, 1-based line/column, and the matching line's text.
type Hit struct {
	Path string
	Line int
	Col  int
	Text string
}

// Search finds query across the given roots. It prefers ripgrep (fast, respects .gitignore)
// and falls back to a bounded directory walk. Literal by default; regex=true treats query as a
// regular expression. Smart-case: a lowercase query matches case-insensitively. Results are
// capped at limit.
func Search(query string, roots []string, regex bool, limit int) ([]Hit, error) {
	query = strings.TrimSpace(query)
	if query == "" || len(roots) == 0 {
		return nil, nil
	}
	if limit <= 0 {
		limit = 500
	}
	// Search is the one path that does NOT go through Guard.Resolve — hub passes sessionRoots()
	// straight in, and a hit's text is the file's content echoed back to the client. So the protected
	// rule has to be applied here too, on the way in and on the way out. Filtering the roots keeps rg
	// out of ~/.oculus entirely; filtering the hits covers what a link inside an allowed root reaches,
	// since rg follows nothing by default but the walk fallback and future flags might.
	roots = allowedSearchRoots(roots)
	if len(roots) == 0 {
		return nil, nil
	}
	if rg, err := exec.LookPath("rg"); err == nil {
		if hits, err := searchRipgrep(rg, query, roots, regex, limit); err == nil {
			return dropProtectedHits(hits), nil
		}
	}
	hits, err := searchWalk(query, roots, regex, limit)
	return dropProtectedHits(hits), err
}

// allowedSearchRoots drops roots that lie inside a protected directory (see protectedRules).
func allowedSearchRoots(roots []string) []string {
	out := roots[:0:0]
	for _, r := range roots {
		if ProtectedPath(r) == "" {
			out = append(out, r)
		}
	}
	return out
}

// dropProtectedHits removes matches whose file is protected, so no protected byte is ever quoted
// back in a search result even if the walk reached it.
func dropProtectedHits(hits []Hit) []Hit {
	out := hits[:0]
	for _, h := range hits {
		if ProtectedPath(h.Path) == "" {
			out = append(out, h)
		}
	}
	return out
}

// smartCaseInsensitive reports whether a query should match case-insensitively (all-lowercase).
func smartCaseInsensitive(query string) bool { return query == strings.ToLower(query) }

func searchRipgrep(rg, query string, roots []string, regex bool, limit int) ([]Hit, error) {
	args := []string{"--vimgrep", "--no-heading", "--color", "never", "--max-columns", "400",
		"--max-filesize", "2M", "-S"}
	if !regex {
		args = append(args, "-F") // fixed string (literal)
	}
	// The "--" is load-bearing, not punctuation. It ends option parsing, so the caller-supplied
	// query can never be read as a flag — and ripgrep has flags that execute programs. `--pre=CMD`
	// runs CMD once per searched file; `--hostname-bin` runs a program too; `-f FILE` would read an
	// arbitrary file as the pattern list and echo its lines back through the match text. A search
	// box typed into from a phone is one missing token away from all three. Keep the query behind
	// this separator, and never append anything caller-controlled to args ABOVE this line.
	args = append(args, "--", query)
	args = append(args, roots...)
	out, err := exec.Command(rg, args...).Output()
	if err != nil {
		// rg exits 1 when there are no matches — that's not an error for us.
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 1 {
			return nil, nil
		}
		return nil, err
	}
	var hits []Hit
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() && len(hits) < limit {
		// path:line:col:text  (paths on macOS don't contain ':')
		line := sc.Text()
		p := strings.SplitN(line, ":", 4)
		if len(p) < 4 {
			continue
		}
		ln, _ := strconv.Atoi(p[1])
		col, _ := strconv.Atoi(p[2])
		hits = append(hits, Hit{Path: p[0], Line: ln, Col: col, Text: strings.TrimSpace(p[3])})
	}
	return hits, nil
}

func searchWalk(query string, roots []string, regex bool, limit int) ([]Hit, error) {
	insensitive := smartCaseInsensitive(query)
	var re *regexp.Regexp
	needle := query
	if regex {
		flags := ""
		if insensitive {
			flags = "(?i)"
		}
		r, err := regexp.Compile(flags + query)
		if err != nil {
			return nil, err
		}
		re = r
	} else if insensitive {
		needle = strings.ToLower(query)
	}

	var hits []Hit
	for _, root := range roots {
		if len(hits) >= limit {
			break
		}
		_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil || len(hits) >= limit {
				return nil
			}
			if d.IsDir() {
				if skipDirs[d.Name()] {
					return filepath.SkipDir
				}
				// A root can legitimately CONTAIN a protected directory even when it isn't one itself —
				// registering $HOME as a project puts ~/.oculus and ~/.ssh under the walk. Prune there, so
				// key material is never even read into memory; dropProtectedHits is the backstop.
				if protectedLabel(path) != "" {
					return filepath.SkipDir
				}
				return nil
			}
			if info, e := d.Info(); e == nil && info.Size() > maxReadBytes {
				return nil // skip large files
			}
			f, e := os.Open(path)
			if e != nil {
				return nil
			}
			defer f.Close()
			sc := bufio.NewScanner(f)
			sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
			ln := 0
			for sc.Scan() {
				ln++
				text := sc.Text()
				if strings.IndexByte(text, 0) >= 0 { // looks binary
					return nil
				}
				var col int
				if re != nil {
					if loc := re.FindStringIndex(text); loc != nil {
						col = loc[0] + 1
					} else {
						continue
					}
				} else {
					hay := text
					if insensitive {
						hay = strings.ToLower(text)
					}
					idx := strings.Index(hay, needle)
					if idx < 0 {
						continue
					}
					col = idx + 1
				}
				hits = append(hits, Hit{Path: path, Line: ln, Col: col, Text: strings.TrimSpace(text)})
				if len(hits) >= limit {
					return filepath.SkipAll
				}
			}
			return nil
		})
	}
	return hits, nil
}
