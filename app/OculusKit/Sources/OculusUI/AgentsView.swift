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
    @State private var rescanning = false

    public init(model: Model, palette: OculusPalette) { self.model = model; self.palette = palette }

    private var native: [AgentInfo] { model.agents.filter { $0.kind == "native" } }
    private var detected: [AgentInfo] { model.agents.filter { $0.kind == "detected" } }
    private var custom: [AgentInfo] { model.agents.filter { $0.kind == "custom" } }

    public var body: some View {
        NavigationStack {
            List {
                Section {
                    Text("Detected agents are auto-found — native integrations plus any CLIs on your PATH. Tap the ☆ to set your DEFAULT harness (used for new sessions + chats). Turn one off to hide it from the pickers without removing it.")
                        .font(.caption).foregroundStyle(palette.mutedForeground)
                    // Always-visible controls: check-for/detect agents + add a new one.
                    HStack(spacing: 10) {
                        Button {
                            rescanning = true
                            Task { await model.rescanAgents(); rescanning = false }
                        } label: {
                            if rescanning { ProgressView().controlSize(.small) }
                            else { Label("Check for agents", systemImage: "arrow.clockwise") }
                        }
                        .buttonStyle(.bordered).disabled(rescanning)
                        Button { creating = true } label: { Label("Add agent…", systemImage: "plus") }
                            .buttonStyle(.bordered)
                        Spacer()
                    }
                    // If nothing is actually available, the daemon can't see your agents on its PATH.
                    if !model.agents.contains(where: { $0.available }) && !model.agents.isEmpty {
                        Label("No agents are available — the daemon isn't finding them on its PATH. Tap Check for agents, then open Daemon Logs to see the PATH it searched.",
                              systemImage: "exclamationmark.triangle.fill")
                            .font(.caption).foregroundStyle(.orange)
                    }
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
                    Button {
                        rescanning = true
                        Task { await model.rescanAgents(); rescanning = false }
                    } label: {
                        if rescanning { ProgressView().controlSize(.small) }
                        else { Label("Re-scan", systemImage: "arrow.clockwise") }
                    }
                    .help("Re-detect installed agents on PATH")
                    .disabled(rescanning)
                }
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
            // Choose this as the DEFAULT harness for new sessions + chats. Filled gold star = current
            // default. Only available agents can be the default.
            if a.available {
                let isDefault = model.newSessionProvider == a.name
                Button {
                    model.setDefaultAgent(isDefault ? "" : a.name)
                } label: {
                    Image(systemName: isDefault ? "star.fill" : "star")
                        .foregroundStyle(isDefault ? palette.primary : palette.mutedForeground)
                }
                .buttonStyle(.plain)
                .help(isDefault ? "Default agent — tap to reset to auto" : "Set as default agent")
            }
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
    @State private var envRows: [EnvRow]
    private let isNew: Bool

    struct EnvRow: Identifiable { let id = UUID(); var key = ""; var value = "" }

    init(model: Model, palette: OculusPalette, agent: AgentInfo?, onError: @escaping (String?) -> Void) {
        self.model = model; self.palette = palette; self.onError = onError
        self.isNew = agent == nil
        _name = State(initialValue: agent?.name ?? "")
        _command = State(initialValue: agent?.command ?? "")
        _argsText = State(initialValue: (agent?.args ?? []).joined(separator: " "))
        _modelsText = State(initialValue: (agent?.models ?? []).joined(separator: ", "))
        let env = agent?.env ?? [:]
        _envRows = State(initialValue: env.isEmpty ? [EnvRow()] : env.sorted { $0.key < $1.key }.map { EnvRow(key: $0.key, value: $0.value) })
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

    private var parsedEnv: [String: String] {
        var out: [String: String] = [:]
        for r in envRows where !r.key.trimmingCharacters(in: .whitespaces).isEmpty { out[r.key.trimmingCharacters(in: .whitespaces)] = r.value }
        return out
    }
    private var canSave: Bool {
        !name.trimmingCharacters(in: .whitespaces).isEmpty && !command.trimmingCharacters(in: .whitespaces).isEmpty
    }

    var body: some View {
        VStack(spacing: 0) {
            // Header
            HStack {
                Text(isNew ? "Add agent" : "Edit \(name)").font(.headline).foregroundStyle(palette.foreground)
                Spacer()
                Button("Cancel") { dismiss() }.keyboardShortcut(.cancelAction)
            }
            .padding(16)
            Divider().overlay(palette.border)

            ScrollView {
                VStack(alignment: .leading, spacing: 16) {
                    field("NAME") {
                        TextField("my-agent", text: $name).textFieldStyle(.roundedBorder).disabled(!isNew)
                            .plainInput()
                    }
                    field("COMMAND") {
                        TextField("codex, or /usr/local/bin/foo", text: $command).textFieldStyle(.roundedBorder).plainInput()
                    }
                    field("ARGUMENTS", help: "{prompt} = your message · {cwd} = working dir · {model} = chosen model. Omit {prompt} and the message is appended last.") {
                        TextField("exec {prompt}", text: $argsText, axis: .vertical).lineLimit(1...3).textFieldStyle(.roundedBorder).plainInput()
                        if !parsedArgs.isEmpty {
                            argChips
                        }
                    }
                    field("MODELS", optional: true, help: "Comma-separated. They appear in the chat-header picker; put {model} in the arguments to pass the chosen one.") {
                        TextField("gpt-5, o3, gemini-2.5-pro", text: $modelsText, axis: .vertical).lineLimit(1...2).textFieldStyle(.roundedBorder).plainInput()
                    }
                    field("ENVIRONMENT", optional: true, help: "Point the agent at a config file or set keys, e.g. OPENCODE_CONFIG=/path/to/config.json. Stored on the daemon host.") {
                        ForEach($envRows) { $row in
                            HStack(spacing: 8) {
                                TextField("KEY", text: $row.key).textFieldStyle(.roundedBorder).frame(width: 190).plainInput()
                                TextField("value", text: $row.value).textFieldStyle(.roundedBorder).plainInput()
                            }
                        }
                        Button { envRows.append(EnvRow()) } label: { Label("Add variable", systemImage: "plus").font(.caption) }
                            .buttonStyle(.plain).foregroundStyle(palette.primary)
                    }
                    // Preview
                    VStack(alignment: .leading, spacing: 4) {
                        Text("PREVIEW").font(.system(size: 10.5, weight: .semibold)).tracking(0.8).foregroundStyle(palette.mutedForeground)
                        Text("\(command.isEmpty ? "…" : command) \(parsedArgs.joined(separator: " "))")
                            .font(.system(size: 12, design: .monospaced)).foregroundStyle(palette.foreground)
                            .padding(8).frame(maxWidth: .infinity, alignment: .leading)
                            .background(RoundedRectangle(cornerRadius: 6).fill(palette.secondary.opacity(0.5)))
                            .textSelection(.enabled)
                    }
                }
                .padding(16)
            }

            Divider().overlay(palette.border)
            HStack {
                Spacer()
                Button { save() } label: { Text("Save").frame(minWidth: 60) }
                    .buttonStyle(.borderedProminent).tint(palette.primary).disabled(!canSave)
            }
            .padding(16)
        }
        .background(palette.background)
        #if os(macOS)
        .frame(width: 500, height: 560)
        #endif
    }

    private func save() {
        let a = AgentUpsert(name: name.trimmingCharacters(in: .whitespaces),
                            command: command.trimmingCharacters(in: .whitespaces),
                            args: parsedArgs,
                            models: parsedModels.isEmpty ? nil : parsedModels,
                            env: parsedEnv.isEmpty ? nil : parsedEnv)
        Task {
            let err = await model.upsertAgent(a)
            if let err { onError(err) } else { dismiss() }
        }
    }

    /// A labeled field group: uppercase label, the field(s), and optional help text below.
    @ViewBuilder private func field(_ label: String, optional: Bool = false, help: String? = nil,
                                    @ViewBuilder _ content: () -> some View) -> some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack(spacing: 5) {
                Text(label).font(.system(size: 10.5, weight: .semibold)).tracking(0.8).foregroundStyle(palette.mutedForeground)
                if optional { Text("optional").font(.system(size: 9)).foregroundStyle(palette.mutedForeground.opacity(0.7)) }
            }
            content()
            if let help {
                Text(help).font(.caption2).foregroundStyle(palette.mutedForeground).fixedSize(horizontal: false, vertical: true)
            }
        }
    }

    private var argChips: some View {
        HStack(spacing: 4) {
            ForEach(Array(parsedArgs.enumerated()), id: \.offset) { _, t in
                Text(t).font(.caption2.monospaced()).padding(.horizontal, 5).padding(.vertical, 1)
                    .background(Capsule().fill(palette.muted.opacity(0.5)))
                    .foregroundStyle(t == "{prompt}" || t == "{cwd}" || t == "{model}" ? palette.primary : palette.mutedForeground)
            }
        }
    }
}

private extension View {
    /// No autocapitalization/autocorrect for command/arg/env fields (iOS); no-op on macOS.
    func plainInput() -> some View {
        #if os(iOS)
        return self.textInputAutocapitalization(.never).autocorrectionDisabled()
        #else
        return self
        #endif
    }
}
