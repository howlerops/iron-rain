import SwiftUI
import OculusKit

/// A self-contained agent picker used everywhere a session's agent is chosen (New Session, Loops,
/// ticket launch). It handles the three real states instead of a hardcoded fallback list:
///   • loading  — `provider.list` hasn't returned yet → a spinner
///   • empty    — the daemon has no agents → guidance + "Add an agent"
///   • ready    — a menu of the daemon's real set + a "Manage agents…" entry
/// It owns its own Manage-Agents sheet, so call sites need no extra plumbing.
public struct AgentPicker: View {
    @ObservedObject var model: Model
    @Binding var selection: String
    let palette: OculusPalette
    var label: String = "Agent"

    @State private var showManage = false

    public init(model: Model, selection: Binding<String>, palette: OculusPalette, label: String = "Agent") {
        self.model = model; self._selection = selection; self.palette = palette; self.label = label
    }

    public var body: some View {
        Group {
            if !model.providersLoaded && model.providers.isEmpty {
                HStack(spacing: 8) {
                    Text(label).foregroundStyle(palette.foreground)
                    Spacer()
                    ProgressView().controlSize(.small)
                    Text("Finding agents…").font(.caption).foregroundStyle(palette.mutedForeground)
                }
            } else if model.providers.isEmpty {
                Button { showManage = true } label: {
                    HStack(spacing: 8) {
                        Image(systemName: "exclamationmark.triangle").foregroundStyle(.orange)
                        VStack(alignment: .leading, spacing: 1) {
                            Text("No agents found").font(.callout.weight(.medium)).foregroundStyle(palette.foreground)
                            Text("Install opencode / claude-code / codex, or add one").font(.caption2).foregroundStyle(palette.mutedForeground)
                        }
                        Spacer()
                        Text("Add").font(.caption.weight(.semibold)).foregroundStyle(palette.primary)
                    }.contentShape(Rectangle())
                }.buttonStyle(.plain)
            } else {
                Menu {
                    Picker(label, selection: $selection) {
                        ForEach(model.providers, id: \.self) { Text($0).tag($0) }
                    }
                    Divider()
                    Button { showManage = true } label: { Label("Manage agents…", systemImage: "slider.horizontal.3") }
                } label: {
                    HStack(spacing: 6) {
                        Text(label).foregroundStyle(palette.foreground)
                        Spacer()
                        Text(selection).foregroundStyle(palette.mutedForeground)
                        Image(systemName: "chevron.up.chevron.down").font(.caption2).foregroundStyle(palette.mutedForeground)
                    }.contentShape(Rectangle())
                }
            }
        }
        .sheet(isPresented: $showManage) { ManageAgentsView(model: model, palette: palette) }
    }
}

/// Manage the agent roster: see native/detected/custom agents and add, edit, or remove custom CLI
/// agents (persisted daemon-side to ~/.oculus/agents.json and registered live).
public struct ManageAgentsView: View {
    @ObservedObject var model: Model
    let palette: OculusPalette
    @Environment(\.dismiss) private var dismiss

    @State private var editing: AgentInfo?
    @State private var creating = false
    @State private var errorText: String?

    public init(model: Model, palette: OculusPalette) { self.model = model; self.palette = palette }

    private var native: [AgentInfo] { model.agents.filter { $0.kind == "native" } }
    private var detected: [AgentInfo] { model.agents.filter { $0.kind == "detected" } }
    private var custom: [AgentInfo] { model.agents.filter { $0.kind == "custom" } }

    public var body: some View {
        NavigationStack {
            List {
                Section {
                    Text("Choose which agents appear when you start a session. Native integrations are richest; installed CLIs are auto-detected; add your own below. Turn an agent off to hide it from the pickers without removing it.")
                        .font(.caption).foregroundStyle(palette.mutedForeground)
                }
                if !native.isEmpty { agentSection("Native", native, note: "Rich integrations") }
                if !detected.isEmpty { agentSection("Detected on PATH", detected, note: "Auto-found CLIs") }
                agentSection("Custom", custom, note: custom.isEmpty ? "None yet" : nil)
            }
            .navigationTitle("Agents")
            #if os(iOS)
            .navigationBarTitleDisplayMode(.inline)
            #endif
            .toolbar {
                ToolbarItem(placement: .confirmationAction) { Button("Done") { dismiss() } }
                ToolbarItem(placement: .primaryAction) {
                    Button { creating = true } label: { Label("Add", systemImage: "plus") }
                }
            }
            .task { await model.loadAgents() }
            .sheet(item: $editing) { CustomAgentEditor(model: model, palette: palette, agent: $0) { errorText = $0 } }
            .sheet(isPresented: $creating) { CustomAgentEditor(model: model, palette: palette, agent: nil) { errorText = $0 } }
            .alert("Couldn’t save agent", isPresented: Binding(get: { errorText != nil }, set: { if !$0 { errorText = nil } })) {
                Button("OK", role: .cancel) { errorText = nil }
            } message: { Text(errorText ?? "") }
        }
        #if os(macOS)
        .frame(width: 460, height: 560)
        #endif
    }

    private func agentSection(_ title: String, _ items: [AgentInfo], note: String?) -> some View {
        Section {
            if items.isEmpty, title == "Custom" {
                Button { creating = true } label: { Label("Add a custom agent", systemImage: "plus.circle") }
            }
            ForEach(items) { a in agentRow(a) }
        } header: {
            HStack { Text(title); if let note { Spacer(); Text(note).font(.caption2).foregroundStyle(palette.mutedForeground) } }
        }
    }

    private func agentRow(_ a: AgentInfo) -> some View {
        HStack(spacing: 10) {
            Circle().fill(a.available ? Color.green : palette.mutedForeground).frame(width: 8, height: 8)
            VStack(alignment: .leading, spacing: 1) {
                Text(a.name).font(.callout.weight(.medium)).foregroundStyle(a.hidden ? palette.mutedForeground : palette.foreground)
                if !a.command.isEmpty {
                    Text(([a.command] + a.args).joined(separator: " ")).font(.caption2.monospaced())
                        .foregroundStyle(palette.mutedForeground).lineLimit(1)
                } else if !a.available {
                    Text("command not found on PATH").font(.caption2).foregroundStyle(.orange)
                }
            }
            Spacer()
            if a.editable {
                Button { editing = a } label: { Image(systemName: "pencil") }.buttonStyle(.plain).foregroundStyle(palette.mutedForeground)
                Button(role: .destructive) { Task { errorText = await model.deleteAgent(a.name) } } label: { Image(systemName: "trash") }
                    .buttonStyle(.plain).foregroundStyle(.red)
            }
            // Show/hide in the session pickers (any kind). Hidden agents stay runnable.
            Toggle("", isOn: Binding(get: { !a.hidden }, set: { on in Task { errorText = await model.setAgentVisible(a.name, on) } }))
                .labelsHidden()
        }
        .padding(.vertical, 2)
    }
}

/// Add or edit a custom CLI agent.
struct CustomAgentEditor: View {
    @ObservedObject var model: Model
    let palette: OculusPalette
    let onError: (String?) -> Void
    @Environment(\.dismiss) private var dismiss

    @State private var name: String
    @State private var command: String
    @State private var argsText: String
    @State private var modelsText: String
    private let isNew: Bool

    init(model: Model, palette: OculusPalette, agent: AgentInfo?, onError: @escaping (String?) -> Void) {
        self.model = model; self.palette = palette; self.onError = onError
        self.isNew = agent == nil
        _name = State(initialValue: agent?.name ?? "")
        _command = State(initialValue: agent?.command ?? "")
        _argsText = State(initialValue: (agent?.args ?? []).joined(separator: " "))
        _modelsText = State(initialValue: (agent?.models ?? []).joined(separator: ", "))
    }

    // Split on whitespace, but keep {prompt}/{cwd}/{model} tokens intact.
    private var parsedArgs: [String] {
        argsText.split(whereSeparator: { $0 == " " || $0 == "\n" || $0 == "\t" }).map(String.init)
    }
    // Model names split on comma or newline (they can contain spaces/slashes).
    private var parsedModels: [String] {
        modelsText.split(whereSeparator: { $0 == "," || $0 == "\n" })
            .map { $0.trimmingCharacters(in: .whitespaces) }.filter { !$0.isEmpty }
    }

    var body: some View {
        NavigationStack {
            Form {
                Section("Agent") {
                    TextField("Name (e.g. my-agent)", text: $name)
                        .disabled(!isNew) // name is the key; editing keeps it stable
                        #if os(iOS)
                        .textInputAutocapitalization(.never).autocorrectionDisabled()
                        #endif
                    TextField("Command (e.g. codex, or /usr/local/bin/foo)", text: $command)
                        #if os(iOS)
                        .textInputAutocapitalization(.never).autocorrectionDisabled()
                        #endif
                }
                Section("Arguments") {
                    TextField("e.g.  exec {prompt}", text: $argsText, axis: .vertical)
                        .lineLimit(1...3)
                        #if os(iOS)
                        .textInputAutocapitalization(.never).autocorrectionDisabled()
                        #endif
                    if !parsedArgs.isEmpty {
                        HStack(spacing: 4) {
                            ForEach(Array(parsedArgs.enumerated()), id: \.offset) { _, t in
                                Text(t).font(.caption2.monospaced()).padding(.horizontal, 5).padding(.vertical, 1)
                                    .background(Capsule().fill(palette.muted.opacity(0.5)))
                                    .foregroundStyle(t == "{prompt}" || t == "{cwd}" ? palette.primary : palette.mutedForeground)
                            }
                        }
                    }
                    Text("`{prompt}` is replaced with your message, `{cwd}` with the working directory, and `{model}` with the chosen model. If you omit `{prompt}`, the message is appended as the last argument.")
                        .font(.caption2).foregroundStyle(palette.mutedForeground)
                }
                Section("Models (optional)") {
                    TextField("e.g.  gpt-5, o3, gemini-2.5-pro", text: $modelsText, axis: .vertical)
                        .lineLimit(1...2)
                        #if os(iOS)
                        .textInputAutocapitalization(.never).autocorrectionDisabled()
                        #endif
                    Text("Comma-separated model names. They appear in the chat-header picker; put `{model}` in the arguments above to pass the chosen one.")
                        .font(.caption2).foregroundStyle(palette.mutedForeground)
                }
                Section {
                    Text("Preview:  \(command.isEmpty ? "…" : command) \(parsedArgs.joined(separator: " "))")
                        .font(.caption.monospaced()).foregroundStyle(palette.foreground)
                }
            }
            .navigationTitle(isNew ? "Add agent" : "Edit \(name)")
            #if os(iOS)
            .navigationBarTitleDisplayMode(.inline)
            #endif
            .toolbar {
                ToolbarItem(placement: .cancellationAction) { Button("Cancel") { dismiss() } }
                ToolbarItem(placement: .confirmationAction) {
                    Button("Save") {
                        let a = AgentUpsert(name: name.trimmingCharacters(in: .whitespaces),
                                            command: command.trimmingCharacters(in: .whitespaces),
                                            args: parsedArgs,
                                            models: parsedModels.isEmpty ? nil : parsedModels)
                        Task {
                            let err = await model.upsertAgent(a)
                            if let err { onError(err) } else { dismiss() }
                        }
                    }
                    .disabled(name.trimmingCharacters(in: .whitespaces).isEmpty || command.trimmingCharacters(in: .whitespaces).isEmpty)
                }
            }
        }
        #if os(macOS)
        .frame(width: 440, height: 460)
        #endif
    }
}
