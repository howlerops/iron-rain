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

    @State private var editing: Loop?
    @State private var creating = false

    public init(model: Model, palette: OculusPalette, onOpenSession: @escaping (String) -> Void = { _ in }, onClose: @escaping () -> Void) {
        self.model = model; self.palette = palette; self.onOpenSession = onOpenSession; self.onClose = onClose
    }

    public var body: some View {
        VStack(spacing: 0) {
            HStack {
                VStack(alignment: .leading, spacing: 1) {
                    Text("Loops").font(.system(size: 17, weight: .semibold))
                    Text("Auto-run agents on tickets or a schedule").font(.caption).foregroundStyle(palette.mutedForeground)
                }
                Spacer()
                Button { creating = true } label: { Label("New loop", systemImage: "plus") }
                    .buttonStyle(.borderedProminent).tint(palette.primary)
                Button { onClose() } label: { Image(systemName: "xmark").font(.system(size: 11, weight: .bold)).foregroundStyle(palette.mutedForeground)
                    .frame(width: 22, height: 22).background(Circle().fill(palette.muted.opacity(0.5))) }.buttonStyle(.plain)
            }
            .padding()
            Divider().overlay(palette.border)

            ScrollView {
                VStack(alignment: .leading, spacing: 20) {
                    if model.loops.isEmpty {
                        emptyState
                    } else {
                        VStack(spacing: 10) { ForEach(model.loops) { loopCard($0) } }
                    }
                    if !model.loopRuns.isEmpty { runsSection }
                }
                .padding(16)
            }
        }
        #if os(macOS)
        .frame(width: 560, height: 620)
        #endif
        .background(palette.background)
        .task { await model.loadLoops() }
        .sheet(item: $editing) { LoopEditor(model: model, palette: palette, loop: $0) { editing = nil } }
        .sheet(isPresented: $creating) { LoopEditor(model: model, palette: palette, loop: nil) { creating = false } }
    }

    private var emptyState: some View {
        VStack(spacing: 10) {
            Image(systemName: "arrow.triangle.2.circlepath").font(.system(size: 30)).foregroundStyle(palette.primary)
            Text("No loops yet").font(.headline)
            Text("Create a loop to run agents hands-free — start one on every new To-Do ticket, or schedule a recurring job like \u{201C}scan for bugs, file issues, and fix them\u{201D} or \u{201C}review open PRs\u{201D} across one or more repos.")
                .font(.subheadline).foregroundStyle(palette.mutedForeground).multilineTextAlignment(.center).frame(maxWidth: 400)
        }
        .frame(maxWidth: .infinity).padding(.vertical, 30)
    }

    private func loopCard(_ loop: Loop) -> some View {
        let repoNames = loop.repos.compactMap { id in model.projects.first { $0.id == id }?.name }
        let isTask = loop.kind == "task"
        return VStack(alignment: .leading, spacing: 8) {
            HStack {
                Circle().fill(loop.enabled ? Color.green : palette.mutedForeground).frame(width: 8, height: 8)
                Text(loop.name.isEmpty ? "Untitled loop" : loop.name).font(.callout.weight(.semibold))
                Spacer()
                Toggle("", isOn: Binding(get: { loop.enabled }, set: { on in Task { await model.setLoopEnabled(loop.id, on) } }))
                    .labelsHidden()
                Menu {
                    Button { editing = loop } label: { Label("Edit", systemImage: "pencil") }
                    Button(role: .destructive) { Task { await model.deleteLoop(loop.id) } } label: { Label("Delete", systemImage: "trash") }
                } label: { Image(systemName: "ellipsis").foregroundStyle(palette.mutedForeground) }.menuStyle(.borderlessButton).fixedSize()
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
        .padding(12)
        .background(palette.card).overlay(RoundedRectangle(cornerRadius: 12).stroke(palette.border)).clipShape(RoundedRectangle(cornerRadius: 12))
    }

    private var runsSection: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text("Recent runs").font(.caption.weight(.semibold)).foregroundStyle(palette.mutedForeground)
            ForEach(model.loopRuns.prefix(30)) { run in
                Button { onOpenSession(run.sessionID) } label: {
                    HStack(spacing: 8) {
                        runStatusDot(run)
                        Text(run.issueKey == "task" ? loopName(run.loopID) : run.issueKey)
                            .font(.caption.bold()).foregroundStyle(palette.primary).frame(width: 90, alignment: .leading).lineLimit(1)
                        Text(run.issueTitle).font(.caption).lineLimit(1).foregroundStyle(palette.foreground)
                        Spacer()
                        Text(liveStatus(run)).font(.caption2).foregroundStyle(palette.mutedForeground)
                        Image(systemName: "chevron.right").font(.caption2).foregroundStyle(palette.mutedForeground)
                    }
                    .padding(.horizontal, 10).padding(.vertical, 7)
                    .background(RoundedRectangle(cornerRadius: 8).fill(palette.muted.opacity(0.2)))
                    .contentShape(Rectangle())
                }.buttonStyle(.plain)
            }
        }
    }

    private func loopName(_ id: String) -> String { model.loops.first { $0.id == id }?.name ?? "task" }

    /// The run's live status, derived from the matching session when we have it (else the recorded status).
    private func liveStatus(_ run: LoopRun) -> String {
        if let s = model.sessions.first(where: { $0.id == run.sessionID }) { return s.status }
        return run.status
    }
    private func runStatusDot(_ run: LoopRun) -> some View {
        let st = liveStatus(run)
        let c: Color = st == SessionStatusValue.running ? .green : (st == "error" ? .red : palette.mutedForeground)
        return Circle().fill(c).frame(width: 7, height: 7)
    }

    private func chip(_ icon: Image?, _ t: String) -> some View {
        HStack(spacing: 3) {
            if let icon { icon.font(.system(size: 8)) }
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
    let onDone: () -> Void

    @State private var draft: Loop
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

    init(model: Model, palette: OculusPalette, loop: Loop?, onDone: @escaping () -> Void) {
        self.model = model; self.palette = palette; self.onDone = onDone
        self.isNew = loop == nil
        var initial = loop ?? Loop()
        // Migrate a legacy single-repo loop into the multi-repo field for editing.
        if initial.projectIDs.isEmpty, !initial.projectID.isEmpty {
            initial.projectIDs = [initial.projectID]; initial.projectID = ""
        }
        _draft = State(initialValue: initial)
    }

    private var isTask: Bool { draft.kind == "task" }

    var body: some View {
        NavigationStack {
            Form {
                Section("Loop") {
                    TextField("Name", text: $draft.name)
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
            #endif
            .toolbar {
                ToolbarItem(placement: .cancellationAction) { Button("Cancel") { onDone() } }
                ToolbarItem(placement: .confirmationAction) {
                    Button("Save") { Task { await model.upsertLoop(draft); onDone() } }
                        .disabled(!isValid)
                }
            }
        }
        #if os(macOS)
        .frame(width: 480, height: 640)
        #endif
    }

    private var isValid: Bool {
        guard !draft.name.trimmingCharacters(in: .whitespaces).isEmpty, !draft.projectIDs.isEmpty else { return false }
        if isTask { return !draft.prompt.trimmingCharacters(in: .whitespaces).isEmpty }
        return true
    }

    private func toggleRepo(_ id: String) {
        if let i = draft.projectIDs.firstIndex(of: id) { draft.projectIDs.remove(at: i) } else { draft.projectIDs.append(id) }
    }
}
