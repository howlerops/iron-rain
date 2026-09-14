package fsaccess

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The guard judged the SPELLING of a path, on a filesystem that does not.
//
// macOS is case-insensitive by default, and neither os.Lstat nor filepath.EvalSymlinks normalises
// case — EvalSymlinks("~/.SSH") hands back ".SSH" unchanged while ReadDir on it lists the real
// ~/.ssh. Both refusal rules compared components with exact case, so one capital letter walked
// through all of them.
//
// The consequence is not academic. The daemon runs git in the user's repositories for PR, merge,
// catch-up and checkpoint. Anyone who can write <repo>/.GIT/config can set core.sshCommand, and the
// next git invocation runs whatever they put there, as the owner. Reading the file is the other half:
// vcsMetaDirs exists precisely because repository metadata is configuration, not project content.
func TestRepositoryMetadataIsRefusedWhateverItsCase(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".git", "config"), []byte("[core]\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	g := New([]string{root})
	for _, spelling := range []string{".git", ".GIT", ".Git", ".gIt"} {
		target := filepath.Join(root, spelling, "config")
		if _, err := g.Resolve(target); err == nil {
			t.Errorf("Resolve(%s) was ALLOWED — on a case-insensitive volume this is the same file as "+
				".git/config, and writing core.sshCommand into it runs arbitrary commands as the owner "+
				"the next time the daemon touches this repo", target)
		}
		if got := VCSMetadataComponent(target); got == "" {
			t.Errorf("VCSMetadataComponent(%s) = \"\" — the agent-write approval gate does not see this "+
				"as repository metadata either, so the write is not even surfaced for approval", target)
		}
	}
}

// The same hole in the root-independent rule. ProtectedPath is what stops a session being started
// with a cwd inside the daemon's own state directory or the user's credential stores — and a session
// cwd becomes an allowed root, so a miss here hands over the key material directly.
func TestProtectedDirectoriesAreRecognisedWhateverTheirCase(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	cases := []struct{ path, want string }{
		{filepath.Join(home, ".oculus", "daemon.key"), "~/.oculus"},
		{filepath.Join(home, ".Oculus", "daemon.key"), "~/.oculus"},
		{filepath.Join(home, ".OCULUS", "daemon.key"), "~/.oculus"},
		{filepath.Join(home, ".ssh", "id_ed25519"), "~/.ssh"},
		{filepath.Join(home, ".SSH", "id_ed25519"), "~/.ssh"},
	}
	for _, tc := range cases {
		got := ProtectedPath(tc.path)
		if got == "" {
			t.Errorf("ProtectedPath(%s) = \"\", want %q — validateSessionCwd accepts this, fsaccess.New "+
				"keeps it as a root, and fs.read then returns the file", tc.path, tc.want)
			continue
		}
		if !strings.EqualFold(got, tc.want) {
			t.Errorf("ProtectedPath(%s) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

// The other direction, so the fix cannot be "refuse everything": ordinary project files, including
// ones whose names merely resemble the protected set, must still resolve.
func TestOrdinaryFilesAreStillAllowed(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"main.go", ".gitignore", ".github", "gitconfig", "git"} {
		p := filepath.Join(root, name)
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := g(root).Resolve(p); err != nil {
			t.Errorf("Resolve(%s) was refused: %v — the case-insensitive comparison is matching on a "+
				"prefix rather than a whole path component", p, err)
		}
	}
}

func g(root string) *Guard { return New([]string{root}) }
