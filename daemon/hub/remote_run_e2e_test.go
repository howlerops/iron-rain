package hub_test

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/howlerops/oculus/daemon/crypto"
	"github.com/howlerops/oculus/daemon/hub"
	"github.com/howlerops/oculus/daemon/protocol"
	"github.com/howlerops/oculus/daemon/sshremote"
)

// TestRemoteRunE2E is a REAL integration test of running an agent session on a remote host over SSH:
// it registers localhost as the "remote", starts an "agent" (echo) there via remote.run, and asserts
// the remote command's output streams back through the session. Self-skips where non-interactive
// localhost ssh isn't set up, so it never flakes in CI but gives true end-to-end coverage on a dev box.
func TestRemoteRunE2E(t *testing.T) {
	if _, err := exec.LookPath("ssh"); err != nil {
		t.Skip("no ssh binary")
	}
	if err := exec.Command("ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=4", "localhost", "true").Run(); err != nil {
		t.Skip("ssh localhost not available non-interactively; skipping remote-run E2E")
	}

	h := hub.New()
	reg := sshremote.LoadRegistry(filepath.Join(t.TempDir(), "remotes.json"))
	host := reg.Upsert(sshremote.Host{Name: "local", SSHTarget: "localhost", RemotePath: t.TempDir()})
	h.SetRemotes(reg, sshremote.New())

	daemonKP, _ := crypto.GenerateKeyPair()
	conn := connectClient(t, h, daemonKP)
	r := newReader(conn)

	// Run "echo <prompt>" on the remote — its stdout must stream back.
	send(t, conn, "rr", protocol.TypeRemoteRun, protocol.RemoteRun{
		HostID: host.ID, AgentCommand: "echo", Prompt: "REMOTE_MARKER_42",
	})
	var sess protocol.Session
	if err := json.Unmarshal(r.waitOK(t, "rr"), &sess); err != nil {
		t.Fatalf("remote.run decode: %v", err)
	}

	// Collect streamed output; assert the echoed marker arrives.
	var out strings.Builder
	deadline := time.After(8 * time.Second)
	for {
		select {
		case env, ok := <-r.ch:
			if !ok {
				t.Fatalf("connection closed; got %q", out.String())
			}
			if env.Type == protocol.TypeOutputDelta || env.Type == protocol.TypeSessionMessage {
				var d struct {
					Text string `json:"text"`
				}
				_ = env.Unmarshal(&d)
				out.WriteString(d.Text)
				if strings.Contains(out.String(), "REMOTE_MARKER_42") {
					return // success: the remote agent's output streamed back over ssh
				}
			}
		case <-deadline:
			t.Fatalf("did not see remote output; got %q", out.String())
		}
	}
}
