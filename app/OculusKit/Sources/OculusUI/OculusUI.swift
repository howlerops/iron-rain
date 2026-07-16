import SwiftUI
import OculusKit
#if canImport(AppKit)
import AppKit
#endif

/// Drives one daemon connection. Minimal v0 surface: connect, autodetect running
/// sessions, start a session, stream output, approve/deny tool calls. Built entirely
/// on OculusKit (the proven, vector-locked client), so the app speaks the same
/// protocol as the Go daemon. Shared verbatim by the iOS and macOS app targets.
@MainActor
public final class Model: ObservableObject {
    @Published public var wsURL = "ws://127.0.0.1:6000/ws"
    @Published public var daemonPubHex = ""
    @Published public var secret = ""

    @Published public var connected = false
    @Published public var status = "Not connected"
    @Published public var output: [String] = []
    @Published public var sessionID: String?
    @Published public var pendingApproval: ApprovalRequest?
    @Published public var discovered: [Discovered] = []

    private var client: OculusClient?
    private let clientPrivate = OculusCrypto.generatePrivateKey()

    public init() {}

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
            // Start any prompt queued by the "Start Session" App Intent.
            if let queued = OculusStore.shared.pendingPrompt {
                OculusStore.shared.pendingPrompt = nil
                await startSession(prompt: queued)
            }
        } catch {
            status = "Connect failed: \(error)"
        }
    }

    /// Asks the daemon to autodetect active opencode/claude-code sessions on the host.
    public func discover() async {
        guard let client else { return }
        do {
            let env = try Protocol.encode(id: UUID().uuidString, type: MessageType.discover)
            try await client.send(env)
        } catch {
            status = "Discover failed: \(error)"
        }
    }

    /// Registers this device's APNs token so the daemon can push approval requests
    /// to the lock screen. Call after the OS grants a token (iOS
    /// `didRegisterForRemoteNotificationsWithDeviceToken`).
    public func registerDevice(token: String) async {
        guard let client else { return }
        do {
            let env = try Protocol.encode(id: UUID().uuidString, type: MessageType.deviceRegister,
                                          payload: DeviceRegister(token: token))
            try await client.send(env)
        } catch {
            status = "Device register failed: \(error)"
        }
    }

    public func startSession(prompt: String) async {
        guard let client else { return }
        do {
            let env = try Protocol.encode(id: UUID().uuidString, type: MessageType.sessionCreate,
                                          payload: SessionCreate(provider: "opencode", prompt: prompt))
            try await client.send(env)
        } catch {
            status = "Send failed: \(error)"
        }
    }

    public func respond(_ decision: String) async {
        guard let client, let ap = pendingApproval else { return }
        pendingApproval = nil
        do {
            let env = try Protocol.encode(id: UUID().uuidString, type: MessageType.approvalRespond,
                                          payload: ApprovalRespond(approvalID: ap.approvalID, decision: decision))
            try await client.send(env)
        } catch {
            status = "Respond failed: \(error)"
        }
    }

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
                        output.append("• session \(s.id) [\(s.provider)]")
                    }
                case MessageType.outputDelta:
                    if let d = try? Protocol.payload(data, as: OutputDelta.self) {
                        output.append(d.text)
                    }
                case MessageType.sessionStatus:
                    if let ss = try? Protocol.payload(data, as: SessionStatus.self) {
                        status = "session: \(ss.status)"
                    }
                case MessageType.approvalRequest:
                    if let ar = try? Protocol.payload(data, as: ApprovalRequest.self) {
                        pendingApproval = ar
                    }
                case MessageType.error:
                    if let e = try? Protocol.payload(data, as: ProtocolError.self) {
                        status = "error: \(e.message)"
                    }
                default:
                    break
                }
            } catch {
                connected = false
                status = "Disconnected: \(error)"
            }
        }
    }
}

/// The v0 app surface, identical on iOS and macOS. The `Model` is owned by the App
/// (so the macOS menu-bar and the main window share one connection) and injected.
public struct ContentView: View {
    @ObservedObject var model: Model
    @State private var prompt = ""
    @Environment(\.colorScheme) private var scheme
    private var palette: OculusPalette { .current(scheme) }

    public init(model: Model) { self.model = model }

    public var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            HStack(spacing: 10) {
                Image("WolfMark")
                    .resizable().scaledToFit()
                    .frame(width: 34, height: 34)
                Text("Oculus").font(.largeTitle.bold())
                Spacer()
            }
            Text(model.status).font(.caption).foregroundStyle(palette.mutedForeground)

            if !model.connected {
                connectForm
            } else {
                sessionView
            }
            Spacer()
        }
        .padding()
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
        .background(palette.background.ignoresSafeArea())
        .foregroundStyle(palette.foreground)
        .tint(palette.primary)
        // Handoff: advertise the active session so the other device can continue it.
        .userActivity(oculusSessionActivityType, isActive: model.sessionID != nil) { activity in
            activity.title = "Oculus session"
            if let sid = model.sessionID { activity.userInfo = ["session_id": sid] }
            activity.isEligibleForHandoff = true
        }
        .onContinueUserActivity(oculusSessionActivityType) { activity in
            if let sid = activity.userInfo?["session_id"] as? String {
                OculusStore.shared.handoffSessionID = sid
                model.output.append("↩︎ Handoff: continue session \(sid)")
            }
        }
    }

    private var connectForm: some View {
        GroupBox("Connect") {
            VStack(alignment: .leading, spacing: 6) {
                plainField("Daemon WebSocket URL", text: $model.wsURL)
                plainField("Daemon public key (hex)", text: $model.daemonPubHex)
                SecureField("Pairing secret", text: $model.secret)
                Button("Connect") { Task { await model.connect() } }
                    .keyboardShortcut(.defaultAction)
            }.padding(6)
        }
    }

    @ViewBuilder private var sessionView: some View {
        HStack {
            TextField("Prompt an agent…", text: $prompt)
            Button("Start") {
                let p = prompt; prompt = ""
                Task { await model.startSession(prompt: p) }
            }
        }

        if !model.discovered.isEmpty {
            GroupBox("Detected on host") {
                VStack(alignment: .leading, spacing: 2) {
                    ForEach(Array(model.discovered.enumerated()), id: \.offset) { _, d in
                        Text(describe(d)).font(.system(.caption, design: .monospaced))
                            .frame(maxWidth: .infinity, alignment: .leading)
                    }
                }.padding(4)
            }
        }

        if let ap = model.pendingApproval {
            GroupBox {
                HStack {
                    VStack(alignment: .leading) {
                        Text("Approve tool: \(ap.tool)").bold()
                        Text(ap.sessionID).font(.caption).foregroundStyle(.secondary)
                    }
                    Spacer()
                    Button("Deny") { Task { await model.respond(Decision.deny) } }
                    Button("Allow") { Task { await model.respond(Decision.allow) } }
                        .keyboardShortcut(.defaultAction)
                }
            }
        }

        ScrollView {
            VStack(alignment: .leading, spacing: 2) {
                ForEach(Array(model.output.enumerated()), id: \.offset) { _, line in
                    Text(line).font(.system(.body, design: .monospaced)).textSelection(.enabled)
                }
            }.frame(maxWidth: .infinity, alignment: .leading).padding(8)
        }
        .background(palette.card)
        .clipShape(RoundedRectangle(cornerRadius: 12))
    }

    private func describe(_ d: Discovered) -> String {
        if d.kind == DiscoveredKind.server { return "◆ opencode server \(d.url ?? "")" }
        if d.provider == "opencode" { return "  ● \(d.title ?? d.sessionID ?? "session")" }
        return "◆ claude-code \(d.cwd ?? d.sessionID ?? "")"
    }

    /// A text field with iOS niceties (no autocap/autocorrect for URLs/keys).
    private func plainField(_ title: String, text: Binding<String>) -> some View {
        let field = TextField(title, text: text)
        #if os(iOS)
        return field.textInputAutocapitalization(.never).autocorrectionDisabled()
        #else
        return field
        #endif
    }
}

extension Model {
    /// SF Symbol reflecting live state — used by the menu-bar item so a pending
    /// approval is visible without opening anything.
    public var menuBarSymbol: String {
        if pendingApproval != nil { return "bell.badge.fill" }
        if connected { return "bolt.horizontal.circle.fill" }
        return "bolt.horizontal.circle"
    }
}

#if os(macOS)
/// Compact menu-bar surface: live status + one-tap approve/deny, sharing the App's
/// Model so it stays in lockstep with the main window.
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
                Text("\(model.discovered.count) detected · \(model.output.count) output lines")
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
