# Iron Rain — Performance & Best-Practice Review

_Fan-out review: 13 code areas, 72 agents, every finding adversarially verified against the code. 56 confirmed._

**Severity:** 2 high · 21 medium · 33 low  
**Category:** 21 performance · 10 resource-leak · 10 best-practice · 9 correctness · 5 concurrency · 1 idiom


## High

### [correctness] Counter nonce resets to 0 every session while keys are static — cross-session nonce reuse
`daemon/crypto/crypto.go:128` · _go:crypto_

**Problem.** The scheme is static-static X25519 with a FIXED HKDF salt (hkdfSalt, line 35) and fixed info labels, so DeriveSessionKeys returns byte-identical C2D/D2C keys on every reconnect for a given peer pair. NewSealer initializes counter=0 (line 128) and Seal derives the nonce solely from that counter (lines 133-135). Therefore every new session encrypts its first message under nonce 00..00 with the SAME key as the previous session's first message. For ChaCha20-Poly1305 this is catastrophic (key,nonce) reuse: it leaks plaintext XOR of colliding messages via keystream reuse and enables Poly1305 forgery. The doc comment acknowledges 'no forward secrecy' but does not acknowledge this reuse, which is a confidentiality/integrity break, not just a forward-secrecy gap.

**Fix.** Make the per-session keystream unique. Preferred: add a per-session random 32-byte salt (or ephemeral handshake) into the HKDF salt so each session gets fresh directional keys, and transmit that salt in the handshake. Alternatively persist the counter across sessions per key (fragile), or switch to XChaCha20-Poly1305 with a random 24-byte nonce per message (chacha20poly1305.NewX) so nonce collision probability is negligible and no counter state must survive restarts.

### [concurrency] Sealer counter is mutated without synchronization — concurrent send() risks catastrophic nonce reuse
`app/OculusKit/Sources/OculusKit/Client.swift:57` · _swift:kit_

**Problem.** OculusClient is a plain (non-Sendable) `final class`, and `send()` calls `sealer.seal()`, which reads-then-increments the mutable `counter: UInt64` in Sealer (Crypto.swift:46,52-54). Nothing serializes access. If two tasks call `send()` concurrently (trivial with async/await + SwiftUI callers), both can read the same counter value and produce two frames with the SAME 12-byte nonce under the same key. Nonce reuse in ChaCha20-Poly1305 is catastrophic: it leaks the XOR of plaintexts and allows forgery. Even without collision, out-of-order sends desync the counter vs. the daemon and every subsequent frame fails to decrypt.

**Fix.** Make the mutating boundary serial. Simplest: convert OculusClient to an `actor` (or gate all send/recv through a dedicated serial executor / an `actor` wrapping the Sealer/Opener + task). At minimum make Sealer an actor or guard `counter` with a lock so seal() is atomic (read counter, increment, encrypt under one critical section). Also serialize `task.send` so frame order matches nonce order.


## Medium

### [resource-leak] Spawned sidecar process is never Wait()ed → zombie + fd/goroutine leak
`daemon/agent/claudecode/claudecode.go:86` · _go:agent-adapters_

**Problem.** start() does cmd.Start() but the *exec.Cmd is never retained in the session and cmd.Wait() is never called anywhere (verified by grep across the package). Close() only cancels the context, which makes exec.CommandContext SIGKILL the child — but without Wait() the kernel keeps it as a zombie and the OS pipe fds behind StdinPipe/StdoutPipe are not released (Wait is what closes them). exec.CommandContext also leaves its internal watcher goroutine around. In a long-lived daemon that creates/attaches many claude-code sessions, this accumulates zombie PIDs, leaked file descriptors, and goroutines until the daemon itself exits.

**Fix.** Store cmd on the session and reap it after the reader drains. Change `go s.readLoop(stdout)` to `go func(){ s.readLoop(stdout); _ = cmd.Wait() }()`. readLoop returns on stdout EOF (which the ctx-cancel kill triggers), so calling Wait() after it returns reaps the process and closes the pipes without racing the scanner. Optionally also close stdin in Close() to signal graceful shutdown before the kill.

### [resource-leak] Spawned pi RPC process is never Wait()ed → zombie + fd/goroutine leak
`daemon/agent/pi/pi.go:72` · _go:agent-adapters_

**Problem.** Identical issue to the claudecode adapter: Create() calls cmd.Start() but never retains cmd or calls cmd.Wait(). Close() cancels the context (killing the child via exec.CommandContext) but the process is never reaped, so each pi session leaves a zombie process, leaked stdin/stdout pipe fds, and the CommandContext watcher goroutine behind for the lifetime of the daemon.

**Fix.** Retain cmd on the session and reap it once the reader exits: replace `go s.readLoop(stdout)` with `go func(){ s.readLoop(stdout); _ = cmd.Wait() }()`. Wait() after readLoop returns (on EOF from the kill) reaps the process and releases the pipe fds.

### [concurrency] gitInfo runs two blocking git subprocesses while holding the registry mutex
`daemon/project/project.go:78` · _go:discovery-project_

**Problem.** Add() acquires r.mu at line 70 (defer unlock), then calls gitInfo(abs) at line 78, which shells out to `git rev-parse` twice (lines 151 & 154), and then save() at line 87 which does a synchronous os.WriteFile — all under the lock. Every concurrent List/Get/Remove/Add blocks for the full duration of two git process spawns plus a disk write. On a slow/large repo, a network mount, or if git blocks on a credential/lock prompt, the entire registry is frozen. os.Stat is correctly done before the lock (line 62), but the expensive git work is not.

**Fix.** Move `isRepo, branch := gitInfo(abs)` out of the critical section (before r.mu.Lock, like os.Stat). Keep the dedup check under the lock; a redundant gitInfo computed by a racing duplicate Add is cheap and acceptable versus serializing all registry access behind subprocess spawns. Only the append + save must hold the lock.

### [resource-leak] procCwd ignores the scan context and has no timeout on a per-server hot path
`daemon/discovery/discovery.go:190` · _go:discovery-project_

**Problem.** procCwd runs `lsof -a -p <pid> -d cwd -Fn` via exec.Command (line 191), ignoring the ctx that Scan/combine threads everywhere else. It is called synchronously for every discovered opencode server inside combine (line 227). lsof can be slow or hang (stuck NFS mounts, dead processes), and because it drops ctx there is no cancellation and no deadline — a single stuck lsof stalls the whole discovery scan even if the caller cancels.

**Fix.** Change procCwd to accept ctx and use exec.CommandContext(ctx, ...); pass combine's ctx through. Consider a per-call timeout (context.WithTimeout) since lsof latency is unbounded.

### [performance] Per-event fan-out does serial blocking Send; one stalled client freezes the whole session
`daemon/hub/session.go:91` · _go:hub_

**Problem.** broadcast() snapshots subscribers under the lock (good) but then calls c.Send(raw) serially for every subscriber. c.Send is a blocking write on an encrypted transport. Because this runs on the single run() goroutine that pumps the provider event stream, a single slow or stalled TCP client applies head-of-line blocking: it stalls delivery to every other subscriber AND backs up the provider event pump for that session (Events() is never drained while blocked). This is the core fan-out path hit on every agent event.

**Fix.** Give each subscriber an owned buffered outbound queue (per-conn channel + writer goroutine) and have broadcast do a non-blocking enqueue, dropping/disconnecting slow clients instead of blocking. At minimum, fan the Sends out to per-conn goroutines with a bounded buffer and a write deadline so one wedged socket can't stall the event pump. The same pattern applies to Hub.broadcast (hub.go:504-506).

### [resource-leak] Session transcript grows unbounded — per-session memory leak
`daemon/hub/session.go:85` · _go:hub_

**Problem.** m.transcript = append(m.transcript, raw) appends every encoded event for the entire life of the session and is never trimmed. A long-running agent session (which is the normal case here — sessions persist on the host) accumulates every event forever in RAM. It is also fully copied and replayed to each new subscriber (subscribe, line 67), so join cost grows linearly with session age. This is an unbounded memory growth bug, not a micro-optimization.

**Fix.** Cap the transcript (ring buffer / max N events or max bytes) and/or snapshot-and-compact. If full replay fidelity is required, persist older events out of the hot in-memory slice. Retain only what a late joiner actually needs to reconstruct current state.

### [correctness] NewManager never reconnects a saved Jira provider
`daemon/issues/manager.go:46` · _go:issues_

**Problem.** The doc comment says NewManager 'reconnects any provider that has a saved token,' but only the Linear branch exists. If m.cfg.Jira.Token is non-empty on disk, Jira is silently never registered in m.providers on startup — the user must re-run Connect every daemon restart. The generic newAdapter("jira", token) already exists and handles the site|email|apitoken format, so the omission is purely a missed call.

**Fix.** Add the symmetric block after the Linear one: `if m.cfg.Jira.Token != "" { if p, err := newAdapter("jira", m.cfg.Jira.Token); err == nil { m.providers["jira"] = p } }`. Better: loop over a map of name->token so future providers can't be forgotten.

### [resource-leak] HTTP clients have no timeout; a single hung request stalls the poll loop forever
`daemon/issues/linear.go:22` · _go:issues_

**Problem.** NewLinear (linear.go:22), NewJira (jira.go:29), and the OAuth exchange using http.DefaultClient (oauth.go:82) all use a client with zero Timeout. StartPolling passes a long-lived ctx (cancelled only on daemon shutdown), so there is no per-request deadline: a stalled TCP connection or a server that accepts but never responds hangs Refresh indefinitely, and because Refresh is called from the single poll goroutine, polling wedges permanently with no recovery until process exit.

**Fix.** Give each client a bounded timeout, e.g. `http: &http.Client{Timeout: 30 * time.Second}` in NewLinear and NewJira, and use such a client (not http.DefaultClient) in OAuthCallback. Keep ctx for cancellation but add the timeout as a backstop.

### [best-practice] APNs provider JWT regenerated on every push — triggers TooManyProviderTokenUpdates (403)
`daemon/push/push.go:140` · _go:push-main_

**Problem.** Notify calls providerToken() on every send (push.go:117), and providerToken() mints a brand-new signed JWT each time (new iat, fresh ecdsa.Sign). The code comment claims 'a fresh one per send is simplest and well within limits' — that is wrong. Apple explicitly rejects tokens recreated more often than once per 20 minutes with HTTP 403 'TooManyProviderTokenUpdates'. Under any burst of approvals this will start failing. It also does an ECDSA sign + two json.Marshal + sha256 per request on the hot path for no benefit.

**Fix.** Cache the signed JWT on the apnsNotifier and only regenerate it when older than ~40-50 min (Apple's window is 20-60 min). Guard with a sync.Mutex (or atomic pointer): store token + issue time, and in Notify reuse the cached token unless a.cfg.Now().Sub(issuedAt) > 45*time.Minute. This removes both the 403 risk and the per-send crypto cost.

### [correctness] Duplicate host registration silently orphans the previous host
`daemon/relay/relay.go:80` · _go:server-relay_

**Problem.** serveHost does r.hosts[id] = ch unconditionally. If a second host registers with the same server_id (reconnect after a half-open connection, or an ID collision), it overwrites the first host's channel. The first serveHost goroutine is still blocked on <-ch for a channel no client can ever reach again, so it leaks until its context is cancelled (which, per the previous finding, may be never if the socket is half-open). Its deferred cleanup `if r.hosts[id] == ch` correctly avoids deleting the new entry, but the stale host is stranded.

**Fix.** On registration, detect an existing entry for id and explicitly close/evict the previous host (e.g. close its old ws with a policy-violation status) before installing the new channel, or reject the new registration — so there is always exactly one live host per server_id and no stranded goroutine.

### [performance] Serial fan-out lets one slow client stall every client and halt the session event pump
`daemon/hub/session.go:91` · _go:server-relay_

**Problem.** broadcast() sends to every subscriber synchronously in a for-loop (`for _, c := range conns { _ = c.Send(raw) }`), and c.Send ultimately calls ws.Write which blocks until the frame is flushed to that socket. The identical pattern is in hub.go:504-506. Because managedSession.broadcast runs on the single run() goroutine that drains the provider's Events() channel, a single slow or wedged client (full TCP send buffer, high-latency mobile link over the relay) blocks the whole loop: all other subscribers are delayed AND the provider event stream stops being drained, applying backpressure onto the agent. This is head-of-line blocking across otherwise-independent clients.

**Fix.** Give each connection its own buffered outbound channel plus a dedicated writer goroutine; broadcast does a non-blocking enqueue and drops/closes the client if its buffer overflows, so one stalled client can never block the session pump or other clients.

### [performance] Blocking handlers run inline in the per-connection read loop
`daemon/hub/hub.go:488` · _go:server-relay_

**Problem.** Serve() calls h.dispatch synchronously inside the conn.Recv() read loop, and several dispatch cases perform long, blocking I/O: session.create runs worktree.Create + worktree.Bootstrap (which executes user setup hooks) synchronously via startSession; worktree.pr runs git CommitAll/Push/CreatePR; integration.connect/issue.states/integration.oauth make blocking tracker HTTP calls. While any of these runs, the client's read loop is blocked, so that client cannot send or have processed any other message — including an approval.respond it may need to send — until the long operation finishes. A worktree bootstrap that takes tens of seconds freezes the entire client session.

**Fix.** Dispatch long-running handlers on their own goroutine (they already reply asynchronously via conn.Send), or hand them to a bounded worker so the read loop keeps draining incoming messages. Keep only cheap, ordered operations inline.

### [resource-leak] WebSocket writes use the whole-connection context, so a stalled socket blocks the writer forever
`daemon/server/wsconn.go:24` · _go:server-relay_

**Problem.** WriteMsg calls c.ws.Write(c.ctx, ...) where c.ctx is the per-connection request context (set in server.go:48 from r.Context()), which is only cancelled when the connection itself closes. There is no per-write deadline. The same holds for wsmsg.go:26. If a client's TCP send buffer fills (dead-but-not-reset mobile connection), ws.Write blocks indefinitely, permanently parking whichever goroutine is broadcasting to it. Combined with the serial fan-out above, this is an unbounded goroutine/backpressure hazard rather than a bounded timeout.

**Fix.** Wrap each write in a per-message deadline, e.g. ctx, cancel := context.WithTimeout(c.ctx, writeTimeout); defer cancel(); c.ws.Write(ctx, ...). On timeout, close the connection so the client is dropped instead of blocking the sender.

### [resource-leak] Relay registration read has no timeout — slowloris goroutine/connection leak
`daemon/relay/relay.go:56` · _go:server-relay_

**Problem.** The relay Handler reads the first message (the registration frame) with ws.Read(ctx) where ctx is req.Context(), which for a hijacked WebSocket stays alive for the life of the connection. A peer that opens the WebSocket and then sends nothing parks a goroutine and holds the socket open indefinitely — a cheap slowloris resource-exhaustion vector on a publicly reachable relay. The daemon's ServerHandshake read path (server.go:56 via ServeConn) has the same no-deadline property for its handshake.

**Fix.** Bound the registration/handshake phase with a deadline: rctx, cancel := context.WithTimeout(ctx, 10*time.Second); defer cancel(); ws.Read(rctx). Only switch to the unbounded connection context after a valid registration/handshake completes.

### [concurrency] Send releases sendMu before WriteMsg, allowing wire reordering under concurrent senders
`daemon/transport/transport.go:141` · _go:wire_

**Problem.** Send() takes sendMu only around sealer.Seal (lines 142-144) and releases it before c.mc.WriteMsg(frame) (line 148). The sealer assigns a strictly increasing counter nonce (crypto.go Seal: binary.BigEndian.PutUint64(nonce, s.counter); s.counter++). With two goroutines calling Send concurrently (typical for a daemon that streams output.delta/thinking.delta events while also writing request responses on the same Conn), goroutine A can seal counter=N and goroutine B seal counter=N+1, then B can win the race to WriteMsg and put frame N+1 on the wire ahead of frame N. Since each frame carries its own nonce, AEAD decryption still succeeds, so the reordering is silent — but the client receives streamed deltas out of order, scrambling assistant output/thinking text. The underlying coder/websocket serializes the actual writes with an internal mutex, so holding sendMu across the write costs essentially nothing and is the only thing tying counter order to wire order.

**Fix.** Hold the lock across the write: c.sendMu.Lock(); frame, err := c.sealer.Seal(plaintext); if err == nil { err = c.mc.WriteMsg(frame) }; c.sendMu.Unlock(); return err — i.e. move the Unlock to after WriteMsg so seal order == wire order.

### [correctness] copyPath dereferences symlinks and can recurse infinitely on symlinked/circular dirs
`daemon/worktree/setup.go:124` · _go:worktree_

**Problem.** copyPath uses os.Stat (line 124), which follows symlinks, and copyDir (137) recurses on whatever it resolves. So a symlinked file is copied as its dereferenced content and a symlinked directory is recursively walked and duplicated by value. The package docstring explicitly names node_modules as a `copy` candidate, and pnpm/npm node_modules trees are symlink farms — often with circular links (a package linking back to its own dir). Copying such a tree here dereferences every link (massive size blowup) and a circular symlink drives copyDir into unbounded recursion until it exhausts the stack / fills the disk.

**Fix.** Use os.Lstat instead of os.Stat in copyPath, and add a symlink branch: if fi.Mode()&os.ModeSymlink != 0, read the target with os.Readlink and recreate it with os.Symlink(target, dst) (or skip it) rather than following it. This preserves node_modules link structure and eliminates the infinite-recursion path.

### [correctness] Diff uses CombinedOutput, mixing git stderr warnings into the returned diff text
`daemon/worktree/finish.go:26` · _go:worktree_

**Problem.** Diff runs `git diff` with CombinedOutput() and returns string(out) on success. git emits warnings to stderr even on exit 0 (e.g. "warning: LF will be replaced by CRLF", "warning: in the working copy of X, CRLF will be replaced by LF", or advice lines). Those get interleaved into the returned diff, corrupting a value that is shown to reviewers and parsed downstream. Note ChangedFiles (line 85) correctly uses .Output() (stdout only) — Diff is the inconsistent one that should not merge streams.

**Fix.** Capture streams separately: set cmd.Stdout = &outBuf and cmd.Stderr = &errBuf, run, return outBuf.String() on success and include errBuf.String() only in the error message. Or minimally switch to .Output() and pull stderr from the *exec.ExitError on failure.

### [performance] connectAll() serializes every desktop connection behind a full handshake
`app/OculusKit/Sources/OculusUI/DesktopStore.swift:77` · _swift:model_

**Problem.** connectAll does `for m in models where !m.connected { await m.connect() }`. Each `m.connect()` calls `attemptConnect()`, which awaits the full `OculusClient.connect(...)` handshake AND the follow-up discover/loadProjects/loadSessions/loadIntegrationStatus/loadIssues sends before returning. Because the loop `await`s each model in turn, desktop N does not begin connecting until desktop N-1 has fully connected. A single slow or unreachable desktop (whose handshake blocks or times out) stalls connection to every other paired desktop — the whole point of the store is connecting to all at once.

**Fix.** Fan out concurrently instead of serializing: `await withTaskGroup(of: Void.self) { g in for m in models where !m.connected { g.addTask { await m.connect() } } }`. Each Model is @MainActor so the group children hop to the main actor individually; no one slow handshake blocks the others.

### [performance] Streaming deltas mutate the whole @Published messages array per token
`app/OculusKit/Sources/OculusUI/OculusUI.swift:438` · _swift:model_

**Problem.** appendAssistantDelta/appendThinkingDelta do `messages[messages.count - 1].text += text` on every outputDelta/thinking frame. `messages` is an `@Published [ChatMessage]` value-type array, so each single-token append triggers a full objectWillChange, republishing the entire array. SwiftUI then re-evaluates/diffs the whole message list on every token during streaming — O(n) work per delta, worst-case O(n²) over a long response. This is the dominant churn source on the hot streaming path.

**Fix.** Coalesce deltas before publishing: buffer incoming text and flush on a short timer (e.g. every ~50-100ms via a debounced Task), or move the actively-streaming message out of the @Published array into a separate lightweight @Published field (e.g. `@Published var streamingText`) and only fold it into `messages` when the turn finalizes, so the big array republishes once per message instead of once per token.

### [performance] Attachment thumbnail decodes base64 and rebuilds an image on every render (typing hot path)
`app/OculusKit/Sources/OculusUI/Composer.swift:104` · _swift:views_

**Problem.** attachmentThumb(_:) calls Data(base64Encoded: img.data) and then platformImage(...) (UIImage(data:)/NSImage(data:)) directly inside the view body. attachmentChips is part of Composer's body, and Composer's body re-evaluates on every keystroke (the $draft binding) and every dictator/model change. So for each pending image, the app base64-decodes the full image payload and constructs a UIImage/NSImage on every single keystroke while composing a message. This is a large, repeated allocation + decode on the most frequent hot path in the app.

**Fix.** Decode each attachment's thumbnail once and cache it. Precompute a small decoded thumbnail (e.g. a resized Image) when the image is attached (in model.attachImage) or memoize in a @State dictionary keyed by attachment id, and have attachmentThumb read the cached value instead of decoding img.data inline. Never call Data(base64Encoded:) / UIImage(data:) inside body.

### [performance] Assistant message re-parsed as Markdown via LocalizedStringKey on every streaming token
`app/OculusKit/Sources/OculusUI/ChatView.swift:141` · _swift:views_

**Problem.** Text(LocalizedStringKey(message.text)) runs arbitrary agent output through LocalizedStringKey, which triggers Markdown/AttributedString parsing (and localization-table lookup) of the entire assistant message. During streaming the last message's text mutates on every token, so the full (growing) message is re-parsed as Markdown on every token for every re-render. This is a heavy body computation on the streaming hot path. It is also a correctness hazard: agent text containing %, %@, or interpolation-like sequences is misinterpreted by LocalizedStringKey.

**Fix.** Do not wrap runtime content in LocalizedStringKey. If Markdown rendering is desired, build an AttributedString(markdown:) once when a message finishes streaming (or throttle), cache it on the ChatMessage, and render plain Text for the in-flight streaming message. Otherwise render Text(message.text) directly.


## Low

### [best-practice] Shared http.Client with no timeout is reused for both SSE and unary calls
`daemon/agent/opencode/opencode.go:30` · _go:agent-adapters_

**Problem.** New() builds a single &http.Client{} with no Timeout and uses it for both the long-lived SSE /event stream and the unary List/postJSON/replayHistory calls. A client-level Timeout can't be set because it would kill the SSE stream, so the unary requests are protected only by whatever context the caller passes — and several paths can pass a deadline-less context (e.g. sendParts uses the long-lived subscribe ctx). A hung/unresponsive opencode server can therefore block a POST /message goroutine indefinitely with no upper bound.

**Fix.** Use two clients: keep the no-timeout client for subscribe()'s SSE stream, and a second http.Client with a sane Timeout (or wrap each unary request context with context.WithTimeout in postJSON/List/replayHistory) for the request/response calls.

### [performance] SSE hot path allocates a string plus a []byte copy per line
`daemon/agent/opencode/opencode.go:220` · _go:agent-adapters_

**Problem.** readEvents is the streaming hot path (one iteration per assistant token delta). Each line does sc.Text() (allocates a string), strings.TrimPrefix/TrimSpace (more string allocs), then []byte(payload) at line 226 copies the bytes back to a []byte purely to hand to json.Unmarshal in handle(). That is two avoidable allocations/copies per streamed event.

**Fix.** Work in bytes: use sc.Bytes(), check/trim the `data:` prefix with the bytes package (bytes.HasPrefix/bytes.TrimSpace), and pass the resulting []byte straight to handle() — no string round-trip. Note sc.Bytes() is only valid until the next Scan(), which is fine since handle() fully consumes it synchronously.

### [resource-leak] Per-session maps msgRoles/emittedUser grow unbounded for the session's lifetime
`daemon/agent/opencode/opencode.go:253` · _go:agent-adapters_

**Problem.** readEvents records every message ID it ever sees into s.msgRoles (line 253) and s.emittedUser (line 294) and never evicts entries. For a long-running attached session with thousands of turns these maps grow without bound, holding every historical messageID string in memory even though only in-flight messages are ever looked up. This is a slow memory leak proportional to conversation length per attached session.

**Fix.** Bound the memory: either delete a message's entries from both maps once session.idle is observed for that turn, or cap the maps with a small LRU / ring of recent message IDs. Only the currently-streaming message needs role/dedup state.

### [correctness] No counter-overflow guard before nonce wraps back to 0
`daemon/crypto/crypto.go:134` · _go:crypto_

**Problem.** Seal writes the low 8 bytes of a uint64 counter into the nonce with the top 4 nonce bytes always zero (line 134). When the counter wraps past 2^64 it silently returns to 0 and reuses earlier nonces under the same key. While 2^64 messages is not reachable in practice, correct AEAD counter code fails closed rather than silently reusing a nonce.

**Fix.** Return an explicit error when the counter reaches its maximum (e.g. if s.counter == math.MaxUint64 { return nil, errors.New("oculus/crypto: nonce counter exhausted") } before incrementing), forcing a rekey instead of silent nonce reuse.

### [performance] save() serializes JSON marshal + full-file rewrite under the mutex
`daemon/project/project.go:128` · _go:discovery-project_

**Problem.** save() is called with r.mu held (from Add line 87 and Remove line 125) and performs json.MarshalIndent plus os.MkdirAll + os.WriteFile of the entire registry while the lock is held, blocking all readers for the duration of the disk write. It also rewrites the whole file on every mutation. Minor at small registry sizes, but it compounds the lock-contention problem above.

**Fix.** Snapshot the data to marshal under the lock, then release the lock and perform MkdirAll/WriteFile without it (write to a temp file + os.Rename for atomicity). At minimum, be aware the disk write is on the locked path.

### [resource-leak] gitInfo execs git with no context or timeout — can hang indefinitely
`daemon/project/project.go:151` · _go:discovery-project_

**Problem.** gitInfo uses exec.Command (not exec.CommandContext) for both `git rev-parse` calls (lines 151, 154). git can block indefinitely (index.lock contention, credential/GPG prompt on a repo with signing, a network-backed working tree). There is no timeout and no cancellation, so Add() — and, per the finding above, the whole registry — can wedge forever with a stuck git child process.

**Fix.** Thread a context.Context (or use context.WithTimeout, e.g. 5s) into gitInfo and use exec.CommandContext so a hung git is killed. Propagate ctx from the Add caller if available.

### [concurrency] pushApproval spawns a goroutine per token with context.Background() and no timeout
`daemon/hub/hub.go:442` · _go:hub_

**Problem.** Each device token gets its own goroutine calling n.Notify(context.Background(), ...) with no timeout or cancellation. If the APNs/push call hangs, these goroutines leak indefinitely, and with many tokens/approvals they accumulate unbounded. There is also no concurrency bound on the fan-out.

**Fix.** Use a bounded context (context.WithTimeout, e.g. 10-15s) per Notify call so a hung push cannot leak a goroutine, and consider a small worker pool / semaphore instead of an unbounded goroutine-per-token spawn.

### [performance] Heavy blocking git/worktree/provider work runs inline in the connection dispatch loop
`daemon/hub/hub.go:517` · _go:hub_

**Problem.** Serve() dispatches messages synchronously in the Recv loop, and startSession() (called from TypeSessionCreate and TypeIssueLaunch) performs worktree.RepoRoot, worktree.Create, worktree.Bootstrap (which can run setup hooks/installs), plus p.Create and promptSession — all blocking git/network operations executed on the connection's single read goroutine. While a worktree is being created and bootstrapped, that client cannot have any other request processed; the whole connection is stalled for potentially many seconds. WorktreePR (CommitAll/Push/CreatePR, ~line 624-636) has the same problem.

**Fix.** Run long-running handlers off the Recv loop (dispatch each request in its own goroutine, or at least offload the worktree/provider-creation handlers), replying via the existing send path when done. Ensure ordering/backpressure is handled, but don't block message intake on git and network I/O.

### [resource-leak] Reserved worktree ports are never released — reservedPorts map leaks
`daemon/hub/hub.go:43` · _go:hub_

**Problem.** reservePort() (line 47) permanently adds allocated ports to h.reservedPorts, but nothing ever deletes them. When a worktree session ends (TypeWorktreeRemove, ~line 601-609) or fails, the port stays marked reserved forever. Over the daemon's lifetime the reserved set only grows, permanently burning ports in the configured range and eventually exhausting AllocPort's ability to find a free one.

**Fix.** Track the allocated port on the session (meta.port already holds it) and delete it from h.reservedPorts under h.mu when the session/worktree is removed (in the TypeWorktreeRemove handler and in the Bootstrap-failure cleanup path at hub.go:119). Add a releasePort(p int) helper.

### [concurrency] save() performs blocking disk I/O while holding m.mu
`daemon/issues/manager.go:84` · _go:issues_

**Problem.** Connect calls m.save() at line 84 while still holding m.mu (locked at line 76, unlocked at 85). save() does os.MkdirAll + json.MarshalIndent + os.WriteFile — synchronous filesystem syscalls. Every other method that takes m.mu (Issues(), Connected(), Provider(), Refresh()'s snapshot section, the poll tick) blocks on that disk write. On a slow/networked FS this stalls the whole issue subsystem and can serialize the poll loop behind a config flush.

**Fix.** Snapshot what save() needs (the Config value) under the lock, release the lock, then marshal+write outside it. e.g. copy cfg into a local, Unlock, then call a save(cfg) that takes no lock. save() touches only m.path (immutable) and m.cfg, so only the cfg read must be guarded.

### [correctness] linear.go gql checks HTTP status after JSON decode, masking non-JSON error responses
`daemon/issues/linear.go:47` · _go:issues_

**Problem.** gql decodes resp.Body into the envelope (line 47) before checking resp.StatusCode (line 53). If the server returns a non-2xx with a non-JSON body (e.g. a 401/429/502 HTML page from a gateway or rate limiter), Decode fails first and returns an opaque 'invalid character <' JSON error instead of the useful 'HTTP 429' status. Rate-limit and auth failures — expected on a polling client — get reported as parse errors.

**Fix.** Check resp.StatusCode/100 != 2 first and return the HTTP status (optionally reading a snippet of the body) before attempting to decode the GraphQL envelope.

### [performance] Refresh fetches providers sequentially instead of concurrently
`daemon/issues/manager.go:142` · _go:issues_

**Problem.** Refresh loops over providers and calls p.ListAssigned(ctx) one after another (line 142-151). Each call is an independent network round-trip to a different host (Linear GraphQL, Jira REST). Total latency is the sum of both, so a slow Jira response delays surfacing Linear issues and vice-versa. With N providers polled on every tick this scales linearly for no reason.

**Fix.** Fan out: launch each ListAssigned in its own goroutine, collect (issues, err) via a channel or sync.WaitGroup + a mutex-guarded slice, then merge. Preserve first-error semantics by capturing errors per goroutine.

### [performance] Response body not drained before Close, defeating keep-alive connection reuse
`daemon/issues/linear.go:40` · _go:issues_

**Problem.** In Linear.gql (linear.go:40) and Jira.do (jira.go:54), the body is decoded with json.NewDecoder(resp.Body).Decode(...) then closed. The streaming decoder stops at the end of the top-level JSON value and typically leaves the trailing newline/whitespace (and, on the out==nil paths in gql/do where the body is never read at all) the entire body unread. net/http only returns a connection to the idle pool if the body is read to EOF, so partially-read bodies force a new TCP+TLS handshake on the next poll — repeatedly, on the hot polling path.

**Fix.** Before defer resp.Body.Close(), ensure full drain: after decoding (or on the out==nil branch) do `io.Copy(io.Discard, resp.Body)`. Simplest is a helper that always drains then closes.

### [performance] Poll tick calls Connected() which allocates and sorts a slice just to get a count
`daemon/issues/manager.go:172` · _go:issues_

**Problem.** The polling goroutine does `if len(m.Connected()) > 0`. Connected() (line 97) allocates a []string, appends every provider name, and runs sort.Strings — all discarded to check a length. This runs every tick for the life of the daemon.

**Fix.** Add a cheap guarded helper, e.g. `func (m *Manager) hasProviders() bool { m.mu.Lock(); defer m.mu.Unlock(); return len(m.providers) > 0 }`, and call that in the tick instead of len(Connected()).

### [best-practice] APNs http.Client has no timeout — a stuck HTTP/2 dial hangs the send indefinitely
`daemon/push/push.go:90` · _go:push-main_

**Problem.** NewAPNs defaults cfg.Client to http.DefaultClient, which has Timeout: 0 (no timeout). enablePush in main.go (push.NewAPNs at main.go:298) never sets a Client, so production uses DefaultClient. Delivery relies solely on the caller passing a bounded context; if a caller passes context.Background() (as much of main.go does), a hung TLS/HTTP2 connection to api.push.apple.com blocks the calling goroutine forever. Using the shared http.DefaultClient/DefaultTransport also means push shares connection pool + settings with any other DefaultClient user in-process.

**Fix.** Default cfg.Client to a dedicated &http.Client{Timeout: 10*time.Second} (own client, not DefaultClient) in NewAPNs when cfg.Client == nil. The transport still negotiates HTTP/2 via ALPN for https, so HTTP/2 reuse is preserved while giving every send a hard upper bound.

### [best-practice] http.ListenAndServe with no server timeouts on a potentially public-facing daemon
`daemon/main.go:186` · _go:push-main_

**Problem.** serve() ends with http.ListenAndServe(*addr, mux), which uses a default http.Server with ReadTimeout/ReadHeaderTimeout/IdleTimeout all unset. The --public-url flag documents exposing this over ngrok/wss to the internet, so the /ws, /healthz and /oauth/linear/callback endpoints are reachable remotely with no Slowloris protection — a client can hold a connection open sending headers slowly and tie up server goroutines.

**Fix.** Construct srv := &http.Server{Addr: *addr, Handler: mux, ReadHeaderTimeout: 10*time.Second} and call srv.ListenAndServe(). Keep WriteTimeout/IdleTimeout unset (or large) so long-lived WebSocket upgrades on /ws are not cut off, but ReadHeaderTimeout bounds header-slowloris on the plain HTTP routes.

### [best-practice] context.Context stored in the Conn struct (containedctx) prevents per-call read/write deadlines
`daemon/wsmsg/wsmsg.go:15` · _go:wire_

**Problem.** Conn stashes a context.Context field (line 15) captured at New() time and reuses it for every ws.Read/ws.Write (lines 26, 29). This is the containedctx anti-pattern flagged by go vet/linters: the whole connection lifetime is bound to one context, so an individual ReadMsg that hangs (a stalled/half-open peer) can never be given its own timeout — the only way to unblock it is to cancel the context that kills the entire connection, and WriteMsg has no way to bound a slow write. It also means read and write share a single cancellation scope.

**Fix.** Prefer threading a context through the MsgConn methods (WriteMsg(ctx, b) / ReadMsg(ctx)) so callers can apply per-operation deadlines; if the MsgConn interface can't change, at least document that the stored ctx is the connection's lifetime scope and wrap ws.Read/ws.Write with context.WithTimeout inside the methods so a stalled peer can't wedge a reader/writer indefinitely.

### [performance] Encode double-marshals every message on the streaming hot path
`daemon/protocol/protocol.go:327` · _go:wire_

**Problem.** Encode marshals the payload to a json.RawMessage (line 330) and then marshals the whole Envelope (line 336), which copies those payload bytes verbatim into a second freshly-allocated buffer. That is two json.Marshal passes plus a memcpy and two heap allocations per message. For the explicitly high-frequency events (TypeOutputDelta, TypeThinking — token-by-token streaming) this is per-token GC pressure on the daemon's hottest path.

**Fix.** For the streaming deltas, encode once instead of via the RawMessage round-trip — e.g. assemble the envelope with a pooled *bytes.Buffer + json.Encoder from a sync.Pool, or hand-write the small {"type":...,"payload":...} frame. At minimum, reuse a pooled buffer for the outer Marshal so the delta path doesn't allocate two buffers per token.

### [best-practice] No context/timeout on any git or setup exec — a hung command blocks the daemon forever
`daemon/worktree/setup.go:79` · _go:worktree_

**Problem.** Every command in the package uses exec.Command (never exec.CommandContext). The Bootstrap setup command (line 79, `sh -c cfg.Setup`, run via CombinedOutput) is the highest-risk: an install that prompts on stdin, waits on a lock, or hangs on network will block the calling goroutine indefinitely with no way to cancel. The same applies to Push/CreatePR in finish.go. For a daemon driving many concurrent sessions, this is a real liveness hazard.

**Fix.** Thread a context.Context through these functions and use exec.CommandContext so callers can impose a timeout / cancel on session teardown. At minimum wrap the setup command with a context.WithTimeout.

### [best-practice] filepath.Glob error is discarded, silently copying nothing on a bad pattern
`daemon/worktree/setup.go:59` · _go:worktree_

**Problem.** `matches, _ := filepath.Glob(...)` drops the error. Glob returns ErrBadPattern for a malformed pattern (e.g. an unclosed `[`), so a misconfigured `copy` entry in project.json produces zero matches and the worktree is silently bootstrapped without the file the user asked to carry over — a confusing, hard-to-diagnose failure.

**Fix.** Capture the error and fail (or at least surface it): `matches, err := filepath.Glob(...); if err != nil { return res, fmt.Errorf("copy pattern %q: %w", pat, err) }`.

### [best-practice] RepoRoot discards the underlying git error/stderr in its wrapper
`daemon/worktree/worktree.go:24` · _go:worktree_

**Problem.** RepoRoot returns fmt.Errorf("%s is not a git repository", dir) and throws away err. With .Output(), git's real stderr is available on err.(*exec.ExitError).Stderr, and the failure may not actually be "not a git repository" (git missing from PATH, permission error, corrupt repo). The flattened message misleads debugging.

**Fix.** Wrap the real error: `return "", fmt.Errorf("%s: not a git repository: %w", dir, err)`, and consider surfacing exec.ExitError.Stderr for the git message.

### [best-practice] deriveSessionKeys copies the raw X25519 shared secret into an unmanaged Data buffer
`app/OculusKit/Sources/OculusKit/Crypto.swift:35` · _swift:kit_

**Problem.** `shared.withUnsafeBytes { Data($0) }` extracts the raw ECDH shared secret into a heap Data and wraps it in a SymmetricKey purely to feed HKDF. This materializes sensitive key material in an ordinary, non-zeroed Data buffer that lingers in memory. CryptoKit provides a direct path that never exposes the raw secret.

**Fix.** Use SharedSecret's built-in HKDF: `shared.hkdfDerivedSymmetricKey(using: SHA256.self, salt: hkdfSalt, sharedInfo: hkdfInfoC2D, outputByteCount: 32)` (and likewise for d2c). This keeps the IKM inside CryptoKit and removes the intermediate Data/SymmetricKey copy — verify it produces the same bytes as the Go side's HKDF(IKM=raw shared secret).

### [performance] ProtocolCoding creates a brand-new JSONEncoder/JSONDecoder on every call
`app/OculusKit/Sources/OculusKit/Protocol.swift:292` · _swift:kit_

**Problem.** `encoder()` and `decoder()` allocate a fresh JSONEncoder/JSONDecoder each invocation. Every `Protocol.encode`, `header`, and `payload` call spins up a new coder. On the hot streaming path (output.delta / thinking.delta arrive continuously), this allocates and configures coders per message for the life of a session — pure GC/alloc churn for stateless objects that are safe to reuse.

**Fix.** Cache single static instances: `private static let sharedEncoder = JSONEncoder()` / `private static let sharedDecoder = JSONDecoder()` and return those. JSONEncoder/JSONDecoder are reusable and thread-safe for concurrent encode/decode as long as their configuration isn't mutated after setup.

### [performance] Every inbound envelope is JSON-parsed twice (header then payload)
`app/OculusKit/Sources/OculusKit/Protocol.swift:334` · _swift:kit_

**Problem.** Callers read `Protocol.header(data)` to dispatch on `type`, then call `Protocol.payload(data, as:)` — each does a full independent JSON parse of the same bytes (decode EnvelopeHeader, then decode WireIn<T>). So every received message is fully parsed twice. On the high-frequency delta stream this doubles decode cost for no benefit.

**Fix.** Decode once into a single envelope type that carries both the header fields and a lazily/decodable payload, or decode `WireIn<T>` once and expose id/type/payload from it. E.g. have the receive loop decode the full envelope in one pass and hand callers the already-parsed type + payload rather than re-decoding from raw Data.

### [performance] hexString uses String(format:) per byte
`app/OculusKit/Sources/OculusKit/Crypto.swift:79` · _swift:kit_

**Problem.** `map { String(format: "%02x", $0) }.joined()` allocates a String per byte and invokes the printf-style formatter for each — the slowest common way to hex-encode. It's only on the once-per-connect handshake path today (encoding clientPub), so impact is small, but it's a needless allocation-heavy idiom if hexString is ever used on larger data.

**Fix.** Build directly from a nibble lookup table into a single [UInt8]/String, e.g. index into `"0123456789abcdef"` UTF8 bytes for the high/low nibble of each byte and construct one String. Avoids per-byte String allocation and the formatter.

### [best-practice] Synchronous file I/O on the main actor during connect/bootstrap
`app/OculusKit/Sources/OculusUI/OculusUI.swift:106` · _swift:model_

**Problem.** loadLocalPairing() (OculusUI.swift:106) and DesktopStore.localPairing() (DesktopStore.swift:128) do blocking `FileManager.default.contents(atPath:)` + `JSONSerialization` reads while running on the @MainActor — loadLocalPairing is called from autoConnectIfPaired and localPairing twice from bootstrap. Synchronous disk reads on the main thread block UI, and localPairing is invoked twice per bootstrap re-reading the same file.

**Fix.** Move the file read off the main actor (e.g. read the bytes in a detached/background task, then hop back to touch @Published state), and in bootstrap call localPairing() once and reuse the result instead of reading pairing.json twice.

### [performance] Each .ok frame is decoded up to ~9 times via a try? if-else cascade
`app/OculusKit/Sources/OculusUI/OculusUI.swift:480` · _swift:model_

**Problem.** The `MessageType.ok` branch runs a chain of `try? Protocol.payload(data, as: X.self)` attempts (DiscoverList, ProjectList, SessionList, IntegrationStatus, IssueList, IntegrationOAuth, WorktreeDiff, WorktreeConflicts, WorktreePRResult, Session). Each attempt fully parses the JSON payload from scratch, so a Session-typed OK frame is JSON-decoded ~9 times before the last branch matches. This is repeated full-parse work on every OK response.

**Fix.** Have the OK envelope carry a subtype discriminator (or reuse header info) and switch on it to decode exactly once. Failing a protocol change, decode the raw JSON object once and branch on a present key, rather than re-running `Protocol.payload` for every candidate type.

### [performance] JSONEncoder/JSONDecoder allocated on every save/load
`app/OculusKit/Sources/OculusUI/DesktopStore.swift:122` · _swift:model_

**Problem.** `save()` creates `JSONEncoder()` and `loadDesktops()` creates `JSONDecoder()` on every call. `save()` is invoked on every add/rename/remove/bootstrap. Allocating a fresh coder each time is needless repeated setup for objects that are safe to reuse.

**Fix.** Hoist to stored `private let encoder = JSONEncoder()` / `private let decoder = JSONDecoder()` on DesktopStore and reuse them.

### [correctness] ForEach keyed by array offset instead of stable id (sidebar sessions)
`app/OculusKit/Sources/OculusUI/SessionSidebar.swift:54` · _swift:views_

**Problem.** ForEach(Array(opencodeSessions.enumerated()), id: \.offset) (and the identical claudeSessions loop at line 63) uses the positional index as identity. When discovered sessions are inserted, removed, or reordered (they come from live host discovery), SwiftUI associates rows to the wrong data, causing incorrect re-renders, misplaced selection/tags, and broken row-state animations. The enumerated()/Array() wrapping also allocates each render.

**Fix.** Key by a stable identifier, e.g. ForEach(opencodeSessions, id: \.sessionID) { d in ... } (and by cwd or a composite key for claudeSessions), dropping the enumerated()/offset wrapping.

### [correctness] ForEach keyed by offset combined with remove(at:) on the same index (attachment chips)
`app/OculusKit/Sources/OculusUI/Composer.swift:89` · _swift:views_

**Problem.** ForEach(Array(model.pendingImages.enumerated()), id: \.offset) uses the array index as identity, and the delete button calls model.pendingImages.remove(at: idx). With index-based identity, removing an item shifts every subsequent item's identity, so SwiftUI's diffing/animation can target the wrong chip, and captured idx values can go stale relative to the mutated array.

**Fix.** Give ImageAttachment a stable id and use ForEach(model.pendingImages) with removal by id (pendingImages.removeAll { $0.id == img.id }) instead of enumerated offsets and remove(at:).

### [idiom] Redundant .id(msg.id) on an already-Identifiable ForEach element
`app/OculusKit/Sources/OculusUI/ChatView.swift:78` · _swift:views_

**Problem.** ForEach(model.messages) already derives identity from ChatMessage's Identifiable id. Attaching an extra .id(msg.id) to MessageRow is redundant and forces an explicit identity node; combined with the ScrollViewReader it can also interfere with SwiftUI's row reuse/diffing rather than help it.

**Fix.** Drop the trailing .id(msg.id); the ForEach identity already provides stable ids for scrollTo. Keep only the explicit ids that ScrollViewReader targets ("typing", "bottom").

### [performance] issues(category) re-filters the entire issues array 6 times per board render
`app/OculusKit/Sources/OculusUI/IssuesView.swift:108` · _swift:views_

**Problem.** In board, for each of the 3 columns the code calls issues(col.category) twice — once for the count (line 105) and once for the ForEach (line 108). issues(_:) does model.issues.filter { $0.category == category } each time, so every re-render performs 6 full linear scans of model.issues, allocating 6 new arrays. This scales with issue count and repeats on every unrelated model change.

**Fix.** Group once per render: compute let grouped = Dictionary(grouping: model.issues, by: { $0.category }) (as a computed property or once at the top of board) and index grouped[col.category] ?? [] for both the count and the ForEach.

### [performance] sessionGroups rebuilds a grouped+sorted structure with O(n*m) project lookups every body eval
`app/OculusKit/Sources/OculusUI/SessionSidebar.swift:25` · _swift:views_

**Problem.** sessionGroups is a computed property read from body that runs Dictionary(grouping:), a map, and a sort on every render, and inside the map does model.projects.first { $0.id == pid } — a linear scan of projects per group, making it O(groups * projects). It recomputes on every unrelated model change (busy, status, pendingApproval, etc.), not just when sessions/projects change.

**Fix.** Build a [projectID: name] lookup dictionary once, and consider memoizing sessionGroups so it only recomputes when model.sessions or model.projects change rather than on every body pass.
