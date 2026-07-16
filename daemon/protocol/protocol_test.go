package protocol

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestEncodeDecode_RoundTrip(t *testing.T) {
	raw, err := Encode("req-1", TypeSessionCreate, SessionCreate{
		Provider: "opencode",
		Cwd:      "/work/repo",
		Prompt:   "add tests",
	})
	if err != nil {
		t.Fatal(err)
	}

	env, err := Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	if env.ID != "req-1" || env.Type != TypeSessionCreate {
		t.Fatalf("envelope id/type = %q/%q", env.ID, env.Type)
	}

	var got SessionCreate
	if err := env.Unmarshal(&got); err != nil {
		t.Fatal(err)
	}
	if got.Provider != "opencode" || got.Cwd != "/work/repo" || got.Prompt != "add tests" {
		t.Fatalf("payload = %+v", got)
	}
}

func TestEvent_HasNoID(t *testing.T) {
	raw, err := Encode("", TypeOutputDelta, OutputDelta{SessionID: "s1", Text: "hi"})
	if err != nil {
		t.Fatal(err)
	}
	// Events carry no id; it must be omitted from the JSON, not present as "".
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if _, present := m["id"]; present {
		t.Fatalf("event envelope must omit id, got %s", raw)
	}
}

func TestDecode_RejectsInvalid(t *testing.T) {
	if _, err := Decode([]byte("not json")); err == nil {
		t.Fatal("Decode must reject invalid JSON")
	}
}

// Golden JSON vectors: the exact wire bytes for representative messages. The Swift
// side encodes/decodes against these same vectors to lock protocol parity.
func TestProtocolGoldenVectors(t *testing.T) {
	const vectorsPath = "../../protocol/vectors/messages.json"

	cases := map[string]struct {
		id, typ string
		payload any
	}{
		"session_create":   {"req-1", TypeSessionCreate, SessionCreate{Provider: "opencode", Cwd: "/work/repo", Prompt: "add tests"}},
		"approval_request": {"", TypeApprovalRequest, ApprovalRequest{ApprovalID: "a1", SessionID: "s1", Tool: "bash", Input: json.RawMessage(`{"command":"rm -rf build"}`)}},
		"approval_respond": {"req-2", TypeApprovalRespond, ApprovalRespond{ApprovalID: "a1", Decision: DecisionAllow}},
		"session_status":   {"", TypeSessionStatus, SessionStatus{SessionID: "s1", Status: StatusAwaitingApproval}},
	}

	got := map[string]json.RawMessage{}
	for name, c := range cases {
		raw, err := Encode(c.id, c.typ, c.payload)
		if err != nil {
			t.Fatal(err)
		}
		got[name] = json.RawMessage(raw)
	}

	if os.Getenv("OCULUS_UPDATE_VECTORS") == "1" {
		_ = os.MkdirAll(filepath.Dir(vectorsPath), 0o755)
		b, _ := json.MarshalIndent(got, "", "  ")
		if err := os.WriteFile(vectorsPath, append(b, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s", vectorsPath)
	}

	raw, err := os.ReadFile(vectorsPath)
	if err != nil {
		t.Fatalf("read golden vectors (generate with OCULUS_UPDATE_VECTORS=1): %v", err)
	}
	var want map[string]json.RawMessage
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	for name := range cases {
		if string(normalize(t, got[name])) != string(normalize(t, want[name])) {
			t.Fatalf("%s vector mismatch:\n got  %s\n want %s", name, got[name], want[name])
		}
	}
}

// normalize re-marshals to canonical form so key ordering differences don't matter.
func normalize(t *testing.T, b []byte) []byte {
	t.Helper()
	var v any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	out, _ := json.Marshal(v)
	return out
}
