package hub

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/howlerops/oculus/daemon/agent/cli"
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
	// Reads a watcher is meant to have. Someone you let watch a session can see the session — and
	// nothing else. That last clause is what this list keeps getting wrong: the entries that have left
	// it were all machine-WIDE reads wearing a session-shaped name. The ticket board is the owner's
	// tracker account (ticket bodies included), the handoff index spans every repo on the Mac, the
	// activity ring and the approval rules outlive any one session, and a Loop is the verbatim prompt
	// the owner wrote for an unattended agent. All are capSteer or capOwner now.
	watcherReads := []string{
		"TypeSessionList", "TypeSessionSubscribe", "TypeParticipants", "TypeClientIdentify",
		"TypeThreadTree", "TypeTranscriptPage", "TypeProviderList",
		// TypeAgentList stays a watcher read — a steerer needs the roster to start a session — but
		// its reply is REDACTED for non-owners. See TestAgentRosterHidesEnvFromNonOwners: the Env
		// map on a custom agent holds API keys, and shipping them to a watch-only guest was a
		// disclosure the census could not see, because the census reasons about types and not payloads.
		"TypeAgentList",
		"TypeModelList", "TypeCommandList", "TypeProjectList",
		"TypeCheckpointList", "TypeDiscover", "TypeSessionDefaultsGet",
		"TypeWorktreeStatus", "TypeWorktreeConflicts",
		"TypeTelemetryStatus", "TypeUsageReport",
	}
	// Per-connection bookkeeping: these act on the sender's own connection and nothing else.
	//
	// TypeDeviceRegister used to be here and is now gated at capSteer. It was never per-connection:
	// a push token is a standing subscription to hub-wide content, and the fan-out is not
	// capability-aware — approval requests with their ids and details, agent errors and session
	// titles go to every registered token. A watch-only guest cannot answer an approval, so being
	// woken by one only hands them a decision that is not theirs.
	ownConnection := []string{
		"TypeLogUnsubscribe",
	}
	// Reads of a session's own WORK. An observer can already subscribe to the session and read its
	// transcript, and the diff is the same content by another route: it is what the agent did. Left
	// open deliberately — gating it would make "watch only" mean less than it says.
	sessionWork := []string{"TypeWorktreeDiff", "TypeWorkspaceDiff"}

	known := map[string]bool{}
	for _, group := range [][]string{watcherReads, ownConnection, sessionWork} {
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
//
// The delegate is re-dispatched, NOT merely scanned. Asking only whether "requireCapability" appears
// somewhere inside handleMCP passed the whole mcp.* family on the strength of the gate on mcp.upsert,
// while mcp.list — every server's command line, arguments and endpoint URL — sat ungated a few lines
// above it and this census reported nothing. A helper that switches on env.Type gates each type
// separately, so the census has to look at the arm for the type it is actually asking about.
// gateUse is what a REAL gate looks like: the refusal is acted on.
//
// Matching the bare call name accepted a handler that asks for permission and then ignores the
// answer — `_ = h.requireCapability(...)` reads as gated, sends the refusal to the client, and does
// the work anyway. That is not a hypothetical shape; it is exactly what a refactor produces when
// someone splits an `if !gate { return }` across lines and loses the return.
const gateUse = "if !h.require"

func ungatedDispatchCases() ([]string, error) {
	src, err := os.ReadFile("hub.go")
	if err != nil {
		return nil, err
	}
	s := string(src)
	nameRe := regexp.MustCompile(`protocol\.(Type\w+)`)
	delegateRe := regexp.MustCompile("h\\.((?:handle|require)\\w+)\\(")

	var out []string
	for _, arm := range censusArms(s) {
		// A gate written directly in the arm covers every type the arm names.
		if strings.Contains(arm.body, gateUse) {
			continue
		}
		// Otherwise each type is judged SEPARATELY. Deciding this per ARM is what hid mcp.list: its
		// arm names nine types and delegates to handleMCP, so the gate on mcp.upsert cleared mcp.list
		// along with it. One gate anywhere is not evidence about the type actually being asked about.
		gated := map[string]bool{}
		for _, d := range delegateRe.FindAllStringSubmatch(arm.body, -1) {
			delegate := hubFuncBody(d[1])
			if delegate == "" {
				continue
			}
			inner := censusArms(delegate)
			if len(inner) == 0 {
				// A delegate that does not re-dispatch handles whatever it was given, so its gate
				// applies to all of them (this is how the language-server family shares one).
				if strings.Contains(delegate, gateUse) {
					for _, t := range arm.types {
						gated[t] = true
					}
				}
				continue
			}
			for _, ia := range inner {
				if !strings.Contains(ia.body, gateUse) {
					continue
				}
				for _, t := range ia.types {
					gated[t] = true
				}
			}
		}
		for _, n := range nameRe.FindAllStringSubmatch(arm.header, -1) {
			if !gated[n[1]] {
				out = append(out, n[1])
			}
		}
	}
	return out, nil
}

// censusArm is one `case protocol.TypeX, protocol.TypeY:` and everything up to the next case.
type censusArm struct {
	header string   // the raw case list, for name extraction
	types  []string // the type names in it
	body   string
}

// censusArms splits a type switch into its arms. Used for both the top-level dispatch in hub.go and
// for any delegate that switches again on env.Type.
func censusArms(s string) []censusArm {
	caseRe := regexp.MustCompile(`\n\t+case (protocol\.Type\w+(?:,\s*\n?\s*protocol\.Type\w+)*):`)
	nameRe := regexp.MustCompile(`protocol\.(Type\w+)`)
	found := caseRe.FindAllStringSubmatchIndex(s, -1)
	arms := make([]censusArm, 0, len(found))
	for i, c := range found {
		end := len(s)
		if i+1 < len(found) {
			end = found[i+1][0]
		}
		header := s[c[2]:c[3]]
		var types []string
		for _, n := range nameRe.FindAllStringSubmatch(header, -1) {
			types = append(types, n[1])
		}
		arms = append(arms, censusArm{header: header, types: types, body: s[c[1]:end]})
	}
	return arms
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

// The agent roster is readable by a watcher; the API keys in it are not.
//
// agent.list is capWatch because a steerer needs the roster to start a session. But a custom agent's
// Env map is where a user's ANTHROPIC_API_KEY / OPENAI_API_KEY live — the app's own editor says as
// much — and the reply carried it verbatim. account.list is capOwner for exactly this reason, and
// agent.upsert/delete/visible all are too; only the read was open.
//
// The census above cannot catch this: it reasons about which TYPES are gated, and this type is
// correctly ungated. What was wrong was the payload.
func TestAgentRosterHidesEnvFromNonOwners(t *testing.T) {
	dir := t.TempDir()
	agents := filepath.Join(dir, "agents.json")
	if err := cli.Save(agents, []cli.Config{{
		Name: "work-codex", Command: "codex", Args: []string{"exec"},
		Env: map[string]string{"OPENAI_API_KEY": "sk-secret-value"},
	}}); err != nil {
		t.Fatal(err)
	}
	h := New()
	h.SetAgentsPath(agents, filepath.Join(dir, "visibility.json"))

	owner := h.agentList(true)
	var ownerSaw bool
	for _, a := range owner.Agents {
		if a.Name == "work-codex" && a.Env["OPENAI_API_KEY"] == "sk-secret-value" {
			ownerSaw = true
		}
	}
	if !ownerSaw {
		t.Fatal("the owner cannot see the env it configured — the editor would show an empty key")
	}

	watcher := h.agentList(false)
	for _, a := range watcher.Agents {
		if len(a.Env) != 0 {
			t.Fatalf("a non-owner was sent agent %q's env: %v\n\n"+
				"Those values are API keys that exist nowhere else. A watch-only guest asking for "+
				"the roster received them.", a.Name, a.Env)
		}
		if a.Name == "work-codex" && a.Command != "codex" {
			t.Fatal("redaction went too far — the roster itself must stay readable at capWatch, " +
				"or a steerer cannot pick an agent to start a session with")
		}
	}
}
