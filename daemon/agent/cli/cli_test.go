package cli

import (
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/protocol"
	"os"
	"os/exec"
)

func TestSubstitute(t *testing.T) {
	// {prompt} + {cwd} + {model} expand in place.
	got := substitute([]string{"--model", "{model}", "--cwd", "{cwd}", "{prompt}"}, "fix the bug", "/repo", "gpt-5", "")
	want := []string{"--model", "gpt-5", "--cwd", "/repo", "fix the bug"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("substitute = %v, want %v", got, want)
	}
	// No {prompt} token → prompt appended as the last arg.
	got = substitute([]string{"exec"}, "hello", "/repo", "", "")
	if len(got) != 2 || got[1] != "hello" {
		t.Errorf("substitute (append) = %v, want [exec hello]", got)
	}
}

func TestStripANSI(t *testing.T) {
	in := "\x1b[32mgreen\x1b[0m\r\ndone"
	if out := stripANSI(in); out != "green\ndone" {
		t.Errorf("stripANSI = %q, want %q", out, "green\ndone")
	}
}

func TestMerge_UserOverridesBuiltin(t *testing.T) {
	detected := []Config{{Name: "codex", Command: "codex"}, {Name: "gemini", Command: "gemini"}}
	user := []Config{{Name: "codex", Command: "/custom/codex"}, {Name: "mine", Command: "mine"}}
	got := Merge(detected, user)
	if len(got) != 3 {
		t.Fatalf("merged len = %d, want 3", len(got))
	}
	if got[0].Command != "/custom/codex" { // user overrode built-in in place
		t.Errorf("codex not overridden: %+v", got[0])
	}
	if got[2].Name != "mine" { // new user agent appended
		t.Errorf("new agent missing: %+v", got)
	}
}

// A full turn: `echo` streams the prompt as output and the session goes idle, then Close() ends the
// event stream (channel closes) — exercising the pump + single-close lifecycle.
func TestSession_RunsStreamsAndCloses(t *testing.T) {
	p := NewProvider(Config{Name: "echoer", Command: "echo", Args: []string{"{prompt}"}})
	sess, err := p.Create(context.Background(), t.TempDir(), "hi there")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	var output strings.Builder
	sawIdle := false
	deadline := time.After(5 * time.Second)
loop:
	for {
		select {
		case ev, ok := <-sess.Events():
			if !ok {
				break loop
			}
			switch e := ev.Payload.(type) {
			case protocol.OutputDelta:
				output.WriteString(e.Text)
			case protocol.SessionStatus:
				if e.Status == protocol.StatusIdle {
					sawIdle = true
					sess.Close() // end the session; the events channel should then close
				}
			}
		case <-deadline:
			t.Fatal("timed out waiting for turn to finish")
		}
	}
	if !sawIdle {
		t.Error("never saw idle status")
	}
	if !strings.Contains(output.String(), "hi there") {
		t.Errorf("output = %q, want it to contain 'hi there'", output.String())
	}
}

// Deep integration test: a fake CLI agent that sets its terminal title (OSC) while it works must
// drive live running/waiting statuses derived from those titles — the "OSC-title status for any CLI
// agent" capability, end to end through Create → runTurn → stream → scanner → emitted events.
func TestSession_OSCTitleDrivesStatus(t *testing.T) {
	// Emits: title "editing main.go" (→ running), some output, then title "Approve edit? (y/n)"
	// (→ awaiting_approval). \033=ESC \007=BEL.
	script := `printf '\033]2;editing main.go\007'; printf 'doing work\n'; printf '\033]2;Approve edit? (y/n)\007'; printf 'more\n'`
	p := NewProvider(Config{Name: "tui", Command: "sh", Args: []string{"-c", script}})
	sess, err := p.Create(context.Background(), t.TempDir(), "go")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	var sawRunningTitle, sawWaiting bool
	deadline := time.After(5 * time.Second)
loop:
	for {
		select {
		case ev, ok := <-sess.Events():
			if !ok {
				break loop
			}
			if e, ok := ev.Payload.(protocol.SessionStatus); ok {
				switch e.Status {
				case protocol.StatusRunning:
					if e.Detail == "editing main.go" {
						sawRunningTitle = true
					}
				case protocol.StatusAwaitingApproval:
					sawWaiting = true
				case protocol.StatusIdle:
					sess.Close()
				}
			}
		case <-deadline:
			t.Fatal("timed out")
		}
	}
	if !sawRunningTitle {
		t.Error("never saw a running status carrying the OSC title 'editing main.go'")
	}
	if !sawWaiting {
		t.Error("never saw an awaiting_approval status derived from the '(y/n)' title")
	}
}

// TestSession_AccountEnvReachesProcess proves the multi-account wiring: the active account's env
// overrides are merged into the spawned agent process's environment (so a per-account API key is
// what the agent actually runs with).
func TestSession_AccountEnvReachesProcess(t *testing.T) {
	p := NewProvider(Config{Name: "envcho", Command: "sh", Args: []string{"-c", "printf %s \"$OCULUS_TEST_KEY\""}})
	p.SetAccountEnv(func() map[string]string { return map[string]string{"OCULUS_TEST_KEY": "acct-secret-123"} })
	sess, err := p.Create(context.Background(), t.TempDir(), "go")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	var output strings.Builder
	deadline := time.After(5 * time.Second)
loop:
	for {
		select {
		case ev, ok := <-sess.Events():
			if !ok {
				break loop
			}
			switch e := ev.Payload.(type) {
			case protocol.OutputDelta:
				output.WriteString(e.Text)
			case protocol.SessionStatus:
				if e.Status == protocol.StatusIdle {
					sess.Close()
				}
			}
		case <-deadline:
			t.Fatal("timed out")
		}
	}
	if !strings.Contains(output.String(), "acct-secret-123") {
		t.Errorf("agent process did not see the account env; output = %q", output.String())
	}
}

// TestSession_RateLimitSurfaces proves a rate-limit line in the agent's OWN output is detected and
// surfaced as a status with a retry hint — the universal, account-API-free rate-limit signal.
func TestSession_RateLimitSurfaces(t *testing.T) {
	p := NewProvider(Config{Name: "rl", Command: "sh", Args: []string{"-c", "printf 'Error: 429 Too Many Requests — retry after 30s\\n'"}})
	sess, err := p.Create(context.Background(), t.TempDir(), "go")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	var detail string
	deadline := time.After(5 * time.Second)
loop:
	for {
		select {
		case ev, ok := <-sess.Events():
			if !ok {
				break loop
			}
			if e, ok := ev.Payload.(protocol.SessionStatus); ok {
				if strings.Contains(e.Detail, "Rate limited") {
					detail = e.Detail
				}
				if e.Status == protocol.StatusIdle {
					sess.Close()
				}
			}
		case <-deadline:
			t.Fatal("timed out")
		}
	}
	if detail == "" {
		t.Fatal("rate-limit condition was not surfaced as a status")
	}
	if !strings.Contains(detail, "30s") {
		t.Errorf("detail %q should carry the retry hint", detail)
	}
}

func TestSession_RejectsUnknownCommand(t *testing.T) {
	p := NewProvider(Config{Name: "nope", Command: "definitely-not-a-real-binary-xyz", Args: []string{"{prompt}"}})
	if _, err := p.Create(context.Background(), t.TempDir(), "x"); err == nil {
		t.Error("expected error for a command not on PATH")
	}
}

// TestSaveCreatesParentDir guards the writer against assuming ~/.oculus already exists — it used to
// work only because daemon startup happened to MkdirAll first, so any other caller (or a test) got a
// silent ENOENT.
func TestSaveCreatesParentDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "deeper", "agents.json")
	if err := Save(path, []Config{{Name: "codex", Command: "codex"}}); err != nil {
		t.Fatalf("Save into a missing dir: %v", err)
	}
	got, err := Load(path)
	if err != nil || len(got) != 1 || got[0].Name != "codex" {
		t.Fatalf("round-trip failed: %v %+v", err, got)
	}
}

// TestSubstituteMCPConfig: {mcp_config} expands to the daemon-written server file, which is how a
// BYO CLI agent gets the same MCP servers as the native harnesses with no adapter code.
func TestSubstituteMCPConfig(t *testing.T) {
	got := substitute([]string{"--mcp-config", "{mcp_config}", "{prompt}"}, "hi", "/repo", "", "/tmp/mcp.json")
	want := []string{"--mcp-config", "/tmp/mcp.json", "hi"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("substitute = %v, want %v", got, want)
	}
	// With no MCP servers configured the token expands to empty rather than leaking the literal.
	got = substitute([]string{"--mcp-config", "{mcp_config}", "{prompt}"}, "hi", "/repo", "", "")
	if strings.Contains(strings.Join(got, "|"), "{mcp_config}") {
		t.Errorf("unexpanded token leaked into argv: %v", got)
	}
}

// TestDetectRejectsNameCollisions is the guard for a real incident: `copilot` on a machine with
// AWS's ECS tool installed resolves on PATH, and a name-only check would have routed coding prompts
// to it. An entry that declares an identity marker must PROVE it is the tool we meant.
func TestDetectRejectsNameCollisions(t *testing.T) {
	// A command that exists but identifies as something else must not match.
	if identityMatches("echo", "definitely-not-in-echo-output") {
		t.Error("identityMatches must reject a binary that doesn't identify as the expected tool")
	}
	// A command whose output DOES contain the marker matches. `sh --version` prints its name.
	if !identityMatches("sh", "sh") {
		t.Skip("sh --version/--help didn't mention sh on this platform; identity probing is best-effort")
	}
}

// TestBuiltinsDeclareIdentityForCollisionProneNames locks the policy in place: any short, generic
// command name must carry a marker, or a future edit silently reintroduces the copilot bug.
func TestBuiltinsDeclareIdentityForCollisionProneNames(t *testing.T) {
	risky := map[string]bool{"copilot": true, "amp": true, "grok": true, "crush": true, "goose": true, "qwen": true}
	for _, b := range builtinAgents {
		if risky[b.cfg.Command] && b.identity == "" {
			t.Errorf("builtin %q has a collision-prone command name but declares no identity marker", b.cfg.Name)
		}
	}
}

// Two prompts racing on an idle session must not both start a turn.
//
// Prompt sampled `running`, dropped the lock, and then called a startTurn that claimed
// unconditionally — so both callers saw the session free, both were told they had been accepted, and
// both spawned a subprocess. The second overwrote s.cancel, leaving the first process running with
// nothing holding its handle: Stop could no longer reach it, and it went on writing output into the
// same conversation. Two agents answering one session, from the guard whose whole job was to prevent
// that.
func TestConcurrentPromptsStartExactlyOneTurn(t *testing.T) {
	dir := t.TempDir()
	p := NewProvider(Config{Name: "fake", Command: "/bin/sh", Args: []string{"/bin/sh", "-c", "sleep 0.5"}})
	sess, err := p.Create(context.Background(), dir, "")
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	defer func() { _ = sess.Close() }()

	const callers = 16
	var wg sync.WaitGroup
	var mu sync.Mutex
	accepted := 0
	start := make(chan struct{})
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if err := sess.Prompt(context.Background(), "go"); err == nil {
				mu.Lock()
				accepted++
				mu.Unlock()
			}
		}()
	}
	close(start)
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if accepted != 1 {
		t.Errorf("%d callers were told their prompt was accepted; only one turn can run, so %d "+
			"subprocess(es) are orphaned beyond Stop's reach", accepted, accepted-1)
	}
}

// A built-in agent must be honest about whether it remembers anything.
//
// startTurn switches to ResumeArgs for turn 2+ only when the config declares them, and not one of
// the ten built-ins does — so every codex/gemini/aider/copilot turn is a cold process with no
// history. The client renders a full transcript regardless, so the conversation LOOKS continuous;
// the hub now uses this to decide whether to hand the agent its own conversation back.
func TestBuiltinCLIAgentsReportNoContinuity(t *testing.T) {
	for _, cfg := range Detect() {
		s := &session{cfg: cfg}
		if s.HasContinuity() != (len(cfg.ResumeArgs) > 0) {
			t.Errorf("%s: continuity report disagrees with its own config", cfg.Name)
		}
	}
	// The shipped configuration, independent of what happens to be installed on this machine.
	for _, name := range []string{"codex", "gemini", "aider", "copilot"} {
		cfg := builtinConfig(name)
		if cfg.Name == "" {
			continue
		}
		s := &session{cfg: cfg}
		if s.HasContinuity() {
			t.Errorf("%s declares no ResumeArgs, so it cannot have continuity", name)
		}
	}
	// And one that DOES declare them must report true, or the flag is decorative.
	s := &session{cfg: Config{Name: "x", ResumeArgs: []string{"x", "--continue", "{prompt}"}}}
	if !s.HasContinuity() {
		t.Error("an agent with ResumeArgs resumes, and must say so")
	}
}

// A prompt that will be read by a REMOTE SHELL must arrive as one literal word.
//
// The remote-session template is `ssh <target> "cd '<path>' && <agent> {prompt_sh}"`: ssh joins its
// trailing arguments and the remote login shell parses the result. With a verbatim {prompt} there,
// `session.prompt` — which needs only capSteer — was arbitrary command execution on the owner's box
// under the owner's ssh key, while `remote.run` itself is gated at capOwner.
//
// This runs the composed command through a real shell rather than pattern-matching the string:
// metacharacters are legitimately PRESENT inside the quotes, so only execution settles it.
func TestRemotePromptIsNotRemoteShellCode(t *testing.T) {
	dir := t.TempDir()
	agent := filepath.Join(dir, "agent.sh")
	argsFile := filepath.Join(dir, "args")
	pwned := filepath.Join(dir, "pwned")
	script := "#!/bin/sh\nprintf '%s|%s' \"$#\" \"$1\" > " + argsFile + "\n"
	if err := os.WriteFile(agent, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	// Exactly the shape hub.go builds for a remote session.
	remoteCmd := agent + " {prompt_sh}"
	prompt := "x; touch " + pwned + "; echo done"
	expanded := substitute([]string{"host", remoteCmd}, prompt, dir, "", "")

	// ssh hands its trailing argument to the remote LOGIN SHELL — this is that step.
	if out, err := exec.Command("sh", "-c", expanded[len(expanded)-1]).CombinedOutput(); err != nil {
		t.Fatalf("remote command failed: %v (%s)", err, out)
	}
	if _, err := os.Stat(pwned); err == nil {
		t.Fatal("the prompt EXECUTED on the remote shell — a capSteer client just ran an arbitrary " +
			"command on the owner's machine under the owner's ssh key")
	}
	got, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("agent never ran: %v", err)
	}
	if string(got) != "1|"+prompt {
		t.Fatalf("the agent did not receive the prompt as one intact argument: got %q, want %q",
			got, "1|"+prompt)
	}
}

// The ordinary cases the same bug broke: an apostrophe was a remote syntax error, and a multi-word
// prompt word-split so the agent saw only its first word.
func TestRemotePromptSurvivesQuotingIntact(t *testing.T) {
	dir := t.TempDir()
	agent := filepath.Join(dir, "agent.sh")
	argsFile := filepath.Join(dir, "args")
	script := "#!/bin/sh\nprintf '%s|%s' \"$#\" \"$1\" > " + argsFile + "\n"
	if err := os.WriteFile(agent, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, prompt := range []string{"what's wrong?", "fix the auth bug", `a "quoted" $HOME ` + "`id`"} {
		expanded := substitute([]string{"host", agent + " {prompt_sh}"}, prompt, dir, "", "")
		out, err := exec.Command("sh", "-c", expanded[len(expanded)-1]).CombinedOutput()
		if err != nil {
			t.Fatalf("prompt %q was a remote shell error: %v (%s)", prompt, err, out)
		}
		got, _ := os.ReadFile(argsFile)
		if string(got) != "1|"+prompt {
			t.Errorf("prompt %q did not arrive intact: got %q", prompt, got)
		}
	}
}

// {prompt} must stay verbatim: a LOCAL template is argv handed to exec, and quoting it there would
// pass literal quote characters through to the agent as part of the prompt text.
func TestLocalPromptStaysVerbatim(t *testing.T) {
	got := substitute([]string{"claude", "-p", "{prompt}"}, "what's up; ls", "/repo", "", "")
	if got[len(got)-1] != "what's up; ls" {
		t.Fatalf("a local argv prompt was altered: %q", got[len(got)-1])
	}
}
