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

    private var client: OculusClient?
    private let clientPrivate = OculusCrypto.generatePrivateKey()
    #if os(iOS)
    private var liveActivity: Any?
    #endif

    public init() {}

    // MARK: connection

    public func connect() async {
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
            status = "Connect failed: \(error)"
        }
    }

    public func disconnect() {
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

    public func respond(_ decision: String) async {
        guard let client, let ap = pendingApproval else { return }
        appendTool(decision == Decision.allow ? "✓ Allowed \(ap.tool)" : "✗ Denied \(ap.tool)")
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

    public init(model: Model) { self.model = model }

    public var body: some View {
        Group {
            if model.connected {
                ChatView(model: model)
            } else {
                ConnectView(model: model)
            }
        }
        .background(palette.background.ignoresSafeArea())
        .foregroundStyle(palette.foreground)
        .tint(palette.primary)
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
