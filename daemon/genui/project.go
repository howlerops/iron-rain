package genui

import (
	"encoding/json"

	"github.com/howlerops/oculus/daemon/protocol"
)

// Projection: turning a harness's STRUCTURED events into catalog components.
//
// The fence path (see genui.go) depends on the model choosing to emit an iron:ui block. That works,
// but it is opt-in per message and per model — a harness whose model never learned the grammar shows
// plain text forever. Projection covers the other half: when the daemon already receives a typed
// event carrying exactly the data a component renders, it can build the component itself and the
// model never has to cooperate.
//
// Only events whose shape is already normalized across harnesses are projected. Inventing a
// component from unstructured output would be guesswork, and a wrong card is worse than no card.

// ProjectTodos renders an agent's to-do list as a checklist component.
//
// The id is stable per session so successive updates REPLACE the card in place (the client keys on
// component id) rather than stacking a new checklist after every tool call.
func ProjectTodos(sessionID string, todos []protocol.Todo) (protocol.UIComponent, bool) {
	if len(todos) == 0 {
		return protocol.UIComponent{}, false
	}
	if len(todos) > maxRows {
		todos = todos[:maxRows]
	}
	items := make([]checklistItem, 0, len(todos))
	for _, t := range todos {
		items = append(items, checklistItem{Text: t.Content, Status: checklistStatus(t.Status)})
	}
	props, err := json.Marshal(map[string]any{"title": "Plan", "items": items})
	if err != nil {
		return protocol.UIComponent{}, false
	}
	return protocol.UIComponent{
		SessionID: sessionID,
		ID:        "todos:" + sessionID, // stable → updates in place
		Component: "checklist",
		SchemaV:   1,
		Status:    "ready",
		Props:     props,
		// Mandatory fallback, same contract as a model-emitted component.
		FallbackText: todosFallback(items),
	}, true
}

// checklistStatus maps the normalized todo statuses onto the checklist component's vocabulary.
func checklistStatus(s string) string {
	switch s {
	case "completed", "done":
		return "done"
	case "in_progress", "active":
		return "active"
	case "failed", "error":
		return "failed"
	default:
		return "pending"
	}
}

// checklistItem is one row of the checklist component's props.
type checklistItem struct {
	Text   string `json:"text"`
	Status string `json:"status"`
}

// todosFallback renders the markdown a client shows when it can't draw the component.
func todosFallback(items []checklistItem) string {
	out := ""
	for _, i := range items {
		mark := "- [ ] "
		if i.Status == "done" {
			mark = "- [x] "
		}
		out += mark + i.Text + "\n"
	}
	return out
}
