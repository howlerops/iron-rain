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
	if rg, err := exec.LookPath("rg"); err == nil {
		if hits, err := searchRipgrep(rg, query, roots, regex, limit); err == nil {
			return hits, nil
		}
	}
	return searchWalk(query, roots, regex, limit)
}

// smartCaseInsensitive reports whether a query should match case-insensitively (all-lowercase).
func smartCaseInsensitive(query string) bool { return query == strings.ToLower(query) }

func searchRipgrep(rg, query string, roots []string, regex bool, limit int) ([]Hit, error) {
	args := []string{"--vimgrep", "--no-heading", "--color", "never", "--max-columns", "400",
		"--max-filesize", "2M", "-S"}
	if !regex {
		args = append(args, "-F") // fixed string (literal)
	}
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
