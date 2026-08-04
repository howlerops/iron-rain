import SwiftUI
import OculusKit
#if os(macOS)
import AppKit
#endif
#if canImport(UIKit)
import UIKit
#endif

// MARK: - Takeover derivations (shared by the sheet and the sidebar strip)

/// One terminal session we could continue, flattened out of `Discovered` so the sheet row and the
/// sidebar strip render from the same facts instead of each re-deriving titles and ranking.
struct TakeoverCandidate: Identifiable, Equatable {
    let id: String          // Discovered.discoveryID — stable across re-scans
    let sessionID: String
    let provider: String
    let title: String
    let subtitle: String
    let cwd: String?
    let live: Bool
    let updatedAt: Int?
}

/// What the user has to agree to before we take a session away from a terminal. Nil means there's
/// nothing at stake and we just do it.
struct TakeoverWarning: Equatable {
    let title: String
    let message: String
    let confirm: String
}

/// Pure takeover logic, kept out of the view bodies so it can actually be asserted:
/// which discovered sessions are worth offering, what taking one over costs, and how to
/// hand one back to the terminal.
enum TerminalTakeover {
    /// Discovered terminal sessions worth surfacing — live first, then most recently active.
    ///
    /// Already-managed rows are dropped: they're in the sidebar already, and re-attaching to one
    /// is how you end up with two writers on a single terminal session.
    ///
    /// NOTE: the match here is exact-id only, which the daemon's own id rewriting used to defeat: our
    /// claude sessions are named `cc_…` while discovery reports claude's UUID, so a managed session
    /// slipped through and could be "taken over" a second time. The daemon now drops those rows itself
    /// (it asks each managed session for its provider-side id and dedupes on that), so this filter is
    /// the second line of defence — it still catches every provider whose ids we don't rewrite, and it
    /// still holds if an older daemon is on the other end of the socket.
    static func candidates(discovered: [Discovered], managed: [Session], limit: Int? = nil) -> [TakeoverCandidate] {
        let taken = Set(managed.map(\.id))
        let rows: [TakeoverCandidate] = discovered.compactMap { d in
            guard d.kind == DiscoveredKind.session else { return nil }
            guard let sid = d.sessionID, !sid.isEmpty, !taken.contains(sid) else { return nil }
            return TakeoverCandidate(id: d.discoveryID, sessionID: sid, provider: d.provider,
                                     title: title(d, sessionID: sid), subtitle: subtitle(d),
                                     cwd: d.cwd, live: d.live == true, updatedAt: d.updatedAt)
        }
        let sorted = rows.sorted { a, b in
            if a.live != b.live { return a.live }
            return (a.updatedAt ?? 0) > (b.updatedAt ?? 0)
        }
        guard let limit else { return sorted }
        return Array(sorted.prefix(limit))
    }

    static func title(_ d: Discovered, sessionID: String) -> String {
        if let t = d.title, !t.isEmpty { return t }
        if let cwd = d.cwd, !cwd.isEmpty { return (cwd as NSString).lastPathComponent }
        return sessionID
    }

    static func subtitle(_ d: Discovered) -> String {
        var parts = [d.provider]
        if let cwd = d.cwd, !cwd.isEmpty { parts.append((cwd as NSString).abbreviatingWithTildeInPath) }
        return parts.joined(separator: " · ")
    }

    /// The confirmation a takeover needs, or nil when there's nothing at risk.
    ///
    /// Only LIVE rows warn. An idle transcript has no turn to interrupt and nobody typing into it,
    /// so a dialog there would only train the user to dismiss dialogs — and then they'd dismiss the
    /// one that mattered. The two providers lose different things, so they say different things.
    static func warning(provider: String, live: Bool) -> TakeoverWarning? {
        guard live else { return nil }
        if provider == "claude-code" {
            return TakeoverWarning(
                title: "Fork this terminal session?",
                message: "claude-code can’t be driven from two places at once, so this resumes the conversation as a fork. A reply in flight in the terminal right now won’t carry over, and from here on the two copies diverge — the terminal keeps its own.",
                confirm: "Fork it")
        }
        return TakeoverWarning(
            title: "Take over this live session?",
            message: "This attaches to the session your terminal is still driving, so both will be writing to it. A turn in flight right now can interleave with what you send, and whichever side answers an approval first wins.",
            confirm: "Take over")
    }

    /// The command that hands a session back to a terminal, or nil when we can't name one honestly.
    ///
    /// Only claude has a `--resume <uuid>` handback, and only for CLAUDE's own UUID: our `cc_…` ids
    /// are rejected by the CLI ("not a valid session id"), so offering the command for one would be
    /// a copyable lie. opencode needs nothing — the terminal is still attached to the live session.
    static func resumeCommand(provider: String, sessionID: String) -> String? {
        guard provider == "claude-code", looksLikeUUID(sessionID) else { return nil }
        return "claude --resume \(sessionID)"
    }

    /// 8-4-4-4-12 hex, the shape claude names its session files with.
    static func looksLikeUUID(_ s: String) -> Bool {
        s.range(of: #"^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$"#,
                options: .regularExpression) != nil
    }
}

/// Puts a string on the system pasteboard on either platform.
func copyToPasteboard(_ text: String) {
    #if os(macOS)
    NSPasteboard.general.clearContents()
    NSPasteboard.general.setString(text, forType: .string)
    #elseif canImport(UIKit)
    UIPasteboard.general.string = text
    #endif
}

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
    /// The first instruction, sent WITH the create so the agent works during bootstrap rather than
    /// idling until you come back to it. On a phone — where you open the app to start something and
    /// then put it away — that is the difference between one interaction and two.
    @State private var firstPrompt = ""
    static let lastWorktreeKey = "oculus.newSession.worktree"
    static let lastProjectsKey = "oculus.newSession.projects"
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
    /// A live row awaiting confirmation before we take it away from its terminal.
    @State private var pendingTakeover: PendingTakeover?
    /// Set when closing would throw away a typed prompt.
    @State private var confirmDiscard = false
    @Environment(\.accessibilityDifferentiateWithoutColor) private var differentiateWithoutColor
    @FocusState private var focus: Field?
    #if os(iOS)
    @State private var addPath = ""
    #endif

    /// Keyboard focus order through the form.
    private enum Field: Hashable { case prompt, workspace, addPath, search }

    private struct PendingTakeover: Identifiable {
        let discovered: Discovered
        let warning: TakeoverWarning
        var id: String { discovered.discoveryID }
    }

    /// The typed first prompt is a multi-line task description with no draft storage anywhere —
    /// losing it to a stray swipe-down means retyping the only thing the user came here to write.
    private var hasUnsavedInput: Bool {
        !firstPrompt.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
    }

    private func requestClose() {
        if hasUnsavedInput { confirmDiscard = true } else { onStart() }
    }

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
        #if os(iOS)
        // The folder browser used to open as a THIRD stacked sheet on top of this one. Pushing it
        // instead keeps the modal stack one deep and gives it the system back button.
        NavigationStack {
            form
                .toolbar(.hidden, for: .navigationBar)
                .navigationDestination(isPresented: $showBrowser) {
                    FolderBrowser(model: model, palette: palette, embedded: true,
                                  onPicked: { added in for p in added { selectedProjects.insert(p.id) } },
                                  onClose: { showBrowser = false })
                        .navigationTitle("Add folders")
                        .navigationBarTitleDisplayMode(.inline)
                }
        }
        #else
        form
            .sheet(isPresented: $showBrowser) {
                FolderBrowser(model: model, palette: palette,
                              onPicked: { added in for p in added { selectedProjects.insert(p.id) } },
                              onClose: { showBrowser = false })
            }
        #endif
    }

    private var form: some View {
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
        // A half-written task description is real work; a swipe-down should not be able to delete it
        // silently. Explicit close still works, and asks.
        .interactiveDismissDisabled(hasUnsavedInput)
        .confirmationDialog("Discard this session?", isPresented: $confirmDiscard, titleVisibility: .visible) {
            Button("Discard", role: .destructive) { onStart() }
            Button("Keep editing", role: .cancel) {}
        } message: {
            Text("You've written a task for the agent. Closing now throws it away.")
        }
        .task {
            // Restore the previous session's shape. It re-zeroed on every open, so a user who always
            // works in worktrees on one project re-made both decisions every single time.
            if selectedProjects.isEmpty,
               let saved = UserDefaults.standard.stringArray(forKey: Self.lastProjectsKey), !saved.isEmpty {
                selectedProjects = Set(saved)
            }
            useWorktree = UserDefaults.standard.bool(forKey: Self.lastWorktreeKey)
            await model.loadProjects(); await scan()
        }
        // The prompt is placed first because it is the thing the user came to express — but it did
        // not actually receive focus, so every new session still began with a tap. The delay lets
        // the sheet finish presenting: focus set mid-transition is dropped on the floor.
        .task {
            guard mode == .new else { return }
            try? await Task.sleep(nanoseconds: 400_000_000)
            focus = .prompt
        }
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
    }

    // MARK: header / footer

    private var header: some View {
        VStack(spacing: 14) {
            HStack {
                Text(mode == .new ? "New session" : "Take over a session")
                    .font(.title3.weight(.semibold))
                Spacer()
                Button { requestClose() } label: {
                    Image(systemName: "xmark").font(.caption.weight(.bold))
                        .foregroundStyle(palette.mutedForeground)
                        .frame(width: 22, height: 22)
                        .background(Circle().fill(palette.muted.opacity(0.5)))
                        // The glyph stays 22pt; only the hit area grows to the touch floor, which
                        // .buttonStyle(.plain) otherwise strips off entirely.
                        .frame(width: 44, height: 44)
                        .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                .accessibilityLabel("Close")
            }
            Picker("Mode", selection: $mode) {
                ForEach(Mode.allCases) { Text($0.rawValue).tag($0) }
            }
            .pickerStyle(.segmented).labelsHidden()
        }
        .padding(.horizontal, 20).padding(.top, 4).padding(.bottom, 14)
    }

    private var footer: some View {
        HStack(spacing: 10) {
            if mode == .new && isMulti {
                Label("\(selectedProjects.count) repos", systemImage: "square.stack.3d.up")
                    .font(.caption).foregroundStyle(palette.mutedForeground)
            }
            Spacer()
            Button("Cancel") { requestClose() }
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
                                                  modelProvider: chosen?.provider,
                                                  prompt: firstPrompt)
                        // Remember the shape of this session so the next one opens ready to repeat it.
                        UserDefaults.standard.set(useWorktree && canIsolate, forKey: Self.lastWorktreeKey)
                        UserDefaults.standard.set(Array(selectedProjects), forKey: Self.lastProjectsKey)
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
            // FIRST, not last. The task is the thing the user came to express; the agent, model and
            // isolation are settings that mostly keep their previous value. Putting the prompt at the
            // top also means the agent starts working during bootstrap rather than after you notice
            // it finished bootstrapping.
            // The offer to adopt a terminal session belongs HERE, in the create flow — picking up
            // something already running is a way of starting, not a "recent". It sat in the sidebar
            // above your actual sessions, which put a thing you have never opened where your history
            // should be. Shown only when there is genuinely something to continue.
            if !terminalCandidates.isEmpty {
                Button { mode = .takeOver } label: {
                    HStack(spacing: 9) {
                        Image(systemName: "terminal").font(.footnote)
                            .foregroundStyle(palette.primary)
                            .accessibilityHidden(true)
                        VStack(alignment: .leading, spacing: 1) {
                            Text(terminalCandidates.count == 1
                                 ? "1 session is running in your terminal"
                                 : "\(terminalCandidates.count) sessions are running in your terminal")
                                .font(.footnote.weight(.medium))
                                .foregroundStyle(palette.foreground)
                            Text("Continue one here instead of starting fresh")
                                .font(.caption).foregroundStyle(palette.mutedForeground)
                        }
                        Spacer(minLength: 6)
                        Image(systemName: "chevron.right").font(.caption)
                            .foregroundStyle(palette.mutedForeground)
                            .accessibilityHidden(true)
                    }
                    .padding(.horizontal, 10).padding(.vertical, 9)
                    .frame(minHeight: 44)
                    .background(OculusShape.rounded(OculusRadius.md).fill(palette.primary.opacity(0.10)))
                    .overlay(OculusShape.rounded(OculusRadius.md).strokeBorder(palette.primary.opacity(0.22)))
                    .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
            }

            field("What should the agent do?") {
                TextField("Optional — you can also just start and type", text: $firstPrompt, axis: .vertical)
                    .textFieldStyle(.roundedBorder)
                    .lineLimit(2...5)
                    .focused($focus, equals: .prompt)
            }
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
                        .font(.footnote)
                }
                .toggleStyle(.switch).tint(palette.primary)
                .disabled(!canIsolate)
                if useWorktree && canIsolate {
                    // This string BECOMES A GIT BRANCH NAME. Autocapitalization turned "api-fix"
                    // into "Api-fix", which is a different branch from the one the user meant.
                    TextField(isMulti ? "Workspace name (shared branch)" : "Workspace name (branch)", text: $workspaceName)
                        .textFieldStyle(.roundedBorder)
                        .plainInput()
                        .focused($focus, equals: .workspace)
                        .submitLabel(.done)
                        .onSubmit { focus = nil }
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
                    Text("Keep going until the task is done").font(.footnote)
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
                    .font(.subheadline).foregroundStyle(sel ? palette.primary : palette.mutedForeground)
                    .accessibilityHidden(true)
                VStack(alignment: .leading, spacing: 1) {
                    HStack(spacing: 5) {
                        Text(p.name).font(.footnote.weight(.medium)).foregroundStyle(palette.foreground)
                        if p.isGitRepo {
                            Image(systemName: "arrow.triangle.branch").font(.caption2).foregroundStyle(palette.mutedForeground)
                                .accessibilityLabel("Git repository")
                        }
                    }
                    Text((p.path as NSString).abbreviatingWithTildeInPath)
                        .font(.caption).foregroundStyle(palette.mutedForeground)
                        .lineLimit(1).truncationMode(.middle)
                }
                Spacer(minLength: 0)
            }
            .padding(.horizontal, 10).padding(.vertical, 8)
            .frame(minHeight: 44)
            .background(OculusShape.rounded(OculusRadius.sm).fill(sel ? palette.primary.opacity(0.10) : palette.muted.opacity(0.22)))
            .overlay(OculusShape.rounded(OculusRadius.sm).strokeBorder(sel ? palette.primary.opacity(0.3) : .clear))
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .accessibilityLabel(p.name)
        .accessibilityValue(sel ? "Selected" : "Not selected")
        .accessibilityAddTraits(sel ? [.isButton, .isSelected] : .isButton)
    }

    @ViewBuilder private var addFolderRow: some View {
        VStack(spacing: 8) {
            // Primary: browse into a folder and pick several sub-folders at once (e.g. a "projects"
            // folder → check N repos, each gets its own worktree). Works on iOS + macOS.
            Button { showBrowser = true } label: {
                HStack(spacing: 8) {
                    Image(systemName: "folder.badge.plus").foregroundStyle(palette.primary)
                    Text("Browse folders…").font(.footnote.weight(.medium)).foregroundStyle(palette.primary)
                    Spacer()
                    Text("pick several").font(.caption2).foregroundStyle(palette.mutedForeground)
                }
                .padding(.horizontal, 10).padding(.vertical, 9)
                .frame(minHeight: 44)
                .background(OculusShape.rounded(OculusRadius.sm).strokeBorder(palette.primary.opacity(0.35), style: StrokeStyle(lineWidth: 1, dash: [4, 3])))
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
                    .textFieldStyle(.roundedBorder)
                    .plainInput()
                    .focused($focus, equals: .addPath)
                    .submitLabel(.done)
                    .onSubmit { addTypedPath() }
                Button("Add") { addTypedPath() }.disabled(addPath.isEmpty)
            }
            #endif
        }
    }

    // MARK: take-over body

    private var takeOverContent: some View {
        VStack(alignment: .leading, spacing: 14) {
            HStack(spacing: 8) {
                Image(systemName: "magnifyingglass").foregroundStyle(palette.mutedForeground)
                    .accessibilityHidden(true)
                TextField("Search running sessions", text: $terminalSearch)
                    .textFieldStyle(.plain)
                    .plainInput()
                    .focused($focus, equals: .search)
                    .submitLabel(.search)
                    .accessibilityLabel("Search running sessions")
                Button { Task { await scan() } } label: {
                    Image(systemName: scanning ? "circle.dotted" : "arrow.clockwise")
                        .foregroundStyle(palette.mutedForeground)
                        .frame(width: 44, height: 44).contentShape(Rectangle())
                }
                .buttonStyle(.plain).disabled(scanning)
                .help("Scan for running sessions")
                .accessibilityLabel("Scan for running sessions")
            }
            .padding(.leading, 12)
            .background(OculusShape.rounded(OculusRadius.md).fill(palette.muted.opacity(0.4)))

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
        // Stealing a LIVE session is destructive in a way the row can't express in a chip, so it is
        // confirmed with the specific loss named. Idle rows attach with no dialog at all.
        .confirmationDialog(pendingTakeover?.warning.title ?? "",
                            isPresented: Binding(get: { pendingTakeover != nil },
                                                 set: { if !$0 { pendingTakeover = nil } }),
                            titleVisibility: .visible) {
            if let p = pendingTakeover {
                Button(p.warning.confirm, role: .destructive) {
                    let d = p.discovered
                    pendingTakeover = nil
                    Task { await model.attach(d); onStart() }
                }
            }
            Button("Cancel", role: .cancel) { pendingTakeover = nil }
        } message: {
            if let p = pendingTakeover { Text(p.warning.message) }
        }
    }

    /// Attaches straight away when nothing is at risk; otherwise stages the confirmation.
    private func requestTakeover(_ d: Discovered) {
        if let w = TerminalTakeover.warning(provider: d.provider, live: d.live == true) {
            pendingTakeover = PendingTakeover(discovered: d, warning: w)
        } else {
            Task { await model.attach(d); onStart() }
        }
    }

    private func takeOverRow(_ d: Discovered) -> some View {
        Button {
            requestTakeover(d)
        } label: {
            HStack(spacing: 10) {
                Image(systemName: d.provider == "claude-code" ? "terminal" : "bolt.horizontal.circle")
                    .font(.subheadline).foregroundStyle(palette.primary)
                    .accessibilityHidden(true)
                VStack(alignment: .leading, spacing: 1) {
                    Text(discoveredTitle(d)).font(.footnote.weight(.medium)).foregroundStyle(palette.foreground)
                    Text(discoveredSubtitle(d)).font(.caption).foregroundStyle(palette.mutedForeground)
                        .lineLimit(1).truncationMode(.middle)
                }
                Spacer(minLength: 0)
                if d.live == true { liveChip }
            }
            .padding(.horizontal, 10).padding(.vertical, 8)
            .frame(minHeight: 44)
            .background(OculusShape.rounded(OculusRadius.sm).fill(palette.muted.opacity(0.22)))
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .accessibilityLabel("\(discoveredTitle(d)), \(discoveredSubtitle(d))\(d.live == true ? ", live" : "")")
        .accessibilityHint("Continue this terminal session here")
    }

    // MARK: bits

    @ViewBuilder private func field<Content: View>(_ title: String, @ViewBuilder _ content: () -> Content) -> some View {
        VStack(alignment: .leading, spacing: 8) {
            // Title Case, not ALL CAPS with tracking: OS 26 moved section headers to Title Case
            // systemwide, and a shouty header next to a system one reads as a different app.
            Text(title).font(.footnote.weight(.semibold))
                .foregroundStyle(palette.mutedForeground)
            content()
        }
    }

    private func centerHint(icon: String, text: String) -> some View {
        VStack(spacing: 8) {
            Image(systemName: icon).font(.largeTitle).foregroundStyle(palette.mutedForeground)
                .accessibilityHidden(true)
            Text(text).font(.footnote).foregroundStyle(palette.mutedForeground)
                .multilineTextAlignment(.center)
        }
        .frame(maxWidth: .infinity).padding(.vertical, 30)
    }

    /// The gold dot carried "live" in colour alone. Under Differentiate Without Color the dot
    /// becomes a filled broadcast glyph and the chip gains an outline, so the state survives without
    /// the tint. The word "Live" is always present for VoiceOver.
    private var liveChip: some View {
        HStack(spacing: 3) {
            if differentiateWithoutColor {
                Image(systemName: "dot.radiowaves.left.and.right").font(.caption2.weight(.bold))
            } else {
                Circle().fill(palette.primary).frame(width: 5, height: 5)
            }
            Text("Live").font(.caption2.weight(.semibold))
        }
        .foregroundStyle(palette.primary)
        .padding(.horizontal, 6).padding(.vertical, 2)
        .background(Capsule().fill(palette.primary.opacity(0.16)))
        .overlay { if differentiateWithoutColor { Capsule().strokeBorder(palette.primary, lineWidth: 1) } }
        .accessibilityElement(children: .combine)
    }

    #if os(iOS)
    private func addTypedPath() {
        let p = addPath.trimmingCharacters(in: .whitespaces)
        guard !p.isEmpty else { return }
        addPath = ""
        Task { if let proj = await model.addProject(path: p) { selectedProjects.insert(proj.id) } }
    }
    #endif

    /// Terminal sessions worth offering to adopt — the same set the Take over tab lists.
    private var terminalCandidates: [TakeoverCandidate] {
        TerminalTakeover.candidates(discovered: model.discovered, managed: model.sessions)
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

    // Both the sheet and the sidebar strip label the same rows, so they share one derivation —
    // a row that reads "oculus" here must not read "session" there.
    private func discoveredTitle(_ d: Discovered) -> String {
        TerminalTakeover.title(d, sessionID: d.sessionID ?? "session")
    }

    private func discoveredSubtitle(_ d: Discovered) -> String { TerminalTakeover.subtitle(d) }

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
    /// True when this is PUSHED rather than presented as its own sheet: the navigation bar then
    /// supplies the title and the back button, so drawing our own header too would duplicate both.
    var embedded: Bool = false
    let onPicked: ([Project]) -> Void
    let onClose: () -> Void

    @State private var listing: ProjectBrowse?
    @State private var selected: Set<String> = [] // absolute paths
    @State private var loading = true
    @State private var adding = false

    var body: some View {
        VStack(spacing: 0) {
            if !embedded {
                HStack {
                    Text("Add folders").font(.title3.weight(.semibold))
                    Spacer()
                    Button { onClose() } label: {
                        Image(systemName: "xmark").font(.caption.weight(.bold)).foregroundStyle(palette.mutedForeground)
                            .frame(width: 22, height: 22).background(Circle().fill(palette.muted.opacity(0.5)))
                            .frame(width: 44, height: 44).contentShape(Rectangle())
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel("Close")
                }
                .padding(.horizontal)
            }

            // Path bar with an "up" control.
            HStack(spacing: 8) {
                Button { if let p = listing?.parent, !p.isEmpty { Task { await load(p) } } } label: {
                    Image(systemName: "arrow.up").font(.footnote.weight(.semibold))
                        .foregroundStyle((listing?.parent ?? "").isEmpty ? palette.mutedForeground : palette.primary)
                        .frame(width: 44, height: 44).contentShape(Rectangle())
                }
                .buttonStyle(.plain).disabled((listing?.parent ?? "").isEmpty)
                .help("Go up one folder")
                .accessibilityLabel("Go up one folder")
                Text(listing?.path ?? "…").font(.system(.footnote, design: .monospaced))
                    .lineLimit(1).truncationMode(.head).foregroundStyle(palette.mutedForeground)
                    .accessibilityLabel("Current folder, \(listing?.path ?? "loading")")
                Spacer()
            }
            .padding(.leading, 6).padding(.bottom, 4)
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
            // Selecting was an onTapGesture, so VoiceOver saw a folder name as static text with no
            // button trait and no way to select it at all. It is the row's primary action, so it is
            // the row's Button; the chevron stays a separate control for navigating deeper.
            Button { toggle(e.path) } label: {
                HStack(spacing: 10) {
                    Image(systemName: sel ? "checkmark.circle.fill" : "circle")
                        .foregroundStyle(sel ? palette.primary : palette.mutedForeground)
                    Image(systemName: e.isGitRepo ? "arrow.triangle.branch" : "folder")
                        .font(.footnote).foregroundStyle(e.isGitRepo ? palette.primary : palette.mutedForeground)
                    Text(e.name).font(.footnote).foregroundStyle(palette.foreground).lineLimit(1)
                    if e.isGitRepo {
                        Text("git").font(.caption2.weight(.semibold)).foregroundStyle(palette.primary)
                            .padding(.horizontal, 5).padding(.vertical, 1)
                            .background(Capsule().fill(palette.primary.opacity(0.14)))
                    }
                    Spacer(minLength: 0)
                }
                .frame(minHeight: 44)
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .accessibilityLabel(e.isGitRepo ? "\(e.name), git repository" : e.name)
            .accessibilityValue(sel ? "Selected" : "Not selected")
            .accessibilityAddTraits(sel ? [.isButton, .isSelected] : .isButton)
            // Navigate INTO the folder (browse deeper) — distinct from selecting it.
            Button { Task { await load(e.path) } } label: {
                Image(systemName: "chevron.right").font(.caption.weight(.semibold)).foregroundStyle(palette.mutedForeground)
                    .frame(width: 44, height: 44).contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .help("Open \(e.name)")
            .accessibilityLabel("Open \(e.name)")
        }
        .padding(.leading, 10)
        .background(OculusShape.rounded(OculusRadius.sm).fill(sel ? palette.primary.opacity(0.10) : palette.muted.opacity(0.18)))
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
