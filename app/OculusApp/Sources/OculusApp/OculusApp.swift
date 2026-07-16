import SwiftUI
import OculusKit

@main
struct OculusApp: App {
    var body: some Scene {
        WindowGroup("Oculus") {
            ContentView()
                .frame(minWidth: 520, minHeight: 420)
        }
    }
}

/// Drives one daemon connection. Minimal v0 surface: connect, start a session,
/// stream output, approve/deny tool calls. Built entirely on OculusKit (the proven,
/// vector-locked client), so the app speaks the same protocol as the Go daemon.
@MainActor
final class Model: ObservableObject {
    @Published var wsURL = "ws://127.0.0.1:6000/ws"
    @Published var daemonPubHex = ""
    @Published var secret = ""

    @Published var connected = false
    @Published var status = "Not connected"
    @Published var output: [String] = []
    @Published var sessionID: String?
    @Published var pendingApproval: ApprovalRequest?

    private var client: OculusClient?
    private let clientPrivate = OculusCrypto.generatePrivateKey()

    func connect() async {
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
        } catch {
            status = "Connect failed: \(error)"
        }
    }

    func startSession(prompt: String) async {
        guard let client else { return }
        do {
            let env = try Protocol.encode(id: UUID().uuidString, type: MessageType.sessionCreate,
                                          payload: SessionCreate(provider: "opencode", prompt: prompt))
            try await client.send(env)
        } catch {
            status = "Send failed: \(error)"
        }
    }

    func respond(_ decision: String) async {
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
                    if let s = try? Protocol.payload(data, as: Session.self) {
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

struct ContentView: View {
    @StateObject private var model = Model()
    @State private var prompt = ""

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("Oculus").font(.largeTitle.bold())
            Text(model.status).font(.caption).foregroundStyle(.secondary)

            if !model.connected {
                GroupBox("Connect") {
                    VStack(alignment: .leading, spacing: 6) {
                        TextField("Daemon WebSocket URL", text: $model.wsURL)
                        TextField("Daemon public key (hex)", text: $model.daemonPubHex)
                        SecureField("Pairing secret", text: $model.secret)
                        Button("Connect") { Task { await model.connect() } }
                            .keyboardShortcut(.defaultAction)
                    }.padding(6)
                }
            } else {
                HStack {
                    TextField("Prompt an agent…", text: $prompt)
                    Button("Start") {
                        let p = prompt; prompt = ""
                        Task { await model.startSession(prompt: p) }
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
                    }.frame(maxWidth: .infinity, alignment: .leading)
                }
                .background(Color.black.opacity(0.03))
            }
            Spacer()
        }
        .padding()
    }
}
