package hub

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Every message type must make a DELIBERATE decision about who may send it.
//
// project.browse had no capability check, and every connected client holds capWatch — so someone
// admitted by a watch-only invite link, meant to read one session and nothing else, could list any
// directory the daemon's user can read and walk the entire machine from the `Parent` the reply hands
// back. It was not a wrong decision; it was the absence of one, sitting between two handlers that
// both get it right.
//
// Finding it one handler at a time does not scale: the dispatch switch has 137 cases. So the rule is
// enforced here instead — a new case either gates itself, or names itself below and says why.
//
// Every entry below is a DECISION, with the reason it was made. There is no residual "not looked at
// yet" bucket: the twenty types that were once in one are now gated — the language-server read family
// at capSteer to match the file browser it duplicates, the owner's accounts, remote hosts, devices
// and notification settings at capOwner, and activity.mark_read at capSteer because it mutates a feed
// every device reads.
//
// Gating costs the ordinary user nothing, which is what made the decision easy in the end: with
// sharing off — the default, and the only state a solo user is ever in — roleRegistry.role() returns
// owner for every connection, so every check here passes unconditionally. These boundaries exist
// only once someone has deliberately turned sharing on and invited another person in.
func TestEveryMessageTypeDecidesWhoMaySendIt(t *testing.T) {
	// Reads a watcher is meant to have. Someone you let watch a session can see the session.
	watcherReads := []string{
		"TypeSessionList", "TypeSessionSubscribe", "TypeParticipants", "TypeClientIdentify",
		"TypeThreadTree", "TypeTranscriptPage", "TypeProviderList", "TypeAgentList",
		"TypeModelList", "TypeCommandList", "TypeProjectList", "TypeLoopList", "TypeHandoffList",
		"TypeCheckpointList", "TypeActivityList", "TypeDiscover", "TypeSessionDefaultsGet",
		"TypeApprovalRulesList", "TypeWorktreeStatus", "TypeWorktreeConflicts",
		"TypeIntegrationStatus", "TypeTelemetryStatus", "TypeUsageReport",
	}
	// The ticket board. Read-only against the user's own tracker credentials.
	trackerReads := []string{
		"TypeIssueList", "TypeIssueStates", "TypeIssueColumns", "TypeIssueProjects",
		"TypeIssueDetail", "TypeIssueMembers", "TypeIssueLabels", "TypeIssueCycles",
		"TypeIssueImage", "TypeJiraSites",
	}
	// Per-connection bookkeeping: these act on the sender's own connection and nothing else.
	ownConnection := []string{
		"TypeDeviceRegister", "TypeDeviceCredentialAck", "TypeLogUnsubscribe", "TypePreviewDOMResult",
	}
	// Reads of a session's own WORK. An observer can already subscribe to the session and read its
	// transcript, and the diff is the same content by another route: it is what the agent did. Left
	// open deliberately — gating it would make "watch only" mean less than it says.
	sessionWork := []string{"TypeWorktreeDiff", "TypeWorkspaceDiff"}

	known := map[string]bool{}
	for _, group := range [][]string{watcherReads, trackerReads, ownConnection, sessionWork} {
		for _, n := range group {
			known[n] = true
		}
	}

	ungated, err := ungatedDispatchCases()
	if err != nil {
		t.Fatal(err)
	}

	var undeclared []string
	for _, n := range ungated {
		if !known[n] {
			undeclared = append(undeclared, n)
		}
	}
	if len(undeclared) > 0 {
		sort.Strings(undeclared)
		t.Errorf("%d message type(s) are reachable by ANY connected client with no capability "+
			"check and no entry in this census:\n  %s\n\nEvery client holds capWatch, including "+
			"someone admitted by a watch-only invite. Either gate the handler with "+
			"requireCapability, or add it to a group above with a reason.",
			len(undeclared), strings.Join(undeclared, "\n  "))
	}

	// The other direction: an entry that has since been gated should leave the list, so the backlog
	// shrinks visibly instead of turning into decoration.
	live := map[string]bool{}
	for _, n := range ungated {
		live[n] = true
	}
	var stale []string
	for n := range known {
		if !live[n] {
			stale = append(stale, n)
		}
	}
	if len(stale) > 0 {
		sort.Strings(stale)
		t.Errorf("these are gated now and can come out of the census: %s", strings.Join(stale, ", "))
	}
}

// ungatedDispatchCases returns the message types whose dispatch arm performs no capability check,
// following one level of delegation into an h.handleX or h.requireX helper — several arms gate inside
// one (handleMCP), and the language-server family shares a named gate that carries their common reason.
func ungatedDispatchCases() ([]string, error) {
	src, err := os.ReadFile("hub.go")
	if err != nil {
		return nil, err
	}
	s := string(src)
	caseRe := regexp.MustCompile(`\n\tcase (protocol\.Type\w+(?:,\s*\n?\s*protocol\.Type\w+)*):`)
	nameRe := regexp.MustCompile(`protocol\.(Type\w+)`)
	delegateRe := regexp.MustCompile("h\\.((?:handle|require)\\w+)\\(")

	cases := caseRe.FindAllStringSubmatchIndex(s, -1)
	var out []string
	for i, c := range cases {
		start := c[1]
		end := len(s)
		if i+1 < len(cases) {
			end = cases[i+1][0]
		}
		body := s[start:end]
		if strings.Contains(body, "requireCapability") {
			continue
		}
		gated := false
		for _, d := range delegateRe.FindAllStringSubmatch(body, -1) {
			if strings.Contains(hubFuncBody(d[1]), "requireCapability") {
				gated = true
				break
			}
		}
		if gated {
			continue
		}
		for _, n := range nameRe.FindAllStringSubmatch(s[c[2]:c[3]], -1) {
			out = append(out, n[1])
		}
	}
	return out, nil
}

// hubFuncBody returns the body of `func (h *Hub) name(...)` from anywhere in the package.
func hubFuncBody(name string) string {
	entries, err := os.ReadDir(".")
	if err != nil {
		return ""
	}
	re := regexp.MustCompile(`func \(h \*Hub\) ` + regexp.QuoteMeta(name) + `\([^)]*\)[^{]*\{`)
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		b, err := os.ReadFile(e.Name())
		if err != nil {
			continue
		}
		s := string(b)
		m := re.FindStringIndex(s)
		if m == nil {
			continue
		}
		depth, i := 1, m[1]
		for i < len(s) && depth > 0 {
			switch s[i] {
			case '{':
				depth++
			case '}':
				depth--
			}
			i++
		}
		return s[m[1]:i]
	}
	return ""
}

// readFileString is a tiny helper shared by the source-structure tests in this package.
func readFileString(name string) (string, error) {
	b, err := os.ReadFile(name)
	return string(b), err
}
