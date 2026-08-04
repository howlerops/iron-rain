import SwiftUI
import OculusKit

/// The finish panel for a worktree session: review the diff, open a PR, or remove the
/// worktree. Backed by the Model's worktreeDiff/createPR/removeWorktree methods.
struct WorktreePanel: View {
    @ObservedObject var model: Model
    let palette: OculusPalette
    let onClose: () -> Void

    @State private var prTitle = ""
    @State private var prBody = ""
    @State private var confirmRemove = false

    private var session: Session? { model.currentSession }

    var body: some View {
        NavigationStack {
            Form {
                if let s = session {
                    Section("Workspace") {
                        LabeledContent("Branch", value: s.branch ?? "—")
                        if let ws = s.workspaceName { LabeledContent("Name", value: ws) }
                        if let port = s.port, port != 0 { LabeledContent("Port", value: String(port)) }
                    }
                }

                if !model.conflicts.isEmpty {
                    Section {
                        ForEach(model.conflicts) { c in
                            HStack(alignment: .top, spacing: 8) {
                                Image(systemName: "exclamationmark.triangle.fill")
                                    .foregroundStyle(.orange).font(.caption)
                                VStack(alignment: .leading, spacing: 1) {
                                    Text(c.path).font(.system(.caption, design: .monospaced))
                                    Text("also edited on: \(c.branches.joined(separator: ", "))")
                                        .font(.caption2).foregroundStyle(palette.mutedForeground)
                                }
                            }
                        }
                    } header: {
                        Text("Shared-file conflicts")
                    } footer: {
                        Text("These files are also being changed in other active worktrees — expect merge conflicts.")
                            .font(.caption)
                    }
                }

                Section {
                    Button {
                        Task { await model.catchUpToMain() }
                    } label: {
                        HStack {
                            Label("Catch up to main", systemImage: "arrow.triangle.pull")
                            if model.catchingUp { Spacer(); ProgressView().controlSize(.small) }
                        }
                    }
                    .disabled(model.catchingUp)
                    if let msg = model.catchUpMessage {
                        Text(msg).font(.caption)
                            .foregroundStyle(model.catchUpConflicts.isEmpty ? palette.mutedForeground : .orange)
                    }
                    ForEach(model.catchUpConflicts, id: \.self) { f in
                        Label(f, systemImage: "exclamationmark.triangle")
                            .font(.system(.caption, design: .monospaced)).foregroundStyle(.orange)
                    }
                } header: {
                    Text("Update")
                } footer: {
                    Text("Merges the repo's default branch into this branch so it stays current. Conflicts are left in the worktree for the agent to resolve.")
                        .font(.caption)
                }

                Section("Review") {
                    DiffReviewView(model: model, palette: palette)
                        .frame(height: 360)
                        .listRowInsets(EdgeInsets())
                        .listRowBackground(Color.clear)
                }

                if let st = model.worktreeStatus, let prState = st.state, !prState.isEmpty {
                    Section {
                        LabeledContent("State", value: prState.capitalized)
                        if let c = st.checks { checksRow(c) }
                        if let u = st.url, let link = URL(string: u) {
                            Link(destination: link) {
                                Label("Open on GitHub", systemImage: "arrow.up.right.square")
                            }
                        }
                        Button {
                            Task { await model.refreshWorktreeStatus() }
                        } label: {
                            Label("Refresh checks", systemImage: "arrow.clockwise")
                        }
                    } header: {
                        Text("Pull request")
                    }
                }

                Section("Open a pull request") {
                    TextField("Title", text: $prTitle).textFieldStyle(.roundedBorder)
                    TextField("Description (optional)", text: $prBody, axis: .vertical)
                        .lineLimit(2...5).textFieldStyle(.roundedBorder)
                    Button {
                        Task { await model.createPR(title: prTitle.isEmpty ? (session?.workspaceName ?? "Iron Rain changes") : prTitle,
                                                    body: prBody.isEmpty ? nil : prBody) }
                    } label: {
                        Label("Commit, push & open PR", systemImage: "arrow.up.forward.square")
                    }
                }

                Section {
                    Button(role: .destructive) { confirmRemove = true } label: {
                        Label("Remove worktree", systemImage: "trash")
                    }
                } footer: {
                    Text("Deletes the worktree and its branch checkout. Uncommitted changes are lost.")
                        .font(.caption)
                }
            }
            .navigationTitle("Finish worktree")
            .toolbar {
                ToolbarItem(placement: .cancellationAction) { Button("Done") { onClose() } }
            }
            .onAppear {
                prTitle = session?.workspaceName ?? ""
                Task { await model.worktreeDiff(); await model.loadConflicts(); await model.refreshWorktreeStatus() }
            }
            .confirmationDialog("Remove this worktree?", isPresented: $confirmRemove, titleVisibility: .visible) {
                Button("Remove", role: .destructive) {
                    Task {
                        await model.removeWorktree(force: true)
                        model.newSession()
                        onClose()
                    }
                }
                Button("Cancel", role: .cancel) {}
            }
        }
    }

    /// The PR's CI verdict: one coloured badge with the counts, then the failing check names. This is
    /// the difference between "there's a PR" and "it's safe to land" — the whole reason someone
    /// reviewing from their phone opens this panel.
    @ViewBuilder private func checksRow(_ c: PRChecks) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            HStack(spacing: 6) {
                Image(systemName: checksIcon(c)).font(.caption).foregroundStyle(checksTint(c))
                Text(checksSummary(c)).font(.caption)
            }
            // Indexed: two CI apps can report the same check name, and \.self would collapse them.
            ForEach(Array((c.failing ?? []).enumerated()), id: \.offset) { _, name in
                Label(name, systemImage: "xmark.octagon")
                    .font(.system(.caption, design: .monospaced))
                    .foregroundStyle(palette.destructive)
            }
            // The daemon caps the names it sends, so say so rather than implying only these failed.
            if c.failedCount > (c.failing?.count ?? 0) {
                Text("+\(c.failedCount - (c.failing?.count ?? 0)) more failing")
                    .font(.caption2).foregroundStyle(palette.mutedForeground)
            }
        }
    }

    private func checksIcon(_ c: PRChecks) -> String {
        switch c.state {
        case "SUCCESS": return "checkmark.circle.fill"
        case "FAILURE": return "xmark.circle.fill"
        default: return "clock"
        }
    }

    private func checksTint(_ c: PRChecks) -> Color {
        switch c.state {
        case "SUCCESS": return .green
        case "FAILURE": return palette.destructive
        default: return palette.mutedForeground
        }
    }

    private func checksSummary(_ c: PRChecks) -> String {
        var parts: [String] = []
        if c.passedCount > 0 { parts.append("\(c.passedCount) passed") }
        if c.failedCount > 0 { parts.append("\(c.failedCount) failed") }
        if c.pendingCount > 0 { parts.append("\(c.pendingCount) running") }
        return parts.isEmpty ? "No checks" : parts.joined(separator: " · ")
    }
}
