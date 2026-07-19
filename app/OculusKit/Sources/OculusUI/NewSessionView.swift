import SwiftUI
import OculusKit
#if os(macOS)
import AppKit
#endif

/// Configures a new session: which provider, which project folder, and whether to run
/// it in an isolated git worktree. Applying just sets the Model's pending options; the
/// session is created when the user sends the first message.
struct NewSessionView: View {
    @ObservedObject var model: Model
    let palette: OculusPalette
    let onStart: () -> Void

    @State private var provider = "opencode"
    @State private var selectedProjects: Set<String> = []
    @State private var useWorktree = false
    @State private var workspaceName = ""
    @State private var terminalSearch = ""
    @State private var scanning = false
    @State private var scanned = false
    #if os(iOS)
    @State private var addPath = ""
    #endif

    private static let providers = ["opencode", "claude-code", "pi"]

    private var isMulti: Bool { selectedProjects.count > 1 }
    private var singleSelectedProject: Project? {
        guard selectedProjects.count == 1, let id = selectedProjects.first else { return nil }
        return model.projects.first { $0.id == id }
    }
    private var canWorktree: Bool { singleSelectedProject?.isGitRepo == true }

    var body: some View {
        NavigationStack {
            Form {
                Section("Agent") {
                    Picker("Provider", selection: $provider) {
                        ForEach(Self.providers, id: \.self) { Text($0).tag($0) }
                    }
                }

                Section {
                    ForEach(model.projects) { p in
                        Button { toggle(p.id) } label: {
                            HStack(spacing: 9) {
                                Image(systemName: selectedProjects.contains(p.id) ? "checkmark.circle.fill" : "circle")
                                    .foregroundStyle(selectedProjects.contains(p.id) ? palette.primary : palette.mutedForeground)
                                VStack(alignment: .leading, spacing: 1) {
                                    Text(p.name).foregroundStyle(palette.foreground)
                                    Text(p.path).font(.caption).foregroundStyle(palette.mutedForeground).lineLimit(1)
                                }
                                Spacer()
                                if p.isGitRepo {
                                    Image(systemName: "arrow.triangle.branch").font(.caption).foregroundStyle(palette.mutedForeground)
                                }
                            }
                        }
                        .buttonStyle(.plain)
                    }
                    addProjectControl
                } header: {
                    Text(isMulti ? "Projects · \(selectedProjects.count) selected" : "Project")
                } footer: {
                    if isMulti {
                        Text("Multi-repo: the agent runs in the shared parent folder of the selected repos, so it can work across all of them. (Worktrees are single-repo only.)")
                            .font(.caption)
                    } else {
                        Text("Pick a folder to run the agent in, or none for the daemon default. Select multiple for a multi-repo update.")
                            .font(.caption)
                    }
                }

                Section {
                    Toggle("Isolate in a git worktree", isOn: $useWorktree)
                        .disabled(!canWorktree)
                    if useWorktree {
                        TextField("Workspace name", text: $workspaceName)
                            .textFieldStyle(.roundedBorder)
                    }
                } header: {
                    Text("Worktree")
                } footer: {
                    Text(canWorktree
                         ? "Runs on a fresh oculus/<name> branch; changes stay isolated until you open a PR."
                         : "Pick a git project to enable worktrees.")
                        .font(.caption)
                }

                Section {
                    HStack(spacing: 6) {
                        Image(systemName: "magnifyingglass").foregroundStyle(palette.mutedForeground)
                        TextField("Search sessions", text: $terminalSearch)
                            .textFieldStyle(.plain)
                            #if os(iOS)
                            .autocorrectionDisabled().textInputAutocapitalization(.never)
                            #endif
                    }
                    if scanning {
                        HStack(spacing: 8) { ProgressView().controlSize(.small); Text("Scanning…").foregroundStyle(palette.mutedForeground) }
                    } else if filteredDiscovered.isEmpty && scanned {
                        Text("Nothing found. Start a session in a terminal (opencode/claude) and Scan.")
                            .font(.caption).foregroundStyle(palette.mutedForeground)
                    }
                    ForEach(Array(filteredDiscovered.enumerated()), id: \.offset) { _, d in
                        Button {
                            Task { await model.attach(d); onStart() }
                        } label: {
                            HStack(spacing: 8) {
                                Image(systemName: d.provider == "claude-code" ? "terminal" : "bolt.horizontal.circle")
                                    .foregroundStyle(palette.primary)
                                VStack(alignment: .leading, spacing: 1) {
                                    Text(discoveredTitle(d)).foregroundStyle(palette.foreground)
                                    Text(discoveredSubtitle(d)).font(.caption)
                                        .foregroundStyle(palette.mutedForeground).lineLimit(1)
                                }
                            }
                        }
                        .buttonStyle(.plain)
                    }
                } header: {
                    HStack {
                        Text("Resume a terminal session")
                        Spacer()
                        Button { Task { await scan() } } label: { Label("Scan", systemImage: "arrow.clockwise").font(.caption) }
                            .buttonStyle(.plain)
                    }
                } footer: {
                    Text("Find an opencode or claude-code session already running in a terminal and continue it here.")
                        .font(.caption)
                }
            }
            .navigationTitle("New session")
            .toolbar {
                ToolbarItem(placement: .confirmationAction) {
                    Button("Start") {
                        model.newSession(provider: provider,
                                         projectIDs: selectedProjects.isEmpty ? nil : Array(selectedProjects),
                                         worktree: useWorktree && canWorktree,
                                         workspaceName: workspaceName.isEmpty ? nil : workspaceName)
                        onStart()
                    }
                }
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { onStart() }
                }
            }
            .task { await model.loadProjects(); await scan() }
        }
    }

    private func scan() async {
        scanning = true
        await model.discover()
        scanned = true
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

    @ViewBuilder private var addProjectControl: some View {
        #if os(macOS)
        Button {
            if let path = pickFolder() {
                Task { if let p = await model.addProject(path: path) { selectedProjects.insert(p.id) } }
            }
        } label: {
            Label("Add folder…", systemImage: "folder.badge.plus")
        }
        #else
        HStack {
            TextField("Add folder by path", text: $addPath)
                .textFieldStyle(.roundedBorder)
                .autocorrectionDisabled()
            Button("Add") {
                let p = addPath
                addPath = ""
                Task { if let proj = await model.addProject(path: p) { selectedProjects.insert(proj.id) } }
            }.disabled(addPath.isEmpty)
        }
        #endif
    }

    private func toggle(_ id: String) {
        if selectedProjects.contains(id) { selectedProjects.remove(id) } else { selectedProjects.insert(id) }
    }

    #if os(macOS)
    private func pickFolder() -> String? {
        let panel = NSOpenPanel()
        panel.canChooseDirectories = true
        panel.canChooseFiles = false
        panel.allowsMultipleSelection = false
        panel.prompt = "Add Project"
        return panel.runModal() == .OK ? panel.url?.path : nil
    }
    #endif
}
