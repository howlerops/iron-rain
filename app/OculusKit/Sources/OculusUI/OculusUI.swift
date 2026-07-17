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
    @Published public var lastDiff: String? // populated by worktreeDiff()
    /// Options applied to the NEXT session created (by the first send). Set via newSession(...).
    @Published public var newSessionProvider = "opencode"
    public var pendingProjectID: String?
    public var pendingWorktree = false
    public var pendingWorkspaceName: String?

    private var client: OculusClient?
    private let clientPrivate = OculusCrypto.generatePrivateKey()
    private let defaults = UserDefaults.standard
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
        loadLocalPairing() // macOS: refresh the reachable URL (and pair if unpaired)
        if hasSavedPairing && !connected { await connect() }
    }

    private func loadLocalPairing() {
        #if os(macOS)
        let path = (NSHomeDirectory() as NSString).appendingPathComponent(".oculus/pairing.json")
        guard let data = FileManager.default.contents(atPath: path),
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
            savePairing()
            Task { await receiveLoop() }
            await discover()
            await loadProjects()
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
        } catch {
            status = "Connect failed"
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
        guard !trimmed.isEmpty, let client else { return }
        messages.append(ChatMessage(role: .user, text: trimmed))
        busy = true
        do {
            if let sid = sessionID {
                let env = try Protocol.encode(id: UUID().uuidString, type: MessageType.sessionPrompt,
                                              payload: SessionPrompt(sessionID: sid, text: trimmed))
                try await client.send(env)
            } else {
                let env = try Protocol.encode(id: UUID().uuidString, type: MessageType.sessionCreate,
                                              payload: SessionCreate(provider: newSessionProvider,
                                                                     projectID: pendingProjectID,
                                                                     prompt: trimmed,
                                                                     worktree: pendingWorktree ? true : nil,
                                                                     workspaceName: pendingWorkspaceName))
                try await client.send(env)
            }
        } catch {
            status = "Send failed: \(error)"
            busy = false
        }
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

    public func attach(_ d: Discovered) async {
        guard let client, d.provider == "opencode", let sid = d.sessionID else { return }
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

    private func appendAssistantDelta(_ text: String) {
        // The answer starting means thinking is done — finalize any streaming thinking.
        finalizeThinking()
        if let last = messages.last, last.role == .assistant, last.streaming {
            messages[messages.count - 1].text += text
        } else {
            messages.append(ChatMessage(role: .assistant, text: text, streaming: true))
        }
    }

    private func appendThinkingDelta(_ text: String) {
        if let last = messages.last, last.role == .thinking, last.streaming {
            messages[messages.count - 1].text += text
        } else {
            messages.append(ChatMessage(role: .thinking, text: text, streaming: true))
        }
    }

    private func finalizeThinking() {
        if let last = messages.last, last.role == .thinking, last.streaming {
            messages[messages.count - 1].streaming = false
        }
    }

    private func finalizeStreaming() {
        finalizeThinking()
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
                let header = try Protocol.header(data)
                switch header.type {
                case MessageType.ok:
                    if let dl = try? Protocol.payload(data, as: DiscoverList.self), !dl.items.isEmpty {
                        discovered = dl.items
                    } else if let pl = try? Protocol.payload(data, as: ProjectList.self) {
                        projects = pl.projects
                    } else if let wd = try? Protocol.payload(data, as: WorktreeDiff.self), wd.diff != nil {
                        lastDiff = wd.diff
                    } else if let pr = try? Protocol.payload(data, as: WorktreePRResult.self) {
                        status = pr.url.map { "PR: \($0)" } ?? "Pushed \(pr.branch)"
                    } else if let s = try? Protocol.payload(data, as: Session.self) {
                        sessionID = s.id
                        currentSession = s
                        refreshLiveActivity()
                    }
                case MessageType.sessionMessage:
                    if let m = try? Protocol.payload(data, as: SessionMessage.self) {
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
                    if let t = try? Protocol.payload(data, as: Thinking.self) {
                        appendThinkingDelta(t.text)
                        busy = true
                    }
                case MessageType.outputDelta:
                    if let d = try? Protocol.payload(data, as: OutputDelta.self) {
                        appendAssistantDelta(d.text)
                    }
                case MessageType.sessionStatus:
                    if let ss = try? Protocol.payload(data, as: SessionStatus.self) {
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
                    if let ar = try? Protocol.payload(data, as: ApprovalRequest.self) {
                        pendingApproval = ar
                        refreshLiveActivity()
                    }
                case MessageType.approvalResolved:
                    // Another device answered this exact approval — clear our card and
                    // mirror the decision so both transcripts match.
                    if let r = try? Protocol.payload(data, as: ApprovalResolved.self),
                       let ap = pendingApproval, ap.approvalID == r.approvalID {
                        let verb = r.decision == Decision.deny ? "✗ Denied"
                            : (r.decision == Decision.always ? "✓ Always allow" : "✓ Allowed")
                        let cmd = (ap.detail?.isEmpty == false) ? " · \(ap.detail!)" : ""
                        appendTool("\(verb) \(ap.tool)\(cmd)")
                        pendingApproval = nil
                        refreshLiveActivity()
                    }
                case MessageType.error:
                    if let e = try? Protocol.payload(data, as: ProtocolError.self) {
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
        // Connection dropped — auto-reconnect if the user didn't disconnect.
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

/// Routes between the connect screen and the chat surface.
public struct ContentView: View {
    @ObservedObject var model: Model
    @Environment(\.colorScheme) private var scheme
    private var palette: OculusPalette { .current(scheme) }
    @State private var selection: String?
    @State private var showNewSession = false

    public init(model: Model) { self.model = model }

    public var body: some View {
        Group {
            if model.connected {
                NavigationSplitView {
                    SessionSidebar(model: model, selection: $selection)
                        .navigationSplitViewColumnWidth(min: 230, ideal: 270)
                } detail: {
                    ChatView(model: model)
                }
                .onChange(of: selection) { sel in
                    guard let sel else { return }
                    if sel == SessionSidebar.newSessionTag {
                        showNewSession = true
                        selection = nil // allow re-triggering
                    } else if let d = model.discovered.first(where: { $0.sessionID == sel }) {
                        Task { await model.attach(d) }
                    }
                }
                .sheet(isPresented: $showNewSession) {
                    NewSessionView(model: model, palette: palette) { showNewSession = false }
                }
            } else {
                ConnectView(model: model)
            }
        }
        .background(palette.background.ignoresSafeArea())
        .foregroundStyle(palette.foreground)
        .tint(palette.primary)
        .task { await model.autoConnectIfPaired() }
        .userActivity(oculusSessionActivityType, isActive: model.sessionID != nil) { activity in
            activity.title = "Oculus session"
            if let sid = model.sessionID { activity.userInfo = ["session_id": sid] }
            activity.isEligibleForHandoff = true
        }
        .onContinueUserActivity(oculusSessionActivityType) { activity in
            if let sid = activity.userInfo?["session_id"] as? String {
                OculusStore.shared.handoffSessionID = sid
            }
        }
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
                Text("Oculus").font(.headline)
                Text("No desktop paired. Open the window to add one.")
                    .font(.caption).foregroundStyle(.secondary)
                Divider()
                Button("Quit Oculus") { NSApplication.shared.terminate(nil) }
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
                Text("Oculus").font(.headline)
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
            Button("Quit Oculus") { NSApplication.shared.terminate(nil) }
        }
        .padding(12)
        .frame(width: 240)
    }
}
#endif
