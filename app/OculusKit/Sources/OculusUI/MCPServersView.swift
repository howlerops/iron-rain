import SwiftUI
import OculusKit

/// MCP servers, registered once with the daemon and injected into every agent it drives.
///
/// The problem this replaces: the same server configured separately for opencode, for Claude Code,
/// and for whatever CLI comes next — three copies of the credentials, no single place to see what's
/// installed, and no way to tell whether any of it actually works. Here a server is defined once,
/// its tools are listed by really connecting to it, and it reaches every harness.
public struct MCPServersView: View {
    @ObservedObject var model: Model
    let palette: OculusPalette
    var onClose: (() -> Void)? = nil

    @State private var editing: MCPServerInfo? = nil
    @State private var adding = false
    @State private var checking: Set<String> = []

    public init(model: Model, palette: OculusPalette, onClose: (() -> Void)? = nil) {
        self.model = model; self.palette = palette; self.onClose = onClose
    }

    public var body: some View {
        VStack(spacing: 0) {
            header
            Divider().overlay(palette.border)
            if model.mcpServers.isEmpty {
                emptyState
            } else {
                List {
                    ForEach(model.mcpServers) { row($0) }
                }
                #if os(macOS)
                .listStyle(.inset)
                #endif
            }
        }
        .frame(minWidth: 520, minHeight: 400)
        .background(palette.background)
        .task { await model.loadMCPServers() }
        .sheet(isPresented: $adding) {
            MCPServerEditor(model: model, palette: palette, existing: nil, onClose: { adding = false })
        }
        .sheet(item: $editing) { server in
            MCPServerEditor(model: model, palette: palette, existing: server, onClose: { editing = nil })
        }
    }

    private var header: some View {
        HStack {
            VStack(alignment: .leading, spacing: 2) {
                Text("MCP servers").font(.headline).foregroundStyle(palette.foreground)
                Text("Registered once here — every agent gets them.")
                    .font(.caption).foregroundStyle(palette.mutedForeground)
            }
            Spacer()
            Button { adding = true } label: { Label("Add", systemImage: "plus") }
            if let onClose {
                Button("Done", action: onClose).keyboardShortcut(.defaultAction)
            }
        }
        .padding(14)
    }

    private func row(_ s: MCPServerInfo) -> some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack(spacing: 8) {
                statusDot(s)
                Text(s.name).font(.system(size: 13, weight: .medium)).foregroundStyle(palette.foreground)
                if let v = s.serverVersion, !v.isEmpty {
                    Text(v).font(.system(size: 10, design: .monospaced)).foregroundStyle(palette.mutedForeground)
                }
                Spacer()
                Toggle("", isOn: Binding(
                    get: { s.enabled },
                    set: { on in Task { await model.setMCPServerEnabled(name: s.name, enabled: on) } }
                ))
                .labelsHidden().toggleStyle(.switch).tint(palette.primary)
            }

            Text(commandLine(s))
                .font(.system(size: 10.5, design: .monospaced))
                .foregroundStyle(palette.mutedForeground).lineLimit(1).truncationMode(.middle)

            if let err = s.error, !err.isEmpty {
                // The server's own stderr — usually the only clue about what's wrong.
                Text(err)
                    .font(.system(size: 10.5, design: .monospaced))
                    .foregroundStyle(palette.destructive)
                    .lineLimit(3)
            } else if let tools = s.tools, !tools.isEmpty {
                Text(tools.prefix(8).map(\.name).joined(separator: " · ") + (tools.count > 8 ? " +\(tools.count - 8) more" : ""))
                    .font(.system(size: 10.5)).foregroundStyle(palette.mutedForeground).lineLimit(2)
            } else {
                Text("Not checked yet — Test to connect and list its tools.")
                    .font(.system(size: 10.5)).italic().foregroundStyle(palette.mutedForeground)
            }

            HStack(spacing: 8) {
                if let p = s.protocolVersion, !p.isEmpty { tag(p) }
                if let pid = s.projectID, !pid.isEmpty { tag("one project") }
                Spacer()
                Button(checking.contains(s.name) ? "Testing…" : "Test") {
                    checking.insert(s.name)
                    Task {
                        await model.checkMCPServer(name: s.name)
                        checking.remove(s.name)
                    }
                }
                .buttonStyle(.bordered).disabled(checking.contains(s.name))
                Button("Edit") { editing = s }.buttonStyle(.bordered)
                Button(role: .destructive) {
                    Task { await model.deleteMCPServer(name: s.name) }
                } label: { Image(systemName: "trash") }
                .buttonStyle(.plain).foregroundStyle(palette.mutedForeground)
            }
            .font(.system(size: 11))
        }
        .padding(.vertical, 5)
        .opacity(s.enabled ? 1 : 0.55)
    }

    private func statusDot(_ s: MCPServerInfo) -> some View {
        Circle()
            .fill(s.checkedAt == nil ? palette.mutedForeground.opacity(0.4)
                  : (s.ok ? Color(hex: 0x3FB950) : palette.destructive))
            .frame(width: 7, height: 7)
    }

    private func commandLine(_ s: MCPServerInfo) -> String {
        if s.transport == "http" { return s.url ?? "" }
        return ([s.command ?? ""] + (s.args ?? [])).joined(separator: " ")
    }

    private func tag(_ t: String) -> some View {
        Text(t).font(.system(size: 9.5, design: .monospaced))
            .padding(.horizontal, 5).padding(.vertical, 1.5)
            .background(palette.input).clipShape(RoundedRectangle(cornerRadius: 4))
            .foregroundStyle(palette.mutedForeground)
    }

    private var emptyState: some View {
        VStack(spacing: 10) {
            Image(systemName: "puzzlepiece.extension").font(.system(size: 30))
                .foregroundStyle(palette.mutedForeground.opacity(0.5))
            Text("No MCP servers").font(.headline).foregroundStyle(palette.foreground)
            Text("Add a server once and every agent — opencode, Claude Code, and any CLI agent you've configured — gets its tools. Credentials stay on this Mac.")
                .font(.callout).foregroundStyle(palette.mutedForeground)
                .multilineTextAlignment(.center).frame(maxWidth: 380)
            Button { adding = true } label: { Label("Add a server", systemImage: "plus") }
                .buttonStyle(.borderedProminent).tint(palette.primary)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity).padding(24)
    }
}

/// Add/edit one MCP server. Kept deliberately plain: a name, how to start it, and any credentials.
struct MCPServerEditor: View {
    @ObservedObject var model: Model
    let palette: OculusPalette
    let existing: MCPServerInfo?
    var onClose: () -> Void

    @State private var name = ""
    @State private var transport = "stdio"
    @State private var command = ""
    @State private var argsText = ""
    @State private var url = ""
    @State private var envText = ""
    @State private var error: String? = nil
    @State private var saving = false

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            Text(existing == nil ? "Add MCP server" : "Edit \(existing!.name)")
                .font(.headline).foregroundStyle(palette.foreground)

            field("Name") {
                TextField("github", text: $name).textFieldStyle(.roundedBorder)
                    .disabled(existing != nil) // the name is the identity
            }
            Picker("", selection: $transport) {
                Text("Local command").tag("stdio")
                Text("Remote URL").tag("http")
            }
            .pickerStyle(.segmented).labelsHidden()

            if transport == "stdio" {
                field("Command") {
                    TextField("npx", text: $command).textFieldStyle(.roundedBorder)
                }
                field("Arguments") {
                    TextField("-y @modelcontextprotocol/server-github", text: $argsText)
                        .textFieldStyle(.roundedBorder)
                    Text("Separated by spaces.").font(.caption).foregroundStyle(palette.mutedForeground)
                }
            } else {
                field("URL") {
                    TextField("https://mcp.example.com/mcp", text: $url).textFieldStyle(.roundedBorder)
                }
            }

            field("Environment") {
                TextEditor(text: $envText)
                    .font(.system(size: 12, design: .monospaced))
                    .frame(height: 70)
                    .overlay(RoundedRectangle(cornerRadius: 6).stroke(palette.border))
                Text("One KEY=value per line. Stored on this Mac only, readable by you alone. Existing secrets show as •••• and are kept unless you replace them.")
                    .font(.caption).foregroundStyle(palette.mutedForeground)
            }

            if let error {
                Text(error).font(.caption).foregroundStyle(palette.destructive)
            }

            HStack {
                Spacer()
                Button("Cancel", action: onClose).keyboardShortcut(.cancelAction)
                Button(saving ? "Saving…" : "Save") { save() }
                    .buttonStyle(.borderedProminent).tint(palette.primary)
                    .keyboardShortcut(.defaultAction)
                    .disabled(saving || name.trimmingCharacters(in: .whitespaces).isEmpty)
            }
        }
        .padding(18)
        .frame(minWidth: 440)
        .background(palette.background)
        .onAppear(perform: load)
    }

    private func field<C: View>(_ label: String, @ViewBuilder content: () -> C) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            Text(label.uppercased()).font(.system(size: 10, weight: .semibold)).tracking(0.7)
                .foregroundStyle(palette.mutedForeground)
            content()
        }
    }

    private func load() {
        guard let e = existing else { return }
        name = e.name
        transport = e.transport
        command = e.command ?? ""
        argsText = (e.args ?? []).joined(separator: " ")
        url = e.url ?? ""
        envText = (e.env ?? [:]).sorted { $0.key < $1.key }.map { "\($0.key)=\($0.value)" }.joined(separator: "\n")
    }

    private func save() {
        saving = true
        error = nil
        var env: [String: String] = [:]
        for line in envText.split(separator: "\n") {
            let parts = line.split(separator: "=", maxSplits: 1, omittingEmptySubsequences: false)
            guard parts.count == 2 else { continue }
            let k = parts[0].trimmingCharacters(in: .whitespaces)
            if !k.isEmpty { env[k] = String(parts[1]) }
        }
        let payload = MCPUpsert(
            name: name.trimmingCharacters(in: .whitespaces),
            transport: transport,
            command: transport == "stdio" ? command.trimmingCharacters(in: .whitespaces) : nil,
            args: transport == "stdio" ? argsText.split(separator: " ").map(String.init) : nil,
            env: env.isEmpty ? nil : env,
            url: transport == "http" ? url.trimmingCharacters(in: .whitespaces) : nil
        )
        Task {
            let err = await model.upsertMCPServer(payload)
            saving = false
            if let err { error = err } else { onClose() }
        }
    }
}
