package hub

import (
	"context"
	"strings"
	"testing"

	"github.com/howlerops/oculus/daemon/agent"
	"github.com/howlerops/oculus/daemon/protocol"
)

// textOnlySession is a provider with no ImagePrompter — cli and AG-UI are real examples.
type textOnlySession struct {
	agent.Session
	got string
}

func (t *textOnlySession) Provider() string { return "cli" }
func (t *textOnlySession) Prompt(_ context.Context, text string) error {
	t.got = text
	return nil
}

// Attaching an image to a provider that cannot take one must fail, not quietly send the text.
//
// promptSession type-asserted ImagePrompter and, when the assertion failed, fell through to the
// text-only Prompt and returned ITS nil. Only claude-code, opencode and pi implement the interface,
// so attaching a screenshot to a cli or AG-UI session sent the words without the picture and reported
// success — the agent answered a question about an image it had never been given, and nothing
// anywhere recorded that an attachment had been dropped.
func TestImagesAreRefusedNotSilentlyDropped(t *testing.T) {
	sess := &textOnlySession{}
	imgs := []protocol.ImageAttachment{{Mime: "image/png", Data: "AAAA"}}

	err := promptSession(context.Background(), sess, "what is in this screenshot?", imgs, false)
	if err == nil {
		t.Fatalf("promptSession reported success for a provider that cannot take images; it sent %q "+
			"with the attachment silently discarded", sess.got)
	}
	if !strings.Contains(err.Error(), "image") {
		t.Errorf("the error should say the images could not be sent, got %q", err)
	}
	if sess.got != "" {
		t.Errorf("the text was sent anyway (%q) — a half-delivered message is worse than a refused one", sess.got)
	}
}

// The text-only path is unaffected: no images, no refusal.
func TestTextOnlyPromptStillWorks(t *testing.T) {
	sess := &textOnlySession{}
	if err := promptSession(context.Background(), sess, "hello", nil, false); err != nil {
		t.Fatalf("a plain text prompt must still go through: %v", err)
	}
	if sess.got != "hello" {
		t.Errorf("text = %q, want %q", sess.got, "hello")
	}
}
