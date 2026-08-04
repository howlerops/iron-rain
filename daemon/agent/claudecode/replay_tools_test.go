package claudecode

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/howlerops/oculus/daemon/agent"
	"github.com/howlerops/oculus/daemon/protocol"
)

// writeTranscript lays a JSONL transcript out the way claude does — under
// <projects>/<encoded-dir>/<uuid>.jsonl — and returns the projects dir to inject.
func writeTranscript(t *testing.T, uuid string, lines []string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "-tmp-proj")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var body string
	for _, l := range lines {
		body += l + "\n"
	}
	if err := os.WriteFile(filepath.Join(dir, uuid+".jsonl"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// replayEvents runs a replay to completion and collects everything it emitted. The buffer is sized
// well past the transcript so emit never blocks and the test can stay single-goroutine.
func replayEvents(t *testing.T, uuid, projects string) []agent.Event {
	t.Helper()
	s := &session{
		id:     "cc_test",
		p:      &Provider{projectsDir: projects},
		events: make(chan agent.Event, 256),
		done:   make(chan struct{}),
	}
	s.replayTranscript(uuid)
	close(s.events)
	var got []agent.Event
	for ev := range s.events {
		got = append(got, ev)
	}
	return got
}

func toolEvents(evs []agent.Event) []protocol.SessionTool {
	var out []protocol.SessionTool
	for _, ev := range evs {
		if ev.Type == protocol.TypeSessionTool {
			out = append(out, ev.Payload.(protocol.SessionTool))
		}
	}
	return out
}

// A taken-over session used to replay as pure conversation — the agent appeared to have talked its
// way through the work and never run anything. A paired tool_use/tool_result must come back as a
// complete card: name, command summary, output, terminal status.
func TestReplayRebuildsToolCards(t *testing.T) {
	const uuid = "11111111-2222-3333-4444-555555555555"
	projects := writeTranscript(t, uuid, []string{
		`{"type":"assistant","uuid":"m1","message":{"role":"assistant","content":[{"type":"text","text":"Running the tests."},{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"go test ./..."}}]}}`,
		`{"type":"user","uuid":"m2","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"ok  everything"}]}}`,
	})

	tools := toolEvents(replayEvents(t, uuid, projects))
	if len(tools) != 1 {
		t.Fatalf("want 1 tool card, got %d", len(tools))
	}
	c := tools[0]
	if c.ID != "toolu_1" || c.Name != "Bash" {
		t.Errorf("card identity lost: %+v", c)
	}
	if c.Title != "go test ./..." {
		t.Errorf("command summary = %q, want the command", c.Title)
	}
	if c.Output != "ok  everything" || c.Status != "completed" {
		t.Errorf("output/status = %q/%q", c.Output, c.Status)
	}
}

// is_error must survive the round trip: a failed command replayed as "completed" would show a green
// card for work that broke.
func TestReplayMarksFailedToolCards(t *testing.T) {
	const uuid = "22222222-2222-3333-4444-555555555555"
	projects := writeTranscript(t, uuid, []string{
		`{"type":"assistant","uuid":"m1","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_x","name":"Bash","input":{"command":"false"}}]}}`,
		`{"type":"user","uuid":"m2","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_x","content":"exit 1","is_error":true}]}}`,
	})

	tools := toolEvents(replayEvents(t, uuid, projects))
	if len(tools) != 1 || tools[0].Status != "error" {
		t.Fatalf("want one error card, got %+v", tools)
	}
}

// A tool that never reported — interrupted turn, transcript ending mid-tool — must emit NOTHING.
// A "running" card in replayed history is a spinner that can never resolve, and "completed" would
// claim a result nobody recorded.
func TestReplaySkipsUnfinishedTools(t *testing.T) {
	const uuid = "33333333-2222-3333-4444-555555555555"
	projects := writeTranscript(t, uuid, []string{
		`{"type":"assistant","uuid":"m1","message":{"role":"assistant","content":[{"type":"text","text":"Starting."},{"type":"tool_use","id":"toolu_open","name":"Bash","input":{"command":"sleep 999"}}]}}`,
	})

	evs := replayEvents(t, uuid, projects)
	if got := toolEvents(evs); len(got) != 0 {
		t.Fatalf("unfinished tool must not be replayed, got %+v", got)
	}
	// The surrounding conversation still replays — dropping the card must not drop the message.
	var msgs int
	for _, ev := range evs {
		if ev.Type == protocol.TypeSessionMessage {
			msgs++
		}
	}
	if msgs != 1 {
		t.Fatalf("want the text message replayed, got %d", msgs)
	}
}

// A result whose tool_use was never seen would produce exactly the anonymous, untitled card this
// adapter was fixed to stop persisting.
func TestReplaySkipsOrphanToolResults(t *testing.T) {
	const uuid = "44444444-2222-3333-4444-555555555555"
	projects := writeTranscript(t, uuid, []string{
		`{"type":"user","uuid":"m1","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_ghost","content":"output with no owner"}]}}`,
	})

	if got := toolEvents(replayEvents(t, uuid, projects)); len(got) != 0 {
		t.Fatalf("orphan result must not become a nameless card, got %+v", got)
	}
}

// Cards and messages must interleave in the order they happened. Replaying all the text and then
// all the tools would render every command after the conversation that followed it.
func TestReplayPreservesOrder(t *testing.T) {
	const uuid = "55555555-2222-3333-4444-555555555555"
	projects := writeTranscript(t, uuid, []string{
		`{"type":"assistant","uuid":"m1","message":{"role":"assistant","content":[{"type":"text","text":"first"},{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"one"}}]}}`,
		`{"type":"user","uuid":"m2","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"done one"}]}}`,
		`{"type":"assistant","uuid":"m3","message":{"role":"assistant","content":[{"type":"text","text":"second"}]}}`,
	})

	var seq []string
	for _, ev := range replayEvents(t, uuid, projects) {
		switch p := ev.Payload.(type) {
		case protocol.SessionMessage:
			seq = append(seq, "msg:"+p.Text)
		case protocol.SessionTool:
			seq = append(seq, "tool:"+p.ID)
		}
	}
	want := []string{"msg:first", "tool:t1", "msg:second"}
	if len(seq) != len(want) {
		t.Fatalf("sequence = %v, want %v", seq, want)
	}
	for i := range want {
		if seq[i] != want[i] {
			t.Fatalf("sequence = %v, want %v", seq, want)
		}
	}
}

// A plain-string content (a typed user prompt) has no blocks at all and must decode to nothing
// rather than erroring out and ending the replay early.
func TestReplayHandlesStringContent(t *testing.T) {
	const uuid = "66666666-2222-3333-4444-555555555555"
	projects := writeTranscript(t, uuid, []string{
		`{"type":"user","uuid":"m1","message":{"role":"user","content":"just text"}}`,
		`{"type":"assistant","uuid":"m2","message":{"role":"assistant","content":[{"type":"tool_use","id":"t9","name":"Read","input":{"file_path":"/tmp/x.go"}}]}}`,
		`{"type":"user","uuid":"m3","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t9","content":[{"type":"text","text":"file body"}]}]}}`,
	})

	tools := toolEvents(replayEvents(t, uuid, projects))
	if len(tools) != 1 {
		t.Fatalf("want 1 card, got %d", len(tools))
	}
	if tools[0].Title != "/tmp/x.go" {
		t.Errorf("title = %q, want the file path", tools[0].Title)
	}
	// Array-shaped tool_result content must flatten, not come back empty.
	if tools[0].Output != "file body" {
		t.Errorf("output = %q, want the flattened text blocks", tools[0].Output)
	}
}

// truncRunes must never split a multi-byte character: invalid UTF-8 is silently rewritten to
// replacement characters when the event is encoded, turning the tail into mojibake.
func TestTruncRunesKeepsValidUTF8(t *testing.T) {
	s := "aaa" + "日本語テキスト"
	for n := 1; n < len(s); n++ {
		got := truncRunes(s, n)
		if !json.Valid([]byte(`"` + got + `"`)) {
			t.Fatalf("truncRunes(%d) produced invalid JSON string: %q", n, got)
		}
		for _, r := range got {
			if r == '�' {
				t.Fatalf("truncRunes(%d) split a rune: %q", n, got)
			}
		}
	}
}
