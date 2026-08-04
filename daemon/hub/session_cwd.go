package hub

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/howlerops/oculus/daemon/fsaccess"
	"github.com/howlerops/oculus/daemon/worktree"
)

// validateSessionCwd judges the working directory a session is about to start in.
//
// It exists because that directory is not just where the agent runs: Hub.fsGuard turns EVERY live
// session's meta.cwd into an allowed root for fs.read, fs.write, fs.tree and fs.search, all of which
// are capSteer. session.create is capSteer too, and its Cwd was taken from the wire and used as-is.
// So the chain was: create a session with Cwd = ~/.oculus, then read ~/.oculus/daemon.key — the
// daemon's private key, which with no forward secrecy in the transport decrypts every session ever
// recorded — or write ~/.oculus/agents.json, whose Command/Args the daemon later exec's, which is
// arbitrary command execution as the owner with the capOwner check on agent.upsert simply skipped.
// One unvalidated field undid owner-only shell, worktree setup trust and per-device credentials at
// once, because all three persist their state in that directory.
//
// Two rules, and they are gated differently on purpose:
//
//   - The protected-path refusal applies to EVERYONE, including the owner. It has to: roles are off
//     in the default single-user deployment, where roleRegistry.role reports every connection as the
//     owner, so any rule that only bites a steerer would be a no-op on most installs — including the
//     install this was found on. Nobody, in any deployment, has a reason to start an agent inside
//     ~/.oculus or ~/.ssh, so nothing legitimate is lost by refusing it outright.
//   - The must-be-a-known-place rule applies only when the requester is a KNOWN non-owner. That is
//     the tighter rule and the one worth having, but it cannot be universal: the owner's own folder
//     picker, the turn-smoke CLI, and a restored session's stored cwd all legitimately name a path
//     that is in no registry yet, and autoProjects exists precisely to turn such a cwd into a
//     project. Restricting the owner there would break the product to defend the owner from
//     themselves. A steerer is a different person with a different threat model: they were invited to
//     drive an agent, not to choose which of the owner's directories it runs in, and they cannot add
//     a project (project.add is capOwner) — so for them, "somewhere the owner already registered" is
//     both enforceable and complete.
//
// What stays open, stated plainly: an owner-role connection may still start a session in any
// ordinary directory on the machine — the user's whole home, /etc, another user's readable files.
// That is a deliberate stopping point, not an oversight. On a single-user install the caller IS the
// owner and already has a shell; where roles are enforced, the owner is the person whose credentials
// the agent spends. The escalation this closes is the one that crossed a privilege boundary.
func (h *Hub) validateSessionCwd(ctx context.Context, cwd string) error {
	cwd = strings.TrimSpace(cwd)
	if cwd == "" {
		return nil // no cwd given: the project path or the provider's own default decides
	}
	// A relative cwd has no defined meaning here — it would be resolved against the daemon's process
	// working directory, which under launchd is "/". Every real caller sends an absolute path.
	if !filepath.IsAbs(cwd) {
		return fmt.Errorf("session working directory must be an absolute path: %s", cwd)
	}
	if label := fsaccess.ProtectedPath(cwd); label != "" {
		return fmt.Errorf("refusing to start a session in %s — that directory holds the daemon's keys and trust records, not project files", label)
	}
	if requesterRole(ctx) != RoleSteerer {
		return nil
	}
	if h.knownProjectLocation(cwd) {
		return nil
	}
	return fmt.Errorf("only the session owner can start a session in an unregistered directory (%s) — pick one of the owner's projects", cwd)
}

// knownProjectLocation reports whether cwd is a registered project, inside one, or inside the base
// directory the daemon creates session worktrees in.
//
// The worktree base counts because it is the daemon's own output, not a caller's choice: an isolated
// session lives at <base>/<repo>/<name> and a multi-repo workspace at <base>/<workspace>/<repo>,
// neither of which is ever registered as a project (autoRegisterCwd deliberately records the MAIN
// repo instead). Leaving it out would refuse the restore of a steerer's own worktree session.
func (h *Hub) knownProjectLocation(cwd string) bool {
	target := fsaccess.NormalizePath(cwd)
	if target == "" {
		return false
	}
	var allowed []string
	h.mu.Lock()
	reg := h.projects
	base := h.worktreeBase
	h.mu.Unlock()
	if base == "" {
		base = worktree.DefaultBase()
	}
	allowed = append(allowed, base)
	if reg != nil {
		for _, p := range reg.List() { // List has its own lock — call it with h.mu released
			allowed = append(allowed, p.Path)
		}
	}
	for _, a := range allowed {
		root := fsaccess.NormalizePath(a)
		if root == "" {
			continue
		}
		if target == root || strings.HasPrefix(target, root+string(os.PathSeparator)) {
			return true
		}
	}
	return false
}
