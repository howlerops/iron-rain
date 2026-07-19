import SwiftUI
import OculusKit
#if canImport(AppKit)
import AppKit
#endif
#if os(iOS)
import ActivityKit
#endif

/// Drives one daemon connection: connect, autodetect running sessions, hold a
/// streaming conversation, and approve/deny tool calls. Built entirely on OculusKit
/// (the proven, vector-locked client). Shared by the iOS and macOS app targets.
@MainActor
public final class Model: ObservableObject {
    @Published public var wsURL = "ws://127.0.0.1:6000/ws"
    @Published public var daemonPubHex = ""
    @Published public var secret = ""
    /// Human name of this desktop (set when managed by a DesktopStore).
    @Published public var name = ""
    /// When true, a DesktopStore owns persistence — the Model won't write the legacy
    /// single-pairing UserDefaults keys.
    public var managed = false
    /// Stable identity for a desktop connection (the daemon's public key).
    public var id: String { daemonPubHex }

    @Published public var connected = false
    @Published public var status = "Not connected"
    @Published public var statusDetail: String? // human reason when not connected (unreachable, wrong secret, key mismatch)
    @Published public var messages: [ChatMessage] = []
    @Published public var sessionID: String?
    @Published public var currentSession: Session? // metadata (project/worktree/branch) of the active session
    @Published public var pendingApproval: ApprovalRequest?
    @Published public var discovered: [Discovered] = []
    @Published public var busy = false // agent is producing output
    @Published public var activity: String? // current step, e.g. "running bash"
    @Published public var pairingPublicURL: String? // reachable URL for the phone-pairing QR

    // Projects + worktrees.
    @Published public var projects: [Project] = []
    @Published public var sessions: [Session] = [] // hub-managed sessions (for sidebar grouping)
    @Published public var lastDiff: String? // populated by worktreeDiff()
    @Published public var conflicts: [FileConflict] = [] // files shared with other worktrees
    @Published public var pendingImages: [ImageAttachment] = [] // attached, sent with the next prompt

    // Trackers (Linear/Jira).
    @Published public var issues: [Issue] = []
    @Published public var connectedTrackers: [String] = []
    @Published public var oauthURL: URL? // set when an OAuth flow returns an authorize URL to open
    /// Options applied to the NEXT session created (by the first send). Set via newSession(...).
    @Published public var newSessionProvider = "opencode"
    public var pendingProjectID: String?
    public var pendingWorktree = false
    public var pendingWorkspaceName: String?

    private var client: OculusClient?
    private let clientPrivate = OculusCrypto.generatePrivateKey()
    private let defaults = UserDefaults.standard
    /// In-flight request/reply calls (fs.*), keyed by envelope id, resolved in receiveLoop.
    private var pendingRequests: [String: CheckedContinuation<Envelope, Error>] = [:]
    private var reconnectWanted = false
    private var reconnecting = false
    #if os(iOS)
    private var liveActivity: Any?
    #endif

    public init() {
        // Restore the last pairing so the app auto-reconnects without re-pairing.
        wsURL = defaults.string(forKey: Keys.ws) ?? wsURL
        daemonPubHex = defaults.string(forKey: Keys.pub) ?? ""
        secret = defaults.string(forKey: Keys.secret) ?? ""
    }

    /// A managed connection owned by a DesktopStore (persistence handled by the store).
    public convenience init(name: String, wsURL: String, daemonPubHex: String, secret: String) {
        self.init()
        self.managed = true
        self.name = name
        self.wsURL = wsURL
        self.daemonPubHex = daemonPubHex
        self.secret = secret
    }

    private enum Keys { static let ws = "oculus.ws", pub = "oculus.pub", secret = "oculus.secret" }

    /// True once the daemon has been paired at least once (creds are saved).
    public var hasSavedPairing: Bool { !wsURL.isEmpty && !daemonPubHex.isEmpty && !secret.isEmpty }

    private func savePairing() {
        guard !managed else { return } // a DesktopStore persists managed connections
        defaults.set(wsURL, forKey: Keys.ws)
        defaults.set(daemonPubHex, forKey: Keys.pub)
        defaults.set(secret, forKey: Keys.secret) // TODO: move the secret to the Keychain
    }

    // MARK: connection

    /// Connects to a locally-running (or paired) daemon and keeps the connection
    /// alive — auto-reconnecting with backoff if it drops. On macOS, if there's no
    /// saved pairing it reads the daemon's local pairing file (~/.oculus/pairing.json)
    /// so a same-machine app connects with zero config.
    public func autoConnectIfPaired() async {
        await loadLocalPairing() // macOS: refresh the reachable URL (and pair if unpaired)
        if hasSavedPairing && !connected { await connect() }
    }

    private func loadLocalPairing() async {
        #if os(macOS)
        let path = (NSHomeDirectory() as NSString).appendingPathComponent(".oculus/pairing.json")
        // Read the bytes off the main actor — synchronous disk I/O must not block the UI.
        let data = await Task.detached { FileManager.default.contents(atPath: path) }.value
        guard let data,
              let obj = try? JSONSerialization.jsonObject(with: data) as? [String: String],
              let ws = obj["ws"], let pub = obj["pub"], let sec = obj["secret"] else { return }
        // Always refresh the reachable URL for the pairing QR — even once we're paired,
        // since the daemon's public URL (relay/tunnel) can change between launches.
        pairingPublicURL = obj["public"]
        if !hasSavedPairing { applyPairing(url: ws, pub: pub, secret: sec) }
        #endif
    }

    /// The `oculus://pair?…` payload to encode in a QR for pairing a phone, using the
    /// daemon's reachable public URL. Nil until we know a reachable URL + creds.
    public var pairingURL: String? {
        let base = pairingPublicURL ?? (wsURL.isEmpty ? nil : wsURL)
        guard let base, !daemonPubHex.isEmpty, !secret.isEmpty else { return nil }
        var c = URLComponents()
        c.scheme = "oculus"
        c.host = "pair"
        c.queryItems = [
            .init(name: "ws", value: base),
            .init(name: "pub", value: daemonPubHex),
            .init(name: "secret", value: secret),
        ]
        return c.url?.absoluteString
    }

    public func connect() async {
        reconnectWanted = true
        await attemptConnect()
    }

    private func attemptConnect() async {
        guard let url = URL(string: wsURL), let pub = Data(hexString: daemonPubHex) else {
            status = "Invalid URL or daemon public key"
            return
        }
        let c = OculusClient(url: url)
        do {
            try await c.connect(clientPrivate: clientPrivate, daemonPublic: pub, secret: secret)
            client = c
            connected = true
            status = "Connected"
            statusDetail = nil
            savePairing()
            Task { await receiveLoop() }
            await discover()
            await loadProjects()
            await loadSessions()
            await loadIntegrationStatus()
            await loadIssues()
            if let token = OculusStore.shared.deviceToken {
                await registerDevice(token: token)
            }
            if let queued = OculusStore.shared.pendingPrompt {
                OculusStore.shared.pendingPrompt = nil
                await send(queued)
            }
            if let decision = OculusStore.shared.pendingDecision, pendingApproval != nil {
                OculusStore.shared.pendingDecision = nil
                await respond(decision)
            }
        } catch OculusClientError.handshakeRejected(let msg) {
            status = "Connect failed"
            statusDetail = msg.isEmpty ? "Pairing rejected" : "Pairing rejected: \(msg)"
            scheduleReconnect()
        } catch {
            status = "Connect failed"
            statusDetail = (error as NSError).domain == NSURLErrorDomain
                ? "Can’t reach this Mac"          // daemon down / wrong address
                : "Handshake failed — re-pair?"    // key mismatch / another daemon on the port
            scheduleReconnect()
        }
    }

    /// Retries the connection with exponential backoff until it succeeds or the user
    /// disconnects. One loop at a time.
    private func scheduleReconnect() {
        guard reconnectWanted, hasSavedPairing, !reconnecting, !connected else { return }
        reconnecting = true
        Task { // inherits @MainActor from Model
            var delay: UInt64 = 2
            while reconnectWanted && !connected {
                status = "Reconnecting…"
                try? await Task.sleep(nanoseconds: delay * 1_000_000_000)
                if reconnectWanted && !connected { await attemptConnect() }
                delay = min(delay * 2, 15)
            }
            reconnecting = false
        }
    }

    public func disconnect() {
        reconnectWanted = false
        client?.close()
        client = nil
        connected = false
        status = "Not connected"
        refreshLiveActivity(ended: true)
    }

    /// Fills the connect fields from a scanned pairing payload (oculus://pair?...).
    public func applyPairing(url: String, pub: String, secret: String) {
        self.wsURL = url
        self.daemonPubHex = pub
        self.secret = secret
    }

    // MARK: conversation

    /// Sends a user turn: creates the session on the first message, then follow-ups
    /// go to the same session (a real multi-turn conversation).
    public func send(_ text: String) async {
        let trimmed = text.trimmingCharacters(in: .whitespacesAndNewlines)
        let imgs = pendingImages
        guard (!trimmed.isEmpty || !imgs.isEmpty), let client else { return }
        let shown = trimmed.isEmpty ? "🖼️ \(imgs.count) image\(imgs.count == 1 ? "" : "s")" : trimmed
        messages.append(ChatMessage(role: .user, text: shown))
        busy = true
        pendingImages = []
        do {
            if let sid = sessionID {
                let env = try Protocol.encode(id: UUID().uuidString, type: MessageType.sessionPrompt,
                                              payload: SessionPrompt(sessionID: sid, text: trimmed, images: imgs.isEmpty ? nil : imgs))
                try await client.send(env)
            } else {
                let env = try Protocol.encode(id: UUID().uuidString, type: MessageType.sessionCreate,
                                              payload: SessionCreate(provider: newSessionProvider,
                                                                     projectID: pendingProjectID,
                                                                     prompt: trimmed,
                                                                     images: imgs.isEmpty ? nil : imgs,
                                                                     worktree: pendingWorktree ? true : nil,
                                                                     workspaceName: pendingWorkspaceName))
                try await client.send(env)
            }
        } catch {
            status = "Send failed: \(error)"
            busy = false
        }
    }

    /// Deletes a daemon-managed session: halts its agent (which ends the session server-side)
    /// and removes it from the list immediately (optimistic — the next session.list confirms).
    /// No-op for discovered sessions (not owned here). Clears the conversation if it's on screen.
    public func stopSession(_ id: String) async {
        guard let client else { return }
        // Optimistic removal so the row disappears at once instead of lingering until the
        // server broadcasts the updated session list.
        sessions.removeAll { $0.id == id }
        if sessionID == id { newSession() }
        do {
            let env = try Protocol.encode(id: UUID().uuidString, type: MessageType.sessionStop,
                                          payload: SessionRef(sessionID: id))
            try await client.send(env)
        } catch {
            status = "Delete failed: \(error)"
        }
    }

    /// Adds an image to be sent with the next prompt (converted to base64).
    public func attachImage(mime: String, data: Data) {
        pendingImages.append(ImageAttachment(mime: mime, data: data.base64EncodedString()))
    }

    /// Attaches to an existing session discovered on the host: loads its history and
    /// continues it live. opencode sessions only (claude-code transcripts are view-only).
    /// Clears the current conversation to start a fresh session on the next message.
    public func newSession() {
        sessionID = nil
        currentSession = nil
        messages.removeAll()
        pendingApproval = nil
        busy = false
        lastDiff = nil
    }

    /// Starts a fresh session with explicit options (provider, project folder, and an
    /// opt-in git worktree). The options apply when the first message creates the session.
    public func newSession(provider: String, projectID: String? = nil, worktree: Bool = false, workspaceName: String? = nil) {
        newSessionProvider = provider
        pendingProjectID = projectID
        pendingWorktree = worktree
        pendingWorkspaceName = workspaceName
        newSession()
    }

    // MARK: projects + worktrees

    public func loadProjects() async {
        guard let client else { return }
        if let env = try? Protocol.encode(id: UUID().uuidString, type: MessageType.projectList, payload: Optional<Int>.none) {
            try? await client.send(env)
        }
    }

    public func loadSessions() async {
        guard let client else { return }
        if let env = try? Protocol.encode(id: UUID().uuidString, type: MessageType.sessionList, payload: Optional<Int>.none) {
            try? await client.send(env)
        }
    }

    // MARK: trackers (Linear/Jira)

    public func connectTracker(provider: String, token: String) async {
        guard let client else { return }
        if let env = try? Protocol.encode(id: UUID().uuidString, type: MessageType.integrationConnect,
                                          payload: IntegrationConnect(provider: provider, token: token)) {
            try? await client.send(env)
            await loadIssues()
        }
    }

    /// Begins a Linear OAuth flow; the daemon returns an authorize URL to open in a browser.
    public func startLinearOAuth() async {
        guard let client else { return }
        if let env = try? Protocol.encode(id: UUID().uuidString, type: MessageType.integrationOAuth,
                                          payload: IntegrationOAuth(provider: "linear")) {
            try? await client.send(env)
        }
    }

    public func loadIntegrationStatus() async {
        guard let client else { return }
        if let env = try? Protocol.encode(id: UUID().uuidString, type: MessageType.integrationStatus, payload: Optional<Int>.none) {
            try? await client.send(env)
        }
    }

    public func loadIssues() async {
        guard let client else { return }
        if let env = try? Protocol.encode(id: UUID().uuidString, type: MessageType.issueList, payload: Optional<Int>.none) {
            try? await client.send(env)
        }
    }

    /// Launches an agent on a ticket (worktree on its branch). Requires a project (repo).
    public func launchIssue(_ issue: Issue, projectID: String, agentProvider: String? = nil) async {
        guard let client else { return }
        if let env = try? Protocol.encode(id: UUID().uuidString, type: MessageType.issueLaunch,
                                          payload: IssueLaunch(issueID: issue.id, provider: issue.provider,
                                                               projectID: projectID, worktree: true,
                                                               agentProvider: agentProvider)) {
            try? await client.send(env)
        }
    }

    /// Observes an existing hub-managed session (replays its transcript, then live).
    public func openSession(_ id: String) async {
        guard let client else { return }
        messages.removeAll()
        pendingApproval = nil
        busy = false
        lastDiff = nil
        if let env = try? Protocol.encode(id: UUID().uuidString, type: MessageType.sessionSubscribe, payload: SessionRef(sessionID: id)) {
            try? await client.send(env)
        }
    }

    public func addProject(path: String) async {
        guard let client, !path.isEmpty else { return }
        if let env = try? Protocol.encode(id: UUID().uuidString, type: MessageType.projectAdd, payload: ProjectAdd(path: path)) {
            try? await client.send(env)
            await loadProjects()
        }
    }

    public func removeProject(id: String) async {
        guard let client else { return }
        if let env = try? Protocol.encode(id: UUID().uuidString, type: MessageType.projectRemove, payload: ProjectRef(projectID: id)) {
            try? await client.send(env)
        }
    }

    /// Requests the diff of the current worktree session (result lands in lastDiff).
    public func worktreeDiff() async {
        guard let client, let sid = sessionID else { return }
        if let env = try? Protocol.encode(id: UUID().uuidString, type: MessageType.worktreeDiff, payload: WorktreeDiff(sessionID: sid)) {
            try? await client.send(env)
        }
    }

    public func removeWorktree(force: Bool = false) async {
        guard let client, let sid = sessionID else { return }
        if let env = try? Protocol.encode(id: UUID().uuidString, type: MessageType.worktreeRemove, payload: WorktreeRemove(sessionID: sid, force: force)) {
            try? await client.send(env)
        }
    }

    public func createPR(title: String, body: String? = nil) async {
        guard let client, let sid = sessionID else { return }
        if let env = try? Protocol.encode(id: UUID().uuidString, type: MessageType.worktreePR, payload: WorktreePR(sessionID: sid, title: title, body: body)) {
            try? await client.send(env)
        }
    }

    /// Requests files this worktree shares with other active worktrees (result -> conflicts).
    public func loadConflicts() async {
        guard let client, let sid = sessionID else { return }
        if let env = try? Protocol.encode(id: UUID().uuidString, type: MessageType.worktreeConflicts, payload: WorktreeConflicts(sessionID: sid)) {
            try? await client.send(env)
        }
    }

    /// Opens a session discovered on the host: opencode continues it live; claude-code
    /// resumes it (the daemon's registered provider Attaches via `--resume`, loading history).
    /// The daemon replies "provider cannot attach" for providers without resume support.
    public func attach(_ d: Discovered) async {
        guard let client, let sid = d.sessionID else { return }
        messages.removeAll()
        sessionID = nil
        busy = false
        pendingApproval = nil
        do {
            let env = try Protocol.encode(id: UUID().uuidString, type: MessageType.sessionAttach,
                                          payload: SessionAttach(provider: d.provider, sessionID: sid, url: d.url))
            try await client.send(env)
        } catch {
            status = "Attach failed: \(error)"
        }
    }

    // MARK: - Built-in editor file access (request/reply)

    /// Sends a request and awaits its correlated OK/error reply by envelope id. Unlike prompts
    /// (fire-and-forget events), fs.* calls need a response, so receiveLoop resolves the
    /// matching continuation.
    private func request(_ type: String, payload: some Encodable) async throws -> Envelope {
        guard let client else {
            throw NSError(domain: "Oculus", code: -1, userInfo: [NSLocalizedDescriptionKey: "not connected"])
        }
        let id = UUID().uuidString
        let env = try Protocol.encode(id: id, type: type, payload: payload)
        return try await withCheckedThrowingContinuation { cont in
            pendingRequests[id] = cont
            Task {
                do { try await client.send(env) }
                catch { if let c = pendingRequests.removeValue(forKey: id) { c.resume(throwing: error) } }
            }
        }
    }

    /// Lists a directory (nil/empty path → the available roots).
    public func fsTree(_ path: String?) async throws -> FSTree {
        try await request(MessageType.fsTree, payload: FSTreeReq(path: path)).payload(as: FSTree.self)
    }

    /// Reads a text file (content + sha for conflict detection).
    public func fsRead(_ path: String) async throws -> FSFile {
        try await request(MessageType.fsRead, payload: FSReadReq(path: path)).payload(as: FSFile.self)
    }

    /// Saves a file if `baseSha` still matches on disk; result.conflict signals a stale write.
    public func fsWrite(_ path: String, content: String, baseSha: String) async throws -> FSWriteResult {
        try await request(MessageType.fsWrite, payload: FSWriteReq(path: path, content: content, baseSha: baseSha))
            .payload(as: FSWriteResult.self)
    }

    /// Unified diff for a session's changes or an in-root path (review).
    public func fsDiff(sessionID: String? = nil, path: String? = nil) async throws -> String {
        try await request(MessageType.fsDiff, payload: FSDiffReq(sessionID: sessionID, path: path))
            .payload(as: FSDiff.self).diff
    }

    /// Fails every in-flight fs request (called when the socket drops).
    private func failPendingRequests(_ error: Error) {
        let inflight = pendingRequests
        pendingRequests.removeAll()
        for (_, cont) in inflight { cont.resume(throwing: error) }
    }

    public func respond(_ decision: String) async {
        guard let client, let ap = pendingApproval else { return }
        let verb: String
        switch decision {
        case Decision.deny: verb = "✗ Denied"
        case Decision.always: verb = "✓ Always allow"
        default: verb = "✓ Allowed"
        }
        let cmd = (ap.detail?.isEmpty == false) ? " · \(ap.detail!)" : ""
        appendTool("\(verb) \(ap.tool)\(cmd)")
        pendingApproval = nil
        refreshLiveActivity()
        do {
            let env = try Protocol.encode(id: UUID().uuidString, type: MessageType.approvalRespond,
                                          payload: ApprovalRespond(approvalID: ap.approvalID, decision: decision))
            try await client.send(env)
        } catch {
            status = "Respond failed: \(error)"
        }
    }

    public func discover() async {
        guard let client else { return }
        let env = try? Protocol.encode(id: UUID().uuidString, type: MessageType.discover)
        if let env { try? await client.send(env) }
    }

    public func registerDevice(token: String) async {
        guard let client else { return }
        let env = try? Protocol.encode(id: UUID().uuidString, type: MessageType.deviceRegister,
                                       payload: DeviceRegister(token: token))
        if let env { try? await client.send(env) }
    }

    // MARK: streaming helpers

    // Token deltas accumulate in `streamBuffer` and are folded into the last streaming
    // `messages` element on a short timer, so the @Published array republishes a few
    // times per second instead of once per token — avoiding O(n) list re-diffing per
    // token (worst-case O(n²) over a long response). The streaming message still lives
    // in `messages`, so the view renders it unchanged.
    private var streamBuffer = ""
    private var flushTask: Task<Void, Never>?
    private static let flushInterval: UInt64 = 60_000_000 // 60ms

    /// Folds any buffered token text into the current streaming message (one array
    /// mutation). Safe to call at any time — a no-op when nothing is buffered.
    private func flushStream() {
        guard !streamBuffer.isEmpty else { return }
        if let last = messages.last, last.streaming {
            messages[messages.count - 1].text += streamBuffer
        }
        streamBuffer = ""
    }

    private func scheduleFlush() {
        guard flushTask == nil else { return } // a flush is already pending; it'll drain the buffer
        flushTask = Task { [weak self] in
            try? await Task.sleep(nanoseconds: Model.flushInterval)
            guard let self else { return }
            self.flushTask = nil
            self.flushStream()
        }
    }

    private func cancelFlush() {
        flushTask?.cancel()
        flushTask = nil
    }

    private func appendAssistantDelta(_ text: String) {
        // The answer starting means thinking is done — finalize any streaming thinking.
        finalizeThinking()
        if let last = messages.last, last.role == .assistant, last.streaming {
            // keep buffering into the existing streaming message
        } else {
            flushStream()
            messages.append(ChatMessage(role: .assistant, text: "", streaming: true))
        }
        streamBuffer += text
        scheduleFlush()
    }

    private func appendThinkingDelta(_ text: String) {
        if let last = messages.last, last.role == .thinking, last.streaming {
            // keep buffering into the existing streaming message
        } else {
            flushStream()
            messages.append(ChatMessage(role: .thinking, text: "", streaming: true))
        }
        streamBuffer += text
        scheduleFlush()
    }

    private func finalizeThinking() {
        if let last = messages.last, last.role == .thinking, last.streaming {
            flushStream() // fold any buffered thinking tokens before sealing the message
            messages[messages.count - 1].streaming = false
        }
    }

    private func finalizeStreaming() {
        finalizeThinking()
        flushStream() // fold any buffered assistant tokens before sealing the message
        cancelFlush()
        if let last = messages.last, last.role == .assistant, last.streaming {
            messages[messages.count - 1].streaming = false
        }
    }

    private func appendTool(_ text: String) {
        finalizeStreaming()
        messages.append(ChatMessage(role: .tool, text: text))
    }

    // MARK: receive loop

    private func receiveLoop() async {
        guard let client else { return }
        while connected {
            do {
                let data = try await client.recv()
                // Parse the envelope once; env.payload(as:) then decodes only the payload
                // subtree instead of re-tokenizing the whole frame per candidate type.
                let env = try Protocol.envelope(data)
                // Correlated request/reply (fs.*): resolve the waiting continuation by id and
                // skip the broadcast switch. Events carry an empty id, so they fall through.
                if let id = env.id, !id.isEmpty, let cont = pendingRequests.removeValue(forKey: id) {
                    if env.type == MessageType.error {
                        let msg = (try? env.payload(as: [String: String].self))?["message"] ?? "request failed"
                        cont.resume(throwing: NSError(domain: "Oculus", code: -2, userInfo: [NSLocalizedDescriptionKey: msg]))
                    } else {
                        cont.resume(returning: env)
                    }
                    continue
                }
                switch env.type {
                case MessageType.ok:
                    // Route the OK frame to exactly one typed decode by its unique payload
                    // key — no extra parse (payloadKeys reads the already-parsed envelope).
                    let keys = env.payloadKeys
                    if keys.contains("items"), let dl = try? env.payload(as: DiscoverList.self), !dl.items.isEmpty {
                        discovered = dl.items
                    } else if keys.contains("projects"), let pl = try? env.payload(as: ProjectList.self) {
                        projects = pl.projects
                    } else if keys.contains("sessions"), let sl = try? env.payload(as: SessionList.self) {
                        sessions = sl.sessions
                    } else if keys.contains("connected"), let st = try? env.payload(as: IntegrationStatus.self) {
                        connectedTrackers = st.connected
                    } else if keys.contains("issues"), let il = try? env.payload(as: IssueList.self) {
                        issues = il.issues
                    } else if keys.contains("url"), keys.contains("provider"), let oa = try? env.payload(as: IntegrationOAuth.self), let u = oa.url, let url = URL(string: u) {
                        oauthURL = url
                    } else if keys.contains("diff"), let wd = try? env.payload(as: WorktreeDiff.self), wd.diff != nil {
                        lastDiff = wd.diff
                    } else if keys.contains("files"), let wc = try? env.payload(as: WorktreeConflicts.self), wc.files != nil {
                        conflicts = wc.files ?? []
                    } else if keys.contains("pushed"), let pr = try? env.payload(as: WorktreePRResult.self) {
                        status = pr.url.map { "PR: \($0)" } ?? "Pushed \(pr.branch)"
                    } else if keys.contains("id"), let s = try? env.payload(as: Session.self) {
                        sessionID = s.id
                        currentSession = s
                        refreshLiveActivity()
                        Task { await loadSessions() } // reflect the new session in the sidebar
                    }
                case MessageType.sessionMessage:
                    if let m = try? env.payload(as: SessionMessage.self) {
                        let role: ChatMessage.Role = m.role == "user" ? .user : (m.role == "tool" ? .tool : .assistant)
                        let trimmed = m.text.trimmingCharacters(in: .whitespacesAndNewlines)
                        // Skip the echo of our own just-sent user turn (appended locally for instant feedback).
                        if role == .user, let last = messages.last, last.role == .user,
                           last.text.trimmingCharacters(in: .whitespacesAndNewlines) == trimmed {
                            break
                        }
                        finalizeStreaming()
                        messages.append(ChatMessage(role: role, text: m.text))
                    }
                case MessageType.thinking:
                    if let t = try? env.payload(as: Thinking.self) {
                        appendThinkingDelta(t.text)
                        busy = true
                    }
                case MessageType.outputDelta:
                    if let d = try? env.payload(as: OutputDelta.self) {
                        appendAssistantDelta(d.text)
                    }
                case MessageType.sessionStatus:
                    if let ss = try? env.payload(as: SessionStatus.self) {
                        status = ss.status
                        activity = ss.detail
                        switch ss.status {
                        case SessionStatusValue.idle, SessionStatusValue.done:
                            pendingApproval = nil; busy = false; activity = nil; finalizeStreaming()
                        case SessionStatusValue.awaitingApproval:
                            busy = false
                        default:
                            busy = true
                            // NOTE: do NOT clear pendingApproval here — with parallel tool
                            // calls a sibling tool can be "running" while another awaits
                            // approval. Cross-client clear happens on idle / when a new
                            // approval replaces it.
                        }
                        refreshLiveActivity()
                    }
                case MessageType.approvalRequest:
                    if let ar = try? env.payload(as: ApprovalRequest.self) {
                        pendingApproval = ar
                        refreshLiveActivity()
                    }
                case MessageType.approvalResolved:
                    // Another device answered this exact approval — clear our card and
                    // mirror the decision so both transcripts match.
                    if let r = try? env.payload(as: ApprovalResolved.self),
                       let ap = pendingApproval, ap.approvalID == r.approvalID {
                        let verb = r.decision == Decision.deny ? "✗ Denied"
                            : (r.decision == Decision.always ? "✓ Always allow" : "✓ Allowed")
                        let cmd = (ap.detail?.isEmpty == false) ? " · \(ap.detail!)" : ""
                        appendTool("\(verb) \(ap.tool)\(cmd)")
                        pendingApproval = nil
                        refreshLiveActivity()
                    }
                case MessageType.issueList: // broadcast from the 60s tracker poll
                    if let il = try? env.payload(as: IssueList.self) {
                        issues = il.issues
                    }
                case MessageType.integrationStatus: // broadcast after (re)connect
                    if let st = try? env.payload(as: IntegrationStatus.self) {
                        connectedTrackers = st.connected
                    }
                case MessageType.error:
                    if let e = try? env.payload(as: ProtocolError.self) {
                        status = "error: \(e.message)"
                        busy = false
                    }
                default:
                    break
                }
            } catch {
                connected = false
                status = "Disconnected"
                busy = false
                refreshLiveActivity(ended: true)
            }
        }
        // Connection dropped — fail any in-flight fs requests and auto-reconnect (unless the
        // user disconnected).
        failPendingRequests(NSError(domain: "Oculus", code: -3, userInfo: [NSLocalizedDescriptionKey: "disconnected"]))
        scheduleReconnect()
    }

    // MARK: live activity

    /// Starts/updates/ends the session Live Activity to match current state.
    func refreshLiveActivity(ended: Bool = false) {
        #if os(iOS)
        if #available(iOS 16.1, *) {
            if ended {
                if let act = liveActivity as? Activity<OculusActivityAttributes> {
                    Task { await act.end(dismissalPolicy: .immediate) }
                }
                liveActivity = nil
                return
            }
            guard ActivityAuthorizationInfo().areActivitiesEnabled, let sid = sessionID else { return }
            let state = OculusActivityAttributes.ContentState(
                status: status, tool: pendingApproval?.tool, awaitingApproval: pendingApproval != nil
            )
            if let act = liveActivity as? Activity<OculusActivityAttributes> {
                Task { await act.update(using: state) }
            } else {
                liveActivity = try? Activity.request(
                    attributes: OculusActivityAttributes(sessionID: sid), contentState: state
                )
            }
        }
        #endif
    }
}

extension Model {
    /// SF Symbol reflecting live state — used by the menu-bar item.
    public var menuBarSymbol: String {
        if pendingApproval != nil { return "bell.badge.fill" }
        if connected { return "bolt.horizontal.circle.fill" }
        return "bolt.horizontal.circle"
    }
}

#if os(macOS)
/// Compact menu-bar surface: live status + one-tap approve/deny for the active desktop.
public struct MenuBarView: View {
    @ObservedObject var store: DesktopStore
    public init(store: DesktopStore) { self.store = store }

    public var body: some View {
        if let model = store.active {
            MenuBarBody(model: model, store: store)
        } else {
            VStack(alignment: .leading, spacing: 8) {
                Text("Iron Rain").font(.headline)
                Text("No desktop paired. Open the window to add one.")
                    .font(.caption).foregroundStyle(.secondary)
                Divider()
                Button("Quit Iron Rain") { NSApplication.shared.terminate(nil) }
            }.padding(12).frame(width: 240)
        }
    }
}

struct MenuBarBody: View {
    @ObservedObject var model: Model
    @ObservedObject var store: DesktopStore

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack {
                Text("Iron Rain").font(.headline)
                Spacer()
                if store.models.count > 1 {
                    Menu(model.name.isEmpty ? "Desktop" : model.name) {
                        ForEach(store.models, id: \.id) { m in
                            Button(m.name.isEmpty ? "Desktop" : m.name) { store.selectedID = m.id }
                        }
                    }.fixedSize()
                }
            }
            Text(model.status).font(.caption).foregroundStyle(.secondary)

            if let ap = model.pendingApproval {
                Divider()
                Text("Approve \(ap.tool)?").bold()
                HStack {
                    Button("Deny") { Task { await model.respond(Decision.deny) } }
                    Button("Allow") { Task { await model.respond(Decision.allow) } }
                }
            } else if model.connected {
                Text("\(model.discovered.count) detected · \(model.messages.count) messages")
                    .font(.caption).foregroundStyle(.secondary)
            } else {
                Text("Open the window to connect.").font(.caption).foregroundStyle(.secondary)
            }

            Divider()
            Button("Quit Iron Rain") { NSApplication.shared.terminate(nil) }
        }
        .padding(12)
        .frame(width: 240)
    }
}
#endif
