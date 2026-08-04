package worktree

import "testing"

// TestParsePRView covers the shapes `gh pr view --json state,url,statusCheckRollup` actually emits:
// a mixed CheckRun/StatusContext array, in-flight runs, no checks at all, and nodes whose shape we
// don't recognise — which must be ignored, never turned into an error or a miscount.
func TestParsePRView(t *testing.T) {
	tests := []struct {
		name    string
		json    string
		wantErr bool
		state   string
		url     string
		checks  *PRChecks // nil = the result must carry no checks
	}{
		{
			name: "mixed CheckRun and StatusContext",
			json: `{"state":"OPEN","url":"https://github.com/o/r/pull/7","statusCheckRollup":[
				{"__typename":"CheckRun","name":"build","status":"COMPLETED","conclusion":"SUCCESS","detailsUrl":"https://x/1"},
				{"__typename":"CheckRun","name":"test (macos)","status":"COMPLETED","conclusion":"FAILURE","detailsUrl":"https://x/2"},
				{"__typename":"CheckRun","name":"lint","status":"IN_PROGRESS","conclusion":"","detailsUrl":"https://x/3"},
				{"__typename":"StatusContext","context":"ci/circleci","state":"SUCCESS","targetUrl":"https://x/4"},
				{"__typename":"StatusContext","context":"license/cla","state":"ERROR","targetUrl":"https://x/5"}
			]}`,
			state: "OPEN", url: "https://github.com/o/r/pull/7",
			// A failure outranks the in-flight run: pending work can't un-fail a broken build.
			checks: &PRChecks{State: "FAILURE", Passed: 2, Failed: 2, Pending: 1,
				Failing: []string{"test (macos)", "license/cla"}},
		},
		{
			name: "all green",
			json: `{"state":"OPEN","url":"u","statusCheckRollup":[
				{"__typename":"CheckRun","name":"build","status":"COMPLETED","conclusion":"SUCCESS"},
				{"__typename":"CheckRun","name":"docs","status":"COMPLETED","conclusion":"SKIPPED"},
				{"__typename":"CheckRun","name":"flaky","status":"COMPLETED","conclusion":"NEUTRAL"}
			]}`,
			state: "OPEN", url: "u",
			checks: &PRChecks{State: "SUCCESS", Passed: 3},
		},
		{
			name: "everything still queued",
			json: `{"state":"OPEN","url":"u","statusCheckRollup":[
				{"__typename":"CheckRun","name":"build","status":"QUEUED","conclusion":""},
				{"__typename":"StatusContext","context":"ci/circleci","state":"PENDING"}
			]}`,
			state: "OPEN", url: "u",
			checks: &PRChecks{State: "PENDING", Pending: 2},
		},
		{
			name:  "empty rollup — a repo with no CI is not a failure",
			json:  `{"state":"MERGED","url":"u","statusCheckRollup":[]}`,
			state: "MERGED", url: "u",
		},
		{
			name:  "rollup absent entirely",
			json:  `{"state":"CLOSED","url":"u"}`,
			state: "CLOSED", url: "u",
		},
		{
			name:  "rollup null (older gh / no checks)",
			json:  `{"state":"OPEN","url":"u","statusCheckRollup":null}`,
			state: "OPEN", url: "u",
		},
		{
			name: "unknown node shapes and conclusions are ignored, not errors",
			json: `{"state":"OPEN","url":"u","statusCheckRollup":[
				{"__typename":"SomeFutureNode","id":"abc"},
				{"__typename":"CheckRun","name":"weird","status":"COMPLETED","conclusion":"WHO_KNOWS"},
				{"__typename":"CheckRun","name":"build","status":"COMPLETED","conclusion":"SUCCESS"}
			]}`,
			state: "OPEN", url: "u",
			checks: &PRChecks{State: "SUCCESS", Passed: 1},
		},
		{
			name: "failing names are capped",
			json: `{"state":"OPEN","url":"u","statusCheckRollup":[
				{"__typename":"CheckRun","name":"f1","status":"COMPLETED","conclusion":"FAILURE"},
				{"__typename":"CheckRun","name":"f2","status":"COMPLETED","conclusion":"TIMED_OUT"},
				{"__typename":"CheckRun","name":"f3","status":"COMPLETED","conclusion":"CANCELLED"},
				{"__typename":"CheckRun","name":"f4","status":"COMPLETED","conclusion":"ACTION_REQUIRED"},
				{"__typename":"CheckRun","name":"f5","status":"COMPLETED","conclusion":"STARTUP_FAILURE"},
				{"__typename":"CheckRun","name":"f6","status":"COMPLETED","conclusion":"FAILURE"},
				{"__typename":"CheckRun","name":"f7","status":"COMPLETED","conclusion":"FAILURE"}
			]}`,
			state: "OPEN", url: "u",
			checks: &PRChecks{State: "FAILURE", Failed: 7,
				Failing: []string{"f1", "f2", "f3", "f4", "f5"}},
		},
		{
			name:    "malformed gh output",
			json:    `not json`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parsePRView([]byte(tt.json))
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected an error, got none")
				}
				return
			}
			if err != nil {
				t.Fatalf("parsePRView: %v", err)
			}
			if got.State != tt.state || got.URL != tt.url {
				t.Errorf("state,url = %q,%q want %q,%q", got.State, got.URL, tt.state, tt.url)
			}
			if tt.checks == nil {
				if got.Checks != nil {
					t.Fatalf("checks = %+v, want none", *got.Checks)
				}
				return
			}
			if got.Checks == nil {
				t.Fatalf("checks = nil, want %+v", *tt.checks)
			}
			c := *got.Checks
			w := *tt.checks
			if c.State != w.State || c.Passed != w.Passed || c.Failed != w.Failed || c.Pending != w.Pending {
				t.Errorf("checks = %+v, want %+v", c, w)
			}
			if len(c.Failing) != len(w.Failing) {
				t.Fatalf("failing = %v, want %v", c.Failing, w.Failing)
			}
			for i := range w.Failing {
				if c.Failing[i] != w.Failing[i] {
					t.Errorf("failing = %v, want %v", c.Failing, w.Failing)
					break
				}
			}
		})
	}
}
