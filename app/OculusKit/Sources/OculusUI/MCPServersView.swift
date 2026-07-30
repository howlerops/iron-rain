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
    @State private var query = ""
    @State private var filter: Filter = .all

    /// Filtering by STATE, not just text: with a lot of servers the question is usually "which ones
    /// are broken" or "what's actually on", and scanning dots for that is exactly the work the UI
    /// should be doing.
    enum Filter: Hashable { case all, enabled, attention, unchecked }

    public init(model: Model, palette: OculusPalette, onClose: (() -> Void)? = nil) {
        self.model = model; self.palette = palette; self.onClose = onClose
    }

    private var needsAttention: [MCPServerInfo] {
        model.mcpServers.filter { $0.checkedAt != nil && !$0.ok }
    }
    private var unchecked: [MCPServerInfo] { model.mcpServers.filter { $0.checkedAt == nil } }

    private var visible: [MCPServerInfo] {
        var out = model.mcpServers
        switch filter {
        case .all: break
        case .enabled: out = out.filter { $0.enabled }
        case .attention: out = out.filter { $0.checkedAt != nil && !$0.ok }
        case .unchecked: out = out.filter { $0.checkedAt == nil }
        }
        let q = query.trimmingCharacters(in: .whitespaces).lowercased()
        guard !q.isEmpty else { return out }
        // Search the tool names too — you often remember what a server DOES, not what it's called.
        return out.filter { s in
            if s.name.lowercased().contains(q) { return true }
            if (s.command ?? "").lowercased().contains(q) { return true }
            if (s.url ?? "").lowercased().contains(q) { return true }
            return (s.tools ?? []).contains { $0.name.lowercased().contains(q) }
        }
    }

    public var body: some View {
        OculusSheet(
            title: "MCP servers",
            subtitle: "Registered once here — every agent gets them.",
            palette: palette,
            actions: AnyView(headerActions),
            // The search bar only earns its space once there's enough to search.
            search: model.mcpServers.count >= 6 ? $query : nil,
            searchPrompt: "Search servers and tools",
            filters: model.mcpServers.count >= 6 ? AnyView(filterChips) : nil,
            onClose: onClose
        ) {
            if model.daemonOutdated { outdatedBanner }
            if !model.mcpFound.isEmpty { importBanner }

            if model.mcpServers.isEmpty && model.mcpFound.isEmpty {
                emptyState
            } else if visible.isEmpty {
                SheetEmptyState(icon: "line.3.horizontal.decrease.circle",
                                title: "Nothing matches",
                                message: query.isEmpty
                                    ? "No servers in this state."
                                    : "No server or tool matching “\(query)”.",
                                palette: palette) {
                    Button("Clear filters") { query = ""; filter = .all }.buttonStyle(.bordered)
                }
            } else {
                VStack(spacing: OculusSpace.sm) {
                    ForEach(visible) { row($0) }
                }
                if !model.mcpServers.isEmpty { exclusiveRow }
            }
        }
        .task {
            await model.loadMCPServers()
            await model.discoverMCPServers()
        }
        .sheet(isPresented: $adding) {
            MCPServerEditor(model: model, palette: palette, existing: nil, prefill: nil, onClose: { adding = false })
        }
        .sheet(isPresented: $browsing) {
            MCPDirectoryView(model: model, palette: palette,
                             onPick: { entry in browsing = false; prefill = entry },
                             onClose: { browsing = false })
        }
        .sheet(item: $prefill) { entry in
            MCPServerEditor(model: model, palette: palette, existing: nil, prefill: entry, onClose: { prefill = nil })
        }
        .sheet(item: $editing) { server in
            MCPServerEditor(model: model, palette: palette, existing: server, prefill: nil, onClose: { editing = nil })
        }
    }

    private var headerActions: some View {
        HStack(spacing: OculusSpace.sm) {
            Button { browsing = true } label: { Label("Browse", systemImage: "magnifyingglass") }
            Button { adding = true } label: { Label("Add", systemImage: "plus") }
        }
        .controlSize(.small)
    }

    private var filterChips: some View {
        FilterChips(selection: $filter, options: [
            .init(value: .all, label: "All", count: model.mcpServers.count),
            .init(value: .enabled, label: "On", count: model.mcpServers.filter(\.enabled).count),
            .init(value: .attention, label: "Needs attention", count: needsAttention.count),
            .init(value: .unchecked, label: "Untested", count: unchecked.count),
        ], palette: palette)
    }

    /// An out-of-date daemon used to look EXACTLY like an empty screen: the app sends mcp.list, the
    /// daemon answers "unknown type", the `try?` swallows it, and nothing renders. Saying so directly
    /// is the difference between "this feature is broken" and "restart your daemon".
    private var outdatedBanner: some View {
        SheetCard(palette: palette, tint: Color(hex: 0xE0912A)) {
            HStack(alignment: .top, spacing: OculusSpace.sm) {
                Image(systemName: "exclamationmark.triangle.fill").foregroundStyle(Color(hex: 0xE0912A))
                VStack(alignment: .leading, spacing: OculusSpace.xxs) {
                    Text("Your daemon is older than this app")
                        .font(.system(size: 12.5, weight: .medium)).foregroundStyle(palette.foreground)
                    Text("It doesn't know about MCP servers yet, so nothing here will work. Quit and reopen Iron Rain to restart the daemon — it updates itself on start.")
                        .font(.system(size: 11.5)).foregroundStyle(palette.mutedForeground)
                        .fixedSize(horizontal: false, vertical: true)
                }
            }
        }
    }

    /// Servers your agents already have. Offered rather than absorbed: each one carries a command
    /// that will run with your credentials, so it gets a look first.
    private var importBanner: some View {
        SheetCard(palette: palette, tint: palette.primary) {
            HStack(spacing: OculusSpace.sm) {
                Image(systemName: "arrow.down.circle").foregroundStyle(palette.primary)
                Text("\(model.mcpFound.count) server\(model.mcpFound.count == 1 ? "" : "s") already set up in your agents")
                    .font(.system(size: 12.5, weight: .medium)).foregroundStyle(palette.foreground)
                Spacer(minLength: OculusSpace.sm)
                Button("Import all") {
                    Task { await model.importMCPServers(names: model.mcpFound.map(\.name)) }
                }
                .buttonStyle(.borderedProminent).tint(palette.primary).controlSize(.small)
            }
            VStack(spacing: OculusSpace.xs) {
                ForEach(model.mcpFound) { f in
                    HStack(spacing: OculusSpace.sm) {
                        VStack(alignment: .leading, spacing: 1) {
                            Text(f.name).font(.system(size: 12)).foregroundStyle(palette.foreground)
                            Text(f.source).font(.system(size: 10)).foregroundStyle(palette.mutedForeground)
                                .lineLimit(1).truncationMode(.middle)
                        }
                        Spacer(minLength: OculusSpace.sm)
                        if let keys = f.envKeys, !keys.isEmpty {
                            Text(keys.joined(separator: ", "))
                                .font(.system(size: 9.5, design: .monospaced))
                                .foregroundStyle(palette.mutedForeground)
                                .lineLimit(1).truncationMode(.tail).frame(maxWidth: 140, alignment: .trailing)
                        }
                        Button("Import") { Task { await model.importMCPServers(names: [f.name]) } }
                            .buttonStyle(.bordered).controlSize(.small)
                    }
                }
            }
            Text("Importing copies the definition here. Turn the original off in that agent's own config so it isn't started twice — or use the switch below to have Iron Rain manage MCP for all of them.")
                .font(.system(size: 11)).foregroundStyle(palette.mutedForeground)
                .fixedSize(horizontal: false, vertical: true)
        }
        .transition(.opacity)
    }

    private func row(_ s: MCPServerInfo) -> some View {
        SheetCard(palette: palette) {
            HStack(spacing: OculusSpace.sm) {
                statusDot(s)
                Text(s.name).font(.system(size: 13, weight: .medium)).foregroundStyle(palette.foreground)
                if let v = s.serverVersion, !v.isEmpty {
                    Text(v).font(.system(size: 10, design: .monospaced)).foregroundStyle(palette.mutedForeground)
                }
                if let p = s.protocolVersion, !p.isEmpty { tag(p) }
                if let pid = s.projectID, !pid.isEmpty { tag("one project") }
                Spacer(minLength: OculusSpace.sm)
                Toggle("", isOn: Binding(
                    get: { s.enabled },
                    set: { on in Task { await model.setMCPServerEnabled(name: s.name, enabled: on) } }
                ))
                .labelsHidden().toggleStyle(.switch).tint(palette.primary).controlSize(.mini)
            }

            Text(commandLine(s))
                .font(.system(size: 10.5, design: .monospaced))
                .foregroundStyle(palette.mutedForeground)
                .lineLimit(1).truncationMode(.middle)
                .frame(maxWidth: .infinity, alignment: .leading)

            if let err = s.error, !err.isEmpty {
                // The server's own stderr — usually the only clue about what's wrong.
                Text(err)
                    .font(.system(size: 10.5, design: .monospaced))
                    .foregroundStyle(palette.destructive)
                    .lineLimit(3).fixedSize(horizontal: false, vertical: true)
            } else if let tools = s.tools, !tools.isEmpty {
                Text(tools.prefix(8).map(\.name).joined(separator: " · ") + (tools.count > 8 ? " +\(tools.count - 8) more" : ""))
                    .font(.system(size: 10.5)).foregroundStyle(palette.mutedForeground)
                    .lineLimit(2).fixedSize(horizontal: false, vertical: true)
            } else {
                Text("Not checked yet — Test to connect and list its tools.")
                    .font(.system(size: 10.5)).italic().foregroundStyle(palette.mutedForeground)
            }

            HStack(spacing: OculusSpace.sm) {
                Spacer(minLength: 0)
                Button(checking.contains(s.name) ? "Testing…" : "Test") {
                    checking.insert(s.name)
                    Task {
                        await model.checkMCPServer(name: s.name)
                        checking.remove(s.name)
                    }
                }
                .disabled(checking.contains(s.name))
                Button("Edit") { editing = s }
                Button(role: .destructive) {
                    Task { await model.deleteMCPServer(name: s.name) }
                } label: { Image(systemName: "trash") }
                .buttonStyle(.plain).foregroundStyle(palette.mutedForeground)
            }
            .buttonStyle(.bordered).controlSize(.small)
        }
        .opacity(s.enabled ? 1 : 0.55)
        .animation(.easeOut(duration: 0.15), value: s.enabled)
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
            .padding(.horizontal, OculusSpace.xs).padding(.vertical, 1.5)
            .background(palette.input).clipShape(RoundedRectangle(cornerRadius: OculusRadius.sm))
            .foregroundStyle(palette.mutedForeground)
    }

    /// The dedupe switch. Off by default because turning it on when servers haven't been imported
    /// would silently remove tools the user relies on.
    private var exclusiveRow: some View {
        SheetCard(palette: palette) {
            Toggle(isOn: Binding(
                get: { model.mcpExclusive },
                set: { on in Task { await model.setMCPExclusive(on) } }
            )) {
                Text("Iron Rain manages MCP for my agents").font(.system(size: 12.5))
            }
            .toggleStyle(.switch).tint(palette.primary)
            Text(model.mcpExclusive
                 ? "Your agents ignore their own MCP config and use only the servers above — one process per server."
                 : "Your agents ALSO load their own MCP config. A server configured in both places runs twice.")
                .font(.system(size: 11)).foregroundStyle(palette.mutedForeground)
                .fixedSize(horizontal: false, vertical: true)
        }
    }

    private var emptyState: some View {
        SheetEmptyState(icon: "puzzlepiece.extension",
                        title: "No MCP servers",
                        message: "Add a server once and every agent — opencode, Claude Code, and any CLI agent you've configured — gets its tools. Credentials stay on this Mac.",
                        palette: palette) {
            HStack(spacing: OculusSpace.sm) {
                Button { browsing = true } label: { Label("Browse the registry", systemImage: "magnifyingglass") }
                    .buttonStyle(.borderedProminent).tint(palette.primary)
                Button { adding = true } label: { Label("Add manually", systemImage: "plus") }
                    .buttonStyle(.bordered)
            }
        }
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
        OculusSheet(
            title: existing == nil ? "Add MCP server" : "Edit \(existing!.name)",
            subtitle: transport == "stdio"
                ? "A command this Mac runs. It executes with your credentials."
                : "A hosted endpoint the daemon connects to.",
            palette: palette
        ) {
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
                SheetCard(palette: palette, tint: palette.destructive) {
                    Text(error).font(.system(size: 11.5)).foregroundStyle(palette.destructive)
                        .fixedSize(horizontal: false, vertical: true)
                }
            }

            HStack(spacing: OculusSpace.sm) {
                Spacer()
                Button("Cancel", action: onClose).keyboardShortcut(.cancelAction)
                Button(saving ? "Saving…" : "Save") { save() }
                    .buttonStyle(.borderedProminent).tint(palette.primary)
                    .keyboardShortcut(.defaultAction)
                    .disabled(saving || name.trimmingCharacters(in: .whitespaces).isEmpty)
            }
            .padding(.top, OculusSpace.xs)
        }
        .onAppear(perform: load)
    }

    /// Registry names are namespaced (io.github.owner/thing); the last segment is the usable name.
    private func shortName(_ full: String) -> String {
        let tail = full.split(separator: "/").last.map(String.init) ?? full
        return tail.replacingOccurrences(of: " ", with: "-")
    }

    private func field<C: View>(_ label: String, @ViewBuilder content: () -> C) -> some View {
        VStack(alignment: .leading, spacing: OculusSpace.xs) {
            Text(label.uppercased()).font(.system(size: 10, weight: .semibold)).tracking(0.7)
                .foregroundStyle(palette.mutedForeground)
            content()
        }
        .frame(maxWidth: .infinity, alignment: .leading)
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
        OculusSheet(
            title: "Browse MCP servers",
            subtitle: "From the public registry. You'll confirm before anything is saved.",
            palette: palette,
            search: $query,
            searchPrompt: "Search (github, postgres, slack…)",
            onClose: onClose
        ) {
            if model.mcpBrowsing && model.mcpDirectory.isEmpty {
                HStack(spacing: OculusSpace.sm) {
                    ProgressView().controlSize(.small)
                    Text("Searching the registry…").font(.system(size: 12))
                        .foregroundStyle(palette.mutedForeground)
                }
                .frame(maxWidth: .infinity).padding(.vertical, OculusSpace.xxl)
            } else if let err = model.mcpBrowseError, model.mcpDirectory.isEmpty {
                SheetEmptyState(icon: "magnifyingglass",
                                title: "Nothing found",
                                message: err,
                                palette: palette)
            } else {
                VStack(spacing: OculusSpace.sm) {
                    ForEach(model.mcpDirectory) { entry($0) }
                }
            }
        }
        // Debounced live search: typing shouldn't fire a request per keystroke, and pressing Enter
        // shouldn't be required to see results.
        .task(id: query) {
            try? await Task.sleep(nanoseconds: 350_000_000)
            guard !Task.isCancelled else { return }
            await model.browseMCPDirectory(query: query)
        }
    }

    private func entry(_ e: MCPDirectoryEntry) -> some View {
        SheetCard(palette: palette) {
            HStack(spacing: OculusSpace.xs) {
                Text(e.name).font(.system(size: 12.5, weight: .medium))
                    .foregroundStyle(palette.foreground).lineLimit(1).truncationMode(.middle)
                if let v = e.version, !v.isEmpty {
                    Text(v).font(.system(size: 10, design: .monospaced))
                        .foregroundStyle(palette.mutedForeground)
                }
                Spacer(minLength: OculusSpace.sm)
                if let u = e.unsupported, !u.isEmpty {
                    Text(u).font(.system(size: 10)).foregroundStyle(palette.mutedForeground)
                        .lineLimit(1).truncationMode(.tail).frame(maxWidth: 200, alignment: .trailing)
                } else {
                    Button("Add") { onPick(e) }.buttonStyle(.bordered).controlSize(.small)
                }
            }
            if let d = e.description, !d.isEmpty {
                Text(d).font(.system(size: 11)).foregroundStyle(palette.mutedForeground)
                    .lineLimit(3).fixedSize(horizontal: false, vertical: true)
            }
            if let keys = e.envKeys, !keys.isEmpty {
                Text("needs: " + keys.joined(separator: ", "))
                    .font(.system(size: 10, design: .monospaced))
                    .foregroundStyle(palette.mutedForeground)
                    .lineLimit(1).truncationMode(.tail)
            }
        }
    }
}
