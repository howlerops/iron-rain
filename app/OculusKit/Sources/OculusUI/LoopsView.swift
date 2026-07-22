import SwiftUI
import OculusKit

/// Loops — recurring autonomous workflows. A loop watches a tracker for new tickets in a category
/// (e.g. "To do") and starts an agent on each: plan → execute in a worktree, hands-free. This screen
/// lists loops (enable/pause/edit), shows recent runs, and creates/edits a loop.
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
                    Text("Auto-run agents on new tickets").font(.caption).foregroundStyle(palette.mutedForeground)
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
            Text("Create a loop to have Iron Rain automatically start an agent on every new To-Do ticket — plan, build, and open a PR, hands-free.")
                .font(.subheadline).foregroundStyle(palette.mutedForeground).multilineTextAlignment(.center).frame(maxWidth: 400)
        }
        .frame(maxWidth: .infinity).padding(.vertical, 30)
    }

    private func loopCard(_ loop: Loop) -> some View {
        let project = model.projects.first { $0.id == loop.projectID }
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
            HStack(spacing: 6) {
                chip("New \(categoryLabel(loop.triggerCategory)) tickets")
                if let t = loop.tracker, !t.isEmpty { chip(t.capitalized) }
                chip("→ \(project?.name ?? "?")")
                chip(loop.provider)
                if loop.worktree { chip("worktree") }
                if loop.plan { chip("plan") }
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
                        Text(run.issueKey).font(.caption.bold()).foregroundStyle(palette.primary).frame(width: 78, alignment: .leading)
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

    private func chip(_ t: String) -> some View {
        Text(t).font(.caption2).padding(.horizontal, 6).padding(.vertical, 2)
            .background(Capsule().fill(palette.muted.opacity(0.4))).foregroundStyle(palette.mutedForeground)
    }
    private func categoryLabel(_ c: String) -> String {
        switch c { case "todo": return "To-Do"; case "in_progress": return "In-Progress"; case "done": return "Done"; default: return c }
    }
}

/// Create/edit a loop.
struct LoopEditor: View {
    @ObservedObject var model: Model
    let palette: OculusPalette
    let onDone: () -> Void

    @State private var draft: Loop
    private let isNew: Bool

    init(model: Model, palette: OculusPalette, loop: Loop?, onDone: @escaping () -> Void) {
        self.model = model; self.palette = palette; self.onDone = onDone
        self.isNew = loop == nil
        _draft = State(initialValue: loop ?? Loop())
    }

    private var providers: [String] { model.providers.isEmpty ? ["opencode", "claude-code", "pi"] : model.providers }

    var body: some View {
        NavigationStack {
            Form {
                Section("Loop") {
                    TextField("Name", text: $draft.name)
                    Toggle("Enabled", isOn: $draft.enabled)
                }
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
                Section("Run") {
                    Picker("Agent", selection: $draft.provider) { ForEach(providers, id: \.self) { Text($0).tag($0) } }
                    Picker("Repo", selection: $draft.projectID) {
                        Text("— pick a project —").tag("")
                        ForEach(model.projects) { Text($0.name).tag($0.id) }
                    }
                    Toggle("Isolate in a worktree", isOn: $draft.worktree)
                    Toggle("Plan first (approve before building)", isOn: $draft.plan)
                    Stepper("Max concurrent: \(draft.maxConcurrent)", value: $draft.maxConcurrent, in: 1...5)
                    Stepper("Budget: $\(String(format: "%.0f", draft.budgetUSD))", value: $draft.budgetUSD, in: 1...100, step: 1)
                }
                Section {
                    Text("The loop starts an autonomous agent on each new matching ticket — it plans, builds, and opens a PR, supervised by the heartbeat within the budget. Already-seen tickets aren't re-run.")
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
                        .disabled(draft.projectID.isEmpty || draft.name.trimmingCharacters(in: .whitespaces).isEmpty)
                }
            }
        }
        #if os(macOS)
        .frame(width: 460, height: 520)
        #endif
    }
}
