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
/// One step in the session-create checklist (see Model.createSteps). Identified by stage so
/// repeated events for the same phase (e.g. worktree 1/3 → 2/3) update one row instead of stacking.
public struct CreateStep: Identifiable, Equatable {
    public let stage: String
    public var detail: String
    public var step: Int
    public var total: Int
    public var done: Bool
    public var id: String { stage }
}

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
    @Published public var connecting = false // a connect/handshake attempt is in flight (not an error)
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
    /// Live, ordered steps streamed by the daemon while a session is being created (worktree per
    /// repo, provider spin-up), so the loading screen is a prescriptive checklist rather than a
    /// generic skeleton. Reset each create; the last step completes on "ready".
    @Published public var createSteps: [CreateStep] = []
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
    // Sub-agent (child session) inline transcripts. When a child card is expanded, we subscribe to
    // its session and stream its tool calls + outputs into its own buffer — without leaving the parent.
    @Published public var childMessages: [String: [ChatMessage]] = [:] // child session id → its messages
    @Published public var childActivity: [String: String] = [:]        // child id → current tool activity
    @Published public var expandedChildIDs: Set<String> = []           // which child cards are open
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
    @Published public var pendingFiles: [FileAttachment] = []    // document attachments (text extracted)

    // Trackers (Linear/Jira).
    @Published public var issues: [Issue] = []
    /// Saved issue views (named filter presets) + tickets the user has hidden. Persisted locally.
    @Published public var savedIssueFilters: [SavedIssueFilter] = []
    @Published public var hiddenIssueIDs: Set<String> = []
    @Published public var connectedTrackers: [String] = []
    // Trackers that have an OAuth app configured (client_id present) — drives whether the connect
    // screen shows the OAuth button or asks for the OAuth app credentials.
    @Published public var oauthApps: [String] = []
    /// Connected trackers whose fetch/refresh is failing — drives a "reconnect" pill.
    @Published public var trackerAuthErrors: [String] = []
    /// Provider -> the ACTUAL failure message (e.g. "jira: 401 Unauthorized"), so the UI can show
    /// WHY a connected tracker isn't loading, not just that it isn't.
    @Published public var trackerAuthDetails: [String: String] = [:]
    /// Whether the daemon is shipping anonymized diagnostics (on by default; toggled in Settings).
    @Published public var telemetryEnabled: Bool = true
    /// Atlassian sites the Jira token can reach + the active one — for orgs with more than one Jira
    /// site (picking the wrong one is the classic "connected but no tickets").
    @Published public var jiraSites: [JiraSite] = []
    @Published public var jiraCurrentSite: String = ""
    /// Recurring autonomous workflows ("loops") + their run history.
    @Published public var loops: [Loop] = []
    @Published public var loopRuns: [LoopRun] = []
    // Last tracker-connect/OAuth error, surfaced on the connect screen.
    @Published public var trackerError: String?
    @Published public var oauthURL: URL? // set when an OAuth flow returns an authorize URL to open

    // Kanban board (real workflow-status columns + drag/drop).
    /// Selectable boards/projects (from `issue.projects`).
    @Published public var issueProjects: [IssueProject] = []
    /// The board currently shown; persisted globally so the app reopens on the same board.
    @Published public var selectedProjectID: String?
    /// The selected board's ordered workflow-status columns (from `issue.columns`).
    @Published public var boardColumns: [IssueState] = []
    /// Per-project column ordering (ids), overriding the daemon's default position order.
    @Published public var boardColumnOrder: [String: [String]] = [:]
    /// Per-project hidden column ids.
    @Published public var hiddenBoardColumns: [String: Set<String>] = [:]
    /// Options applied to the NEXT session created (by the first send). Set via newSession(...).
    @Published public var newSessionProvider = "opencode"
    // Agent providers this daemon actually has (opencode/claude-code/pi + any generic CLI agents),
    // fetched on connect so the picker reflects reality instead of a hardcoded list. Starts empty
    // with providersLoaded=false so pickers can show a loader rather than a fake default.
    @Published public var providers: [String] = []
    @Published public var providersLoaded = false
    // Full agent roster (native + detected + custom) for the "Manage agents" screen.
    @Published public var agents: [AgentInfo] = []
    // Models available to the CURRENT session's provider (for the chat-header model picker).
    @Published public var sessionModels: [ModelInfo] = []
    @Published public var modelEditable = false
    @Published public var currentModel: String?
    // Live daemon log (Developer bottom panel). Populated on subscribe (replay + streamed lines).
    @Published public var daemonLog: [String] = []
    @Published public var showLogPanel = false
    private var logSubscribed = false
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
    /// Child sessions we've already sent a sessionSubscribe for — so expanding a card twice (or
    /// collapse+re-expand) doesn't re-subscribe. Kept across collapse; cleared on parent switch.
    private var subscribedChildIDs: Set<String> = []
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
        selectedProjectID = defaults.string(forKey: BoardKeys.project)
        if let p = selectedProjectID { loadBoardPrefs(for: p) }
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
        connecting = true
        status = "Connecting…"
        defer { connecting = false }
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
        await loadIssueProjects() // board picker options
        await loadBoardColumns()  // real workflow-status columns for the selected board
        await listProviders() // reflect the daemon's real agent set in the picker
        await listHandoffs()  // seed the handoff index (live updates arrive via handoff.list)
        await loadLoops()     // recurring autonomous workflows
        await loadTelemetryStatus() // reflect the daemon's diagnostics toggle
        // If a session was open when the socket dropped (e.g. the daemon restarted and forgot its
        // in-memory sessions), re-attach it so its transcript + prompts resume.
        await reopenCurrentSession()
        // Fresh launch: nothing open in memory, but reopen the session we last had open on this
        // desktop so you land back where you left off. Best-effort — no-ops if it no longer exists.
        if currentSession == nil, let last = UserDefaults.standard.string(forKey: lastSessionKey), !last.isEmpty {
            await openSession(last)
        }
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
        var trimmed = text.trimmingCharacters(in: .whitespacesAndNewlines)
        let imgs = pendingImages
        let files = pendingFiles
        guard (!trimmed.isEmpty || !imgs.isEmpty || !files.isEmpty), let client else { return }
        // Document attachments ride along as fenced text blocks so every provider sees their content.
        let shownBody = trimmed
        if !files.isEmpty {
            let blocks = files.map { "Attached file: \($0.name)\n```\n\($0.text)\n```" }.joined(separator: "\n\n")
            trimmed = trimmed.isEmpty ? blocks : blocks + "\n\n" + trimmed
        }
        var badges: [String] = []
        if !imgs.isEmpty { badges.append("🖼️ \(imgs.count) image\(imgs.count == 1 ? "" : "s")") }
        if !files.isEmpty { badges.append("📎 \(files.count) file\(files.count == 1 ? "" : "s")") }
        let shown = shownBody.isEmpty ? badges.joined(separator: "  ") : shownBody
        messages.append(ChatMessage(role: .user, text: shown))
        busy = true
        pendingImages = []
        pendingFiles = []
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
    private func armWatchdog(seconds: Double = 25) {
        cancelWatchdog()
        watchdogTask = Task { [weak self] in
            try? await Task.sleep(nanoseconds: UInt64(seconds * 1_000_000_000))
            guard !Task.isCancelled else { return }
            self?.watchdogFired()
        }
    }

    /// Reset the no-response watchdog on live activity. Progress events used to just CANCEL it, so a
    /// turn that started then hung mid-way (a stuck opencode/claude turn) left the app "thinking"
    /// forever with nothing to catch it. Re-arming on each event with a generous window means a real
    /// mid-turn stall (no events for ~2 min) surfaces "no response — send again" instead of hanging.
    private func bumpWatchdog() {
        guard busy else { cancelWatchdog(); return }
        armWatchdog(seconds: 120)
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

    /// Attaches a document (its extracted text) to send with the next prompt.
    public func attachFile(name: String, text: String) {
        let trimmed = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else { return }
        pendingFiles.append(FileAttachment(name: name, text: trimmed))
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
    /// Fetches the models a provider offers (for the New Session picker), without a live session.
    public func providerModels(_ provider: String) async -> (models: [ModelInfo], editable: Bool) {
        guard client != nil else { return ([], false) }
        if let resp = try? await request(MessageType.modelList, payload: ModelListReq(provider: provider)),
           let ml = try? resp.payload(as: ModelList.self) {
            return (ml.models, ml.editable)
        }
        return ([], false)
    }

    /// Folds a streamed create step into `createSteps`: repeated events for the same stage update
    /// that row (e.g. worktree 1/3 → 2/3); a new stage marks the prior steps done and appends the
    /// current one. "ready" completes them all.
    private func applyCreateStep(_ p: SessionProgress) {
        if p.stage == "ready" {
            for i in createSteps.indices { createSteps[i].done = true }
            return
        }
        if let i = createSteps.firstIndex(where: { $0.stage == p.stage }) {
            createSteps[i].detail = p.detail
            createSteps[i].step = p.step ?? 0
            createSteps[i].total = p.total ?? 0
        } else {
            for i in createSteps.indices { createSteps[i].done = true } // prior phases finished
            createSteps.append(CreateStep(stage: p.stage, detail: p.detail, step: p.step ?? 0, total: p.total ?? 0, done: false))
        }
    }

    public func createSession(provider: String, projectIDs: [String]? = nil, worktree: Bool = false, workspaceName: String? = nil, plan: Bool = false, autonomous: Bool = false, model: String? = nil, modelProvider: String? = nil) async {
        guard client != nil else { return }
        startingProvider = provider
        createSteps = [] // fresh checklist; the daemon streams session.progress as it works
        startingSession = true // loading overlay + UI lock until the session is ready
        defer { startingSession = false; createSteps = [] }
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
                                       autonomous: autonomous ? true : nil,
                                       model: model,
                                       modelProvider: modelProvider))
            let s = try resp.payload(as: Session.self)
            sessionID = s.id
            currentSession = s
            // Optimistically place it in the sidebar NOW so the empty-state → split transition
            // happens synchronously with currentSession set. Otherwise the fire-and-forget
            // session.list broadcast can land after `startingSession` clears, leaving the empty
            // CTA (or a split whose detail races currentSession) until the user reopens it.
            if !sessions.contains(where: { $0.id == s.id }) { sessions.insert(s, at: 0) }
            refreshLiveActivity()
            // Subscribe so the fresh session streams events immediately (not only after the first send).
            if let env = try? Protocol.encode(id: UUID().uuidString, type: MessageType.sessionSubscribe, payload: SessionRef(sessionID: s.id)) {
                try? await client?.send(env)
            }
            await loadSessions() // reconcile with the daemon's authoritative list
            await loadCommands(sessionID: s.id) // populate the "/" palette for this agent
            await loadModels(sessionID: s.id)   // populate the header model picker
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
        defer { providersLoaded = true }
        if let resp = try? await request(MessageType.providerList, payload: ProviderList()),
           let pl = try? resp.payload(as: ProviderList.self) {
            applyProviders(pl.providers)
        }
    }

    /// Adopt a provider set (from a request reply or an unsolicited provider.list broadcast) and keep
    /// the default selection valid.
    func applyProviders(_ list: [String]) {
        providers = list
        if let first = list.first, !list.contains(newSessionProvider) { newSessionProvider = first }
    }

    /// Full agent roster for the management UI.
    public func loadAgents() async {
        guard client != nil else { return }
        if let resp = try? await request(MessageType.agentList, payload: Optional<Int>.none),
           let al = try? resp.payload(as: AgentList.self) {
            agents = al.agents
        }
    }

    /// Add or edit a custom CLI agent; the daemon persists it, registers it live, and returns the
    /// updated roster. Returns an error message on failure (nil on success).
    @discardableResult
    public func upsertAgent(_ a: AgentUpsert) async -> String? {
        guard client != nil else { return "Not connected" }
        do {
            let resp = try await request(MessageType.agentUpsert, payload: a)
            if let al = try? resp.payload(as: AgentList.self) { agents = al.agents }
            await listProviders()
            return nil
        } catch {
            return error.localizedDescription
        }
    }

    /// Loads the models available to the current session's provider (for the header picker).
    /// Clears to non-editable if the provider is agent-managed (e.g. pi, generic CLI).
    public func loadModels(sessionID override: String? = nil) async {
        guard client != nil, let sid = override ?? sessionID else { sessionModels = []; modelEditable = false; return }
        if let resp = try? await request(MessageType.modelList, payload: ModelListReq(sessionID: sid)),
           let ml = try? resp.payload(as: ModelList.self) {
            sessionModels = ml.models
            modelEditable = ml.editable
            currentModel = ml.current?.isEmpty == false ? ml.current : currentSession?.model
        } else {
            sessionModels = []; modelEditable = false
        }
    }

    /// Switches the current session's model.
    @discardableResult
    public func setSessionModel(_ m: ModelInfo) async -> String? {
        guard client != nil, let sid = sessionID else { return "No session" }
        do {
            _ = try await request(MessageType.sessionSetModel, payload: SessionSetModel(sessionID: sid, model: m.id, provider: m.provider))
            currentModel = m.id
            return nil
        } catch {
            return error.localizedDescription
        }
    }

    /// Show or hide an agent in the session pickers (any kind). The daemon persists it and pushes
    /// the new visible set to every client.
    @discardableResult
    public func setAgentVisible(_ name: String, _ visible: Bool) async -> String? {
        guard client != nil else { return "Not connected" }
        do {
            let resp = try await request(MessageType.agentVisible, payload: AgentVisible(name: name, visible: visible))
            if let al = try? resp.payload(as: AgentList.self) { agents = al.agents }
            await listProviders()
            return nil
        } catch {
            return error.localizedDescription
        }
    }

    /// Remove a custom CLI agent.
    @discardableResult
    public func deleteAgent(_ name: String) async -> String? {
        guard client != nil else { return "Not connected" }
        do {
            let resp = try await request(MessageType.agentDelete, payload: AgentRef(name: name))
            if let al = try? resp.payload(as: AgentList.self) { agents = al.agents }
            await listProviders()
            return nil
        } catch {
            return error.localizedDescription
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

    /// Removes a tracker's connection (clears its token on the daemon, keeping the OAuth app so
    /// reconnecting is one tap) and refreshes the board. Also used to clear a broken connection.
    public func disconnectTracker(_ provider: String) async {
        guard client != nil else { return }
        trackerError = nil
        do {
            let resp = try await request(MessageType.integrationDisconnect,
                                         payload: IntegrationConnect(provider: provider, token: ""))
            if let st = try? resp.payload(as: IntegrationStatus.self) { applyIntegrationStatus(st) }
            await loadIssues()
        } catch {
            trackerError = "Couldn’t disconnect \(provider): \(error.localizedDescription)"
        }
    }

    /// Folds an IntegrationStatus into the published tracker state (one place so every path — the
    /// broadcast, connect, disconnect, oauth-app — stays consistent).
    func applyIntegrationStatus(_ st: IntegrationStatus) {
        connectedTrackers = st.connected
        oauthApps = st.oauthApps ?? []
        trackerAuthErrors = st.authErrors ?? []
        trackerAuthDetails = st.authErrorDetails ?? [:]
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
            if let st = try? resp.payload(as: IntegrationStatus.self) { applyIntegrationStatus(st) }
            await startOAuth(provider: provider)
        } catch {
            trackerError = "Couldn't save the \(provider) OAuth app: \(error.localizedDescription)"
        }
    }

    public func startLinearOAuth() async { await startOAuth(provider: "linear") }

    /// Lists the Atlassian sites the Jira token can access (+ the active one). Empty on non-OAuth or
    /// single-site setups (the picker only matters with >1 site).
    public func loadJiraSites() async {
        guard client != nil, connectedTrackers.contains("jira") else { jiraSites = []; return }
        if let resp = try? await request(MessageType.jiraSites, payload: Optional<Int>.none),
           let js = try? resp.payload(as: JiraSites.self) {
            jiraSites = js.sites
            jiraCurrentSite = js.current ?? ""
        }
    }

    /// Switches the active Jira site (cloud id) — no re-auth; the token spans all the org's sites.
    public func setJiraSite(_ cloudID: String) async {
        guard client != nil else { return }
        trackerError = nil
        jiraCurrentSite = cloudID // optimistic
        if let resp = try? await request(MessageType.jiraSetSite, payload: JiraSetSite(cloudID: cloudID)),
           let st = try? resp.payload(as: IntegrationStatus.self) {
            applyIntegrationStatus(st)
        }
        await loadIssues()
    }

    /// Reads whether the daemon is sending anonymized diagnostics (drives the Settings toggle).
    public func loadTelemetryStatus() async {
        guard client != nil else { return }
        if let resp = try? await request(MessageType.telemetryStatus, payload: Optional<Int>.none),
           let t = try? resp.payload(as: Telemetry.self) {
            telemetryEnabled = t.enabled
        }
    }

    /// Turns anonymized diagnostics on/off on the daemon (persisted there).
    public func setTelemetry(_ on: Bool) async {
        guard client != nil else { return }
        telemetryEnabled = on // optimistic
        if let resp = try? await request(MessageType.telemetrySet, payload: Telemetry(enabled: on)),
           let t = try? resp.payload(as: Telemetry.self) {
            telemetryEnabled = t.enabled
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

    // MARK: Kanban board — projects / columns / move / create

    private enum BoardKeys {
        static let project = "oculus.board.project"
        static func order(_ p: String) -> String { "oculus.board.order.\(p)" }
        static func hidden(_ p: String) -> String { "oculus.board.hidden.\(p)" }
    }

    /// Reads a project's saved column order + hidden set from defaults into the published caches.
    private func loadBoardPrefs(for project: String) {
        if boardColumnOrder[project] == nil {
            let s = defaults.string(forKey: BoardKeys.order(project)) ?? ""
            boardColumnOrder[project] = s.isEmpty ? [] : s.components(separatedBy: ",")
        }
        if hiddenBoardColumns[project] == nil {
            let s = defaults.string(forKey: BoardKeys.hidden(project)) ?? ""
            hiddenBoardColumns[project] = Set(s.isEmpty ? [] : s.components(separatedBy: ","))
        }
    }

    /// The provider (linear/jira) that owns a given project id.
    private func providerForProject(_ id: String) -> String? {
        issueProjects.first(where: { $0.id == id })?.provider
    }

    /// Switches the active board, persisting the choice and hydrating its column prefs.
    public func selectProject(_ id: String?) {
        selectedProjectID = id
        if let id {
            defaults.set(id, forKey: BoardKeys.project)
            loadBoardPrefs(for: id)
        } else {
            defaults.removeObject(forKey: BoardKeys.project)
        }
    }

    /// All columns for the current board, applying the saved order (unordered ids fall back to position).
    public func orderedColumns() -> [IssueState] {
        guard let p = selectedProjectID, let order = boardColumnOrder[p], !order.isEmpty else {
            return boardColumns.sorted { $0.position < $1.position }
        }
        return boardColumns.sorted { a, b in
            let ia = order.firstIndex(of: a.id) ?? Int.max
            let ib = order.firstIndex(of: b.id) ?? Int.max
            if ia != ib { return ia < ib }
            return a.position < b.position
        }
    }

    /// Visible columns for the current board (ordered, minus hidden).
    public func visibleColumns() -> [IssueState] {
        guard let p = selectedProjectID else { return orderedColumns() }
        let hidden = hiddenBoardColumns[p] ?? []
        return orderedColumns().filter { !hidden.contains($0.id) }
    }

    /// Columns the user has hidden on the current board (for a "reveal" menu).
    public func hiddenColumns() -> [IssueState] {
        guard let p = selectedProjectID, let hidden = hiddenBoardColumns[p], !hidden.isEmpty else { return [] }
        return orderedColumns().filter { hidden.contains($0.id) }
    }

    public func hideBoardColumn(_ id: String) {
        guard let p = selectedProjectID else { return }
        var s = hiddenBoardColumns[p] ?? []
        s.insert(id)
        hiddenBoardColumns[p] = s
        defaults.set(s.sorted().joined(separator: ","), forKey: BoardKeys.hidden(p))
    }

    public func showBoardColumn(_ id: String) {
        guard let p = selectedProjectID else { return }
        var s = hiddenBoardColumns[p] ?? []
        s.remove(id)
        hiddenBoardColumns[p] = s
        defaults.set(s.sorted().joined(separator: ","), forKey: BoardKeys.hidden(p))
    }

    /// Moves a column one slot left/right within the visible order, persisting the new order.
    public func moveBoardColumn(_ id: String, left: Bool) {
        guard let p = selectedProjectID else { return }
        var ids = orderedColumns().map(\.id)
        guard let idx = ids.firstIndex(of: id) else { return }
        let target = left ? idx - 1 : idx + 1
        guard ids.indices.contains(target) else { return }
        ids.swapAt(idx, target)
        boardColumnOrder[p] = ids
        defaults.set(ids.joined(separator: ","), forKey: BoardKeys.order(p))
    }

    /// Loads the selectable boards; defaults the selection to the first if none is set.
    public func loadIssueProjects() async {
        guard client != nil else { return }
        if let resp = try? await request(MessageType.issueProjects, payload: Optional<Int>.none),
           let list = try? resp.payload(as: IssueProjectsList.self) {
            issueProjects = list.projects
            if selectedProjectID == nil || !list.projects.contains(where: { $0.id == selectedProjectID }) {
                selectProject(list.projects.first?.id)
            } else if let p = selectedProjectID {
                loadBoardPrefs(for: p)
            }
        }
    }

    /// Loads the current board's workflow-status columns (needs a selected project + known provider).
    public func loadBoardColumns() async {
        guard client != nil, let p = selectedProjectID, let provider = providerForProject(p) else {
            boardColumns = []
            return
        }
        if let resp = try? await request(MessageType.issueColumns, payload: IssueColumnsReq(provider: provider, project: p)),
           let list = try? resp.payload(as: IssueStateList.self) {
            boardColumns = list.states.sorted { $0.position < $1.position }
        }
    }

    /// Moves a card to a workflow status. Optimistic: the card jumps to the target column immediately,
    /// is replaced with the daemon's returned issue on success, and reverts (with an error) on failure.
    public func moveIssue(_ id: String, toStatus statusID: String) async {
        guard let idx = issues.firstIndex(where: { $0.id == id }) else { return }
        let original = issues[idx]
        if let col = boardColumns.first(where: { $0.id == statusID }) {
            issues[idx].status = col.name
            issues[idx].category = col.category
        }
        do {
            let updated = try await request(MessageType.issueMove,
                payload: IssueMove(provider: original.provider, issueID: id, statusID: statusID))
                .payload(as: Issue.self)
            if let i = issues.firstIndex(where: { $0.id == updated.id }) { issues[i] = updated }
        } catch {
            if let i = issues.firstIndex(where: { $0.id == id }) { issues[i] = original }
            trackerError = "Couldn’t move ticket: \(error.localizedDescription)"
        }
    }

    /// Creates a ticket on a board and refreshes the list on success.
    public func createIssue(project: String, title: String, description: String? = nil,
                            priority: Int? = nil, type: String? = nil) async {
        guard client != nil, let provider = providerForProject(project) else { return }
        trackerError = nil
        do {
            _ = try await request(MessageType.issueCreate,
                payload: IssueCreate(provider: provider, project: project, title: title,
                                     description: description, priority: priority, type: type))
                .payload(as: Issue.self)
            await loadIssues()
        } catch {
            trackerError = "Couldn’t create ticket: \(error.localizedDescription)"
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
        // Already the open session → no-op. Re-running the full clear+resubscribe (e.g. when you
        // click the active row again) briefly wiped messages/currentSession and blanked the detail.
        if id == currentSession?.id, !messages.isEmpty { return }
        // A stopped session (daemon restarted, provider couldn't re-attach) has nothing to subscribe
        // to — load it and let ChatView show a Restart affordance, instead of erroring on subscribe.
        if let s = sessions.first(where: { $0.id == id }), s.status == SessionStatusValue.stopped {
            sessionID = id
            currentSession = s
            messages.removeAll()
            todos = []; pendingApproval = nil; busy = false; lastDiff = nil
            clearChildState()
            UserDefaults.standard.set(id, forKey: lastSessionKey)
            return
        }
        // Switch the active session id NOW, before clearing/subscribing — so streaming events from
        // the session we're leaving (still Live) are filtered out instead of bleeding into this one.
        sessionID = id
        messages.removeAll()
        todos = []
        pendingApproval = nil
        busy = false
        lastDiff = nil
        clearChildState() // a new parent session starts with no expanded/subscribed children
        UserDefaults.standard.set(id, forKey: lastSessionKey) // remember for auto-reopen next launch
        if let env = try? Protocol.encode(id: UUID().uuidString, type: MessageType.sessionSubscribe, payload: SessionRef(sessionID: id)) {
            try? await client.send(env)
        }
        await loadCommands(sessionID: id)
        await loadModels(sessionID: id)
    }

    /// Expands/collapses a sub-agent's inline transcript. On first expand, subscribes to the child
    /// session so its tool calls + outputs stream live into `childMessages[id]` — without leaving the
    /// parent. The subscription + buffer are kept on collapse (cheap; avoids re-replay churn); collapse
    /// just hides the body.
    public func toggleChildExpanded(_ id: String) {
        if expandedChildIDs.contains(id) {
            expandedChildIDs.remove(id)
            return
        }
        expandedChildIDs.insert(id)
        if childMessages[id] == nil { childMessages[id] = [] }
        guard !subscribedChildIDs.contains(id) else { return }
        subscribedChildIDs.insert(id)
        if let client, let env = try? Protocol.encode(id: UUID().uuidString, type: MessageType.sessionSubscribe, payload: SessionRef(sessionID: id)) {
            Task { try? await client.send(env) }
        }
    }

    /// Re-creates a stopped session as a FRESH conversation in the same folder/agent/model and opens
    /// it. The provider conversation itself can't be resumed (that's why it stopped — a CLI agent has
    /// no server-side session, or the old one is gone), so history isn't restored; the context is.
    public func restartSession(_ id: String) async {
        guard sessions.contains(where: { $0.id == id }) else { return }
        busy = true
        status = "Restarting…"
        do {
            let env = try await request(MessageType.sessionRestart, payload: SessionRef(sessionID: id))
            let revived = try env.payload(as: Session.self)
            busy = false
            if let idx = sessions.firstIndex(where: { $0.id == id }) { sessions[idx] = revived }
            else { sessions.append(revived) }
            await openSession(revived.id) // its id changed (new live session)
        } catch {
            busy = false
            status = "Restart failed"
            let msg = error.localizedDescription
            // If the daemon no longer has this session (its record/worktree is gone), it can never be
            // restarted — drop the dead card locally and re-sync the list so it stops nagging. The
            // user should start a fresh session instead; say so.
            if msg.lowercased().contains("no such session") || msg.lowercased().contains("agent")  {
                sessions.removeAll { $0.id == id }
                if sessionID == id { newSession() }
                await loadSessions() // converge with the daemon's real state
                actionError = "That session can’t be restarted — its workspace is gone. Start a fresh session instead."
            } else {
                actionError = "Couldn’t restart the session.\n\n\(msg)"
            }
        }
    }

    /// Recovers a "broken" session — one that opens but whose sends silently fail because the daemon
    /// bound it to the wrong directory partition. Re-attaches on the daemon, which re-resolves the
    /// session's real directory from the provider and heals the stored cwd, KEEPING all history (the
    /// id is unchanged). Use this instead of Restart when you don't want to lose the conversation.
    public func recoverSession(_ id: String) async {
        busy = true
        status = "Recovering…"
        do {
            let env = try await request(MessageType.sessionRecover, payload: SessionRef(sessionID: id))
            let healed = try env.payload(as: Session.self)
            busy = false
            status = "Recovered"
            if let idx = sessions.firstIndex(where: { $0.id == id }) { sessions[idx] = healed }
            else { sessions.append(healed) }
            await openSession(healed.id) // re-subscribe to the freshly re-attached session
        } catch {
            busy = false
            status = "Recover failed"
            let msg = error.localizedDescription
            if msg.lowercased().contains("use restart") {
                actionError = "This agent can’t re-attach an existing conversation. Use Restart to start fresh in the same folder."
            } else {
                actionError = "Couldn’t recover the session.\n\n\(msg)"
            }
        }
    }

    private var lastSessionKey: String { "oculus.lastSession.\(id)" }

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

    // MARK: loops (recurring autonomous workflows)

    public func loadLoops() async {
        guard client != nil else { return }
        if let resp = try? await request(MessageType.loopList, payload: Optional<Int>.none),
           let ll = try? resp.payload(as: LoopList.self) { loops = ll.loops; loopRuns = ll.runs }
    }
    public func upsertLoop(_ l: Loop) async {
        guard client != nil else { return }
        _ = try? await request(MessageType.loopUpsert, payload: l)
        await loadLoops()
    }
    public func deleteLoop(_ id: String) async {
        guard client != nil else { return }
        if let resp = try? await request(MessageType.loopDelete, payload: LoopRef(id: id)),
           let ll = try? resp.payload(as: LoopList.self) { loops = ll.loops; loopRuns = ll.runs }
    }
    public func setLoopEnabled(_ id: String, _ on: Bool) async {
        guard client != nil else { return }
        if let resp = try? await request(MessageType.loopSetEnabled, payload: LoopSetEnabled(id: id, enabled: on)),
           let ll = try? resp.payload(as: LoopList.self) { loops = ll.loops; loopRuns = ll.runs }
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

    /// Whether a catch-up is in flight, and its last outcome (message + any conflicted files) — for
    /// the WorktreePanel to show.
    @Published public var catchingUp = false
    @Published public var catchUpMessage: String?
    @Published public var catchUpConflicts: [String] = []

    /// Merges the repo's default branch INTO this session's branch ("catch up to main"). Correlated
    /// so the outcome (updated / already-current / conflicts to resolve) surfaces in the panel.
    public func catchUpToMain() async {
        guard client != nil, let sid = sessionID else { return }
        catchingUp = true
        catchUpMessage = nil
        catchUpConflicts = []
        defer { catchingUp = false }
        do {
            let resp = try await request(MessageType.worktreeCatchUp, payload: WorktreeCatchUp(sessionID: sid))
            let r = try resp.payload(as: WorktreeCatchUp.self)
            catchUpMessage = r.message ?? "Done."
            catchUpConflicts = r.conflicts ?? []
            if r.status == "updated" { await worktreeDiff() } // refresh the diff after a clean merge
        } catch {
            catchUpMessage = "Couldn’t catch up: \(error.localizedDescription)"
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

    /// Opens the Developer log panel and starts streaming the daemon's log (replays recent lines,
    /// then tails new ones). Idempotent — safe to call each time the panel is shown.
    public func openLogPanel() {
        showLogPanel = true
        guard !logSubscribed else { return }
        logSubscribed = true
        Task {
            do {
                let hist = try await request(MessageType.logSubscribe, payload: [String: String]()).payload(as: LogHistory.self)
                daemonLog = hist.lines
            } catch {
                logSubscribed = false
                actionError = "Couldn't stream daemon logs: \(error.localizedDescription)"
            }
        }
    }

    /// Hides the panel and tells the daemon to stop streaming (frees the subscription).
    public func closeLogPanel() {
        showLogPanel = false
        guard logSubscribed else { return }
        logSubscribed = false
        Task { try? await client?.send(Protocol.encode(id: UUID().uuidString, type: MessageType.logUnsubscribe, payload: [String: String]())) }
    }

    public func clearDaemonLog() { daemonLog = [] }

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

    // MARK: sub-agent (child) transcript buffers

    /// Resets all inline child-transcript state — called on parent-session switch so a new session
    /// starts clean (no stale expanded cards, buffers, or lingering subscriptions).
    private func clearChildState() {
        childMessages.removeAll()
        childActivity.removeAll()
        expandedChildIDs.removeAll()
        subscribedChildIDs.removeAll()
    }

    /// Per-child version of appendAssistantDelta: fold streamed output into the child's last message
    /// if it's a streaming assistant, else start a new one. No throttled flush — a child transcript
    /// is a secondary peek, so a direct append keeps it simple.
    private func appendChildDelta(_ sid: String, _ text: String) {
        var buf = childMessages[sid] ?? []
        if let last = buf.last, last.role == .assistant, last.streaming {
            buf[buf.count - 1].text += text
        } else {
            buf.append(ChatMessage(role: .assistant, text: text, streaming: true))
        }
        childMessages[sid] = buf
    }

    /// Seals any in-flight streaming assistant message in a child's buffer.
    private func finalizeChildStreaming(_ sid: String) {
        guard var buf = childMessages[sid], let last = buf.last, last.role == .assistant, last.streaming else { return }
        buf[buf.count - 1].streaming = false
        childMessages[sid] = buf
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
                        applyIntegrationStatus(st)
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
                        let low = m.lowercased()
                        // Benign restart artifact: a fire-and-forget subscribe/attach to a session the
                        // daemon dropped after a restart. It's now surfaced as a "stopped" session with
                        // a Restart action, so don't raise a scary alert for it.
                        if low.contains("no such session") || low.contains("no session") || low.contains("cannot attach") {
                            break
                        }
                        status = "Error"
                        statusDetail = m
                        actionError = m
                        busy = false
                    }
                case MessageType.sessionMessage:
                    if let m = try? env.payload(as: SessionMessage.self), m.sessionID == sessionID {
                        bumpWatchdog()
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
                    } else if let m = try? env.payload(as: SessionMessage.self), childMessages[m.sessionID] != nil {
                        // A subscribed sub-agent's message — route into its own buffer, never the main
                        // transcript. Tool calls arrive as role=="tool" with Text=the tool name; keep
                        // them — they ARE the child's tool-call list.
                        let role: ChatMessage.Role = m.role == "user" ? .user : (m.role == "tool" ? .tool : .assistant)
                        finalizeChildStreaming(m.sessionID)
                        childMessages[m.sessionID, default: []].append(ChatMessage(role: role, text: m.text))
                    }
                case MessageType.thinking:
                    if let t = try? env.payload(as: Thinking.self), t.sessionID == sessionID {
                        appendThinkingDelta(t.text)
                        busy = true
                        bumpWatchdog() // reset AFTER busy=true so a mid-turn stall is still caught
                    }
                case MessageType.outputDelta:
                    if let d = try? env.payload(as: OutputDelta.self), d.sessionID == sessionID {
                        bumpWatchdog()
                        appendAssistantDelta(d.text)
                    } else if let d = try? env.payload(as: OutputDelta.self), childMessages[d.sessionID] != nil {
                        appendChildDelta(d.sessionID, d.text)
                    }
                case MessageType.sessionStatus:
                    if let ss = try? env.payload(as: SessionStatus.self), ss.sessionID == sessionID {
                        bumpWatchdog() // re-arms while running; no-ops once busy clears on idle/done below
                        status = ss.status
                        activity = ss.detail
                        switch ss.status {
                        case SessionStatusValue.idle, SessionStatusValue.done:
                            pendingApproval = nil; busy = false; activity = nil; finalizeStreaming()
                        case SessionStatusValue.awaitingApproval:
                            busy = false
                        case SessionStatusValue.error, "errored":
                            // An errored session isn't "working": stop the spinner, keep the reason
                            // (statusDetail) AND put it in the transcript so it's actually readable —
                            // it was previously only flashed as transient `activity` next to the dots.
                            busy = false; pendingApproval = nil; activity = nil; finalizeStreaming()
                            let reason = (ss.detail?.isEmpty == false) ? ss.detail! : "The agent reported an error."
                            statusDetail = reason
                            if messages.last?.text != "⚠️ \(reason)" {
                                messages.append(ChatMessage(role: .system, text: "⚠️ \(reason)"))
                            }
                        default:
                            busy = true
                            // NOTE: do NOT clear pendingApproval here — with parallel tool
                            // calls a sibling tool can be "running" while another awaits
                            // approval. Cross-client clear happens on idle / when a new
                            // approval replaces it.
                        }
                        refreshLiveActivity()
                    } else if let ss = try? env.payload(as: SessionStatus.self), childMessages[ss.sessionID] != nil {
                        // A subscribed sub-agent's status — drive its inline card's activity chip only,
                        // never the main session's busy/activity. On idle/done/error, clear the chip and
                        // seal any streaming output.
                        switch ss.status {
                        case SessionStatusValue.idle, SessionStatusValue.done, SessionStatusValue.error, "errored":
                            childActivity[ss.sessionID] = nil
                            finalizeChildStreaming(ss.sessionID)
                        default:
                            childActivity[ss.sessionID] = ss.detail
                        }
                    }
                case MessageType.approvalRequest:
                    if let ar = try? env.payload(as: ApprovalRequest.self), ar.sessionID == sessionID {
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
                case MessageType.integrationStatus: // broadcast after (re)connect/disconnect
                    if let st = try? env.payload(as: IntegrationStatus.self) { applyIntegrationStatus(st) }
                case MessageType.telemetryStatus: // broadcast after a toggle on any device
                    if let t = try? env.payload(as: Telemetry.self) { telemetryEnabled = t.enabled }
                case MessageType.sessionProgress: // live create step → prescriptive loading checklist
                    if startingSession, let p = try? env.payload(as: SessionProgress.self) {
                        applyCreateStep(p)
                    }
                case MessageType.logLine: // streamed daemon log line → Developer log panel
                    if let l = try? env.payload(as: LogLine.self) {
                        daemonLog.append(l.line)
                        if daemonLog.count > 2000 { daemonLog.removeFirst(daemonLog.count - 2000) }
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
                case MessageType.loopList: // a loop config changed or a run started
                    if let ll = try? env.payload(as: LoopList.self) { loops = ll.loops; loopRuns = ll.runs }
                case MessageType.providerList: // pushed after a custom agent is added/removed
                    if let pl = try? env.payload(as: ProviderList.self) { applyProviders(pl.providers); providersLoaded = true }
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
