package procutil

import (
	"bytes"
	"log"
	"strings"
	"sync"
)

// maxLogLine bounds a single forwarded stderr line. A child that emits a megabyte-long line (a stack
// dump, a base64 blob) must not be able to push the daemon's in-memory log ring out of the app.
const maxLogLine = 2000

// LogWriter returns an io.Writer suitable for cmd.Stderr that forwards the child's stderr into the
// daemon log — and therefore into loghub, and therefore into the app's log panel — one line at a
// time, tagged with prefix.
//
// Why a Writer and not StderrPipe: os/exec spawns its own copier for a non-*os.File writer and Wait
// blocks until that copier finishes, so there is no "read the pipe before Wait" race to get wrong.
// Two failure modes this deliberately avoids:
//
//   - Handing a child a pipe nobody drains: the pipe fills, the child's next write blocks forever, and
//     the child appears hung. (lsp/server.go drains to io.Discard for exactly this reason.)
//   - Handing a child a pipe whose read end dies with a UI process: the child's next write takes
//     SIGPIPE and the child is KILLED. (See the warning in DaemonLauncher.swift.) The daemon owns
//     this writer for the child's whole life, so neither can happen.
func LogWriter(prefix string) *lineWriter {
	return &lineWriter{prefix: prefix}
}

type lineWriter struct {
	prefix string
	mu     sync.Mutex
	buf    bytes.Buffer
	// tail keeps the last few lines so a caller can explain a crash.
	//
	// A child's dying words go to stderr, and this writer is the only thing that sees them — it
	// logged each line and dropped it. So when a harness exited non-zero, all the daemon could tell
	// the user was "pi exited: exit status 1": an exit code with no cause, on a session that looks
	// broken for no stated reason, while the sentence explaining it (a missing API key, a bad
	// config) had already scrolled past in the log. The generic CLI adapter learned this lesson and
	// grew its own exitHint; keeping the tail here gives every child the same ability.
	tail []string
}

// maxTail is how many recent stderr lines are kept. Enough for a diagnosis that spans two lines
// (which is common: the cause and the instruction are often printed separately), short enough that
// this can never be mistaken for a log buffer.
const maxTail = 6

// Tail returns the child's most recent stderr lines, oldest first.
func (w *lineWriter) Tail() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.tail...)
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := len(p)
	for {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			w.buf.Write(p)
			// A child spewing one endless line must not grow this buffer forever: flush what we have.
			if w.buf.Len() > maxLogLine {
				w.emit(w.buf.String())
				w.buf.Reset()
			}
			return n, nil
		}
		w.buf.Write(p[:i])
		w.emit(w.buf.String())
		w.buf.Reset()
		p = p[i+1:]
	}
}

// emit writes one line, trimming trailing CR and truncating over-long output.
func (w *lineWriter) emit(line string) {
	line = strings.TrimRight(line, "\r")
	if strings.TrimSpace(line) == "" {
		return
	}
	if len(line) > maxLogLine {
		line = line[:maxLogLine] + "…"
	}
	w.tail = append(w.tail, line)
	if len(w.tail) > maxTail {
		w.tail = w.tail[len(w.tail)-maxTail:]
	}
	log.Printf("%s: %s", w.prefix, line)
}

// Flush emits any buffered partial line. Call it after the child exits so a final unterminated line
// (a crash message with no trailing newline) isn't lost.
func (w *lineWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.buf.Len() > 0 {
		w.emit(w.buf.String())
		w.buf.Reset()
	}
}
