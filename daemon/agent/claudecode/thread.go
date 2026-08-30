package claudecode

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/howlerops/oculus/daemon/genui"
	"github.com/howlerops/oculus/daemon/protocol"
	"github.com/howlerops/oculus/daemon/textutil"
)

// Branching a claude-code conversation.
//
// This adapter declared an EMPTY ThreadCaps, with a comment asserting that the SDK "has no way to
// truncate a session". That was wrong, and only reading the SDK's own type definitions settled it:
//
//	forkSession(sessionId, { upToMessageId })  →  { sessionId }
//	  "Slice transcript up to this message UUID (inclusive)."
//	resumeSessionAt?: string
//	  "When resuming, only resume messages up to and including the message with this UUID."
//
// So fork-at-an-arbitrary-message is directly supported, and the earlier claim was an assumption
// dressed as a limitation.
//
// The TREE comes from disk rather than from the SDK, because claude's transcripts already are one:
// every entry carries uuid + parentUuid, `last-prompt` entries record leafUuid, and isSidechain
// marks a sub-agent's own branch. Reading it costs nothing and needs no running session.
//
// Rewind stays UNDECLARED. resumeSessionAt looks like it could serve, but whether it truncates the
// stored session or merely limits what a new query replays is not stated, and a "rewind" that
// silently left the original intact would be the exact lie the manifest exists to prevent. Fork is
// unambiguous; that is what is offered.

// threadCaps is what this adapter can actually perform.
func (s *session) threadCaps() protocol.ThreadCaps {
	return protocol.ThreadCaps{Tree: true, Fork: true}
}

// transcriptEntry is the subset of a claude JSONL line the tree needs.
type transcriptEntry struct {
	Type        string `json:"type"`
	UUID        string `json:"uuid"`
	ParentUUID  string `json:"parentUuid"`
	IsSidechain bool   `json:"isSidechain"`
	Timestamp   string `json:"timestamp"`
	LeafUUID    string `json:"leafUuid"`
	Message     struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"message"`
}

// text pulls a readable line out of a message's content, which is either a string or an array of
// typed blocks.
func (e transcriptEntry) text() string {
	if len(e.Message.Content) == 0 {
		return ""
	}
	var str string
	if json.Unmarshal(e.Message.Content, &str) == nil {
		return str
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
		Name string `json:"name"`
	}
	if json.Unmarshal(e.Message.Content, &blocks) != nil {
		return ""
	}
	for _, b := range blocks {
		if b.Type == "text" && strings.TrimSpace(b.Text) != "" {
			return b.Text
		}
	}
	for _, b := range blocks {
		if b.Name != "" {
			return b.Name
		}
	}
	return ""
}

// ThreadTree reads claude's own transcript and returns the points this conversation can be forked
// from (agent.ThreadOps).
//
// USER messages only, matching the other adapters: forkSession slices "up to and including" a
// message, so offering an assistant turn would name a destination whose behaviour is not the one the
// row implies. Sidechain entries are skipped — a sub-agent's internal turns are not points the
// PARENT conversation can branch from.
func (s *session) ThreadTree(ctx context.Context) ([]protocol.ThreadNode, error) {
	path, err := s.transcriptPath()
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("this session's transcript is not readable yet: %w", err)
	}
	defer f.Close()

	var (
		nodes    []protocol.ThreadNode
		parentOf = map[string]string{}
		leaf     string
	)
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024) // transcripts carry large tool results
	for sc.Scan() {
		var e transcriptEntry
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue // one unreadable line must not lose the rest of the tree
		}
		if e.UUID != "" {
			parentOf[e.UUID] = e.ParentUUID
		}
		// `last-prompt` entries record where the conversation currently is.
		if e.LeafUUID != "" {
			leaf = e.LeafUUID
		}
		if e.Type != "user" || e.IsSidechain || e.UUID == "" {
			continue
		}
		// Skip everything recorded as "user" that the user did not write.
		//
		// claude's transcripts use role=user for tool RESULTS and for harness-injected frames as
		// well as for prompts. Without this the branch picker offered "[Context]" and
		// "<task-notification>" as destinations — things the human never typed, presented as points
		// in their own conversation. Found by reading a real transcript; a synthetic fixture would
		// have contained only what I already expected.
		body := strings.TrimSpace(genui.StripGuide(e.text()))
		if body == "" || !humanAuthored(body) {
			continue
		}
		nodes = append(nodes, protocol.ThreadNode{
			ID:       e.UUID,
			ParentID: e.ParentUUID,
			Kind:     "user",
			Preview:  textutil.FirstLine(body, 120),
			At:       parseTranscriptTime(e.Timestamp),
		})
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("reading this session's transcript: %w", err)
	}
	if len(nodes) == 0 {
		return nil, nil
	}

	// OnPath: walk from the current leaf back to the root. Everything on that walk is the line the
	// conversation is actually on; anything else is an abandoned branch and is dimmed rather than
	// hidden — you may well want to go back to it.
	onPath := map[string]bool{}
	for id := leaf; id != ""; id = parentOf[id] {
		onPath[id] = true
		if len(onPath) > len(parentOf)+1 {
			break // a cycle in the parent links would otherwise spin forever
		}
	}
	for i := range nodes {
		nodes[i].OnPath = len(onPath) == 0 || onPath[nodes[i].ID]
	}
	sort.SliceStable(nodes, func(i, j int) bool { return nodes[i].At < nodes[j].At })
	// The last on-path user message is where the conversation stands.
	for i := len(nodes) - 1; i >= 0; i-- {
		if nodes[i].OnPath {
			nodes[i].Current = true
			break
		}
	}
	return nodes, nil
}

// ThreadFork branches into a NEW claude session sliced at nodeID (agent.ThreadOps).
func (s *session) ThreadFork(ctx context.Context, nodeID string) (string, error) {
	if strings.TrimSpace(nodeID) == "" {
		return "", fmt.Errorf("forking needs the message to branch from")
	}
	real := s.realSessionID()
	if real == "" {
		return "", fmt.Errorf("this session has not reported its claude session id yet — send a message first")
	}
	id := "fk_" + randID()
	ch := s.forks.open(id)
	defer s.forks.close(id)

	if err := s.send(inMsg{T: "fork", ID: id, SessionID: real, UpToMessageID: nodeID}); err != nil {
		return "", err
	}
	select {
	case res := <-ch:
		if res.Error != "" {
			return "", fmt.Errorf("claude could not fork this conversation: %s", res.Error)
		}
		if res.SessionID == "" {
			return "", fmt.Errorf("claude forked the conversation but reported no new session id")
		}
		return res.SessionID, nil
	case <-time.After(30 * time.Second):
		return "", fmt.Errorf("claude did not answer the fork in time")
	case <-ctx.Done():
		return "", ctx.Err()
	case <-s.done:
		return "", fmt.Errorf("the session ended")
	}
}

// ThreadRewind is not offered — see the file comment. Refusing beats forking silently.
func (s *session) ThreadRewind(context.Context, string) error {
	return fmt.Errorf("claude-code can branch a conversation but not move this one backwards; " +
		"use Branch to continue from an earlier point")
}

// forkWaiters correlates a fork request with the sidecar's reply.
type forkWaiters struct {
	mu sync.Mutex
	ch map[string]chan forkReply
}

type forkReply struct {
	SessionID string
	Error     string
}

func newForkWaiters() *forkWaiters { return &forkWaiters{ch: map[string]chan forkReply{}} }

func (w *forkWaiters) open(id string) chan forkReply {
	c := make(chan forkReply, 1) // buffered: a late reply must not block the read loop
	w.mu.Lock()
	w.ch[id] = c
	w.mu.Unlock()
	return c
}

func (w *forkWaiters) close(id string) {
	w.mu.Lock()
	delete(w.ch, id)
	w.mu.Unlock()
}

func (w *forkWaiters) resolve(id string, r forkReply) {
	w.mu.Lock()
	c, ok := w.ch[id]
	if ok {
		delete(w.ch, id)
	}
	w.mu.Unlock()
	if ok {
		c <- r
	}
}

// injectedPrefixes are the openings of frames the HARNESS writes into the user role.
//
// Deliberately an EXPLICIT list rather than a pattern like "starts with [Word]". The cost of the two
// errors is not symmetric: including an injected row shows a destination that is merely useless,
// while excluding a real one HIDES a point the user wanted to branch from, silently. A prompt
// legitimately beginning "[TODO] ..." must survive.
//
// Matched as prefixes, not searched for anywhere, so a prompt that quotes one of these markers —
// exactly what a conversation about this code does — is still treated as a prompt.
//
// Iron Rain's OWN sessions rarely need this: their user entries are the prompt, plus the generative
// UI guide that StripGuide removes. It matters for transcripts taken over from a terminal, where
// another harness's context blocks appear under the user role.
var injectedPrefixes = []string{
	"<task-notification>",
	"<system-reminder>",
	"[Context]",
	"[Base]",
	"<local-command-stdout>",
	"<local-command-stderr>",
	"Caveat: The messages below were generated by the user while running local commands",
	"[Request interrupted",
}

// humanAuthored reports whether a user-role entry is something the person actually wrote.
func humanAuthored(body string) bool {
	for _, p := range injectedPrefixes {
		if strings.HasPrefix(body, p) {
			return false
		}
	}
	return true
}

// parseTranscriptTime converts claude's RFC3339 timestamp to unix seconds (0 when absent).
func parseTranscriptTime(s string) int64 {
	if s == "" {
		return 0
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return 0
	}
	return t.Unix()
}

// realSessionID is claude's OWN session uuid for this session, which is what forkSession and the
// on-disk transcript are both keyed by. Our cc_… id is ours alone and claude rejects it.
func (s *session) realSessionID() string {
	if s.replayUUID != "" {
		return s.replayUUID
	}
	if s.p != nil {
		if id := s.p.resumeID(s.id); id != "" {
			return id
		}
	}
	if looksLikeUUID(s.id) {
		return s.id
	}
	return ""
}

// transcriptPath locates this session's JSONL, which lives under claude's real uuid in whichever
// project directory it belongs to.
func (s *session) transcriptPath() (string, error) {
	uuid := s.realSessionID()
	if uuid == "" {
		return "", fmt.Errorf("this session has no claude session id yet — send a message first")
	}
	matches, _ := filepath.Glob(filepath.Join(s.projects(), "*", uuid+".jsonl"))
	if len(matches) == 0 {
		return "", fmt.Errorf("no transcript on disk for this session yet")
	}
	return matches[0], nil
}
