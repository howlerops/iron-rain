package protocol

import "testing"

// The three status vocabularies overlap in every word but one, which is exactly how they get mixed up.
//
// A finished TOOL is "completed"; a finished SUB-AGENT is "done"; a finished TURN is "done" or "idle".
// The AG-UI adapter reported a finished tool as StatusDone — which reads as obviously right and was
// not. The hub retires a tool card on IsToolFinished only, and its default treats every other word as
// STILL RUNNING, so the card stayed outstanding all turn and turn close sealed it as an error with the
// seal note written over its real output. Every successful AG-UI tool call was rendered and stored as
// a failure, silently, for the whole life of the adapter.
//
// This test exists to make that specific confusion a test failure rather than a discovery.
func TestTurnStatusIsNotAToolStatus(t *testing.T) {
	if IsToolFinished(StatusDone) {
		t.Error("StatusDone must not read as a finished TOOL — a tool completes, a turn is done")
	}
	if IsToolFinished(StatusIdle) {
		t.Error("StatusIdle is a turn status and must not retire a tool card")
	}
	if !IsToolFinished(ToolCompleted) || !IsToolFinished(ToolError) {
		t.Error("ToolCompleted and ToolError are the only terminal tool statuses")
	}
	if IsToolFinished(ToolRunning) {
		t.Error("a running tool is not finished")
	}

	// Sub-agents genuinely do use "done" — the vocabularies differ, and that difference is the trap.
	if !IsSubAgentFinished(SubAgentDone) || !IsSubAgentFinished(SubAgentError) {
		t.Error("SubAgentDone and SubAgentError are the terminal sub-agent statuses")
	}
	if IsSubAgentFinished(SubAgentRunning) {
		t.Error("a running sub-agent is not finished")
	}
	if IsSubAgentFinished(ToolCompleted) {
		t.Error("\"completed\" is the TOOL word; a sub-agent finishes as \"done\"")
	}

	// And the words themselves must not drift into agreement, or the distinction stops being testable.
	if ToolCompleted == SubAgentDone {
		t.Error("the tool and sub-agent terminal words have converged; the hub keys on them separately")
	}
}
