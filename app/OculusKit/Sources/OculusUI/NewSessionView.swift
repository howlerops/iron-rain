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

// MARK: - Working-directory validation

/// Whether the folders picked in the sheet can actually become a session's working directory.
///
/// The daemon stays the authority — it re-checks, and only it can see the filesystem — but the rule
/// that decides a MULTI-repo session (`session.create` in daemon/hub/hub.go) is pure prefix
/// arithmetic on absolute paths, so it can be run here as well. Running it here is the whole point:
/// an impossible combination used to be accepted in silence at selection time and refused only after
/// the user had written a prompt and pressed Start — by an alert that blamed the agent ("check the
/// agent is installed and running") for what was a choice of folders. A check costs one string
/// comparison; the failure it prevents costs a written task. So it happens where the choice is made.
///
/// Deliberately NOT mirrored here: anything that needs the filesystem (does the folder still exist,
/// is it really a git repo). Guessing at those from the client would produce confident wrong
/// answers; they stay the daemon's to report.
enum WorkingDirectoryPlan: Equatable {
    /// The selection works. `runsIn` is the folder the agent will start in, or nil when it can't be
    /// named from here (nothing selected → the daemon's default; an isolated workspace → a layout
    /// directory the daemon has yet to create).
    case ok(runsIn: String?)
    /// Start cannot succeed with this selection. `summary` is a few words for tight spots (the
    /// footer, a tooltip), `detail` says what is wrong with the FOLDERS, and `fix` is the way out.
    /// None of the three mention the agent: the agent is fine.
    case blocked(summary: String, detail: String, fix: String)

    /// - Parameters:
    ///   - paths: absolute paths of the selected projects, in display order.
    ///   - isolate: the isolation flag AS SENT (`useWorktree && canIsolate`), not the toggle's value.
    ///   - canIsolate: whether isolation is available for this selection, which decides whether the
    ///     suggested fix can be "isolate them" or has to be "change the selection".
    static func evaluate(paths: [String], isolate: Bool, canIsolate: Bool) -> WorkingDirectoryPlan {
        switch paths.count {
        case 0: return .ok(runsIn: nil)         // daemon default cwd
        case 1: return .ok(runsIn: paths[0])
        default: break
        }
        // An isolated multi-repo workspace doesn't need the repos to be related at all: each gets its
        // own worktree under one layout folder the daemon creates (worktree.CreateWorkspace), so
        // there is nothing for them to share. That's why it's the offered escape hatch below.
        if isolate { return .ok(runsIn: nil) }
        let ancestor = commonAncestor(paths)
        if ancestor.isEmpty || ancestor == "/" {
            return .blocked(
                summary: "No shared parent folder",
                detail: "These folders have no parent folder in common, so a shared session has nowhere to run.",
                fix: canIsolate
                    ? "Give each repo its own worktree below, or pick folders that live under one parent."
                    : "Pick folders that live under one parent, or remove the odd one out.")
        }
        return .ok(runsIn: ancestor)
    }

    /// Deepest folder every path lives under, "/" when that's all they share, "" for no paths.
    /// A port of `commonAncestor` in daemon/hub/hub.go — same component-prefix walk, so the sheet
    /// and the daemon agree on which selections are startable and on which folder they run in.
    static func commonAncestor(_ paths: [String]) -> String {
        guard let first = paths.first else { return "" }
        var parts = components(first)
        for p in paths.dropFirst() {
            let other = components(p)
            let n = min(parts.count, other.count)
            var i = 0
            while i < n, parts[i] == other[i] { i += 1 }
            parts = Array(parts.prefix(i))
        }
        let ancestor = parts.joined(separator: "/")
        return ancestor.isEmpty ? "/" : ancestor
    }

    /// Path split the way Go's `filepath.Split(filepath.Clean(p))` splits it — the leading empty
    /// component of an absolute path is KEPT, so "/a/b" is ["", "a", "b"] and two paths on different
    /// roots still share that first component (which is what makes "/" the answer, not "").
    /// `standardizedFileURL` resolves "." and ".." without touching symlinks, matching Clean.
    private static func components(_ path: String) -> [String] {
        URL(fileURLWithPath: path).standardizedFileURL.path
            .split(separator: "/", omittingEmptySubsequences: false).map(String.init)
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
    ///
    /// A DRAFT, not view state. Nothing in this sheet ever assigns "" to it, so the only way the
    /// field could empty itself mid-edit — as it did when the agent picker was touched — is the
    /// sheet's transient `@State` being torn down and rebuilt underneath the user. Storing the text
    /// outside the view makes that unobservable, and buys the same protection against the other two
    /// ways a written task used to evaporate: a backgrounded phone, and a sheet dismissed by mistake.
    /// Cleared when the task is handed to a session, or when the user explicitly discards it.
    @AppStorage("oculus.newSession.draftPrompt") private var firstPrompt = ""
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

    /// The selection RESOLVED against the projects the daemon actually knows, in list order.
    ///
    /// Everything downstream counts this rather than the raw id set. Ids are restored from
    /// UserDefaults and can outlive their project (folder removed, different daemon), and an id with
    /// no project draws no row — so counting ids made the sheet claim "2 selected" with one row
    /// ticked, and then fail the create with "multi-repo needs at least 2 valid projects".
    private var chosenProjects: [Project] { model.projects.filter { selectedProjects.contains($0.id) } }
    private var isMulti: Bool { chosenProjects.count > 1 }
    private var singleSelectedProject: Project? {
        chosenProjects.count == 1 ? chosenProjects.first : nil
    }
    private var canWorktree: Bool { singleSelectedProject?.isGitRepo == true }
    /// Every selected repo is a git repo, so a multi-repo workspace (one worktree per repo) is
    /// possible. Single-repo isolation is canWorktree; canIsolate covers both.
    private var canIsolateMulti: Bool { isMulti && chosenProjects.allSatisfy(\.isGitRepo) }
    private var canIsolate: Bool { canWorktree || canIsolateMulti }
    /// Isolation as the daemon will receive it — the toggle can stay on from a previous session
    /// whose selection could isolate, while this one can't.
    private var effectiveIsolate: Bool { useWorktree && canIsolate }
    /// The verdict on the current folder selection, recomputed as it changes. See WorkingDirectoryPlan.
    private var plan: WorkingDirectoryPlan {
        .evaluate(paths: chosenProjects.map(\.path), isolate: effectiveIsolate, canIsolate: canIsolateMulti)
    }
    private var blocked: (summary: String, detail: String, fix: String)? {
        if case .blocked(let s, let d, let f) = plan { return (s, d, f) }
        return nil
    }
    /// Said in full on the Start button, because a disabled control with no explanation is its own
    /// usability trap — and VoiceOver otherwise announces "Start, dimmed" and nothing else.
    private var startHint: String {
        guard let b = blocked else { return "" }
        return "Unavailable. \(b.detail) \(b.fix)"
    }
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
            Button("Discard", role: .destructive) { firstPrompt = ""; onStart() }
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
            await model.loadProjects()
            // Drop ids whose project is gone. They draw no row, so they'd sit in the count as
            // phantoms and then fail the create for a reason nothing on screen could explain.
            // Guarded on a non-empty list: an empty one means the load failed, not that every
            // project vanished, and wiping the selection over a dropped socket would be worse.
            if !model.projects.isEmpty {
                selectedProjects.formIntersection(Set(model.projects.map(\.id)))
            }
            await scan()
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
            if mode == .new, let b = blocked {
                // The reason Start is dimmed, restated where the eye goes when a button doesn't
                // respond. The selection itself carries the full explanation and the fix.
                Label(b.summary, systemImage: "exclamationmark.triangle.fill")
                    .font(.caption).foregroundStyle(palette.warning)
                    .lineLimit(1)
            } else if mode == .new && isMulti {
                Label("\(chosenProjects.count) repos", systemImage: "square.stack.3d.up")
                    .font(.caption).foregroundStyle(palette.mutedForeground)
            }
            Spacer()
            Button("Cancel") { requestClose() }
                .keyboardShortcut(.cancelAction)
            if mode == .new {
                Button {
                    // Read the draft and the resolved selection BEFORE clearing the draft below:
                    // `firstPrompt` is backed by UserDefaults, so the Task would otherwise race the
                    // clear and send an empty first turn.
                    let prompt = firstPrompt
                    let ids = chosenProjects.map(\.id)
                    let isolate = effectiveIsolate
                    Task {
                        let chosen = models.first { $0.id == selectedModel }
                        await model.createSession(provider: provider,
                                                  projectIDs: ids.isEmpty ? nil : ids,
                                                  worktree: isolate,
                                                  workspaceName: workspaceName.isEmpty ? nil : workspaceName,
                                                  mode: sessionMode,
                                                  autonomous: autonomous,
                                                  model: selectedModel.isEmpty ? nil : selectedModel,
                                                  modelProvider: chosen?.provider,
                                                  prompt: prompt)
                        // Remember the shape of this session so the next one opens ready to repeat it.
                        UserDefaults.standard.set(isolate, forKey: Self.lastWorktreeKey)
                        UserDefaults.standard.set(ids, forKey: Self.lastProjectsKey)
                    }
                    firstPrompt = "" // handed to the session; don't offer it again next time
                    onStart()
                } label: { Text("Start").frame(minWidth: 52) }
                .buttonStyle(.borderedProminent).tint(palette.primary)
                .keyboardShortcut(.defaultAction)
                // Refused HERE rather than by the daemon after the prompt is written — see
                // WorkingDirectoryPlan for why the check lives on this side at all.
                .disabled(blocked != nil)
                .help(blocked?.summary ?? "Start the session")
                .accessibilityHint(startHint)
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
                    Text("Add").font(.caption.weight(.semibold)).foregroundStyle(palette.primaryText)
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
                }.buttonStyle(.plain).foregroundStyle(palette.primaryText)
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
                            .foregroundStyle(palette.primaryText)
                            .accessibilityHidden(true)
                        VStack(alignment: .leading, spacing: 1) {
                            // Count only what is actually RUNNING.
                            //
                            // Discovery returns every Claude transcript touched in the last 24 hours
                            // and flags which are live; this banner counted the whole list, so a busy
                            // day reported "50 sessions are running in your terminal" when almost all
                            // had long since exited. A number that overstates by an order of
                            // magnitude teaches you to ignore it.
                            //
                            // The dead ones stay in the takeover list — resuming yesterday's session
                            // is the point of it — they just are not claimed to be running.
                            Text(headlineForTerminalCandidates)
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

            workingDirectorySection

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

    /// The folder list: a MULTI-select that used to look like a radio group.
    ///
    /// Round marks, a singular title and no way to deselect but "click the same row again" meant a
    /// second pick read as "change my mind" when it means "add" — and the only feedback was a count
    /// in the footer, two hundred points away. Four things carry it now: square marks (the platform's
    /// multi-select glyph), a plural title with the count and a Clear beside it, one line that says
    /// picking more is allowed and how to undo it, and the verdict on the current selection right
    /// under the list — because some pairs of folders cannot start at all.
    private var workingDirectorySection: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack(spacing: 8) {
                Text("Working directories").font(.footnote.weight(.semibold))
                    .foregroundStyle(palette.mutedForeground)
                if !chosenProjects.isEmpty {
                    Text("\(chosenProjects.count) selected")
                        .font(.caption2.weight(.semibold)).foregroundStyle(palette.primaryText)
                        .padding(.horizontal, 6).padding(.vertical, 2)
                        .background(Capsule().fill(palette.primary.opacity(0.16)))
                }
                Spacer(minLength: 0)
                if !chosenProjects.isEmpty {
                    // Deselecting one row at a time is discoverable only once you know the rows
                    // toggle; this is the escape hatch for someone who doesn't yet.
                    Button { selectedProjects.removeAll() } label: {
                        Text("Clear").font(.caption.weight(.medium))
                            .foregroundStyle(palette.primaryText)
                            .frame(minWidth: 44, minHeight: 44, alignment: .trailing)
                            .contentShape(Rectangle())
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel("Clear selected folders")
                }
            }
            Text("Pick a repository — or several for a multi-repo task. \(selectVerb) a chosen folder again to remove it.")
                .font(.caption).foregroundStyle(palette.mutedForeground)
                .fixedSize(horizontal: false, vertical: true)
            VStack(spacing: 8) {
                // Repository first. What used to sit here was every folder the daemon had ever seen
                // an agent run in — worktrees, duplicate checkouts, temp directories — listed in
                // full and unsearchable, because auto-registration adds them and nothing removes
                // them.
                GitHubPicker(model: model, palette: palette) { path in
                    Task {
                        if let p = await model.addProject(path: path) {
                            selectedProjects.insert(p.id)
                        }
                    }
                }
                // Only what has actually been CHOSEN, so this can never grow back into the list it
                // replaced. Tapping one still removes it, which is where that gesture was learned.
                if !chosenProjects.isEmpty {
                    VStack(spacing: 5) {
                        ForEach(chosenProjects) { p in projectRow(p) }
                    }
                }
                addFolderRow
            }
            planNote
        }
    }

    /// Phone taps, Mac clicks. The instruction is worthless if it names the wrong gesture.
    private var selectVerb: String {
        #if os(macOS)
        return "Click"
        #else
        return "Tap"
        #endif
    }

    /// What the current selection means — where the agent will run, or why it can't.
    ///
    /// Sits directly under the folders, not in the footer and not in an alert after Start: the
    /// selection is what has to change, so this is the only place where reading it and fixing it are
    /// the same motion.
    @ViewBuilder private var planNote: some View {
        switch plan {
        case .blocked(_, let detail, let fix):
            VStack(alignment: .leading, spacing: 7) {
                HStack(alignment: .top, spacing: 7) {
                    // Icon + text, never colour alone — the warning tint is reinforcement here, not
                    // the message, so it survives Differentiate Without Color untouched.
                    Image(systemName: "exclamationmark.triangle.fill").font(.caption)
                        .foregroundStyle(palette.warning).accessibilityHidden(true)
                    VStack(alignment: .leading, spacing: 2) {
                        Text(detail).font(.caption).foregroundStyle(palette.foreground)
                        Text(fix).font(.caption).foregroundStyle(palette.mutedForeground)
                    }
                    .fixedSize(horizontal: false, vertical: true)
                }
                // One tap out of the dead end, when the repos are all git and isolation is therefore
                // available. Isolated workspaces don't need a shared parent — each repo gets its own
                // worktree under a folder the daemon creates.
                if canIsolateMulti && !useWorktree {
                    Button { useWorktree = true } label: {
                        Text("Give each repo its own worktree").font(.caption.weight(.medium))
                            .frame(minHeight: 30)
                    }
                    .buttonStyle(.bordered).controlSize(.small).tint(palette.primary)
                }
            }
            .padding(9)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(OculusShape.rounded(OculusRadius.sm).fill(palette.warning.opacity(0.12)))
            .overlay(OculusShape.rounded(OculusRadius.sm).strokeBorder(palette.warning.opacity(0.35)))
        case .ok(let runsIn):
            Text(okHelp(runsIn))
                .font(.caption).foregroundStyle(palette.mutedForeground)
                .fixedSize(horizontal: false, vertical: true)
        }
    }

    /// Says which folder the agent starts in rather than only that one exists — for a multi-repo
    /// session that folder is derived, and the derivation is exactly what surprises people.
    private func okHelp(_ runsIn: String?) -> String {
        guard isMulti else {
            return "Where the agent runs. Pick none for the daemon default, or pick several folders for a multi-repo task."
        }
        if effectiveIsolate {
            return "Isolated: each repo gets its own worktree under one workspace folder, so these \(chosenProjects.count) folders don't have to share a parent."
        }
        guard let runsIn else { return "" }
        return "The agent runs in \((runsIn as NSString).abbreviatingWithTildeInPath) — the folder these \(chosenProjects.count) repos share — so it can work across all of them."
    }

    private func projectRow(_ p: Project) -> some View {
        let sel = selectedProjects.contains(p.id)
        return Button { toggle(p.id) } label: {
            HStack(spacing: 10) {
                // Square, not round: a circle is the platform's one-of-N mark, and reading these as
                // radio buttons is what made a second pick feel like a replacement.
                Image(systemName: sel ? "checkmark.square.fill" : "square")
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
        // The visual multi-select cue is a glyph shape; VoiceOver needs it said. The hint is where
        // "these add up, and this is how you take one back" belongs.
        .accessibilityHint(sel ? "Removes this folder from the session" : "Adds this folder to the session")
    }

    @ViewBuilder private var addFolderRow: some View {
        VStack(spacing: 8) {
            // Primary: browse into a folder and pick several sub-folders at once (e.g. a "projects"
            // folder → check N repos, each gets its own worktree). Works on iOS + macOS.
            Button { showBrowser = true } label: {
                HStack(spacing: 8) {
                    Image(systemName: "folder.badge.plus").foregroundStyle(palette.primaryText)
                    Text("Browse folders…").font(.footnote.weight(.medium)).foregroundStyle(palette.primaryText)
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
                    .font(.subheadline).foregroundStyle(palette.primaryText)
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
        .foregroundStyle(palette.primaryText)
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

    /// Those the daemon could confirm are actually running, as opposed to merely recent.
    private var liveTerminalCandidates: [TakeoverCandidate] { terminalCandidates.filter(\.live) }

    /// Says what is true: how many are running, or — when none are — that there are recent ones to
    /// resume. Both are useful; only one of them is "running".
    private var headlineForTerminalCandidates: String {
        let live = liveTerminalCandidates.count
        switch live {
        case 0:
            let n = terminalCandidates.count
            return n == 1 ? "1 recent terminal session" : "\(n) recent terminal sessions"
        case 1:
            return "1 session is running in your terminal"
        default:
            return "\(live) sessions are running in your terminal"
        }
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
                    // Square marks here too — this list is multi-select as well, and two shapes for
                    // one meaning in the same flow is how "pick several" stops being believed.
                    Image(systemName: sel ? "checkmark.square.fill" : "square")
                        .foregroundStyle(sel ? palette.primary : palette.mutedForeground)
                    Image(systemName: e.isGitRepo ? "arrow.triangle.branch" : "folder")
                        .font(.footnote).foregroundStyle(e.isGitRepo ? palette.primary : palette.mutedForeground)
                    Text(e.name).font(.footnote).foregroundStyle(palette.foreground).lineLimit(1)
                    if e.isGitRepo {
                        Text("git").font(.caption2.weight(.semibold)).foregroundStyle(palette.primaryText)
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
