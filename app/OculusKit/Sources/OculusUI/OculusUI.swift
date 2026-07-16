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

    @Published public var connected = false
    @Published public var status = "Not connected"
    @Published public var messages: [ChatMessage] = []
    @Published public var sessionID: String?
    @Published public var pendingApproval: ApprovalRequest?
    @Published public var discovered: [Discovered] = []
    @Published public var busy = false // agent is producing output
    @Published public var pairingPublicURL: String? // reachable URL for the phone-pairing QR

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

    private enum Keys { static let ws = "oculus.ws", pub = "oculus.pub", secret = "oculus.secret" }

    /// True once the daemon has been paired at least once (creds are saved).
    public var hasSavedPairing: Bool { !wsURL.isEmpty && !daemonPubHex.isEmpty && !secret.isEmpty }

    private func savePairing() {
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
                                              payload: SessionCreate(provider: "opencode", prompt: trimmed))
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
        messages.removeAll()
        pendingApproval = nil
        busy = false
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
        if let last = messages.last, last.role == .assistant, last.streaming {
            messages[messages.count - 1].text += text
        } else {
            messages.append(ChatMessage(role: .assistant, text: text, streaming: true))
        }
    }

    private func finalizeStreaming() {
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
                    } else if let s = try? Protocol.payload(data, as: Session.self) {
                        sessionID = s.id
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
                case MessageType.outputDelta:
                    if let d = try? Protocol.payload(data, as: OutputDelta.self) {
                        appendAssistantDelta(d.text)
                    }
                case MessageType.sessionStatus:
                    if let ss = try? Protocol.payload(data, as: SessionStatus.self) {
                        status = ss.status
                        if ss.status == SessionStatusValue.idle || ss.status == SessionStatusValue.done {
                            pendingApproval = nil
                            busy = false
                            finalizeStreaming()
                        } else if ss.status == SessionStatusValue.awaitingApproval {
                            busy = false
                        } else {
                            busy = true
                        }
                        refreshLiveActivity()
                    }
                case MessageType.approvalRequest:
                    if let ar = try? Protocol.payload(data, as: ApprovalRequest.self) {
                        pendingApproval = ar
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
                        model.newSession()
                    } else if let d = model.discovered.first(where: { $0.sessionID == sel }) {
                        Task { await model.attach(d) }
                    }
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
/// Compact menu-bar surface: live status + one-tap approve/deny.
public struct MenuBarView: View {
    @ObservedObject var model: Model
    public init(model: Model) { self.model = model }

    public var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text("Oculus").font(.headline)
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
