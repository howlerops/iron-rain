package hub

import (
	"bufio"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/howlerops/oculus/daemon/protocol"
)

// detectTestCommand picks a sensible test/build command from the project files in cwd.
// Returns nil if it can't tell (the caller then asks for an explicit command).
func detectTestCommand(cwd string) []string {
	has := func(f string) bool { _, err := os.Stat(filepath.Join(cwd, f)); return err == nil }
	switch {
	case has("go.mod"):
		return []string{"go", "test", "./..."}
	case has("Cargo.toml"):
		return []string{"cargo", "test"}
	case has("package.json"):
		return []string{"npm", "test", "--silent"}
	case has("pyproject.toml"), has("setup.py"), has("pytest.ini"), has("tox.ini"):
		return []string{"pytest"}
	case has("Package.swift"):
		return []string{"swift", "test"}
	case has("Makefile"), has("makefile"):
		return []string{"make", "test"}
	}
	return nil
}

// runTest runs a test/build command in the session's workspace, streaming each output line as
// a run.output event and finishing with a run.result. On failure it fires a TESTS_FAILED push
// so a remote user learns tests broke without watching. One run per session at a time.
func (h *Hub) runTest(sessionID, command string) {
	roots := h.sessionRoots(sessionID)
	if len(roots) == 0 {
		h.broadcast(protocol.TypeRunResult, protocol.RunResult{SessionID: sessionID, OK: false, ExitCode: -1})
		return
	}
	cwd := roots[0]

	var argv []string
	if strings.TrimSpace(command) != "" {
		argv = []string{"/bin/sh", "-c", command}
	} else if argv = detectTestCommand(cwd); argv == nil {
		h.broadcast(protocol.TypeRunOutput, protocol.RunOutput{SessionID: sessionID,
			Line: "No test command detected for this project — pass one explicitly."})
		h.broadcast(protocol.TypeRunResult, protocol.RunResult{SessionID: sessionID, OK: false, ExitCode: -1})
		return
	}
	cmdStr := command
	if cmdStr == "" {
		cmdStr = strings.Join(argv, " ")
	}

	// One run per session.
	h.mu.Lock()
	if h.runningTests == nil {
		h.runningTests = map[string]bool{}
	}
	if h.runningTests[sessionID] {
		h.mu.Unlock()
		return
	}
	h.runningTests[sessionID] = true
	label := ""
	if m := h.sessions[sessionID]; m != nil {
		label = m.meta.label
		if label == "" {
			label = m.meta.workspaceName
		}
	}
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.runningTests, sessionID)
		h.mu.Unlock()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()

	h.broadcast(protocol.TypeRunOutput, protocol.RunOutput{SessionID: sessionID, Line: "$ " + cmdStr})
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = cwd
	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw
	if err := cmd.Start(); err != nil {
		h.broadcast(protocol.TypeRunOutput, protocol.RunOutput{SessionID: sessionID, Line: err.Error()})
		h.broadcast(protocol.TypeRunResult, protocol.RunResult{SessionID: sessionID, Command: cmdStr, OK: false, ExitCode: -1})
		return
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait(); pw.Close() }()

	sc := bufio.NewScanner(pr)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		h.broadcast(protocol.TypeRunOutput, protocol.RunOutput{SessionID: sessionID, Line: sc.Text()})
	}
	waitErr := <-done

	exit := 0
	if cmd.ProcessState != nil {
		exit = cmd.ProcessState.ExitCode()
	}
	ok := waitErr == nil
	h.broadcast(protocol.TypeRunResult, protocol.RunResult{SessionID: sessionID, Command: cmdStr, OK: ok, ExitCode: exit})
	if !ok {
		h.pushTestsFailed(sessionID, label, cmdStr)
	}
}
