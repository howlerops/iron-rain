import SwiftUI
import OculusKit
#if canImport(AppKit)
import AppKit
#endif
#if os(iOS)
import ActivityKit
#endif

/// Outcome of one raced connection route: a live client, an explicit daemon refusal, or couldn't
/// reach it. Sendable so it can be returned from the connection race's child tasks.
private enum RouteOutcome: Sendable {
    case connected(OculusClient)
    case rejected(String)
    case unreachable
}

/// Drives one daemon connection: connect, autodetect running sessions, hold a
/// streaming conversation, and approve/deny tool calls. Built entirely on OculusKit
/// (the proven, vector-locked client). Shared by the iOS and macOS app targets.
@MainActor
public final class Model: ObservableObject {
    @Published public var wsURL = "ws://127.0.0.1:6000/ws"
    /// Shared relay URL for remote access from anywhere (off-LAN). When set, the client dials the
    /// relay and bridges to this daemon by server_id (= the daemon pubkey); the direct `wsURL` is
    /// the LAN fallback. Empty = LAN-only.
    @Published public var relayURL = ""
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
    /// A prominent, dismissable error for a user action that failed while connected (e.g. a session
    /// that couldn't start). Drives an alert on the main surface — status text alone is invisible
    /// once the triggering sheet has dismissed.
    @Published public var actionError: String?
    /// True while a session is being created (worktree setup + provider spin-up can take a few
    /// seconds). Drives a skeleton loading overlay that locks the surface until it's ready.
    @Published public var startingSession = false
    @Published public var startingProvider = ""
    /// The active session's agent slash commands (built-in + custom from .claude/commands), for the
    /// composer's "/" palette. Loaded per session.
    @Published public var commands: [SlashCommand] = []
    @Published public var messages: [ChatMessage] = []
    @Published public var sessionID: String?
    @Published public var currentSession: Session? // metadata (project/worktree/branch) of the active session
    @Published public var pendingApproval: ApprovalRequest? { didSet { refreshLiveActivity() } }
    @Published public var discovered: [Discovered] = []
    @Published public var busy = false { didSet { refreshLiveActivity() } } // agent is producing output; drives the Live Activity
    @Published public var activity: String? // current step, e.g. "running bash"
    @Published public var pairingPublicURL: String? // reachable URL for the phone-pairing QR
    /// Live LSP diagnostics keyed by file path (editor underlines + the problems list).
    @Published public var diagnostics: [String: [LSPDiagnostic]] = [:]
    /// The active session's live to-do list (from the agent).
    @Published public var todos: [Todo] = []

    // Heartbeat supervision: latest derived state per session, and whether the active session
    // is enrolled in autonomous nudging (mirrors the daemon; user-toggleable).
    @Published public var heartbeats: [String: SessionHeartbeat] = [:]
    @Published public var autonomous = false

    // Indexed agent-authored handoff files (progress externalized to disk). Keyed lookups by
    // session id let the chat surface a "handoff saved" affordance for the active session.
    @Published public var handoffs: [HandoffEntry] = []
    /// Test/build run output + outcome for the active session.
    @Published public var testOutput: [String] = []
    @Published public var testResult: RunResult?
    @Published public var testRunning = false
    @Published public var showTests = false

    // Projects + worktrees.
    @Published public var projects: [Project] = []
    @Published public var sessions: [Session] = [] // hub-managed sessions (for sidebar grouping)
    @Published public var lastDiff: String? // populated by worktreeDiff()
    @Published public var workspaceDiffs: [WorkspaceMemberDiff] = [] // per-repo diffs (workspace sessions)
    @Published public var workspacePRResults: [WorkspaceMemberPR] = [] // per-repo PR outcomes
    @Published public var workspacePRRunning = false
    @Published public var conflicts: [FileConflict] = [] // files shared with other worktrees
    @Published public var pendingImages: [ImageAttachment] = [] // attached, sent with the next prompt

    // Trackers (Linear/Jira).
    @Published public var issues: [Issue] = []
    /// Saved issue views (named filter presets) + tickets the user has hidden. Persisted locally.
    @Published public var savedIssueFilters: [SavedIssueFilter] = []
    @Published public var hiddenIssueIDs: Set<String> = []
    @Published public var connectedTrackers: [String] = []
    // Trackers that have an OAuth app configured (client_id present) — drives whether the connect
    // screen shows the OAuth button or asks for the OAuth app credentials.
    @Published public var oauthApps: [String] = []
    /// Connected trackers whose OAuth token refresh is failing — drives a "reconnect" pill.
    @Published public var trackerAuthErrors: [String] = []
    // Last tracker-connect/OAuth error, surfaced on the connect screen.
    @Published public var trackerError: String?
    @Published public var oauthURL: URL? // set when an OAuth flow returns an authorize URL to open
    /// Options applied to the NEXT session created (by the first send). Set via newSession(...).
    @Published public var newSessionProvider = "opencode"
    // Agent providers this daemon actually has (opencode/claude-code/pi + any generic CLI agents),
    // fetched on connect so the picker reflects reality instead of a hardcoded list.
    @Published public var providers: [String] = ["opencode", "claude-code", "pi"]
    public var pendingProjectID: String?
    public var pendingProjectIDs: [String]?  // multi-root workspace (multi-repo)
    public var pendingWorktree = false
    public var pendingWorkspaceName: String?
    public var pendingPlan = false

    private var client: OculusClient?
    private let clientPrivate = OculusCrypto.generatePrivateKey()
    private let defaults = UserDefaults.standard
    /// In-flight request/reply calls (fs.*), keyed by envelope id, resolved in receiveLoop.
    private var pendingRequests: [String: CheckedContinuation<Envelope, Error>] = [:]
    /// Decoded tracker images keyed by URL (fetched through the daemon; auth-gated).
    private var imageCache: [String: Data] = [:]
    /// Fires if a prompt gets no response within the window (dead/orphaned session).
    private var watchdogTask: Task<Void, Never>?
    /// While true, skip replayed transcript messages that duplicate ones already shown
    /// (set briefly around a live re-attach so reviving a session doesn't double the chat).
    private var dedupReplay = false
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
        relayURL = defaults.string(forKey: Keys.relay) ?? ""
        loadIssuePrefs()
    }

    // MARK: saved issue views + hidden tickets

    private enum IssuePrefKeys { static let filters = "oculus.issueFilters", hidden = "oculus.hiddenIssues" }

    private func loadIssuePrefs() {
        if let d = defaults.data(forKey: IssuePrefKeys.filters),
           let f = try? JSONDecoder().decode([SavedIssueFilter].self, from: d) { savedIssueFilters = f }
        if let h = defaults.stringArray(forKey: IssuePrefKeys.hidden) { hiddenIssueIDs = Set(h) }
    }

    private func persistIssuePrefs() {
        if let d = try? JSONEncoder().encode(savedIssueFilters) { defaults.set(d, forKey: IssuePrefKeys.filters) }
        defaults.set(Array(hiddenIssueIDs), forKey: IssuePrefKeys.hidden)
    }

    public func hideIssue(_ id: String) { hiddenIssueIDs.insert(id); persistIssuePrefs() }
    public func unhideIssue(_ id: String) { hiddenIssueIDs.remove(id); persistIssuePrefs() }
    public func unhideAllIssues() { hiddenIssueIDs.removeAll(); persistIssuePrefs() }
    public func addSavedIssueFilter(_ f: SavedIssueFilter) { savedIssueFilters.append(f); persistIssuePrefs() }
    public func deleteSavedIssueFilter(_ id: String) { savedIssueFilters.removeAll { $0.id == id }; persistIssuePrefs() }

    /// A managed connection owned by a DesktopStore (persistence handled by the store).
    public convenience init(name: String, wsURL: String, daemonPubHex: String, secret: String, relay: String = "") {
        self.init()
        self.managed = true
        self.name = name
        self.wsURL = wsURL
        self.daemonPubHex = daemonPubHex
        self.secret = secret
        self.relayURL = relay
    }

    private enum Keys { static let ws = "oculus.ws", pub = "oculus.pub", secret = "oculus.secret", relay = "oculus.relay" }

    /// True once the daemon has been paired at least once (creds are saved).
    public var hasSavedPairing: Bool { !wsURL.isEmpty && !daemonPubHex.isEmpty && !secret.isEmpty }

    private func savePairing() {
        guard !managed else { return } // a DesktopStore persists managed connections
        defaults.set(wsURL, forKey: Keys.ws)
        defaults.set(daemonPubHex, forKey: Keys.pub)
        defaults.set(secret, forKey: Keys.secret) // TODO: move the secret to the Keychain
        defaults.set(relayURL, forKey: Keys.relay)
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
        // Always refresh the relay URL (the daemon may have been upgraded to a build that connects
        // to the shared relay), even once paired — so an existing local pairing gains remote access.
        if let relay = obj["relay"], !relay.isEmpty { relayURL = relay }
        if !hasSavedPairing { applyPairing(url: ws, pub: pub, secret: sec, relay: obj["relay"] ?? "") }
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
        if !relayURL.isEmpty {
            c.queryItems?.append(.init(name: "relay", value: relayURL))
        }
        return c.url?.absoluteString
    }

    public func connect() async {
        reconnectWanted = true
        await attemptConnect()
    }

    private func attemptConnect() async {
        guard let pub = Data(hexString: daemonPubHex) else {
            status = "Invalid daemon public key"
            return
        }
        // Build candidate routes and RACE them: the direct LAN/localhost URL (instant + free when
        // you're on the same network) plus every shared relay (reachable from anywhere), each with
        // URL-param registration. First to finish the handshake wins; the rest are cancelled (their
        // in-flight sockets closed) — so you get LAN latency at home and relay reachability when
        // away, with no sequential-timeout penalty either way. Relays forward only ciphertext, so
        // every route is equally end-to-end-secure.
        var routes: [URL] = []
        if let u = URL(string: wsURL) { routes.append(u) }                 // direct (LAN / localhost)
        for r in Self.splitRelays(relayURL) {                              // shared relays (DO, Fly, …)
            if let u = Self.relayClientURL(r, serverID: daemonPubHex) { routes.append(u) }
        }
        guard !routes.isEmpty else { status = "No address to connect to"; return }

        let priv = clientPrivate, sec = secret
        var winner: OculusClient?
        var rejected: String?      // a route reached the daemon and it refused (wrong secret / key)
        await withTaskGroup(of: RouteOutcome.self) { group in
            for url in routes {
                group.addTask {
                    let c = OculusClient(url: url)
                    return await withTaskCancellationHandler {
                        do {
                            try await c.connect(clientPrivate: priv, daemonPublic: pub, secret: sec)
                            return .connected(c)
                        } catch OculusClientError.handshakeRejected(let m) {
                            c.close(); return .rejected(m)
                        } catch {
                            c.close(); return .unreachable
                        }
                    } onCancel: {
                        c.close() // a faster route won — stop this in-flight attempt immediately
                    }
                }
            }
            for await outcome in group {
                switch outcome {
                case .connected(let c):
                    if winner == nil {
                        winner = c
                        group.cancelAll()  // a route won — abandon the slower ones
                    } else {
                        c.close()          // a straggler connected after we already had a winner
                    }
                case .rejected(let m):
                    if rejected == nil { rejected = m }
                case .unreachable:
                    break
                }
            }
        }

        if let c = winner {
            client = c
            connected = true
            status = "Connected"
            statusDetail = nil
            savePairing()
            Task { await receiveLoop() }
            await finishConnect()
            return
        }

        // Every route failed.
        status = "Connect failed"
        if let rejected {
            statusDetail = rejected.isEmpty ? "Pairing rejected" : "Pairing rejected: \(rejected)"
        } else {
            statusDetail = "Can’t reach this Mac" // daemon down / asleep / all relays unreachable
        }
        scheduleReconnect()
    }

    /// Post-connection hydration: load projects/sessions/integrations and replay any pending
    /// notification-driven action. Runs once, after a route wins the race.
    private func finishConnect() async {
        // Note: discovery of terminal-owned sessions is on-demand (the Add Session search),
        // not auto-loaded — the sidebar shows only sessions started/opened in the app.
        await loadProjects()
        await loadSessions()
        await loadIntegrationStatus()
        await loadIssues()
        await listProviders() // reflect the daemon's real agent set in the picker
        await listHandoffs()  // seed the handoff index (live updates arrive via handoff.list)
        // If a session was open when the socket dropped (e.g. the daemon restarted and forgot its
        // in-memory sessions), re-attach it so its transcript + prompts resume.
        await reopenCurrentSession()
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
        // Tapped a notification (agent finished / error / approval) → open that session.
        if let sid = OculusStore.shared.handoffSessionID {
            OculusStore.shared.handoffSessionID = nil
            await openSession(sid)
        }
    }

    /// Splits a comma-separated relay list into individual URLs (trimmed; empties dropped).
    static func splitRelays(_ list: String) -> [String] {
        list.split(separator: ",").map { $0.trimmingCharacters(in: .whitespaces) }.filter { !$0.isEmpty }
    }

    /// Builds a relay client URL carrying the unified registration params (?sid=&role=client).
    static func relayClientURL(_ base: String, serverID: String) -> URL? {
        guard var comps = URLComponents(string: base) else { return nil }
        var items = comps.queryItems ?? []
        items.append(.init(name: "sid", value: serverID))
        items.append(.init(name: "role", value: "client"))
        comps.queryItems = items
        return comps.url
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
    public func applyPairing(url: String, pub: String, secret: String, relay: String = "") {
        self.wsURL = url
        self.daemonPubHex = pub
        self.secret = secret
        self.relayURL = relay
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
        if let sid = sessionID {
            await deliverPrompt(sessionID: sid, text: trimmed, images: imgs, allowReattach: true)
        } else {
            do {
                let env = try Protocol.encode(id: UUID().uuidString, type: MessageType.sessionCreate,
                                              payload: SessionCreate(provider: newSessionProvider,
                                                                     projectID: pendingProjectID,
                                                                     projectIDs: pendingProjectIDs,
                                                                     prompt: trimmed,
                                                                     images: imgs.isEmpty ? nil : imgs,
                                                                     worktree: pendingWorktree ? true : nil,
                                                                     workspaceName: pendingWorkspaceName,
                                                                     plan: pendingPlan ? true : nil))
                try await client.send(env)
                armWatchdog()
            } catch {
                status = "Send failed: \(error.localizedDescription)"
                busy = false
            }
        }
    }

    /// Sends a prompt to an existing session and awaits the daemon's ack. If the daemon no
    /// longer knows the session (e.g. it restarted and forgot its in-memory sessions), the
    /// underlying opencode/claude session still lives server-side, so we transparently
    /// re-attach it and retry once — instead of the chat hanging on "working…" forever.
    private func deliverPrompt(sessionID sid: String, text: String, images: [ImageAttachment], allowReattach: Bool) async {
        do {
            _ = try await request(MessageType.sessionPrompt,
                                  payload: SessionPrompt(sessionID: sid, text: text, images: images.isEmpty ? nil : images))
            armWatchdog()
        } catch {
            let msg = error.localizedDescription.lowercased()
            if allowReattach, msg.contains("no such session") || msg.contains("no session"),
               let revived = await reattachCurrentSync() {
                await deliverPrompt(sessionID: revived, text: text, images: images, allowReattach: false)
                return
            }
            actionError = "Couldn’t send your message.\n\n\(error.localizedDescription)"
            status = "Send failed"
            busy = false
        }
    }

    /// Re-establishes the daemon's managed session for the currently open session, keeping the
    /// on-screen transcript (used mid-send when the daemon forgot the session). Returns the
    /// revived session id, or nil. Replayed history is de-duplicated briefly so the chat
    /// doesn't double up.
    private func reattachCurrentSync() async -> String? {
        guard let s = currentSession else { return nil }
        dedupReplay = true
        Task { try? await Task.sleep(nanoseconds: 5_000_000_000); dedupReplay = false }
        do {
            let env = try await request(MessageType.sessionAttach,
                payload: SessionAttach(provider: s.provider, sessionID: s.id, url: nil, cwd: s.cwd))
            if let revived = try? env.payload(as: Session.self) {
                sessionID = revived.id; currentSession = revived; return revived.id
            }
            return s.id
        } catch {
            return nil
        }
    }

    /// Re-opens the currently active session after a reconnect (the daemon may have restarted
    /// and dropped its in-memory sessions). Clears the local transcript and lets the attach
    /// replay rebuild it. No-op if nothing is open.
    private func reopenCurrentSession() async {
        guard let client, let s = currentSession else { return }
        messages.removeAll()
        busy = false
        pendingApproval = nil
        cancelWatchdog()
        do {
            let env = try Protocol.encode(id: UUID().uuidString, type: MessageType.sessionAttach,
                                          payload: SessionAttach(provider: s.provider, sessionID: s.id, url: nil, cwd: s.cwd))
            try await client.send(env)
        } catch { /* best-effort; the user can resend to trigger a mid-send re-attach */ }
    }

    /// Arms the no-response watchdog: if the agent produces nothing within the window while
    /// we're still "busy", clear the spinner and prompt a retry. Any live event cancels it.
    private func armWatchdog() {
        cancelWatchdog()
        watchdogTask = Task { [weak self] in
            try? await Task.sleep(nanoseconds: 25_000_000_000) // 25s
            guard !Task.isCancelled else { return }
            self?.watchdogFired()
        }
    }

    private func cancelWatchdog() { watchdogTask?.cancel(); watchdogTask = nil }

    private func watchdogFired() {
        guard busy else { return }
        busy = false
        activity = nil
        finalizeStreaming()
        actionError = "No response from the agent. It may have stopped or its backend may be unreachable — send again to retry, or check the agent."
        status = "No response"
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

    /// Renames a managed session (empty clears the label → back to the derived title). Updates
    /// locally at once; the daemon broadcasts the change to every client.
    public func renameSession(_ id: String, to name: String) async {
        guard let client else { return }
        let trimmed = name.trimmingCharacters(in: .whitespacesAndNewlines)
        if let i = sessions.firstIndex(where: { $0.id == id }) { sessions[i].name = trimmed.isEmpty ? nil : trimmed }
        if sessionID == id { currentSession?.name = trimmed.isEmpty ? nil : trimmed }
        do {
            let env = try Protocol.encode(id: UUID().uuidString, type: MessageType.sessionRename,
                                          payload: SessionRename(sessionID: id, name: trimmed))
            try await client.send(env)
        } catch {
            status = "Rename failed: \(error.localizedDescription)"
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
        todos = []
        testOutput = []; testResult = nil; testRunning = false; showTests = false
    }

    /// Starts a fresh session with explicit options (provider, one or more project folders,
    /// and an opt-in git worktree). Options apply when the first message creates the session.
    /// Passing 2+ project IDs makes a multi-root workspace (the daemon runs in their common
    /// ancestor); a single folder uses `projectID` and can be worktree-isolated.
    public func newSession(provider: String, projectID: String? = nil, projectIDs: [String]? = nil, worktree: Bool = false, workspaceName: String? = nil, plan: Bool = false) {
        newSessionProvider = provider
        let multi = (projectIDs?.count ?? 0) > 1
        pendingProjectIDs = multi ? projectIDs : nil
        pendingProjectID = multi ? nil : (projectID ?? projectIDs?.first)
        pendingWorktree = multi ? false : worktree
        pendingWorkspaceName = workspaceName
        newSession()
        pendingPlan = plan
    }

    /// Eagerly creates a session NOW (no prompt) so it appears in the sidebar and opens in the
    /// detail immediately — rather than only stashing options until the first message (which
    /// looked like "nothing happened"). The provider makes an idle session; the first message
    /// then prompts it. 2+ folders → a multi-root workspace (common ancestor, no worktree).
    public func createSession(provider: String, projectIDs: [String]? = nil, worktree: Bool = false, workspaceName: String? = nil, plan: Bool = false, autonomous: Bool = false) async {
        guard client != nil else { return }
        startingProvider = provider
        startingSession = true // skeleton loading overlay + UI lock until the session is ready
        defer { startingSession = false }
        newSession() // clear the conversation; the created session replaces it on success
        self.autonomous = autonomous
        let multi = (projectIDs?.count ?? 0) > 1
        do {
            // Await the reply so a failure (e.g. the provider's backend is unreachable) surfaces to
            // the user instead of silently doing nothing. On success the daemon returns the Session.
            let resp = try await request(MessageType.sessionCreate,
                payload: SessionCreate(provider: provider,
                                       projectID: multi ? nil : projectIDs?.first,
                                       projectIDs: multi ? projectIDs : nil,
                                       prompt: nil,
                                       // Isolation applies to both single-repo (one worktree) and
                                       // multi-repo (a worktree per repo) — the daemon branches on
                                       // it. Don't drop it for the multi-repo case.
                                       worktree: worktree ? true : nil,
                                       workspaceName: workspaceName,
                                       plan: plan ? true : nil,
                                       autonomous: autonomous ? true : nil))
            let s = try resp.payload(as: Session.self)
            sessionID = s.id
            currentSession = s
            refreshLiveActivity()
            await loadSessions() // reflect the new session in the sidebar
            await loadCommands(sessionID: s.id) // populate the "/" palette for this agent
        } catch {
            // Surface prominently: the New Session sheet has already dismissed, so status text alone
            // is invisible while connected — actionError drives an alert on the main surface.
            let reason = error.localizedDescription
            actionError = "Couldn’t start \(provider).\n\n\(reason)\n\nCheck the agent is installed and running, or pick another agent."
            status = "Couldn’t start \(provider)"
            statusDetail = reason
        }
    }

    /// Fetches the agent providers this daemon has registered, so the new-session picker shows the
    /// real set (including generic CLI agents like codex/gemini) instead of a hardcoded default.
    public func listProviders() async {
        guard client != nil else { return }
        if let resp = try? await request(MessageType.providerList, payload: ProviderList()),
           let pl = try? resp.payload(as: ProviderList.self), !pl.providers.isEmpty {
            providers = pl.providers
            if !providers.contains(newSessionProvider) { newSessionProvider = providers.first ?? "opencode" }
        }
    }

    /// Fetches the handoff index (optionally scoped to a working directory). Also arrives
    /// unsolicited via the handoff.list event whenever an agent saves progress.
    public func listHandoffs(cwd: String? = nil) async {
        guard client != nil else { return }
        if let resp = try? await request(MessageType.handoffList, payload: HandoffList(cwd: cwd)),
           let hl = try? resp.payload(as: HandoffList.self) {
            handoffs = hl.handoffs
        }
    }

    /// The indexed handoff for the active session, if the agent has saved one.
    public var activeHandoff: HandoffEntry? {
        guard let sid = sessionID else { return nil }
        return handoffs.first { $0.sessionID == sid }
    }

    /// Delegates a subtask to a scoped sub-agent seeded from the parent's handoff (not its
    /// transcript). On success the child becomes the active session (it arrives via the OK).
    public func delegateSubtask(subtask: String, files: [String]? = nil, autonomous: Bool = false) async {
        guard client != nil, let parent = sessionID else { return }
        let trimmed = subtask.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else { return }
        do {
            let resp = try await request(MessageType.sessionChild,
                                         payload: SessionChild(parentSessionID: parent, subtask: trimmed,
                                                               files: files, autonomous: autonomous))
            let child = try resp.payload(as: Session.self)
            // Only now replace the view — a failed delegate must not lose the parent conversation.
            // The daemon already subscribed this connection to the child, so its transcript replays
            // over the subscription.
            newSession()
            self.autonomous = autonomous
            sessionID = child.id
            currentSession = child
        } catch {
            status = "Delegate failed: \(error.localizedDescription)"
        }
    }

    /// Toggles autonomous heartbeat supervision for the active session. Re-arming (autonomous =
    /// true) also resets the daemon's nudge counter so a previously-exhausted session runs again.
    public func setAutonomy(_ on: Bool) async {
        autonomous = on
        guard let client, let sid = sessionID else { return }
        if let env = try? Protocol.encode(id: UUID().uuidString, type: MessageType.sessionAutonomy,
                                          payload: SessionAutonomy(sessionID: sid, autonomous: on)) {
            try? await client.send(env)
        }
    }

    /// Runs the session's tests/build (daemon auto-detects the command, or pass one). Output
    /// streams into testOutput; the outcome lands in testResult.
    public func runTests(command: String? = nil) async {
        guard let client, let sid = sessionID else { return }
        testOutput = []
        testResult = nil
        testRunning = true
        showTests = true
        if let env = try? Protocol.encode(id: UUID().uuidString, type: MessageType.runTest,
                                          payload: RunTest(sessionID: sid, command: command)) {
            try? await client.send(env)
        }
    }

    /// Interrupts the current agent turn without ending the session — so you can redirect it
    /// with a new prompt (mid-run steering).
    public func interrupt() async {
        guard let client, let sid = sessionID else { return }
        cancelWatchdog()
        busy = false
        activity = nil
        finalizeStreaming()
        status = "Interrupted"
        if let env = try? Protocol.encode(id: UUID().uuidString, type: MessageType.sessionInterrupt,
                                          payload: SessionRef(sessionID: sid)) {
            try? await client.send(env)
        }
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

    /// Begins an OAuth flow for a tracker (linear/jira). The daemon replies with an authorize URL,
    /// which is opened in a browser. Surfaces a clear error (e.g. no OAuth app configured) via
    /// `trackerError` instead of silently doing nothing.
    public func startOAuth(provider: String) async {
        guard client != nil else { return }
        trackerError = nil
        do {
            let resp = try await request(MessageType.integrationOAuth, payload: IntegrationOAuth(provider: provider))
            if let oa = try? resp.payload(as: IntegrationOAuth.self), let u = oa.url, let url = URL(string: u) {
                oauthURL = url
            } else {
                trackerError = "\(provider): the daemon didn't return an authorize URL."
            }
        } catch {
            trackerError = "Couldn't start \(provider) OAuth: \(error.localizedDescription)"
        }
    }

    /// Saves a tracker's OAuth app credentials (client_id/secret) on the daemon, then starts the
    /// OAuth flow — so the user never has to hand-edit integrations.json.
    public func setOAuthApp(provider: String, clientID: String, clientSecret: String) async {
        guard client != nil else { return }
        trackerError = nil
        do {
            let resp = try await request(MessageType.integrationOAuthApp,
                                         payload: IntegrationOAuthApp(provider: provider, clientID: clientID, clientSecret: clientSecret))
            if let st = try? resp.payload(as: IntegrationStatus.self) {
                connectedTrackers = st.connected
                oauthApps = st.oauthApps ?? []
                trackerAuthErrors = st.authErrors ?? []
            }
            await startOAuth(provider: provider)
        } catch {
            trackerError = "Couldn't save the \(provider) OAuth app: \(error.localizedDescription)"
        }
    }

    public func startLinearOAuth() async { await startOAuth(provider: "linear") }

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
        todos = []
        pendingApproval = nil
        busy = false
        lastDiff = nil
        if let env = try? Protocol.encode(id: UUID().uuidString, type: MessageType.sessionSubscribe, payload: SessionRef(sessionID: id)) {
            try? await client.send(env)
        }
        await loadCommands(sessionID: id)
    }

    /// Loads the agent's slash commands (built-in + custom from .claude/commands) for the composer's
    /// "/" palette. Scoped to the session's provider + working directory.
    public func loadCommands(sessionID sid: String) async {
        guard client != nil else { commands = []; return }
        if let resp = try? await request(MessageType.commandList, payload: CommandListReq(sessionID: sid)),
           let cl = try? resp.payload(as: CommandList.self) {
            commands = cl.commands
        } else {
            commands = []
        }
    }

    /// Registers a project folder and returns it (the daemon replies with the created
    /// Project). The returned project is merged into `projects` so callers can select it
    /// immediately — fixing the old fire-and-forget flow where an added folder never appeared.
    @discardableResult
    public func addProject(path: String) async -> Project? {
        guard path.trimmingCharacters(in: .whitespaces).isEmpty == false else { return nil }
        do {
            let env = try await request(MessageType.projectAdd, payload: ProjectAdd(path: path))
            let p = try env.payload(as: Project.self)
            if let i = projects.firstIndex(where: { $0.id == p.id }) { projects[i] = p } else { projects.append(p) }
            return p
        } catch {
            status = "Add project failed: \(error.localizedDescription)"
            return nil
        }
    }

    /// Lists the subdirectories of `path` (nil = the user's home) for the new-session folder picker,
    /// so you can browse INTO a folder and select several sub-folders. Returns nil on failure.
    public func browseFolders(path: String?) async -> ProjectBrowse? {
        guard client != nil else { return nil }
        if let resp = try? await request(MessageType.projectBrowse, payload: ProjectBrowseReq(path: path)) {
            return try? resp.payload(as: ProjectBrowse.self)
        }
        return nil
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

    /// Fetches the per-member diff for a cross-repo workspace session. Populates workspaceDiffs
    /// and also concatenates them into lastDiff so the existing diff review UI renders every repo.
    public func workspaceDiff() async {
        guard client != nil, let sid = sessionID else { return }
        if let resp = try? await request(MessageType.workspaceDiff, payload: WorkspaceDiff(sessionID: sid)),
           let wd = try? resp.payload(as: WorkspaceDiff.self) {
            let members = wd.members ?? []
            workspaceDiffs = members
            lastDiff = members.map(\.diff).filter { !$0.isEmpty }.joined(separator: "\n")
        }
    }

    /// Commits, pushes, and opens a PR for every member repo of a workspace session — the
    /// coordinated multi-PR finish. Populates workspacePRResults (per-repo outcome).
    public func workspacePR(title: String, body: String? = nil) async {
        guard client != nil, let sid = sessionID else { return }
        workspacePRRunning = true
        defer { workspacePRRunning = false }
        do {
            let resp = try await request(MessageType.workspacePR, payload: WorkspacePR(sessionID: sid, title: title, body: body))
            if let pr = try? resp.payload(as: WorkspacePR.self) { workspacePRResults = pr.members ?? [] }
        } catch {
            actionError = "Couldn’t open the workspace PRs.\n\n\(error.localizedDescription)"
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
                                          payload: SessionAttach(provider: d.provider, sessionID: sid, url: d.url, cwd: d.cwd))
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

    /// Lists a directory (nil/empty path → the available roots). With sessionID, the roots are
    /// scoped to that session's workspace folder(s) — a per-session file tree.
    public func fsTree(_ path: String?, sessionID: String? = nil) async throws -> FSTree {
        try await request(MessageType.fsTree, payload: FSTreeReq(path: path, sessionID: sessionID)).payload(as: FSTree.self)
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

    /// Reads a file's raw bytes (an image to show inline in the editor).
    public func fsReadBytes(_ path: String) async throws -> FSBytes {
        try await request(MessageType.fsReadBytes, payload: FSReadBytesReq(path: path)).payload(as: FSBytes.self)
    }

    // MARK: - LSP (editor diagnostics/types/definition)

    /// Opens a document in its language server (diagnostics then arrive via lsp.diagnostics).
    public func lspOpen(_ path: String, content: String) async {
        _ = try? await request(MessageType.lspOpen, payload: LSPDocReq(path: path, content: content))
    }

    /// Notifies the language server the document changed (full-sync).
    public func lspChange(_ path: String, content: String) async {
        _ = try? await request(MessageType.lspChange, payload: LSPDocReq(path: path, content: content))
    }

    /// Closes the document in its language server + clears its diagnostics.
    public func lspClose(_ path: String) async {
        diagnostics[path] = nil
        _ = try? await request(MessageType.lspClose, payload: LSPDocReq(path: path))
    }

    /// Hover (type info / docs) at a 0-based position; empty string if none.
    public func lspHover(_ path: String, line: Int, character: Int) async -> String {
        (try? await request(MessageType.lspHover, payload: LSPPosReq(path: path, line: line, character: character))
            .payload(as: LSPHover.self).contents) ?? ""
    }

    /// Go-to-definition at a 0-based position; nil if none.
    public func lspDefinition(_ path: String, line: Int, character: Int) async -> LSPDefinition? {
        guard let d = try? await request(MessageType.lspDefinition, payload: LSPPosReq(path: path, line: line, character: character))
            .payload(as: LSPDefinition.self), d.found else { return nil }
        return d
    }

    /// Autocomplete suggestions at a 0-based position.
    public func lspComplete(_ path: String, line: Int, character: Int) async -> [LSPCompletionItem] {
        (try? await request(MessageType.lspComplete, payload: LSPPosReq(path: path, line: line, character: character))
            .payload(as: LSPCompletion.self).items) ?? []
    }

    /// Formats the whole document via the language server; returns the formatted text (or nil
    /// if unchanged / no formatter).
    public func lspFormat(_ path: String, content: String) async -> String? {
        guard let r = try? await request(MessageType.lspFormat, payload: LSPFormatReq(path: path, content: content))
            .payload(as: LSPFormatResult.self), r.changed else { return nil }
        return r.text
    }

    /// All references to the symbol at a 0-based position.
    public func lspReferences(_ path: String, line: Int, character: Int) async -> [LSPLocation] {
        (try? await request(MessageType.lspReferences, payload: LSPPosReq(path: path, line: line, character: character))
            .payload(as: LSPLocations.self).locations) ?? []
    }

    /// Document symbols (outline) for a file.
    public func lspSymbols(_ path: String) async -> [LSPSymbol] {
        (try? await request(MessageType.lspSymbols, payload: LSPDocReq(path: path))
            .payload(as: LSPSymbols.self).symbols) ?? []
    }

    /// Renames the symbol at a position across files; returns the paths that were rewritten.
    public func lspRename(_ path: String, line: Int, character: Int, newName: String) async -> [String] {
        (try? await request(MessageType.lspRename, payload: LSPRenameReq(path: path, line: line, character: character, newName: newName))
            .payload(as: LSPRenameResult.self).files) ?? []
    }

    /// Multi-file text search across a session's workspace (or all roots).
    public func fsSearch(_ query: String, sessionID: String? = nil, regex: Bool = false) async -> [FSSearchHit] {
        (try? await request(MessageType.fsSearch, payload: FSSearchReq(query: query, sessionID: sessionID, regex: regex))
            .payload(as: FSSearchResult.self).results) ?? []
    }

    /// Whether a language server is installed for a file (and whether we can install one).
    public func lspServerInfo(_ path: String) async -> LSPServerInfo? {
        try? await request(MessageType.lspServerInfo, payload: LSPDocReq(path: path)).payload(as: LSPServerInfo.self)
    }

    /// Installs the language server for a file (runs a package manager; may take minutes).
    public func lspInstall(_ path: String) async -> LSPInstallResult? {
        try? await request(MessageType.lspInstall, payload: LSPDocReq(path: path)).payload(as: LSPInstallResult.self)
    }

    // MARK: issue detail / edit / comments / images

    /// Fetches a ticket's full body + comments.
    public func issueDetail(_ issue: Issue) async throws -> IssueDetail {
        try await request(MessageType.issueDetail, payload: IssueDetailReq(provider: issue.provider, issueID: issue.id))
            .payload(as: IssueDetail.self)
    }

    /// Fetches a team's workflow states (for the status picker). Empty if no team id.
    public func issueStates(_ issue: Issue) async throws -> [IssueState] {
        guard let team = issue.teamID, !team.isEmpty else { return [] }
        return try await request(MessageType.issueStates, payload: IssueStatesReq(provider: issue.provider, teamID: team))
            .payload(as: IssueStateList.self).states
    }

    /// Applies a partial edit (only the non-nil fields) and returns the refreshed issue.
    /// Updates the board cache in place so the change is reflected without a full reload.
    @discardableResult
    public func updateIssue(_ issue: Issue, title: String? = nil, description: String? = nil,
                            stateID: String? = nil, priority: Int? = nil) async throws -> Issue {
        let updated = try await request(MessageType.issueUpdate,
            payload: IssueUpdate(provider: issue.provider, issueID: issue.id,
                                 title: title, description: description, stateID: stateID, priority: priority))
            .payload(as: Issue.self)
        if let i = issues.firstIndex(where: { $0.id == updated.id }) { issues[i] = updated }
        return updated
    }

    /// Posts a new comment and returns it.
    public func addComment(_ issue: Issue, body: String) async throws -> IssueComment {
        try await request(MessageType.issueComment, payload: IssueCommentAdd(provider: issue.provider, issueID: issue.id, body: body))
            .payload(as: IssueComment.self)
    }

    /// Edits an existing comment.
    public func editComment(provider: String, commentID: String, body: String) async throws {
        _ = try await request(MessageType.issueCommentEdit, payload: IssueCommentEdit(provider: provider, commentID: commentID, body: body))
    }

    /// Fetches an auth-gated tracker image through the daemon, cached by URL.
    public func issueImage(provider: String, url: String) async throws -> Data {
        if let cached = imageCache[url] { return cached }
        let img = try await request(MessageType.issueImage, payload: IssueImageReq(provider: provider, url: url))
            .payload(as: IssueImage.self)
        guard let data = Data(base64Encoded: img.data) else {
            throw NSError(domain: "Oculus", code: -4, userInfo: [NSLocalizedDescriptionKey: "bad image data"])
        }
        imageCache[url] = data
        return data
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
            // Restore the pending approval so the agent isn't left hanging on a decision the daemon
            // never received, and tell the user instead of silently dropping it.
            pendingApproval = ap
            refreshLiveActivity()
            actionError = "Couldn’t send your \(decision == Decision.deny ? "denial" : "approval").\n\n\(error.localizedDescription)"
            status = "Respond failed"
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
                        oauthApps = st.oauthApps ?? []
                        trackerAuthErrors = st.authErrors ?? []
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
                case MessageType.error:
                    // An uncorrelated error (a fire-and-forget send the daemon rejected, e.g. a
                    // session that couldn't start) — surface it instead of silently doing nothing.
                    if let e = try? env.payload(as: [String: String].self), let m = e["message"] {
                        status = "Error"
                        statusDetail = m
                        actionError = m
                        busy = false
                    }
                case MessageType.sessionMessage:
                    if let m = try? env.payload(as: SessionMessage.self) {
                        cancelWatchdog()
                        let role: ChatMessage.Role = m.role == "user" ? .user : (m.role == "tool" ? .tool : .assistant)
                        let trimmed = m.text.trimmingCharacters(in: .whitespacesAndNewlines)
                        // Skip the echo of our own just-sent user turn (appended locally for instant feedback).
                        if role == .user, let last = messages.last, last.role == .user,
                           last.text.trimmingCharacters(in: .whitespacesAndNewlines) == trimmed {
                            break
                        }
                        // Just after a live re-attach, the provider replays history; skip messages
                        // that duplicate ones already on screen so the transcript doesn't double.
                        if dedupReplay, messages.contains(where: { $0.role == role && $0.text.trimmingCharacters(in: .whitespacesAndNewlines) == trimmed }) {
                            break
                        }
                        finalizeStreaming()
                        messages.append(ChatMessage(role: role, text: m.text))
                    }
                case MessageType.thinking:
                    if let t = try? env.payload(as: Thinking.self) {
                        cancelWatchdog()
                        appendThinkingDelta(t.text)
                        busy = true
                    }
                case MessageType.outputDelta:
                    if let d = try? env.payload(as: OutputDelta.self) {
                        cancelWatchdog()
                        appendAssistantDelta(d.text)
                    }
                case MessageType.sessionStatus:
                    if let ss = try? env.payload(as: SessionStatus.self) {
                        cancelWatchdog()
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
                        cancelWatchdog()
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
                        oauthApps = st.oauthApps ?? []
                        trackerAuthErrors = st.authErrors ?? []
                    }
                case MessageType.lspDiagnostics: // language server published diagnostics for a file
                    if let d = try? env.payload(as: LSPDiagnostics.self) {
                        diagnostics[d.path] = d.diagnostics
                    }
                case MessageType.sessionUsage: // per-turn token/cost usage; accumulate onto the session
                    if let u = try? env.payload(as: SessionUsage.self), u.sessionID == sessionID {
                        currentSession?.inputTokens = (currentSession?.inputTokens ?? 0) + u.inputTokens
                        currentSession?.outputTokens = (currentSession?.outputTokens ?? 0) + u.outputTokens
                        currentSession?.costUSD = (currentSession?.costUSD ?? 0) + u.costUSD
                    }
                case MessageType.sessionTodos: // the agent's live to-do list (replaces the prior list)
                    if let t = try? env.payload(as: SessionTodos.self), t.sessionID == sessionID {
                        todos = t.todos
                    }
                case MessageType.sessionHeartbeat: // supervision "on-track" state for a session
                    if let hb = try? env.payload(as: SessionHeartbeat.self) {
                        heartbeats[hb.sessionID] = hb
                        // The daemon disarms autonomy when a session exhausts its budget; mirror that
                        // so the toggle reflects reality.
                        if hb.sessionID == sessionID, hb.state == "exhausted" { autonomous = false }
                    }
                case MessageType.handoffList: // the handoff index changed (agent saved progress)
                    if let hl = try? env.payload(as: HandoffList.self) {
                        handoffs = hl.handoffs
                    }
                case MessageType.runOutput: // streamed line from a test/build run
                    if let o = try? env.payload(as: RunOutput.self), o.sessionID == sessionID {
                        testOutput.append(o.line)
                        if testOutput.count > 2000 { testOutput.removeFirst(testOutput.count - 2000) }
                    }
                case MessageType.runResult: // test/build run finished
                    if let r = try? env.payload(as: RunResult.self), r.sessionID == sessionID {
                        testResult = r
                        testRunning = false
                    }
                default:
                    break
                }
            } catch {
                connected = false
                status = "Disconnected"
                busy = false
                cancelWatchdog()
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
    /// Ends any Live Activities left over from a previous launch (e.g. the app was killed while an
    /// activity was showing), so none linger in the Dynamic Island. A new one is created on demand
    /// when an agent is actually working.
    func clearStaleLiveActivities() {
        #if os(iOS)
        if #available(iOS 16.1, *) {
            for act in Activity<OculusActivityAttributes>.activities {
                Task { await act.end(dismissalPolicy: .immediate) }
            }
            liveActivity = nil
        }
        #endif
    }

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
            // The activity is meaningful only while the agent is WORKING or an approval is pending.
            // When idle/done (or no session) end it so it doesn't linger in the Dynamic Island.
            guard ActivityAuthorizationInfo().areActivitiesEnabled, let sid = sessionID,
                  busy || pendingApproval != nil else {
                if let act = liveActivity as? Activity<OculusActivityAttributes> {
                    Task { await act.end(dismissalPolicy: .immediate) }
                }
                liveActivity = nil
                return
            }
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
