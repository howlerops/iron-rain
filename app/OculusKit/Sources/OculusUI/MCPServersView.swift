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

    @State private var checking: Set<String> = []
    @State private var query = ""
    @State private var filter: Filter = .all
    /// The server a delete is staged against. Removing one destroys the credentials stored with it,
    /// and nothing in the app can put them back — so it never happens on a single tap.
    @State private var pendingDelete: MCPServerInfo? = nil
    /// The registry entry a prefilled editor is opening from. Held beside the route rather than in
    /// it: a `NavigationStack` path element has to be Hashable, and a directory entry isn't.
    @State private var prefill: MCPDirectoryEntry? = nil

    /// The child screens. This used to be four independent sheets, and reaching the editor for a
    /// server found in the registry meant three modals stacked over the list. Worse, the browse →
    /// prefill hop dismissed one sheet and presented another in the same tick, which is a race by
    /// construction: whichever presentation lands while the first is still animating away is simply
    /// dropped, and the user taps Add in the registry and nothing happens.
    ///
    /// One route, so at most one child exists at a time. On iOS it is PUSHED, which is both the
    /// platform idiom and a single state change with nothing to dismiss.
    private enum Route: Hashable, Identifiable {
        case add
        case browse
        case prefill
        case edit(String)

        var id: String {
            switch self {
            case .add: return "add"
            case .browse: return "browse"
            case .prefill: return "prefill"
            case .edit(let name): return "edit:" + name
            }
        }
    }

    #if os(iOS)
    @State private var path: [Route] = []
    #else
    @State private var route: Route? = nil
    #endif

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

    /// The native list is only the right shape once there is something to list. With nothing
    /// registered the sheet is one centred empty state, which is not a list row.
    private var usesList: Bool {
        #if os(iOS)
        return !(model.mcpServers.isEmpty && model.mcpFound.isEmpty)
        #else
        return false
        #endif
    }

    public var body: some View {
        #if os(iOS)
        NavigationStack(path: $path) {
            core
                // The scaffold already draws this screen's title and its Done button; a navigation
                // bar on top of it would be a second, empty title.
                .toolbar(.hidden, for: .navigationBar)
                .navigationDestination(for: Route.self) { child($0) }
        }
        #else
        core.sheet(item: $route) { child($0) }
        #endif
    }

    private var core: some View {
        OculusSheet(
            title: "MCP servers",
            subtitle: "Registered once here — every agent gets them.",
            palette: palette,
            actions: AnyView(headerActions),
            // The search bar only earns its space once there's enough to search.
            search: model.mcpServers.count >= 6 ? $query : nil,
            searchPrompt: "Search servers and tools",
            filters: model.mcpServers.count >= 6 ? AnyView(filterChips) : nil,
            onClose: onClose,
            scrolls: !usesList
        ) {
            content
        }
        .task {
            await model.loadMCPServers()
            await model.discoverMCPServers()
        }
        .confirmationDialog(
            "Remove this server?",
            isPresented: Binding(get: { pendingDelete != nil }, set: { if !$0 { pendingDelete = nil } }),
            titleVisibility: .visible,
            presenting: pendingDelete
        ) { s in
            Button("Remove server", role: .destructive) { delete(s) }
            Button("Cancel", role: .cancel) { pendingDelete = nil }
        } message: { s in
            Text(deleteWarning(s))
        }
    }

    @ViewBuilder private var content: some View {
        #if os(iOS)
        if usesList { serverList } else { cardBody }
        #else
        cardBody
        #endif
    }

    // MARK: - Routing

    private func open(_ r: Route) {
        #if os(iOS)
        path.append(r)
        #else
        // A single assignment, not "dismiss this one then present that one". Swapping the item of
        // one sheet is what makes browse → prefill a state change instead of a race.
        route = r
        #endif
    }

    private func closeChild() {
        #if os(iOS)
        if !path.isEmpty { path.removeLast() }
        #else
        route = nil
        #endif
    }

    /// Back to the server list from anywhere. The prefill editor is reached THROUGH the registry, but
    /// leaving it — saved or cancelled — has always returned to the list, not to the search results.
    private func closeToRoot() {
        #if os(iOS)
        path.removeAll()
        #else
        route = nil
        #endif
    }

    /// True where children are pushed rather than presented, so they drop their own header and let
    /// the stack supply the title.
    private var pushes: Bool {
        #if os(iOS)
        return true
        #else
        return false
        #endif
    }

    @ViewBuilder private func child(_ r: Route) -> some View {
        switch r {
        case .add:
            MCPServerEditor(model: model, palette: palette, existing: nil, prefill: nil,
                            pushed: pushes, onClose: closeChild)
        case .browse:
            MCPDirectoryView(model: model, palette: palette, pushed: pushes,
                             onPick: { entry in prefill = entry; open(.prefill) },
                             onClose: closeChild)
        case .prefill:
            MCPServerEditor(model: model, palette: palette, existing: nil, prefill: prefill,
                            pushed: pushes, onClose: closeToRoot)
        case .edit(let name):
            // Looked up by name rather than carried: the daemon replaces the whole server list on
            // every mutation, so a captured struct goes stale the moment anything else changes.
            if let s = model.mcpServers.first(where: { $0.name == name }) {
                MCPServerEditor(model: model, palette: palette, existing: s, prefill: nil,
                                pushed: pushes, onClose: closeChild)
            }
        }
    }

    // MARK: - Body variants

    /// The macOS shape: a scrolling column of bordered cards.
    @ViewBuilder private var cardBody: some View {
        if model.daemonOutdated { outdatedBanner }
        if !model.mcpFound.isEmpty { importBanner }

        if model.mcpServers.isEmpty && model.mcpFound.isEmpty {
            emptyState
        } else if visible.isEmpty {
            noMatches
        } else {
            VStack(spacing: OculusSpace.sm) {
                ForEach(visible) { s in
                    SheetCard(palette: palette) { rowBody(s, inList: false) }
                        .opacity(s.enabled ? 1 : 0.55)
                        .animation(.easeOut(duration: 0.15), value: s.enabled)
                }
            }
            if !model.mcpServers.isEmpty { exclusiveCard }
        }
    }

    #if os(iOS)
    /// The iOS shape: the platform's grouped list. Sections carry the headers the cards used to
    /// imply, and the swipe restores the delete gesture the hand-rolled cards had no way to offer.
    private var serverList: some View {
        List {
            if model.daemonOutdated { bannerSection { outdatedBanner } }
            if !model.mcpFound.isEmpty { bannerSection { importBanner } }

            Section("Servers") {
                if visible.isEmpty {
                    noMatches
                } else {
                    ForEach(visible) { s in
                        rowBody(s, inList: true)
                            .opacity(s.enabled ? 1 : 0.55)
                            .animation(.easeOut(duration: 0.15), value: s.enabled)
                            .sheetSwipeDelete("Remove") { pendingDelete = s }
                    }
                }
            }

            if !model.mcpServers.isEmpty {
                Section {
                    exclusiveToggle
                } footer: {
                    exclusiveNote
                }
            }
        }
        .sheetListChrome(palette)
    }

    /// A banner keeps its card border, so it must not also get a list row's background and insets —
    /// that draws a box inside a box.
    private func bannerSection<V: View>(@ViewBuilder _ content: () -> V) -> some View {
        Section {
            content()
                .listRowInsets(EdgeInsets(top: OculusSpace.xs, leading: OculusSpace.md,
                                          bottom: OculusSpace.xs, trailing: OculusSpace.md))
                .listRowBackground(Color.clear)
        }
    }
    #endif

    private var noMatches: some View {
        SheetEmptyState(icon: "line.3.horizontal.decrease.circle",
                        title: "Nothing matches",
                        message: query.isEmpty
                            ? "No servers in this state."
                            : "No server or tool matching “\(query)”.",
                        palette: palette) {
            Button("Clear filters") { query = ""; filter = .all }.buttonStyle(.bordered)
        }
    }

    /// The credentials are the part that can't be re-derived: the argv is in the registry, an API key
    /// typed in here exists nowhere else. Say so when there is one.
    private func deleteWarning(_ s: MCPServerInfo) -> String {
        let keys = (s.env ?? [:]).keys.sorted()
        let base = "“\(s.name)” will be removed, and every agent loses its tools."
        guard !keys.isEmpty else { return base + " You can add it again later." }
        return base + " The credentials stored with it (\(keys.joined(separator: ", "))) are deleted with it and can't be recovered — you'd have to re-enter them."
    }

    /// `deleteMCPServer` returns Void and swallows its transport error, so a delete that never
    /// reached the daemon is indistinguishable from one that did. The daemon replies with the whole
    /// server list, so a list that still contains the server is the honest signal it didn't happen.
    private func delete(_ s: MCPServerInfo) {
        pendingDelete = nil
        Task {
            await model.deleteMCPServer(name: s.name)
            if model.mcpServers.contains(where: { $0.name == s.name }) {
                model.setError("Couldn’t remove \(s.name)",
                               "It's still registered. Check the daemon is connected and try again.")
            }
        }
    }

    private var headerActions: some View {
        HStack(spacing: OculusSpace.sm) {
            Button { open(.browse) } label: { Label("Browse", systemImage: "magnifyingglass") }
            Button { open(.add) } label: { Label("Add", systemImage: "plus") }
        }
        // Shrinking controls is a Mac idiom; on a phone .small lands well under the 44pt target.
        #if os(macOS)
        .controlSize(.small)
        #endif
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
        SheetCard(palette: palette, tint: palette.warning) {
            HStack(alignment: .top, spacing: OculusSpace.sm) {
                Image(systemName: "exclamationmark.triangle.fill").foregroundStyle(palette.warning)
                VStack(alignment: .leading, spacing: OculusSpace.xxs) {
                    Text("Your daemon is older than this app")
                        .font(.footnote.weight(.medium)).foregroundStyle(palette.foreground)
                    Text("It doesn't know about MCP servers yet, so nothing here will work. Quit and reopen Iron Rain to restart the daemon — it updates itself on start.")
                        .font(.caption).foregroundStyle(palette.mutedForeground)
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
                    .font(.footnote.weight(.medium)).foregroundStyle(palette.foreground)
                Spacer(minLength: OculusSpace.sm)
                Button("Import all") {
                    Task { await model.importMCPServers(names: model.mcpFound.map(\.name)) }
                }
                .buttonStyle(.borderedProminent).tint(palette.primary)
                #if os(macOS)
                .controlSize(.small)
                #endif
            }
            VStack(spacing: OculusSpace.xs) {
                ForEach(model.mcpFound) { f in
                    HStack(spacing: OculusSpace.sm) {
                        VStack(alignment: .leading, spacing: 1) {
                            Text(f.name).font(.footnote).foregroundStyle(palette.foreground)
                            Text(f.source).font(.caption2).foregroundStyle(palette.mutedForeground)
                                .lineLimit(1).truncationMode(.middle)
                        }
                        Spacer(minLength: OculusSpace.sm)
                        if let keys = f.envKeys, !keys.isEmpty {
                            Text(keys.joined(separator: ", "))
                                .font(.caption2.monospaced())
                                .foregroundStyle(palette.mutedForeground)
                                .lineLimit(1).truncationMode(.tail).frame(maxWidth: 140, alignment: .trailing)
                        }
                        Button("Import") { Task { await model.importMCPServers(names: [f.name]) } }
                            .buttonStyle(.bordered)
                            #if os(macOS)
                            .controlSize(.small)
                            #endif
                    }
                }
            }
            Text("Importing copies the definition here. Turn the original off in that agent's own config so it isn't started twice — or use the switch below to have Iron Rain manage MCP for all of them.")
                .font(.caption).foregroundStyle(palette.mutedForeground)
                .fixedSize(horizontal: false, vertical: true)
        }
        .transition(.opacity)
    }

    /// One server, shared by both shapes. `inList` drops the trash button: in a List the delete is
    /// the swipe (which stages the same confirmation), and a second target in an already-crowded row
    /// is just another thing to hit by accident.
    @ViewBuilder private func rowBody(_ s: MCPServerInfo, inList: Bool) -> some View {
        VStack(alignment: .leading, spacing: OculusSpace.sm) {
            HStack(spacing: OculusSpace.sm) {
                statusDot(s)
                Text(s.name).font(.subheadline.weight(.medium)).foregroundStyle(palette.foreground)
                if let v = s.serverVersion, !v.isEmpty {
                    Text(v).font(.caption2.monospaced()).foregroundStyle(palette.mutedForeground)
                }
                if let p = s.protocolVersion, !p.isEmpty { tag(p) }
                if let pid = s.projectID, !pid.isEmpty { tag("one project") }
                Spacer(minLength: OculusSpace.sm)
                // `Toggle("", …)` announces as a switch with NO referent — in a list of servers,
                // VoiceOver said only "off, switch". labelsHidden() keeps the accessibility label
                // while hiding the visual one, which is what we wanted all along.
                Toggle("Enable \(s.name)", isOn: Binding(
                    get: { s.enabled },
                    set: { on in setEnabled(s, on) }
                ))
                .labelsHidden().toggleStyle(.switch).tint(palette.primary)
                #if os(macOS)
                .controlSize(.mini)
                #endif
            }

            Text(commandLine(s))
                .font(.caption.monospaced())
                .foregroundStyle(palette.mutedForeground)
                .lineLimit(1).truncationMode(.middle)
                .frame(maxWidth: .infinity, alignment: .leading)

            if let err = s.error, !err.isEmpty {
                serverError(s, err)
            } else if let tools = s.tools, !tools.isEmpty {
                Text(tools.prefix(8).map(\.name).joined(separator: " · ") + (tools.count > 8 ? " +\(tools.count - 8) more" : ""))
                    .font(.caption).foregroundStyle(palette.mutedForeground)
                    .lineLimit(2).fixedSize(horizontal: false, vertical: true)
            } else {
                Text("Not checked yet — Test to connect and list its tools.")
                    .font(.caption).italic().foregroundStyle(palette.mutedForeground)
            }

            HStack(spacing: OculusSpace.sm) {
                Spacer(minLength: 0)
                Button(checking.contains(s.name) ? "Testing…" : "Test") {
                    check(s)
                }
                .disabled(checking.contains(s.name))
                Button("Edit") { open(.edit(s.name)) }
                if !inList {
                    Button(role: .destructive) {
                        pendingDelete = s
                    } label: { Image(systemName: "trash") }
                    .buttonStyle(.plain).foregroundStyle(palette.destructive)
                    .accessibilityLabel("Remove \(s.name)")
                    .sheetTapTarget()
                }
            }
            .buttonStyle(.bordered)
            #if os(macOS)
            .controlSize(.small)
            #endif
        }
    }

    /// The server's own stderr — usually the only clue about what's wrong, and previously three
    /// clipped lines you could neither copy nor act on. Selectable, and paired with the two things
    /// you'd do next, the way `outdatedBanner` names its own fix.
    private func serverError(_ s: MCPServerInfo, _ err: String) -> some View {
        VStack(alignment: .leading, spacing: OculusSpace.xs) {
            Text("This server didn't start. The message below is its own output — usually a missing command or a rejected credential.")
                .font(.caption).foregroundStyle(palette.foreground)
                .fixedSize(horizontal: false, vertical: true)
            Text(err)
                .font(.caption.monospaced())
                .foregroundStyle(palette.destructive)
                .lineLimit(6).fixedSize(horizontal: false, vertical: true)
                .textSelection(.enabled)
            HStack(spacing: OculusSpace.sm) {
                Button(checking.contains(s.name) ? "Testing…" : "Test again") { check(s) }
                    .disabled(checking.contains(s.name))
                Button("Edit") { open(.edit(s.name)) }
            }
            .buttonStyle(.bordered)
            #if os(macOS)
            .controlSize(.small)
            #endif
        }
    }

    private func check(_ s: MCPServerInfo) {
        checking.insert(s.name)
        Task {
            await model.checkMCPServer(name: s.name)
            checking.remove(s.name)
        }
    }

    /// NOTE: a failed toggle cannot be detected here. `setMCPServerEnabled` writes the optimistic
    /// value into `mcpServers` BEFORE the request and returns Void either way, so on failure the row
    /// keeps showing the state the user asked for while the daemon still injects the old one. The fix
    /// has to be in the model — it needs to return `String?` like `upsertMCPServer` does.
    private func setEnabled(_ s: MCPServerInfo, _ on: Bool) {
        Task { await model.setMCPServerEnabled(name: s.name, enabled: on) }
    }

    private func statusDot(_ s: MCPServerInfo) -> some View {
        Circle()
            .fill(s.checkedAt == nil ? palette.mutedForeground.opacity(0.4)
                  : (s.ok ? palette.success : palette.destructive))
            .frame(width: 7, height: 7)
            .accessibilityLabel(s.checkedAt == nil ? "Not tested"
                                : (s.ok ? "Working" : "Not working"))
    }

    private func commandLine(_ s: MCPServerInfo) -> String {
        if s.transport == "http" { return s.url ?? "" }
        return ([s.command ?? ""] + (s.args ?? [])).joined(separator: " ")
    }

    private func tag(_ t: String) -> some View {
        Text(t).font(.caption2.monospaced())
            .padding(.horizontal, OculusSpace.xs).padding(.vertical, 1.5)
            .background(palette.input).clipShape(OculusShape.rounded(OculusRadius.sm))
            .foregroundStyle(palette.mutedForeground)
    }

    /// The dedupe switch. Off by default because turning it on when servers haven't been imported
    /// would silently remove tools the user relies on.
    private var exclusiveToggle: some View {
        Toggle(isOn: Binding(
            get: { model.mcpExclusive },
            set: { on in Task { await model.setMCPExclusive(on) } }
        )) {
            Text("Iron Rain manages MCP for my agents").font(.footnote)
        }
        .toggleStyle(.switch).tint(palette.primary)
    }

    private var exclusiveNote: some View {
        Text(model.mcpExclusive
             ? "Your agents ignore their own MCP config and use only the servers above — one process per server."
             : "Your agents ALSO load their own MCP config. A server configured in both places runs twice.")
            .font(.caption).foregroundStyle(palette.mutedForeground)
            .fixedSize(horizontal: false, vertical: true)
    }

    private var exclusiveCard: some View {
        SheetCard(palette: palette) {
            exclusiveToggle
            exclusiveNote
        }
    }

    private var emptyState: some View {
        SheetEmptyState(icon: "puzzlepiece.extension",
                        title: "No MCP servers",
                        message: "Add a server once and every agent — opencode, Claude Code, and any CLI agent you've configured — gets its tools. Credentials stay on this Mac.",
                        palette: palette) {
            HStack(spacing: OculusSpace.sm) {
                Button { open(.browse) } label: { Label("Browse the registry", systemImage: "magnifyingglass") }
                    .buttonStyle(.borderedProminent).tint(palette.primary)
                Button { open(.add) } label: { Label("Add manually", systemImage: "plus") }
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
    /// Pushed onto a navigation stack rather than presented. The stack supplies the title bar, so
    /// the scaffold header and the in-content button row would both be duplicates of it.
    var pushed: Bool = false
    var onClose: () -> Void

    @State private var name = ""
    @State private var transport = "stdio"
    @State private var command = ""
    @State private var argsText = ""
    @State private var url = ""
    @State private var envText = ""
    @State private var error: String? = nil
    @State private var saving = false
    /// What `load()` put in the fields. Anything different from this is unsaved work.
    @State private var initial: [String] = []
    @State private var confirmDiscard = false
    @FocusState private var focus: Field?

    private enum Field: Hashable { case name, command, args, url, env }

    private var current: [String] { [name, transport, command, argsText, url, envText] }
    /// A dirty draft is the thing worth protecting: the env box can hold a hand-typed API key that
    /// exists nowhere else, so a swipe-dismiss here is unrecoverable data loss, not an inconvenience.
    private var dirty: Bool { !initial.isEmpty && current != initial }

    private var title: String { existing == nil ? "Add MCP server" : "Edit \(existing!.name)" }

    var body: some View {
        form
            .onAppear {
                load()
                initial = current
                // The first meaningful field: the name on a new server, the thing you came to change
                // on an existing one (the name is fixed once set).
                focus = existing == nil ? .name : (transport == "stdio" ? .command : .url)
            }
            .sheetDraftGuard(dirty)
            .confirmationDialog("Discard changes?", isPresented: $confirmDiscard, titleVisibility: .visible) {
                Button("Discard", role: .destructive, action: onClose)
                Button("Keep editing", role: .cancel) {}
            } message: {
                Text(envText.isEmpty
                     ? "Your edits to this server won't be saved."
                     : "Your edits won't be saved — including anything typed into Environment, which isn't stored anywhere else.")
            }
            #if os(iOS)
            .navigationTitle(pushed ? title : "")
            .navigationBarTitleDisplayMode(.inline)
            // Back would leave WITHOUT the discard confirmation, which is the one thing the guard
            // exists to prevent: the env box can hold an API key that exists nowhere else. Cancel is
            // the only way out of a pushed editor, and Cancel asks.
            .navigationBarBackButtonHidden(pushed)
            .toolbar { pushedActions }
            #endif
    }

    #if os(iOS)
    @ToolbarContentBuilder private var pushedActions: some ToolbarContent {
        if pushed {
            ToolbarItem(placement: .cancellationAction) {
                Button("Cancel") { cancel() }
            }
            ToolbarItem(placement: .confirmationAction) {
                Button(saving ? "Saving…" : "Save") { save() }.disabled(saving)
            }
        }
    }
    #endif

    private var form: some View {
        OculusSheet(
            title: title,
            subtitle: transport == "stdio"
                ? "A command this Mac runs. It executes with your credentials."
                : "A hosted endpoint the daemon connects to.",
            palette: palette,
            showsHeader: !pushed
        ) {
            field("Name", required: true) {
                TextField("github", text: $name).textFieldStyle(.roundedBorder)
                    .disabled(existing != nil) // the name is the identity
                    .plainInput().focused($focus, equals: .name)
                    .submitLabel(.next)
                    .onSubmit { focus = transport == "stdio" ? .command : .url }
            }
            Picker("", selection: $transport) {
                Text("Local command").tag("stdio")
                Text("Remote URL").tag("http")
            }
            .pickerStyle(.segmented).labelsHidden()

            if transport == "stdio" {
                field("Command", required: true) {
                    TextField("npx", text: $command).textFieldStyle(.roundedBorder)
                        .plainInput().focused($focus, equals: .command)
                        .submitLabel(.next).onSubmit { focus = .args }
                }
                field("Arguments") {
                    TextField("-y @modelcontextprotocol/server-github", text: $argsText)
                        .textFieldStyle(.roundedBorder)
                        .plainInput().focused($focus, equals: .args)
                        .submitLabel(.next).onSubmit { focus = .env }
                    Text("Separated by spaces.").font(.caption).foregroundStyle(palette.mutedForeground)
                }
            } else {
                field("URL", required: true) {
                    TextField("https://mcp.example.com/mcp", text: $url).textFieldStyle(.roundedBorder)
                        .plainInput().focused($focus, equals: .url)
                        .submitLabel(.next).onSubmit { focus = .env }
                        #if os(iOS)
                        .keyboardType(.URL)
                        #endif
                }
            }

            field("Environment") {
                TextEditor(text: $envText)
                    .font(.system(.footnote, design: .monospaced))
                    .frame(minHeight: 70)
                    .plainInput().focused($focus, equals: .env)
                    .overlay(OculusShape.rounded(6).strokeBorder(palette.border))
                Text("One KEY=value per line. Stored on this Mac only, readable by you alone. Existing secrets show as •••• and are kept unless you replace them.")
                    .font(.caption).foregroundStyle(palette.mutedForeground)
            }

            if let error {
                SheetCard(palette: palette, tint: palette.destructive) {
                    Text(error).font(.footnote).foregroundStyle(palette.destructive)
                        .fixedSize(horizontal: false, vertical: true)
                        .textSelection(.enabled)
                }
            }

            if !pushed {
                HStack(spacing: OculusSpace.sm) {
                    Spacer()
                    Button("Cancel") { cancel() }.keyboardShortcut(.cancelAction)
                    // Kept ENABLED and validated on tap. A dead Save teaches nothing: the user can
                    // see it won't work but not which of name/command/URL is the reason.
                    Button(saving ? "Saving…" : "Save") { save() }
                        .buttonStyle(.borderedProminent).tint(palette.primary)
                        .keyboardShortcut(.defaultAction)
                        .disabled(saving)
                }
                .padding(.top, OculusSpace.xs)
            }
        }
    }

    private func cancel() {
        if dirty { confirmDiscard = true } else { onClose() }
    }

    /// Names the field that's missing rather than greying the button out. Returns nil when valid.
    private func firstProblem() -> String? {
        if name.trimmingCharacters(in: .whitespaces).isEmpty {
            return "Give the server a name — it's how agents refer to it."
        }
        if transport == "stdio", command.trimmingCharacters(in: .whitespaces).isEmpty {
            return "Enter the command that starts the server, e.g. npx."
        }
        if transport == "http", url.trimmingCharacters(in: .whitespaces).isEmpty {
            return "Enter the URL the daemon should connect to."
        }
        return nil
    }

    /// Registry names are namespaced (io.github.owner/thing); the last segment is the usable name.
    private func shortName(_ full: String) -> String {
        let tail = full.split(separator: "/").last.map(String.init) ?? full
        return tail.replacingOccurrences(of: " ", with: "-")
    }

    private func field<C: View>(_ label: String, required: Bool = false,
                                @ViewBuilder content: () -> C) -> some View {
        VStack(alignment: .leading, spacing: OculusSpace.xs) {
            HStack(spacing: OculusSpace.xs) {
                Text(label).font(.caption.weight(.semibold))
                if required {
                    Text("required").font(.caption2).foregroundStyle(palette.mutedForeground.opacity(0.8))
                }
            }
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
        if let problem = firstProblem() { error = problem; return }
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
    /// Pushed rather than presented — see `MCPServerEditor.pushed`. There is nothing unsaved here,
    /// so the stack's own Back button is the right way out and stays visible.
    var pushed: Bool = false
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
            onClose: pushed ? nil : onClose,
            showsHeader: !pushed
        ) {
            if model.mcpBrowsing && model.mcpDirectory.isEmpty {
                HStack(spacing: OculusSpace.sm) {
                    ProgressView().controlSize(.small)
                    Text("Searching the registry…").font(.footnote)
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
        #if os(iOS)
        .navigationTitle(pushed ? "Browse" : "")
        .navigationBarTitleDisplayMode(.inline)
        #endif
    }

    private func entry(_ e: MCPDirectoryEntry) -> some View {
        SheetCard(palette: palette) {
            HStack(spacing: OculusSpace.xs) {
                Text(e.name).font(.footnote.weight(.medium))
                    .foregroundStyle(palette.foreground).lineLimit(1).truncationMode(.middle)
                if let v = e.version, !v.isEmpty {
                    Text(v).font(.caption2.monospaced())
                        .foregroundStyle(palette.mutedForeground)
                }
                Spacer(minLength: OculusSpace.sm)
                if let u = e.unsupported, !u.isEmpty {
                    Text(u).font(.caption2).foregroundStyle(palette.mutedForeground)
                        .lineLimit(1).truncationMode(.tail).frame(maxWidth: 200, alignment: .trailing)
                } else {
                    Button("Add") { onPick(e) }.buttonStyle(.bordered)
                        #if os(macOS)
                        .controlSize(.small)
                        #endif
                }
            }
            if let d = e.description, !d.isEmpty {
                Text(d).font(.caption).foregroundStyle(palette.mutedForeground)
                    .lineLimit(3).fixedSize(horizontal: false, vertical: true)
            }
            if let keys = e.envKeys, !keys.isEmpty {
                Text("needs: " + keys.joined(separator: ", "))
                    .font(.caption2.monospaced())
                    .foregroundStyle(palette.mutedForeground)
                    .lineLimit(1).truncationMode(.tail)
            }
        }
    }
}
