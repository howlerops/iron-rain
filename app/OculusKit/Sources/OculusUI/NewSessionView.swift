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
    @State private var workspaceName = ""
    @State private var terminalSearch = ""
    @State private var scanning = false
    @State private var scanned = false
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

    private static let providers = ["opencode", "claude-code", "pi"]

    private var isMulti: Bool { selectedProjects.count > 1 }
    private var singleSelectedProject: Project? {
        guard selectedProjects.count == 1, let id = selectedProjects.first else { return nil }
        return model.projects.first { $0.id == id }
    }
    private var canWorktree: Bool { singleSelectedProject?.isGitRepo == true }

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
                    model.newSession(provider: provider,
                                     projectIDs: selectedProjects.isEmpty ? nil : Array(selectedProjects),
                                     worktree: useWorktree && canWorktree,
                                     workspaceName: workspaceName.isEmpty ? nil : workspaceName)
                    onStart()
                } label: { Text("Start").frame(minWidth: 52) }
                .buttonStyle(.borderedProminent).tint(palette.primary)
                .keyboardShortcut(.defaultAction)
            }
        }
        .padding(.horizontal, 20).padding(.vertical, 13)
    }

    // MARK: new-session body

    private var newContent: some View {
        VStack(alignment: .leading, spacing: 22) {
            field("Agent") {
                Picker("", selection: $provider) {
                    ForEach(Self.providers, id: \.self) { Text($0).tag($0) }
                }
                .pickerStyle(.segmented).labelsHidden()
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

            field("Worktree") {
                Toggle(isOn: $useWorktree) {
                    Text("Isolate in a fresh git worktree").font(.system(size: 13))
                }
                .toggleStyle(.switch).tint(palette.primary)
                .disabled(!canWorktree)
                if useWorktree {
                    TextField("Workspace name (branch)", text: $workspaceName)
                        .textFieldStyle(.roundedBorder)
                }
                Text(canWorktree
                     ? "Runs on a fresh oculus/<name> branch; changes stay isolated until you open a PR."
                     : "Select one git project to enable worktrees.")
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
        #if os(macOS)
        Button {
            if let path = pickFolder() {
                Task { if let p = await model.addProject(path: path) { selectedProjects.insert(p.id) } }
            }
        } label: {
            HStack(spacing: 8) {
                Image(systemName: "folder.badge.plus").foregroundStyle(palette.primary)
                Text("Add folder…").font(.system(size: 13, weight: .medium)).foregroundStyle(palette.primary)
                Spacer()
            }
            .padding(.horizontal, 10).padding(.vertical, 9)
            .background(RoundedRectangle(cornerRadius: 8).strokeBorder(palette.primary.opacity(0.35), style: StrokeStyle(lineWidth: 1, dash: [4, 3])))
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        #else
        HStack(spacing: 8) {
            TextField("Add folder by path", text: $addPath)
                .textFieldStyle(.roundedBorder).autocorrectionDisabled()
            Button("Add") {
                let p = addPath; addPath = ""
                Task { if let proj = await model.addProject(path: p) { selectedProjects.insert(proj.id) } }
            }.disabled(addPath.isEmpty)
        }
        #endif
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
                    ForEach(Array(filteredDiscovered.enumerated()), id: \.offset) { _, d in takeOverRow(d) }
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
