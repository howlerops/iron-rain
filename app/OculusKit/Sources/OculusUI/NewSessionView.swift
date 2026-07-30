import SwiftUI
import OculusKit
#if os(macOS)
import AppKit
#endif

/// Start a new agent session (provider + working folder(s) + optional worktree), or take over
/// a session already running in a terminal. A modern modal: fixed header with a Start-new /
/// Take-over switch, a scrollable body of card rows, and a pinned footer. Starting just sets
/// the Model's pending options; the session is created on the first message.
struct NewSessionView: View {
    @ObservedObject var model: Model
    let palette: OculusPalette
    let onStart: () -> Void

    @State private var provider = "opencode"
    @State private var selectedProjects: Set<String> = []
    @State private var useWorktree = false
    @State private var sessionMode = SessionMode.code
    @State private var autonomous = false
    @State private var workspaceName = ""
    @State private var terminalSearch = ""
    @State private var scanning = false
    @State private var showBrowser = false
    @State private var showManageAgents = false
    @State private var models: [ModelInfo] = []   // models for the chosen provider (empty = none)
    @State private var selectedModel = ""          // "" = provider default
    @State private var mode: Mode
    #if os(iOS)
    @State private var addPath = ""
    #endif

    init(model: Model, palette: OculusPalette, initialTakeOver: Bool = false, onStart: @escaping () -> Void) {
        self.model = model
        self.palette = palette
        self.onStart = onStart
        _mode = State(initialValue: initialTakeOver ? .takeOver : .new)
    }

    private enum Mode: String, CaseIterable, Identifiable {
        case new = "Start new"
        case takeOver = "Take over"
        var id: String { rawValue }
    }

    // Whether the harness ALSO has a native plan mode we can hint (enforcement is daemon-side either way).
    private var planCapable: Bool { provider == "opencode" || provider == "claude-code" }
    private var modeHelp: String {
        switch sessionMode {
        case SessionMode.ask:
            return "Read-only. The agent can read, search and explain, but every edit or command is refused."
        case SessionMode.architect:
            return planCapable
                ? "Plans first. Edits and commands are refused until you switch to Code."
                : "Plans first. Edits and commands are refused until you switch to Code (this agent has no native plan mode, so the daemon enforces it)."
        default:
            return "Normal. Your approval rules decide; anything else asks."
        }
    }

    private var isMulti: Bool { selectedProjects.count > 1 }
    private var singleSelectedProject: Project? {
        guard selectedProjects.count == 1, let id = selectedProjects.first else { return nil }
        return model.projects.first { $0.id == id }
    }
    private var canWorktree: Bool { singleSelectedProject?.isGitRepo == true }
    /// Every selected repo is a git repo, so a multi-repo workspace (one worktree per repo) is
    /// possible. Single-repo isolation is canWorktree; canIsolate covers both.
    private var canIsolateMulti: Bool {
        isMulti && selectedProjects.allSatisfy { id in model.projects.first { $0.id == id }?.isGitRepo == true }
    }
    private var canIsolate: Bool { canWorktree || canIsolateMulti }
    private var isolationHelp: String {
        if isMulti {
            return canIsolateMulti
                ? "Each repo checks out on a shared oculus/<name> branch under one workspace folder — the agent works across all of them, and you finish with a coordinated PR per repo."
                : "Every selected folder must be a git repo to isolate a workspace."
        }
        return canWorktree
            ? "Runs on a fresh oculus/<name> branch; changes stay isolated until you open a PR."
            : "Select one git project to enable worktrees."
    }

    var body: some View {
        VStack(spacing: 0) {
            header
            Divider().overlay(palette.border)
            ScrollView { (mode == .new ? AnyView(newContent) : AnyView(takeOverContent)).padding(20) }
            Divider().overlay(palette.border)
            footer
        }
        #if os(macOS)
        .frame(width: 560, height: 640)
        #endif
        .background(palette.background)
        .task { await model.loadProjects(); await scan() }
        .task(id: model.providers) {
            if !model.providers.isEmpty, !model.providers.contains(provider) { provider = model.providers.first ?? provider }
        }
        .task(id: provider) {
            // Load the chosen provider's models for the picker; reset the selection to its default.
            selectedModel = ""
            let r = await model.providerModels(provider)
            models = r.editable ? r.models : []
        }
        .sheet(isPresented: $showManageAgents) { ManageAgentsView(model: model, palette: palette) }
        .sheet(isPresented: $showBrowser) {
            FolderBrowser(model: model, palette: palette,
                          onPicked: { added in for p in added { selectedProjects.insert(p.id) } },
                          onClose: { showBrowser = false })
        }
    }

    // MARK: header / footer

    private var header: some View {
        VStack(spacing: 14) {
            HStack {
                Text(mode == .new ? "New session" : "Take over a session")
                    .font(.system(size: 17, weight: .semibold))
                Spacer()
                Button { onStart() } label: {
                    Image(systemName: "xmark").font(.system(size: 11, weight: .bold))
                        .foregroundStyle(palette.mutedForeground)
                        .frame(width: 22, height: 22)
                        .background(Circle().fill(palette.muted.opacity(0.5)))
                }
                .buttonStyle(.plain)
            }
            Picker("", selection: $mode) {
                ForEach(Mode.allCases) { Text($0.rawValue).tag($0) }
            }
            .pickerStyle(.segmented).labelsHidden()
        }
        .padding(.horizontal, 20).padding(.top, 18).padding(.bottom, 14)
    }

    private var footer: some View {
        HStack(spacing: 10) {
            if mode == .new && isMulti {
                Label("\(selectedProjects.count) repos", systemImage: "square.stack.3d.up")
                    .font(.caption).foregroundStyle(palette.mutedForeground)
            }
            Spacer()
            Button("Cancel") { onStart() }
                .keyboardShortcut(.cancelAction)
            if mode == .new {
                Button {
                    Task {
                        let chosen = models.first { $0.id == selectedModel }
                        await model.createSession(provider: provider,
                                                  projectIDs: selectedProjects.isEmpty ? nil : Array(selectedProjects),
                                                  worktree: useWorktree && canIsolate,
                                                  workspaceName: workspaceName.isEmpty ? nil : workspaceName,
                                                  mode: sessionMode,
                                                  autonomous: autonomous,
                                                  model: selectedModel.isEmpty ? nil : selectedModel,
                                                  modelProvider: chosen?.provider)
                    }
                    onStart()
                } label: { Text("Start").frame(minWidth: 52) }
                .buttonStyle(.borderedProminent).tint(palette.primary)
                .keyboardShortcut(.defaultAction)
            }
        }
        .padding(.horizontal, 20).padding(.vertical, 13)
    }

    // MARK: new-session body

    // Segmented reads well for the few native agents; a menu keeps a longer list (native + generic
    // CLI agents) from cramming. PickerStyle types differ, so branch in a ViewBuilder.
    @ViewBuilder private var agentPicker: some View {
        if !model.providersLoaded && model.providers.isEmpty {
            HStack(spacing: 8) {
                ProgressView().controlSize(.small)
                Text("Finding agents…").font(.caption).foregroundStyle(palette.mutedForeground)
                Spacer()
            }
        } else if model.providers.isEmpty {
            Button { showManageAgents = true } label: {
                HStack(spacing: 6) {
                    Image(systemName: "exclamationmark.triangle").foregroundStyle(.orange)
                    Text("No agents found — add one").foregroundStyle(palette.foreground)
                    Spacer()
                    Text("Add").font(.caption.weight(.semibold)).foregroundStyle(palette.primary)
                }.contentShape(Rectangle())
            }.buttonStyle(.plain)
        } else {
            VStack(alignment: .leading, spacing: 6) {
                let picker = Picker("", selection: $provider) {
                    ForEach(model.providers, id: \.self) { Text($0).tag($0) }
                }.labelsHidden()
                if model.providers.count > 4 { picker.pickerStyle(.menu) } else { picker.pickerStyle(.segmented) }
                Button { showManageAgents = true } label: {
                    Label("Manage agents…", systemImage: "slider.horizontal.3").font(.caption)
                }.buttonStyle(.plain).foregroundStyle(palette.primary)
            }
        }
    }

    private var newContent: some View {
        VStack(alignment: .leading, spacing: 22) {
            field("Agent") {
                agentPicker
                if !models.isEmpty {
                    Picker("Model", selection: $selectedModel) {
                        Text("Default").tag("")
                        ForEach(models) { m in Text(m.name).tag(m.id) }
                    }
                    .pickerStyle(.menu)
                    .labelsHidden()
                }
            }

            field(isMulti ? "Working directory · \(selectedProjects.count) selected" : "Working directory") {
                VStack(spacing: 5) {
                    ForEach(model.projects) { p in projectRow(p) }
                    addFolderRow
                }
                Text(isMulti
                     ? "Multi-repo: the agent runs in the shared parent folder, so it can work across all selected repos."
                     : "Where the agent runs. Pick none for the daemon default, or select multiple folders for a multi-repo task.")
                    .font(.caption).foregroundStyle(palette.mutedForeground)
            }

            field(isMulti ? "Workspace" : "Worktree") {
                Toggle(isOn: $useWorktree) {
                    Text(isMulti ? "Isolate each repo in its own worktree" : "Isolate in a fresh git worktree")
                        .font(.system(size: 13))
                }
                .toggleStyle(.switch).tint(palette.primary)
                .disabled(!canIsolate)
                if useWorktree && canIsolate {
                    TextField(isMulti ? "Workspace name (shared branch)" : "Workspace name (branch)", text: $workspaceName)
                        .textFieldStyle(.roundedBorder)
                }
                Text(isolationHelp)
                    .font(.caption).foregroundStyle(palette.mutedForeground)
            }

            // Mode applies to EVERY provider: the daemon enforces it at the approval layer, so even a
            // harness with no native permission mode is held to it. planCapable only decides whether
            // we can additionally ask the harness to plan natively.
            field("Mode") {
                Picker("", selection: $sessionMode) {
                    Text("Code").tag(SessionMode.code)
                    Text("Ask").tag(SessionMode.ask)
                    Text("Architect").tag(SessionMode.architect)
                }
                .pickerStyle(.segmented).labelsHidden()
                Text(modeHelp).font(.caption).foregroundStyle(palette.mutedForeground)
            }

            field("Autonomous") {
                Toggle(isOn: $autonomous) {
                    Text("Keep going until the task is done").font(.system(size: 13))
                }
                .toggleStyle(.switch).tint(palette.primary)
                Text("A heartbeat nudges the agent to continue when it stalls with unfinished to-dos, checkpoints its progress before context fills, and pings you if it gets stuck or hits its budget.")
                    .font(.caption).foregroundStyle(palette.mutedForeground)
            }
        }
    }

    private func projectRow(_ p: Project) -> some View {
        let sel = selectedProjects.contains(p.id)
        return Button { toggle(p.id) } label: {
            HStack(spacing: 10) {
                Image(systemName: sel ? "checkmark.circle.fill" : "circle")
                    .font(.system(size: 15)).foregroundStyle(sel ? palette.primary : palette.mutedForeground)
                VStack(alignment: .leading, spacing: 1) {
                    HStack(spacing: 5) {
                        Text(p.name).font(.system(size: 13, weight: .medium)).foregroundStyle(palette.foreground)
                        if p.isGitRepo {
                            Image(systemName: "arrow.triangle.branch").font(.system(size: 9)).foregroundStyle(palette.mutedForeground)
                        }
                    }
                    Text((p.path as NSString).abbreviatingWithTildeInPath)
                        .font(.system(size: 11)).foregroundStyle(palette.mutedForeground)
                        .lineLimit(1).truncationMode(.middle)
                }
                Spacer(minLength: 0)
            }
            .padding(.horizontal, 10).padding(.vertical, 8)
            .background(RoundedRectangle(cornerRadius: 8).fill(sel ? palette.primary.opacity(0.10) : palette.muted.opacity(0.22)))
            .overlay(RoundedRectangle(cornerRadius: 8).stroke(sel ? palette.primary.opacity(0.3) : .clear))
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
    }

    @ViewBuilder private var addFolderRow: some View {
        VStack(spacing: 8) {
            // Primary: browse into a folder and pick several sub-folders at once (e.g. a "projects"
            // folder → check N repos, each gets its own worktree). Works on iOS + macOS.
            Button { showBrowser = true } label: {
                HStack(spacing: 8) {
                    Image(systemName: "folder.badge.plus").foregroundStyle(palette.primary)
                    Text("Browse folders…").font(.system(size: 13, weight: .medium)).foregroundStyle(palette.primary)
                    Spacer()
                    Text("pick several").font(.caption2).foregroundStyle(palette.mutedForeground)
                }
                .padding(.horizontal, 10).padding(.vertical, 9)
                .background(RoundedRectangle(cornerRadius: 8).strokeBorder(palette.primary.opacity(0.35), style: StrokeStyle(lineWidth: 1, dash: [4, 3])))
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)

            #if os(macOS)
            // Secondary: the native picker (also multi-select — cmd-click several).
            Button {
                let paths = pickFolders()
                guard !paths.isEmpty else { return }
                Task { for path in paths { if let p = await model.addProject(path: path) { selectedProjects.insert(p.id) } } }
            } label: {
                Text("or use the system picker…").font(.caption).foregroundStyle(palette.mutedForeground)
            }
            .buttonStyle(.plain)
            #else
            HStack(spacing: 8) {
                TextField("…or add by path", text: $addPath)
                    .textFieldStyle(.roundedBorder).autocorrectionDisabled()
                Button("Add") {
                    let p = addPath; addPath = ""
                    Task { if let proj = await model.addProject(path: p) { selectedProjects.insert(proj.id) } }
                }.disabled(addPath.isEmpty)
            }
            #endif
        }
    }

    // MARK: take-over body

    private var takeOverContent: some View {
        VStack(alignment: .leading, spacing: 14) {
            HStack(spacing: 8) {
                Image(systemName: "magnifyingglass").foregroundStyle(palette.mutedForeground)
                TextField("Search running sessions", text: $terminalSearch)
                    .textFieldStyle(.plain)
                    #if os(iOS)
                    .autocorrectionDisabled().textInputAutocapitalization(.never)
                    #endif
                Button { Task { await scan() } } label: {
                    Image(systemName: scanning ? "circle.dotted" : "arrow.clockwise").foregroundStyle(palette.mutedForeground)
                }
                .buttonStyle(.plain).disabled(scanning)
            }
            .padding(.horizontal, 12).padding(.vertical, 9)
            .background(RoundedRectangle(cornerRadius: 10).fill(palette.muted.opacity(0.4)))

            if scanning && filteredDiscovered.isEmpty {
                centerHint(icon: "circle.dotted", text: "Scanning for running sessions…")
            } else if filteredDiscovered.isEmpty {
                centerHint(icon: "terminal", text: "No running sessions found.\nStart one in a terminal (opencode/claude), then Scan.")
            } else {
                VStack(spacing: 6) {
                    ForEach(filteredDiscovered, id: \.discoveryID) { d in takeOverRow(d) }
                }
            }

            Text("opencode attaches to the live session (shared control with your terminal). claude-code resumes it as a safe fork. Either way it becomes a managed session in your sidebar.")
                .font(.caption).foregroundStyle(palette.mutedForeground)
        }
    }

    private func takeOverRow(_ d: Discovered) -> some View {
        Button {
            Task { await model.attach(d); onStart() }
        } label: {
            HStack(spacing: 10) {
                Image(systemName: d.provider == "claude-code" ? "terminal" : "bolt.horizontal.circle")
                    .font(.system(size: 15)).foregroundStyle(palette.primary)
                VStack(alignment: .leading, spacing: 1) {
                    Text(discoveredTitle(d)).font(.system(size: 13, weight: .medium)).foregroundStyle(palette.foreground)
                    Text(discoveredSubtitle(d)).font(.system(size: 11)).foregroundStyle(palette.mutedForeground)
                        .lineLimit(1).truncationMode(.middle)
                }
                Spacer(minLength: 0)
                if d.live == true { liveChip }
            }
            .padding(.horizontal, 10).padding(.vertical, 8)
            .background(RoundedRectangle(cornerRadius: 8).fill(palette.muted.opacity(0.22)))
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
    }

    // MARK: bits

    @ViewBuilder private func field<Content: View>(_ title: String, @ViewBuilder _ content: () -> Content) -> some View {
        VStack(alignment: .leading, spacing: 8) {
            Text(title.uppercased()).font(.system(size: 11, weight: .semibold)).tracking(0.4)
                .foregroundStyle(palette.mutedForeground)
            content()
        }
    }

    private func centerHint(icon: String, text: String) -> some View {
        VStack(spacing: 8) {
            Image(systemName: icon).font(.system(size: 26)).foregroundStyle(palette.mutedForeground)
            Text(text).font(.system(size: 12)).foregroundStyle(palette.mutedForeground)
                .multilineTextAlignment(.center)
        }
        .frame(maxWidth: .infinity).padding(.vertical, 30)
    }

    private var liveChip: some View {
        HStack(spacing: 3) {
            Circle().fill(palette.primary).frame(width: 5, height: 5)
            Text("Live").font(.system(size: 10, weight: .semibold))
        }
        .foregroundStyle(palette.primary)
        .padding(.horizontal, 6).padding(.vertical, 2)
        .background(Capsule().fill(palette.primary.opacity(0.16)))
    }

    private func scan() async {
        scanning = true
        await model.discover()
        scanning = false
    }

    private var filteredDiscovered: [Discovered] {
        let q = terminalSearch.trimmingCharacters(in: .whitespaces).lowercased()
        return model.discovered
            .filter { $0.kind == DiscoveredKind.session }
            .filter { d in
                q.isEmpty
                    || (d.title ?? "").lowercased().contains(q)
                    || (d.cwd ?? "").lowercased().contains(q)
                    || (d.sessionID ?? "").lowercased().contains(q)
            }
            .sorted { a, b in
                if (a.live == true) != (b.live == true) { return a.live == true }
                return (a.updatedAt ?? 0) > (b.updatedAt ?? 0)
            }
    }

    private func discoveredTitle(_ d: Discovered) -> String {
        if let t = d.title, !t.isEmpty { return t }
        if let cwd = d.cwd, !cwd.isEmpty { return (cwd as NSString).lastPathComponent }
        return d.sessionID ?? "session"
    }

    private func discoveredSubtitle(_ d: Discovered) -> String {
        var parts = [d.provider]
        if let cwd = d.cwd, !cwd.isEmpty { parts.append((cwd as NSString).abbreviatingWithTildeInPath) }
        return parts.joined(separator: " · ")
    }

    private func toggle(_ id: String) {
        if selectedProjects.contains(id) { selectedProjects.remove(id) } else { selectedProjects.insert(id) }
    }

    #if os(macOS)
    private func pickFolders() -> [String] {
        let panel = NSOpenPanel()
        panel.canChooseDirectories = true
        panel.canChooseFiles = false
        panel.allowsMultipleSelection = true // cmd-click several sub-folders at once
        panel.prompt = "Add"
        return panel.runModal() == .OK ? panel.urls.map(\.path) : []
    }
    #endif
}

/// Browse INTO a folder and pick several sub-folders for one session — e.g. open a "projects"
/// folder, check the repos you want, and each becomes a project (worktree per repo when isolated).
/// Selections persist across navigation, so you can gather folders from more than one parent.
struct FolderBrowser: View {
    @ObservedObject var model: Model
    let palette: OculusPalette
    let onPicked: ([Project]) -> Void
    let onClose: () -> Void

    @State private var listing: ProjectBrowse?
    @State private var selected: Set<String> = [] // absolute paths
    @State private var loading = true
    @State private var adding = false

    var body: some View {
        VStack(spacing: 0) {
            HStack {
                Text("Add folders").font(.system(size: 16, weight: .semibold))
                Spacer()
                Button { onClose() } label: {
                    Image(systemName: "xmark").font(.system(size: 11, weight: .bold)).foregroundStyle(palette.mutedForeground)
                        .frame(width: 22, height: 22).background(Circle().fill(palette.muted.opacity(0.5)))
                }.buttonStyle(.plain)
            }
            .padding()

            // Path bar with an "up" control.
            HStack(spacing: 8) {
                Button { if let p = listing?.parent, !p.isEmpty { Task { await load(p) } } } label: {
                    Image(systemName: "arrow.up").font(.system(size: 12, weight: .semibold))
                        .foregroundStyle((listing?.parent ?? "").isEmpty ? palette.mutedForeground : palette.primary)
                }
                .buttonStyle(.plain).disabled((listing?.parent ?? "").isEmpty)
                Text(listing?.path ?? "…").font(.system(size: 12, design: .monospaced))
                    .lineLimit(1).truncationMode(.head).foregroundStyle(palette.mutedForeground)
                Spacer()
            }
            .padding(.horizontal, 14).padding(.bottom, 8)
            Divider().overlay(palette.border)

            ScrollView {
                if loading {
                    ProgressView().padding(40)
                } else if let entries = listing?.entries, !entries.isEmpty {
                    LazyVStack(spacing: 2) {
                        ForEach(entries) { entry in row(entry) }
                    }.padding(10)
                } else {
                    Text("No sub-folders here.").font(.caption).foregroundStyle(palette.mutedForeground).padding(40)
                }
            }

            Divider().overlay(palette.border)
            HStack {
                Text("\(selected.count) selected").font(.caption).foregroundStyle(palette.mutedForeground)
                Spacer()
                Button("Cancel") { onClose() }.keyboardShortcut(.cancelAction)
                Button {
                    Task { await addSelected() }
                } label: { Text(adding ? "Adding…" : "Add \(selected.count)").frame(minWidth: 56) }
                    .buttonStyle(.borderedProminent).tint(palette.primary)
                    .disabled(selected.isEmpty || adding)
            }
            .padding()
        }
        #if os(macOS)
        .frame(width: 520, height: 560)
        #endif
        .background(palette.background)
        .task { await load(nil) }
    }

    private func row(_ e: ProjectDirEntry) -> some View {
        let sel = selected.contains(e.path)
        return HStack(spacing: 10) {
            Image(systemName: sel ? "checkmark.circle.fill" : "circle")
                .foregroundStyle(sel ? palette.primary : palette.mutedForeground)
            Image(systemName: e.isGitRepo ? "arrow.triangle.branch" : "folder")
                .font(.system(size: 13)).foregroundStyle(e.isGitRepo ? palette.primary : palette.mutedForeground)
            Text(e.name).font(.system(size: 13)).foregroundStyle(palette.foreground).lineLimit(1)
            if e.isGitRepo {
                Text("git").font(.system(size: 9, weight: .semibold)).foregroundStyle(palette.primary)
                    .padding(.horizontal, 5).padding(.vertical, 1)
                    .background(Capsule().fill(palette.primary.opacity(0.14)))
            }
            Spacer()
            // Navigate INTO the folder (browse deeper) — distinct from selecting it.
            Button { Task { await load(e.path) } } label: {
                Image(systemName: "chevron.right").font(.system(size: 11, weight: .semibold)).foregroundStyle(palette.mutedForeground)
                    .frame(width: 26, height: 26).contentShape(Rectangle())
            }.buttonStyle(.plain)
        }
        .padding(.horizontal, 10).padding(.vertical, 7)
        .background(RoundedRectangle(cornerRadius: 7).fill(sel ? palette.primary.opacity(0.10) : palette.muted.opacity(0.18)))
        .contentShape(Rectangle())
        .onTapGesture { toggle(e.path) } // tapping the row selects; the chevron navigates in
    }

    private func toggle(_ p: String) {
        if selected.contains(p) { selected.remove(p) } else { selected.insert(p) }
    }

    private func load(_ path: String?) async {
        loading = true
        let res = await model.browseFolders(path: path)
        if let res { listing = res }
        loading = false
    }

    private func addSelected() async {
        adding = true
        var added: [Project] = []
        for path in selected {
            if let p = await model.addProject(path: path) { added.append(p) }
        }
        adding = false
        onPicked(added)
        onClose()
    }
}
