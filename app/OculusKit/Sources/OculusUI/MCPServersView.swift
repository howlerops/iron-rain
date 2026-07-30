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
    @State private var browsing = false
    @State private var prefill: MCPDirectoryEntry? = nil

    public init(model: Model, palette: OculusPalette, onClose: (() -> Void)? = nil) {
        self.model = model; self.palette = palette; self.onClose = onClose
    }

    public var body: some View {
        VStack(spacing: 0) {
            header
            Divider().overlay(palette.border)
            if !model.mcpFound.isEmpty { importBanner }
            if model.mcpServers.isEmpty && model.mcpFound.isEmpty {
                emptyState
            } else {
                List {
                    ForEach(model.mcpServers) { row($0) }
                    if !model.mcpServers.isEmpty { exclusiveRow }
                }
                #if os(macOS)
                .listStyle(.inset)
                #endif
            }
        }
        .frame(minWidth: 520, minHeight: 400)
        .background(palette.background)
        .task {
            await model.loadMCPServers()
            await model.discoverMCPServers()
        }
        .sheet(isPresented: $adding) {
            MCPServerEditor(model: model, palette: palette, existing: nil, prefill: nil, onClose: { adding = false })
        }
        .sheet(isPresented: $browsing) {
            MCPDirectoryView(model: model, palette: palette,
                             onPick: { entry in
                                 browsing = false
                                 prefill = entry
                             },
                             onClose: { browsing = false })
        }
        .sheet(item: $prefill) { entry in
            MCPServerEditor(model: model, palette: palette, existing: nil, prefill: entry, onClose: { prefill = nil })
        }
        .sheet(item: $editing) { server in
            MCPServerEditor(model: model, palette: palette, existing: server, prefill: nil, onClose: { editing = nil })
        }
    }

    /// Servers your agents already have. Offered rather than absorbed: each one carries a command
    /// that will run with your credentials, so it gets a look first.
    private var importBanner: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack(spacing: 6) {
                Image(systemName: "arrow.down.circle").foregroundStyle(palette.primary)
                Text("\(model.mcpFound.count) server\(model.mcpFound.count == 1 ? "" : "s") already set up in your agents")
                    .font(.system(size: 12, weight: .medium)).foregroundStyle(palette.foreground)
                Spacer()
                Button("Import all") {
                    Task { await model.importMCPServers(names: model.mcpFound.map(\.name)) }
                }
                .buttonStyle(.borderedProminent).tint(palette.primary).font(.system(size: 11))
            }
            ForEach(model.mcpFound) { f in
                HStack(spacing: 8) {
                    VStack(alignment: .leading, spacing: 1) {
                        Text(f.name).font(.system(size: 12)).foregroundStyle(palette.foreground)
                        Text(f.source).font(.system(size: 10)).foregroundStyle(palette.mutedForeground)
                    }
                    Spacer()
                    if let keys = f.envKeys, !keys.isEmpty {
                        Text(keys.joined(separator: ", "))
                            .font(.system(size: 9.5, design: .monospaced))
                            .foregroundStyle(palette.mutedForeground).lineLimit(1)
                    }
                    Button("Import") { Task { await model.importMCPServers(names: [f.name]) } }
                        .buttonStyle(.bordered).font(.system(size: 11))
                }
            }
            Text("Importing copies the definition here. Turn the original off in that agent's own config so it isn't started twice — or use the switch below to have Iron Rain manage MCP for all of them.")
                .font(.caption).foregroundStyle(palette.mutedForeground)
        }
        .padding(12)
        .background(palette.card)
        .overlay(RoundedRectangle(cornerRadius: 10).stroke(palette.primary.opacity(0.35)))
        .clipShape(RoundedRectangle(cornerRadius: 10))
        .padding(.horizontal, 14).padding(.top, 10)
    }

    /// The dedupe switch. Off by default because turning it on when servers haven't been imported
    /// would silently remove tools the user relies on.
    private var exclusiveRow: some View {
        VStack(alignment: .leading, spacing: 4) {
            Toggle(isOn: Binding(
                get: { model.mcpExclusive },
                set: { on in Task { await model.setMCPExclusive(on) } }
            )) {
                Text("Iron Rain manages MCP for my agents").font(.system(size: 12))
            }
            .toggleStyle(.switch).tint(palette.primary)
            Text(model.mcpExclusive
                 ? "Your agents ignore their own MCP config and use only the servers above — one process per server."
                 : "Your agents ALSO load their own MCP config. A server configured in both places runs twice.")
                .font(.caption).foregroundStyle(palette.mutedForeground)
        }
        .padding(.vertical, 4)
    }

    private var header: some View {
        HStack {
            VStack(alignment: .leading, spacing: 2) {
                Text("MCP servers").font(.headline).foregroundStyle(palette.foreground)
                Text("Registered once here — every agent gets them.")
                    .font(.caption).foregroundStyle(palette.mutedForeground)
            }
            Spacer()
            Button { browsing = true } label: { Label("Browse", systemImage: "magnifyingglass") }
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
            HStack {
                Button { browsing = true } label: { Label("Browse the registry", systemImage: "magnifyingglass") }
                    .buttonStyle(.borderedProminent).tint(palette.primary)
                Button { adding = true } label: { Label("Add manually", systemImage: "plus") }
                    .buttonStyle(.bordered)
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity).padding(24)
    }
}

/// Add/edit one MCP server. Kept deliberately plain: a name, how to start it, and any credentials.
struct MCPServerEditor: View {
    @ObservedObject var model: Model
    let palette: OculusPalette
    let existing: MCPServerInfo?
    /// A registry entry to pre-fill from. The user still confirms and saves — a one-tap install of a
    /// third-party command that then runs with their credentials should require a look first.
    let prefill: MCPDirectoryEntry?
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

    /// Registry names are namespaced (io.github.owner/thing); the last segment is the usable name.
    private func shortName(_ full: String) -> String {
        let tail = full.split(separator: "/").last.map(String.init) ?? full
        return tail.replacingOccurrences(of: " ", with: "-")
    }

    private func field<C: View>(_ label: String, @ViewBuilder content: () -> C) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            Text(label.uppercased()).font(.system(size: 10, weight: .semibold)).tracking(0.7)
                .foregroundStyle(palette.mutedForeground)
            content()
        }
    }

    private func load() {
        if let p = prefill, existing == nil {
            name = shortName(p.name)
            transport = p.transport
            command = p.command ?? ""
            argsText = (p.args ?? []).joined(separator: " ")
            url = p.url ?? ""
            // Seed the credential keys the registry says this server wants, with empty values, so
            // it's obvious what still needs filling in.
            envText = (p.envKeys ?? []).map { "\($0)=" }.joined(separator: "\n")
            return
        }
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


/// Search results from the public MCP registry.
struct MCPDirectoryView: View {
    @ObservedObject var model: Model
    let palette: OculusPalette
    var onPick: (MCPDirectoryEntry) -> Void
    var onClose: () -> Void

    @State private var query = ""

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack {
                VStack(alignment: .leading, spacing: 2) {
                    Text("Browse MCP servers").font(.headline).foregroundStyle(palette.foreground)
                    Text("From the public registry. You'll confirm before anything is saved.")
                        .font(.caption).foregroundStyle(palette.mutedForeground)
                }
                Spacer()
                Button("Done", action: onClose).keyboardShortcut(.cancelAction)
            }
            .padding(14)
            HStack {
                TextField("Search (e.g. github, postgres, slack)", text: $query)
                    .textFieldStyle(.roundedBorder)
                    .onSubmit { Task { await model.browseMCPDirectory(query: query) } }
                Button(model.mcpBrowsing ? "…" : "Search") {
                    Task { await model.browseMCPDirectory(query: query) }
                }
                .buttonStyle(.bordered).disabled(model.mcpBrowsing)
            }
            .padding(.horizontal, 14).padding(.bottom, 10)
            Divider().overlay(palette.border)

            if let err = model.mcpBrowseError, model.mcpDirectory.isEmpty {
                Text(err).font(.callout).foregroundStyle(palette.mutedForeground)
                    .frame(maxWidth: .infinity, maxHeight: .infinity).padding(24)
            } else {
                List(model.mcpDirectory) { e in
                    VStack(alignment: .leading, spacing: 4) {
                        HStack(spacing: 6) {
                            Text(e.name).font(.system(size: 13, weight: .medium))
                                .foregroundStyle(palette.foreground)
                            if let v = e.version, !v.isEmpty {
                                Text(v).font(.system(size: 10, design: .monospaced))
                                    .foregroundStyle(palette.mutedForeground)
                            }
                            Spacer()
                            if let u = e.unsupported, !u.isEmpty {
                                Text(u).font(.system(size: 10)).foregroundStyle(palette.mutedForeground)
                            } else {
                                Button("Add") { onPick(e) }.buttonStyle(.bordered).font(.system(size: 11))
                            }
                        }
                        if let d = e.description, !d.isEmpty {
                            Text(d).font(.system(size: 11)).foregroundStyle(palette.mutedForeground)
                                .lineLimit(3)
                        }
                        if let keys = e.envKeys, !keys.isEmpty {
                            Text("needs: " + keys.joined(separator: ", "))
                                .font(.system(size: 10, design: .monospaced))
                                .foregroundStyle(palette.mutedForeground)
                        }
                    }
                    .padding(.vertical, 3)
                }
                #if os(macOS)
                .listStyle(.inset)
                #endif
            }
        }
        .frame(minWidth: 520, minHeight: 400)
        .background(palette.background)
        .task { await model.browseMCPDirectory(query: "") }
    }
}
