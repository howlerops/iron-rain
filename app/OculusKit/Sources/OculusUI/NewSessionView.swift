import SwiftUI
import OculusKit
#if os(macOS)
import AppKit
#endif

/// Configures a new session: which provider, which project folder, and whether to run
/// it in an isolated git worktree. Applying just sets the Model's pending options; the
/// session is created when the user sends the first message.
struct NewSessionView: View {
    @ObservedObject var model: Model
    let palette: OculusPalette
    let onStart: () -> Void

    @State private var provider = "opencode"
    @State private var projectID: String? = nil
    @State private var useWorktree = false
    @State private var workspaceName = ""
    #if os(iOS)
    @State private var addPath = ""
    #endif

    private static let providers = ["opencode", "claude-code", "pi"]

    private var selectedProject: Project? {
        model.projects.first { $0.id == projectID }
    }
    private var canWorktree: Bool { selectedProject?.isGitRepo == true }

    var body: some View {
        NavigationStack {
            Form {
                Section("Agent") {
                    Picker("Provider", selection: $provider) {
                        ForEach(Self.providers, id: \.self) { Text($0).tag($0) }
                    }
                }

                Section("Project") {
                    Picker("Folder", selection: $projectID) {
                        Text("None (daemon default)").tag(String?.none)
                        ForEach(model.projects) { p in
                            Text(p.name).tag(String?.some(p.id))
                        }
                    }
                    if let p = selectedProject {
                        LabeledContent("Path", value: p.path)
                            .font(.caption).foregroundStyle(palette.mutedForeground)
                    }
                    addProjectControl
                }

                Section {
                    Toggle("Isolate in a git worktree", isOn: $useWorktree)
                        .disabled(!canWorktree)
                    if useWorktree {
                        TextField("Workspace name", text: $workspaceName)
                            .textFieldStyle(.roundedBorder)
                    }
                } header: {
                    Text("Worktree")
                } footer: {
                    Text(canWorktree
                         ? "Runs on a fresh oculus/<name> branch; changes stay isolated until you open a PR."
                         : "Pick a git project to enable worktrees.")
                        .font(.caption)
                }
            }
            .navigationTitle("New session")
            .toolbar {
                ToolbarItem(placement: .confirmationAction) {
                    Button("Start") {
                        model.newSession(provider: provider,
                                         projectID: projectID,
                                         worktree: useWorktree && canWorktree,
                                         workspaceName: workspaceName.isEmpty ? nil : workspaceName)
                        onStart()
                    }
                }
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { onStart() }
                }
            }
            .task { await model.loadProjects() }
        }
    }

    @ViewBuilder private var addProjectControl: some View {
        #if os(macOS)
        Button {
            if let path = pickFolder() {
                Task { await model.addProject(path: path) }
            }
        } label: {
            Label("Add folder…", systemImage: "folder.badge.plus")
        }
        #else
        HStack {
            TextField("Add folder by path", text: $addPath)
                .textFieldStyle(.roundedBorder)
                .autocorrectionDisabled()
            Button("Add") {
                let p = addPath
                addPath = ""
                Task { await model.addProject(path: p) }
            }.disabled(addPath.isEmpty)
        }
        #endif
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
