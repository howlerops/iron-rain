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

                Section("Review") {
                    Button {
                        Task { await model.worktreeDiff() }
                    } label: {
                        Label("Refresh diff", systemImage: "arrow.triangle.branch")
                    }
                    if let diff = model.lastDiff, !diff.isEmpty {
                        ScrollView {
                            Text(diff)
                                .font(.system(.caption2, design: .monospaced))
                                .frame(maxWidth: .infinity, alignment: .leading)
                                .textSelection(.enabled)
                        }
                        .frame(maxHeight: 220)
                        .padding(8)
                        .background(palette.input)
                        .clipShape(RoundedRectangle(cornerRadius: 8))
                    } else {
                        Text("No diff yet — tap Refresh diff.")
                            .font(.caption).foregroundStyle(palette.mutedForeground)
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
                Task { await model.worktreeDiff(); await model.loadConflicts() }
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
}
