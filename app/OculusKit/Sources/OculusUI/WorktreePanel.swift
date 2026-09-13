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
    @State private var mergeMessage = ""
    @State private var confirmRemove = false

    private var session: Session? { model.currentSession }

    /// The CI verdict must not be carried by the badge's colour alone: the icon set below already
    /// differs per state, and this adds the word to the summary line for the same reason.
    private func checksStateWord(_ c: PRChecks) -> String {
        switch c.state {
        case "SUCCESS": return "Passing"
        case "FAILURE": return "Failing"
        default: return "Running"
        }
    }

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
                                    .foregroundStyle(palette.warning).font(.caption)
                                    .accessibilityHidden(true)
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
                            .foregroundStyle(model.catchUpConflicts.isEmpty ? palette.mutedForeground : palette.warning)
                    }
                    ForEach(model.catchUpConflicts, id: \.self) { f in
                        Label(f, systemImage: "exclamationmark.triangle")
                            .font(.system(.caption, design: .monospaced)).foregroundStyle(palette.warning)
                    }
                } header: {
                    Text("Update")
                } footer: {
                    Text("Merges the repo's default branch into this branch so it stays current. Conflicts are left in the worktree for the agent to resolve.")
                        .font(.caption)
                }

                Section("Review") {
                    DiffReviewView(model: model, palette: palette)
                        // minHeight so the reviewer can grow with Dynamic Type; capped so a large
                        // diff can't turn one Form row into an unscrollable wall.
                        .frame(minHeight: 360, maxHeight: 560)
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

                // A repo with no remote cannot have a pull request, and until now that was the end of
                // the road: the agent's work sat on a worktree branch with nothing in the app able to
                // land it. `worktree.merge` has been implemented end to end the whole time with no
                // caller. Gated on has_remote, which already arrives on every status poll, so the
                // ordinary GitHub flow below is untouched.
                if model.worktreeStatus?.hasRemote == false {
                    Section {
                        TextField("Merge message (optional)", text: $mergeMessage)
                            .textFieldStyle(.roundedBorder)
                        Button {
                            Task {
                                if await model.mergeWorktree(message: mergeMessage.isEmpty ? nil : mergeMessage) {
                                    onClose()
                                }
                            }
                        } label: {
                            Label("Land on the default branch", systemImage: "arrow.triangle.merge")
                        }
                        .disabled(model.busy)
                    } header: {
                        Text("Land locally")
                    } footer: {
                        Text("This repo has no remote, so there is nothing to open a pull request against. This merges the branch into the repo's default branch on this machine.")
                            .font(.caption)
                    }
                }

                // Hidden when we KNOW there is no remote — offering to push to one that does not
                // exist is an error message with extra steps, and it would contradict the section
                // above. Shown while the status is still unknown, which is the pre-poll state.
                if model.worktreeStatus?.hasRemote != false {
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
                    .accessibilityHidden(true)
                Text("\(checksStateWord(c)) · \(checksSummary(c))").font(.caption)
            }
            // Indexed: two CI apps can report the same check name, and \.self would collapse them.
            ForEach(Array(c.failures.enumerated()), id: \.offset) { _, f in
                // Each failure links to its own log where the provider gave us one. Naming the check
                // says WHAT broke; this is the only thing on the screen that can say why, and its
                // absence is what made a red build on a phone something you could only wait out.
                if let link = f.link {
                    Link(destination: link) {
                        Label(f.name, systemImage: "arrow.up.right.square")
                            .font(.system(.caption, design: .monospaced))
                            .foregroundStyle(palette.destructive)
                    }
                    .accessibilityLabel("\(f.name) failed. Open its log.")
                } else {
                    Label(f.name, systemImage: "xmark.octagon")
                        .font(.system(.caption, design: .monospaced))
                        .foregroundStyle(palette.destructive)
                }
            }
            // The daemon caps the names it sends, so say so rather than implying only these failed.
            if c.failedCount > c.failures.count {
                Text("+\(c.failedCount - c.failures.count) more failing")
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
        case "SUCCESS": return palette.success
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
