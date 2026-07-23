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
