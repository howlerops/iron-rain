package genui

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/howlerops/oculus/daemon/protocol"
)

func TestProjectTodos(t *testing.T) {
	todos := []protocol.Todo{
		{Content: "Read the code", Status: "completed"},
		{Content: "Write the fix", Status: "in_progress"},
		{Content: "Run tests", Status: "pending"},
	}
	comp, ok := ProjectTodos("sess-1", todos)
	if !ok {
		t.Fatal("a non-empty todo list must project")
	}
	if comp.Component != "checklist" {
		t.Errorf("component = %q, want checklist", comp.Component)
	}
	// The id must be STABLE per session so successive updates replace the card rather than stacking.
	if comp.ID != "todos:sess-1" {
		t.Errorf("id = %q, want a stable per-session id", comp.ID)
	}
	if again, _ := ProjectTodos("sess-1", todos[:2]); again.ID != comp.ID {
		t.Error("a later projection for the same session must reuse the id")
	}
	// The projected component must satisfy the SAME validation a model-emitted one does — otherwise
	// the daemon could emit something its own catalog would reject.
	if !knownComponent(comp.Component) {
		t.Fatal("projection produced a component outside the catalog")
	}
	if comp.FallbackText == "" {
		t.Error("fallback text is mandatory for every component")
	}
	if !strings.Contains(comp.FallbackText, "[x] Read the code") {
		t.Errorf("fallback should mark completed items: %q", comp.FallbackText)
	}

	var props struct {
		Title string          `json:"title"`
		Items []checklistItem `json:"items"`
	}
	if err := json.Unmarshal(comp.Props, &props); err != nil {
		t.Fatal(err)
	}
	want := []string{"done", "active", "pending"}
	for i, w := range want {
		if props.Items[i].Status != w {
			t.Errorf("item %d status = %q, want %q", i, props.Items[i].Status, w)
		}
	}

	// An empty list projects nothing rather than an empty card.
	if _, ok := ProjectTodos("sess-1", nil); ok {
		t.Error("an empty todo list must not project a component")
	}
}

func TestChecklistStatusMapping(t *testing.T) {
	cases := map[string]string{
		"completed": "done", "done": "done",
		"in_progress": "active", "active": "active",
		"failed": "failed", "error": "failed",
		"pending": "pending", "": "pending", "something-new": "pending",
	}
	for in, want := range cases {
		if got := checklistStatus(in); got != want {
			t.Errorf("checklistStatus(%q) = %q, want %q", in, got, want)
		}
	}
}
