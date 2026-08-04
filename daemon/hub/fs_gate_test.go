package hub

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/project"
	"github.com/howlerops/oculus/daemon/protocol"
	"github.com/howlerops/oculus/daemon/transport"
)

// The property under test: the file-system surface is reachable only by someone the owner trusted
// to steer, and no role at all can reach a repository's .git. These drive the real dispatch over a
// real handshake (see gateHub in shell_gate_test.go) rather than asserting on roleAllows, because a
// unit test of the capability table keeps passing if a call site loses its gate — which is exactly
// how these handlers shipped ungated in the first place.

// fsGateHub returns a hub whose fs guard has one allowed root: a temp project directory containing
// a secret-looking file and a .git with a config in it.
func fsGateHub(t *testing.T, ownerSecret string) (*Hub, func(secret string) *transport.Conn, string) {
	t.Helper()
	h, dial := gateHub(t, ownerSecret)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".env"), []byte("API_KEY=super-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".git", "config"), []byte("[core]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	reg, err := project.Load(filepath.Join(t.TempDir(), "projects.json"))
	if err != nil {
		t.Fatalf("project registry: %v", err)
	}
	if _, err := reg.Add(root); err != nil {
		t.Fatalf("register project: %v", err)
	}
	h.SetProjects(reg)
	return h, dial, root
}

// TestObserverCannotReadFiles is the confidentiality hole this closes. capWatch is granted to every
// connected client, so an ungated fs.* handler let the read-only role enumerate, grep, open and diff
// every allowed root — source, .env, anything ever committed.
func TestObserverCannotReadFiles(t *testing.T) {
	h, dial, root := fsGateHub(t, "owner-secret")
	inv := h.invites.create("Pat", RoleObserver, time.Hour)
	observer := dial(inv.Secret)

	cases := []struct {
		typ     string
		payload any
	}{
		{protocol.TypeFSTree, protocol.FSTreeReq{Path: root}},
		{protocol.TypeFSRead, protocol.FSReadReq{Path: filepath.Join(root, ".env")}},
		{protocol.TypeFSReadBytes, protocol.FSReadBytesReq{Path: filepath.Join(root, ".env")}},
		{protocol.TypeFSSearch, protocol.FSSearchReq{Query: "API_KEY"}},
		{protocol.TypeFSDiff, protocol.FSDiffReq{Path: root}},
	}
	for _, c := range cases {
		env := ask(t, observer, "obs-"+c.typ, c.typ, c.payload)
		msg := errMessage(t, env)
		if msg == "" {
			t.Errorf("%s: an observer must be refused, got a %s reply", c.typ, env.Type)
			continue
		}
		// The refusal has to tell them what to do about it — the client renders these controls
		// unconditionally, so a bare failure reads as a broken app.
		if !strings.Contains(msg, "permission to steer") {
			t.Errorf("%s: refusal should point at the owner, got %q", c.typ, msg)
		}
		// Nothing of the file may leak through the error text either.
		if strings.Contains(msg, "super-secret") {
			t.Errorf("%s: refusal leaked file content: %q", c.typ, msg)
		}
	}
}

// TestSteererMayStillBrowse: the gate is capSteer, not capOwner. Someone the owner invited to steer
// keeps the editor, or the feature is gone for the collaborator it exists for.
func TestSteererMayStillBrowse(t *testing.T) {
	h, dial, root := fsGateHub(t, "owner-secret")
	inv := h.invites.create("Sam", RoleSteerer, time.Hour)
	steerer := dial(inv.Secret)

	env := ask(t, steerer, "steer-read", protocol.TypeFSRead,
		protocol.FSReadReq{Path: filepath.Join(root, ".env")})
	if env.Type != protocol.TypeOK {
		t.Fatalf("a steerer must still read files, got %s: %q", env.Type, errMessage(t, env))
	}
	var f protocol.FSFile
	if err := env.Unmarshal(&f); err != nil {
		t.Fatalf("decode fs.read reply: %v", err)
	}
	if !strings.Contains(f.Content, "super-secret") {
		t.Fatalf("fs.read returned no content: %+v", f)
	}

	if env := ask(t, steerer, "steer-tree", protocol.TypeFSTree, protocol.FSTreeReq{Path: root}); env.Type != protocol.TypeOK {
		t.Fatalf("a steerer must still browse, got %s: %q", env.Type, errMessage(t, env))
	}
}

// TestNobodyMayWriteGitConfig is the privilege escalation. .git/config's core.fsmonitor is a string
// git runs, and the daemon runs git in these repos on ordinary steer-level actions, so a steerer who
// could write this file had a shell as the owner. The owner is refused too — the rule is about the
// path, not the role, because the daemon's own git invocations don't distinguish who asked.
func TestNobodyMayWriteGitConfig(t *testing.T) {
	h, dial, root := fsGateHub(t, "owner-secret")
	h.SetRolesEnabled(true) // otherwise every connection is the owner and the steerer case is vacuous
	inv := h.invites.create("Sam", RoleSteerer, time.Hour)

	// One steerer connection, dialled once and reused for both writes below. Invites are single-device
	// by default, so redeeming the same one twice is REFUSED — correctly, that is the point of the
	// change. The second write here is the same device continuing to work, not a second guest.
	steerer := dial(inv.Secret)

	cfg := filepath.Join(root, ".git", "config")
	evil := "[core]\n\tfsmonitor = \"touch /tmp/pwned\"\n"
	for _, who := range []struct {
		name string
		c    *transport.Conn
	}{
		{"steerer", steerer},
		{"owner", dial("owner-secret")},
	} {
		c := who.c
		env := ask(t, c, "write-git", protocol.TypeFSWrite,
			protocol.FSWriteReq{Path: cfg, Content: evil})
		if msg := errMessage(t, env); msg == "" {
			t.Errorf("%s: writing .git/config must be refused, got a %s reply", who.name, env.Type)
		} else if !strings.Contains(msg, "repository metadata") {
			t.Errorf("%s: the refusal must say why, got %q", who.name, msg)
		}
	}
	// The refusal is an error, not a silent no-op: the file on disk is untouched.
	if got, _ := os.ReadFile(cfg); string(got) != "[core]\n" {
		t.Fatalf(".git/config was modified: %q", got)
	}

	// An ordinary file in the same root still writes — this closed one path, not the editor.
	env := ask(t, steerer, "write-ok", protocol.TypeFSWrite,
		protocol.FSWriteReq{Path: filepath.Join(root, "main.go"), Content: "package main\n"})
	if env.Type != protocol.TypeOK {
		t.Fatalf("an ordinary write in the same root failed: %s: %q", env.Type, errMessage(t, env))
	}
	if got, _ := os.ReadFile(filepath.Join(root, "main.go")); string(got) != "package main\n" {
		t.Fatalf("ordinary write not applied: %q", got)
	}
}

// TestGitMetadataIsUnreadable: the same rule covers reads. .git/config carries remote URLs with
// embedded tokens, and nothing in the product ever reads inside .git — Tree and Search skip it — so
// allowing reads would have been a credential leak bought for no feature.
func TestGitMetadataIsUnreadable(t *testing.T) {
	h, dial, root := fsGateHub(t, "owner-secret")
	inv := h.invites.create("Sam", RoleSteerer, time.Hour)
	steerer := dial(inv.Secret)

	env := ask(t, steerer, "read-git", protocol.TypeFSRead,
		protocol.FSReadReq{Path: filepath.Join(root, ".git", "config")})
	if msg := errMessage(t, env); msg == "" {
		t.Fatalf("reading .git/config must be refused, got a %s reply", env.Type)
	} else if !strings.Contains(msg, "repository metadata") {
		t.Errorf("the refusal must say why, got %q", msg)
	}
}
