package pi

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/howlerops/oculus/daemon/protocol"
	"github.com/howlerops/oculus/daemon/textutil"
)

// Branching a pi conversation: going back to an earlier point and trying something else.
//
// What is implemented here is bounded by pi's RPC mode, not by its product. The TUI can move the
// session's leaf anywhere in a tree of messages, tool calls and edits, and summarise the branch it
// abandons. `--mode rpc` — the only way the daemon drives pi — exposes none of that. It offers
// get_fork_messages (user messages only), fork(entryId), clone and compact.
//
// So this is fork, honestly labelled, and the capability manifest says so. A "rewind" that quietly
// forked instead, losing the branch summary and the tree, would be worse than no button at all.

// rpcTimeout bounds one request/response round trip. pi answers these from memory, so a slow answer
// means something is wrong rather than something is big.
const rpcTimeout = 15 * time.Second

// rpcWaiters correlates a sent request with the response that answers it.
//
// Keyed by the request ID that pi echoes back — {"id","type":"response","command","success","data"}.
// Correlating on the command NAME instead would work only while exactly one call of each command is
// in flight; the id makes concurrent calls safe and is already on the wire.
type rpcWaiters struct {
	mu sync.Mutex
	ch map[string]chan json.RawMessage
}

func newRPCWaiters() *rpcWaiters {
	return &rpcWaiters{ch: map[string]chan json.RawMessage{}}
}

func (w *rpcWaiters) open(cmd string) chan json.RawMessage {
	c := make(chan json.RawMessage, 1) // buffered: a late answer must never block the read loop
	w.mu.Lock()
	w.ch[cmd] = c
	w.mu.Unlock()
	return c
}

func (w *rpcWaiters) close(cmd string) {
	w.mu.Lock()
	delete(w.ch, cmd)
	w.mu.Unlock()
}

// resolve delivers a response to whoever is waiting for that command, if anyone is.
func (w *rpcWaiters) resolve(cmd string, payload json.RawMessage) {
	w.mu.Lock()
	c, ok := w.ch[cmd]
	if ok {
		delete(w.ch, cmd)
	}
	w.mu.Unlock()
	if ok {
		c <- payload
	}
}

// rpcResponse is pi's reply envelope. The payload lives under `data` — reading the top level
// instead is why the first live run came back with an empty fork list and no error to explain it.
type rpcResponse struct {
	ID      string          `json:"id"`
	Type    string          `json:"type"`
	Command string          `json:"command"`
	Success bool            `json:"success"`
	Error   string          `json:"error"`
	Data    json.RawMessage `json:"data"`
}

// call sends one RPC command and waits for its response.
func (s *session) call(ctx context.Context, cmd string, req map[string]any) (json.RawMessage, error) {
	id := "rpc_" + randID()
	ch := s.rpc.open(id)
	defer s.rpc.close(id)

	if req == nil {
		req = map[string]any{}
	}
	req["type"] = cmd
	req["id"] = id
	if err := s.send(req); err != nil {
		return nil, err
	}
	select {
	case raw := <-ch:
		var r rpcResponse
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, fmt.Errorf("pi returned an unreadable response to %s: %w", cmd, err)
		}
		if !r.Success {
			if r.Error != "" {
				return nil, fmt.Errorf("pi refused %s: %s", cmd, r.Error)
			}
			return nil, fmt.Errorf("pi refused %s", cmd)
		}
		return r.Data, nil
	case <-time.After(rpcTimeout):
		return nil, fmt.Errorf("pi did not answer %s in time", cmd)
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.done:
		return nil, fmt.Errorf("the session ended")
	}
}

// forkMessage is one entry from get_fork_messages.
//
// The key is entryId, NOT id — observed on the wire:
//
//	{"messages":[{"entryId":"330d82ed","text":"Reply with exactly: ONE"}]}
//
// Reading `id` produced a list of entries that all looked empty, which the loop below then skipped,
// so the feature failed as "no fork points" rather than as a parse error. That is the failure mode
// worth remembering: a defensive skip turned a field-name mismatch into silence.
type forkMessage struct {
	EntryID string `json:"entryId"`
	Text    string `json:"text"`
	Content string `json:"content"` // some versions carry the body here instead
}

func (m forkMessage) preview() string {
	t := m.Text
	if t == "" {
		t = m.Content
	}
	return textutil.FirstLine(strings.TrimSpace(t), 120)
}

// ThreadTree lists the points this conversation can be forked from (agent.ThreadOps).
//
// These are pi's USER MESSAGES, which is what get_fork_messages returns — not the full tree the TUI
// draws. Reported as a flat list (Depth 0) rather than dressed up as a tree, because inventing
// structure the source does not carry would be a lie told in a picker.
func (s *session) ThreadTree(ctx context.Context) ([]protocol.ThreadNode, error) {
	payload, err := s.call(ctx, "get_fork_messages", nil)
	if err != nil {
		return nil, err
	}
	var body struct {
		Messages []forkMessage `json:"messages"`
	}
	if err := json.Unmarshal(payload, &body); err != nil {
		return nil, fmt.Errorf("pi returned an unreadable fork list: %w", err)
	}
	nodes := make([]protocol.ThreadNode, 0, len(body.Messages))
	for _, m := range body.Messages {
		if m.EntryID == "" {
			continue
		}
		nodes = append(nodes, protocol.ThreadNode{
			ID:      m.EntryID,
			Kind:    "user",
			Preview: m.preview(),
			// Every one of these is on the line the session is currently on: they are its own past.
			// A sibling branch is not reachable through this API at all.
			OnPath: true,
		})
	}
	// Entries that carry no id are unusable, and ALL of them being unusable means pi's field names
	// moved rather than that the session is empty. Say so instead of returning a blank picker — that
	// exact silence is what made the first version of this look like a working feature with nothing
	// to show.
	if len(nodes) == 0 && len(body.Messages) > 0 {
		return nil, fmt.Errorf("pi returned %d fork points with no usable entryId — its response shape has changed", len(body.Messages))
	}
	if len(nodes) > 0 {
		nodes[len(nodes)-1].Current = true // the most recent user message is where the session is
	}
	return nodes, nil
}

// ThreadFork branches the conversation at nodeID (agent.ThreadOps).
//
// pi's fork REBINDS this session onto the new branch — the same session id keeps running, continuing
// from the chosen message. So the provider-side id does not change, and returning it is the honest
// answer to "which session is the fork".
func (s *session) ThreadFork(ctx context.Context, nodeID string) (string, error) {
	if strings.TrimSpace(nodeID) == "" {
		return "", fmt.Errorf("forking needs the message to branch from")
	}
	payload, err := s.call(ctx, "fork", map[string]any{"entryId": nodeID})
	if err != nil {
		return "", err
	}
	var body struct {
		Cancelled bool `json:"cancelled"`
	}
	_ = json.Unmarshal(payload, &body)
	if body.Cancelled {
		return "", fmt.Errorf("pi cancelled the fork")
	}
	return s.id, nil
}

// ThreadRewind is NOT supported over RPC (agent.ThreadOps).
//
// Present so the interface is satisfied in one place with one explanation, and refusing rather than
// silently forking. The capability manifest declares Rewind false, so a well-behaved client never
// calls this; a client that does gets told why instead of getting a fork it did not ask for.
func (s *session) ThreadRewind(context.Context, string) error {
	return fmt.Errorf("pi's rpc mode has no tree navigation — only forking from an earlier message. " +
		"Moving the session's position within the tree is available in pi's own terminal UI")
}
