import SwiftUI
import OculusKit

/// Loops — recurring autonomous workflows, two kinds:
///   • Ticket loop — watches a tracker for new tickets in a category (e.g. "To do") and starts an
///     agent on each: plan → execute in a worktree, hands-free.
///   • Task loop — runs a custom prompt on a schedule (e.g. "scan for bugs, file issues, fix them" or
///     "review open PRs"), leaning on the agent's MCP tools. Either kind can span multiple repos.
/// This screen lists loops (enable/pause/edit), shows recent runs, and creates/edits a loop.
public struct LoopsView: View {
    @ObservedObject var model: Model
    let palette: OculusPalette
    var onOpenSession: (String) -> Void = { _ in }
    let onClose: () -> Void

    /// The loop a delete is staged against. A loop is a recurring workflow you configured once —
    /// deleting it throws that configuration away with no undo.
    @State private var pendingDelete: Loop? = nil

    /// The editor. On iOS it is PUSHED: this view is itself a sheet, so presenting the editor over it
    /// made two modal layers before the AgentPicker inside the editor could open a third.
    ///
    /// On macOS it stays a sheet. A pushed view there gets a toolbar back button SwiftUI gives no way
    /// to hide, and Back does not run the discard confirmation — which would be a new, unguarded exit
    /// from a form holding a task prompt someone wrote by hand.
    private enum Route: Hashable, Identifiable {
        case new
        case edit(String)

        var id: String {
            switch self {
            case .new: return "new"
            case .edit(let loopID): return "edit:" + loopID
            }
        }
    }

    #if os(iOS)
    @State private var path: [Route] = []
    #else
    @State private var route: Route? = nil
    #endif

    public init(model: Model, palette: OculusPalette, onOpenSession: @escaping (String) -> Void = { _ in }, onClose: @escaping () -> Void) {
        self.model = model; self.palette = palette; self.onOpenSession = onOpenSession; self.onClose = onClose
    }

    private func open(_ r: Route) {
        #if os(iOS)
        path.append(r)
        #else
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

    @ViewBuilder private func editor(_ r: Route, pushed: Bool) -> some View {
        switch r {
        case .new:
            LoopEditor(model: model, palette: palette, loop: nil, pushed: pushed, onDone: closeChild)
        case .edit(let loopID):
            // Looked up by id rather than carried: the daemon replaces the whole loop list on every
            // mutation, so a captured struct goes stale as soon as anything else changes.
            if let loop = model.loops.first(where: { $0.id == loopID }) {
                LoopEditor(model: model, palette: palette, loop: loop, pushed: pushed, onDone: closeChild)
            }
        }
    }

    /// The native list is only the right shape once there is something to list. With no loops the
    /// sheet is one centred empty state, which is not a list row.
    private var usesList: Bool {
        #if os(iOS)
        return !model.loops.isEmpty
        #else
        return false
        #endif
    }

    public var body: some View {
        #if os(iOS)
        NavigationStack(path: $path) {
            core
                // This screen draws its own header and close button; a navigation bar over it would
                // be a second, empty title.
                .toolbar(.hidden, for: .navigationBar)
                .navigationDestination(for: Route.self) { editor($0, pushed: true) }
        }
        #else
        // No NavigationStack on macOS: nothing is pushed there, and a stack that never navigates is
        // just a chance for it to reserve title-bar space inside a fixed-size sheet.
        core
            .frame(width: 560, height: 620)
            .sheet(item: $route) { editor($0, pushed: false) }
        #endif
    }

    private var core: some View {
        VStack(spacing: 0) {
            HStack {
                VStack(alignment: .leading, spacing: 1) {
                    Text("Loops").font(.body.weight(.semibold))
                    Text("Auto-run agents on tickets or a schedule").font(.caption).foregroundStyle(palette.mutedForeground)
                }
                Spacer()
                Button { open(.new) } label: { Label("New loop", systemImage: "plus") }
                    .buttonStyle(.borderedProminent).tint(palette.primary)
                // A bare ✕ with no label announced as "Button". It is also NOT the default action:
                // Return in a sheet that can create a loop must not mean "close".
                Button { onClose() } label: {
                    Image(systemName: "xmark").font(.caption.weight(.bold))
                        .foregroundStyle(palette.mutedForeground)
                        .frame(width: 22, height: 22).background(Circle().fill(palette.muted.opacity(0.5)))
                }
                .buttonStyle(.plain)
                .accessibilityLabel("Close Loops")
                .sheetTapTarget()
            }
            .padding()
            Divider().overlay(palette.border)

            content
        }
        .background(palette.background)
        .task { await model.loadLoops() }
        .confirmationDialog(
            "Delete this loop?",
            isPresented: Binding(get: { pendingDelete != nil }, set: { if !$0 { pendingDelete = nil } }),
            titleVisibility: .visible,
            presenting: pendingDelete
        ) { l in
            Button("Delete loop", role: .destructive) { delete(l) }
            Button("Cancel", role: .cancel) { pendingDelete = nil }
        } message: { l in
            Text("“\(l.name.isEmpty ? "Untitled loop" : l.name)” stops running and its configuration — trigger, repos, prompt, budget — is removed. Runs it already started keep going and stay in the list. If you only want it to stop for now, use the switch instead.")
        }
    }

    @ViewBuilder private var content: some View {
        #if os(iOS)
        if usesList { loopList } else { scrollBody }
        #else
        scrollBody
        #endif
    }

    /// The macOS shape: a scrolling column of bordered cards.
    private var scrollBody: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 20) {
                if model.loops.isEmpty {
                    emptyState
                } else {
                    VStack(spacing: 10) {
                        ForEach(model.loops) { loop in
                            loopBody(loop)
                                .padding(12)
                                .background(palette.card)
                                .overlay(OculusShape.rounded(OculusRadius.md).strokeBorder(palette.border))
                                .clipShape(OculusShape.rounded(OculusRadius.md))
                        }
                    }
                }
                if !model.loopRuns.isEmpty { runsSection }
            }
            .padding(16)
        }
    }

    #if os(iOS)
    /// The iOS shape: the platform's grouped list. "Recent runs" becomes a real section header, and
    /// the swipe restores the delete gesture the hand-rolled cards had no way to offer — staging the
    /// same confirmation the row's ••• menu does, because a loop you believe is deleted but isn't
    /// keeps starting autonomous agents.
    private var loopList: some View {
        List {
            Section("Loops") {
                ForEach(model.loops) { loop in
                    loopBody(loop)
                        .sheetSwipeDelete("Delete") { pendingDelete = loop }
                }
            }
            if !model.loopRuns.isEmpty {
                Section("Recent Runs") {
                    ForEach(model.loopRuns.prefix(30)) { run in runRow(run) }
                }
            }
        }
        .sheetListChrome(palette)
    }
    #endif

    /// `deleteLoop` returns Void and swallows its error. The daemon answers with the whole loop list,
    /// so a loop that's still in it is the honest signal the delete never landed — and a loop you
    /// believe is gone but isn't will keep starting autonomous agents.
    private func delete(_ loop: Loop) {
        pendingDelete = nil
        Task {
            await model.deleteLoop(loop.id)
            if model.loops.contains(where: { $0.id == loop.id }) {
                model.setError("Couldn’t delete that loop",
                               "It's still configured and will keep running. Check the daemon is connected and try again.")
            }
        }
    }

    /// Uses the shared empty state so this reads like every other empty screen — and carries the
    /// action, which the hand-rolled version left only in a header the eye doesn't visit.
    private var emptyState: some View {
        SheetEmptyState(icon: "arrow.triangle.2.circlepath",
                        title: "No loops yet",
                        message: "Create a loop to run agents hands-free — start one on every new To-Do ticket, or schedule a recurring job like \u{201C}scan for bugs, file issues, and fix them\u{201D} or \u{201C}review open PRs\u{201D} across one or more repos.",
                        palette: palette) {
            Button { open(.new) } label: { Label("New loop", systemImage: "plus") }
                .buttonStyle(.borderedProminent).tint(palette.primary)
        }
    }

    /// One loop, shared by both shapes. The card's padding and border are applied by the scrolling
    /// body; a List draws its own row background, and a bordered card inside one is a box in a box.
    private func loopBody(_ loop: Loop) -> some View {
        let repoNames = loop.repos.compactMap { id in model.projects.first { $0.id == id }?.name }
        let isTask = loop.kind == "task"
        return VStack(alignment: .leading, spacing: 8) {
            HStack {
                Circle().fill(loop.enabled ? palette.success : palette.mutedForeground)
                    .frame(width: 8, height: 8).accessibilityHidden(true)
                Text(loop.name.isEmpty ? "Untitled loop" : loop.name).font(.callout.weight(.semibold))
                Spacer()
                // This switch ARMS AN AUTONOMOUS LOOP. Unlabelled it announced as "off, switch" with
                // no referent — the one control in this app where a mis-hit starts agents by itself.
                Toggle("Enable loop \(loop.name.isEmpty ? "Untitled" : loop.name)",
                       isOn: Binding(get: { loop.enabled }, set: { on in setEnabled(loop, on) }))
                    .labelsHidden()
                    .tint(palette.primary)
                Menu {
                    Button { open(.edit(loop.id)) } label: { Label("Edit", systemImage: "pencil") }
                    Button(role: .destructive) { pendingDelete = loop } label: { Label("Delete", systemImage: "trash") }
                } label: { Image(systemName: "ellipsis").foregroundStyle(palette.mutedForeground) }
                .menuStyle(.borderlessButton).fixedSize()
                .accessibilityLabel("Actions for \(loop.name.isEmpty ? "Untitled loop" : loop.name)")
            }
            if isTask, !loop.prompt.isEmpty {
                Text(loop.prompt).font(.caption).foregroundStyle(palette.foreground).lineLimit(2)
            }
            HStack(spacing: 6) {
                if isTask {
                    chip(Image(systemName: "clock"), intervalLabel(loop.intervalMinutes))
                } else {
                    chip(nil, "New \(categoryLabel(loop.triggerCategory)) tickets")
                    if let t = loop.tracker, !t.isEmpty { chip(nil, t.capitalized) }
                }
                chip(Image(systemName: repoNames.count > 1 ? "square.stack.3d.up" : "folder"),
                     repoNames.isEmpty ? "?" : (repoNames.count <= 2 ? repoNames.joined(separator: ", ") : "\(repoNames.count) repos"))
                chip(nil, loop.provider)
                if loop.worktree { chip(nil, "worktree") }
                if loop.plan { chip(nil, "plan") }
            }
        }
    }

    /// NOTE: a failed enable/disable can't be detected here — `setLoopEnabled` returns Void and only
    /// updates `loops` on SUCCESS, so a failure leaves the row showing the OLD value while the
    /// binding's setter already ran. The switch appears to snap back with no explanation. The fix
    /// belongs in the model: it needs to return `String?`.
    private func setEnabled(_ loop: Loop, _ on: Bool) {
        Task { await model.setLoopEnabled(loop.id, on) }
    }

    private var runsSection: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text("Recent runs").font(.caption.weight(.semibold)).foregroundStyle(palette.mutedForeground)
            ForEach(model.loopRuns.prefix(30)) { run in
                runRow(run)
                    .padding(.horizontal, 10).padding(.vertical, 7)
                    .background(OculusShape.rounded(OculusRadius.sm).fill(palette.muted.opacity(0.2)))
            }
        }
    }

    private func runRow(_ run: LoopRun) -> some View {
        Button { onOpenSession(run.sessionID) } label: {
            HStack(spacing: 8) {
                runStatusDot(run)
                Text(run.issueKey == "task" ? loopName(run.loopID) : run.issueKey)
                    .font(.caption.bold()).foregroundStyle(palette.primaryText).frame(width: 90, alignment: .leading).lineLimit(1)
                Text(run.issueTitle).font(.caption).lineLimit(1).foregroundStyle(palette.foreground)
                Spacer()
                Text(liveStatus(run)).font(.caption2).foregroundStyle(palette.mutedForeground)
                Image(systemName: "chevron.right").font(.caption2).foregroundStyle(palette.mutedForeground)
                    .accessibilityHidden(true)
            }
            .contentShape(Rectangle())
        }.buttonStyle(.plain)
    }

    private func loopName(_ id: String) -> String { model.loops.first { $0.id == id }?.name ?? "task" }

    /// The run's live status, derived from the matching session when we have it (else the recorded status).
    private func liveStatus(_ run: LoopRun) -> String {
        if let s = model.sessions.first(where: { $0.id == run.sessionID }) { return s.status }
        return run.status
    }
    private func runStatusDot(_ run: LoopRun) -> some View {
        let st = liveStatus(run)
        let c: Color = st == SessionStatusValue.running ? palette.success : (st == "error" ? palette.destructive : palette.mutedForeground)
        return Circle().fill(c).frame(width: 7, height: 7)
    }

    private func chip(_ icon: Image?, _ t: String) -> some View {
        HStack(spacing: 3) {
            if let icon { icon.font(.caption2) }
            Text(t)
        }
        .font(.caption2).padding(.horizontal, 6).padding(.vertical, 2)
        .background(Capsule().fill(palette.muted.opacity(0.4))).foregroundStyle(palette.mutedForeground)
    }
    private func categoryLabel(_ c: String) -> String {
        switch c { case "todo": return "To-Do"; case "in_progress": return "In-Progress"; case "done": return "Done"; default: return c }
    }
    private func intervalLabel(_ m: Int) -> String {
        if m % (60 * 24) == 0, m > 0 { let d = m / (60 * 24); return d == 1 ? "daily" : "every \(d)d" }
        if m % 60 == 0, m > 0 { let h = m / 60; return h == 1 ? "hourly" : "every \(h)h" }
        return "every \(max(1, m))m"
    }
}

/// Create/edit a loop.
struct LoopEditor: View {
    @ObservedObject var model: Model
    let palette: OculusPalette
    /// Pushed onto LoopsView's stack rather than presented over it. It then has no NavigationStack of
    /// its own — nesting one inside another gives the pushed screen its own back stack, which strands
    /// the user — and no fixed macOS window size, since it lives inside the window it was pushed in.
    let pushed: Bool
    let onDone: () -> Void

    @State private var draft: Loop
    /// What the editor opened with. Anything different is unsaved work — and a task loop's prompt is
    /// several paragraphs someone wrote by hand, which a stray swipe-dismiss used to throw away.
    private let original: Loop
    @State private var confirmDiscard = false
    @State private var problem: String? = nil
    private let isNew: Bool

    // Task-loop starter templates (each leans on the agent's MCP tools).
    private static let templates: [(String, String, String)] = [
        ("Find & fix bugs", "ladybug",
         "Scan this codebase for real bugs and correctness issues. For each significant one, file a tracker issue describing it, then fix the highest-priority bug and open a PR with the fix."),
        ("Review open PRs", "checkmark.circle",
         "Find the open pull requests in this repository. Review each one for correctness, security, and style issues, and leave concise review feedback. Summarize what you reviewed."),
        ("Dependency & security audit", "shield",
         "Audit the project's dependencies for known vulnerabilities and outdated packages using your security tools. File an issue for anything serious, then open a PR bumping the safest high-value updates."),
        ("Triage new issues", "tray.full",
         "Look at recently opened issues in the connected tracker. For each, investigate the codebase, add a diagnostic comment with likely root cause and a suggested fix, and label it."),
    ]

    init(model: Model, palette: OculusPalette, loop: Loop?, pushed: Bool = false,
         onDone: @escaping () -> Void) {
        self.model = model; self.palette = palette; self.pushed = pushed; self.onDone = onDone
        self.isNew = loop == nil
        var initial = loop ?? Loop()
        // Migrate a legacy single-repo loop into the multi-repo field for editing.
        if initial.projectIDs.isEmpty, !initial.projectID.isEmpty {
            initial.projectIDs = [initial.projectID]; initial.projectID = ""
        }
        _draft = State(initialValue: initial)
        self.original = initial
    }

    private var dirty: Bool { draft != original }

    private var isTask: Bool { draft.kind == "task" }

    var body: some View {
        Group {
            if pushed { form } else { NavigationStack { form } }
        }
        #if os(macOS)
        .frame(width: pushed ? nil : 480, height: pushed ? nil : 640)
        #endif
        .sheetDraftGuard(dirty)
        .confirmationDialog(isNew ? "Discard this loop?" : "Discard changes?",
                            isPresented: $confirmDiscard, titleVisibility: .visible) {
            Button("Discard", role: .destructive) { onDone() }
            Button("Keep editing", role: .cancel) {}
        } message: {
            Text(isTask && !draft.prompt.isEmpty
                 ? "The task you've written won't be saved."
                 : "Your changes won't be saved.")
        }
    }

    private var form: some View {
        Form {
                Section("Loop") {
                    // The name is used as a label everywhere, including in branch-ish contexts —
                    // autocapitalizing it produces a different loop name than the one typed.
                    TextField("Name", text: $draft.name).plainInput()
                    Picker("Kind", selection: $draft.kind) {
                        Text("On new tickets").tag("ticket")
                        Text("Scheduled task").tag("task")
                    }
                    Toggle("Enabled", isOn: $draft.enabled)
                }

                if isTask {
                    Section("Task") {
                        Menu {
                            ForEach(Self.templates, id: \.0) { tpl in
                                Button { draft.prompt = tpl.2; if draft.name.isEmpty { draft.name = tpl.0 } } label: { Label(tpl.0, systemImage: tpl.1) }
                            }
                        } label: { Label("Use a template…", systemImage: "sparkles") }
                        ZStack(alignment: .topLeading) {
                            if draft.prompt.isEmpty {
                                Text("Describe the recurring job — e.g. \u{201C}find bugs, file issues, and fix the top one.\u{201D} The agent uses its MCP tools to carry it out.")
                                    .font(.caption).foregroundStyle(palette.mutedForeground).padding(.top, 8).padding(.leading, 4).allowsHitTesting(false)
                            }
                            TextEditor(text: $draft.prompt).frame(minHeight: 90).font(.callout)
                        }
                        Picker("Run", selection: $draft.intervalMinutes) {
                            Text("Every hour").tag(60)
                            Text("Every 6 hours").tag(360)
                            Text("Every 12 hours").tag(720)
                            Text("Daily").tag(1440)
                            Text("Weekly").tag(10080)
                        }
                    }
                } else {
                    Section("Trigger") {
                        Picker("On new", selection: $draft.triggerCategory) {
                            Text("To-Do tickets").tag("todo")
                            Text("In-Progress tickets").tag("in_progress")
                        }
                        Picker("Tracker", selection: Binding(get: { draft.tracker ?? "" }, set: { draft.tracker = $0.isEmpty ? nil : $0 })) {
                            Text("Any").tag("")
                            ForEach(model.connectedTrackers, id: \.self) { Text($0.capitalized).tag($0) }
                        }
                    }
                }

                Section(model.projects.count > 1 ? "Repos" : "Repo") {
                    if model.projects.isEmpty {
                        Text("No projects registered — add a project first.").font(.caption).foregroundStyle(palette.mutedForeground)
                    }
                    ForEach(model.projects) { p in
                        Button { toggleRepo(p.id) } label: {
                            HStack {
                                Image(systemName: draft.projectIDs.contains(p.id) ? "checkmark.circle.fill" : "circle")
                                    .foregroundStyle(draft.projectIDs.contains(p.id) ? palette.primary : palette.mutedForeground)
                                Text(p.name).foregroundStyle(palette.foreground)
                                Spacer()
                            }.contentShape(Rectangle())
                        }.buttonStyle(.plain)
                    }
                    if draft.projectIDs.count > 1 {
                        Text("Runs across all selected repos as one multi-root workspace (a worktree per repo when isolation is on).")
                            .font(.caption2).foregroundStyle(palette.mutedForeground)
                    }
                }

                Section("Run") {
                    AgentPicker(model: model, selection: $draft.provider, palette: palette)
                    Toggle("Isolate in a worktree", isOn: $draft.worktree)
                    Toggle("Plan first (approve before building)", isOn: $draft.plan)
                    Stepper("Max concurrent: \(draft.maxConcurrent)", value: $draft.maxConcurrent, in: 1...5)
                    Stepper("Budget: $\(String(format: "%.0f", draft.budgetUSD))", value: $draft.budgetUSD, in: 1...100, step: 1)
                }

                if let problem {
                    Section {
                        Text(problem).font(.callout).foregroundStyle(palette.destructive)
                            .fixedSize(horizontal: false, vertical: true)
                    }
                }

                Section {
                    Text(isTask
                         ? "On its schedule the loop starts an autonomous agent with this prompt across the selected repos. It uses its tools — including MCP servers (GitHub, trackers, security scanners) — supervised by the heartbeat within the budget. A new run won't start while a prior one is still going."
                         : "The loop starts an autonomous agent on each new matching ticket — it plans, builds, and opens a PR, supervised by the heartbeat within the budget. Already-seen tickets aren't re-run.")
                        .font(.caption).foregroundStyle(palette.mutedForeground)
                }
        }
        .navigationTitle(isNew ? "New loop" : "Edit loop")
        #if os(iOS)
        .navigationBarTitleDisplayMode(.inline)
        // Back would leave WITHOUT the discard confirmation — and a task loop's prompt is several
        // paragraphs someone wrote by hand. Cancel is the only way out, and Cancel asks.
        .navigationBarBackButtonHidden(pushed)
        #endif
        .toolbar {
            ToolbarItem(placement: .cancellationAction) { Button("Cancel") { cancel() } }
            // Kept ENABLED and validated on tap. The disabled Save spanned name, repos AND
            // prompt, so it could tell you it wouldn't work but not which of the three was why.
            ToolbarItem(placement: .confirmationAction) { Button("Save") { save() } }
        }
    }

    private func cancel() {
        if dirty { confirmDiscard = true } else { onDone() }
    }

    private func save() {
        if draft.name.trimmingCharacters(in: .whitespaces).isEmpty {
            problem = "Give the loop a name — it's how it's listed and reported."; return
        }
        if draft.projectIDs.isEmpty {
            problem = model.projects.isEmpty
                ? "Register a project first — a loop needs at least one repo to run in."
                : "Pick at least one repo for the loop to run in."
            return
        }
        if isTask, draft.prompt.trimmingCharacters(in: .whitespaces).isEmpty {
            problem = "Describe the recurring job — that prompt is what each run is given."; return
        }
        problem = nil
        Task {
            await model.upsertLoop(draft)
            // `upsertLoop` returns Void and swallows its error, then reloads. A loop that isn't in
            // the reloaded list didn't save, and closing on that would silently discard the prompt.
            if model.loops.contains(where: { $0.id == draft.id || $0.name == draft.name }) {
                onDone()
            } else {
                problem = "Couldn't save this loop. Check the daemon is connected — your changes are still here."
            }
        }
    }

    private func toggleRepo(_ id: String) {
        if let i = draft.projectIDs.firstIndex(of: id) { draft.projectIDs.remove(at: i) } else { draft.projectIDs.append(id) }
    }
}
