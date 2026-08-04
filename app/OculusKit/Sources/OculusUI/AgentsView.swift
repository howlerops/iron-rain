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
                        Image(systemName: "exclamationmark.triangle").foregroundStyle(palette.warning)
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
        // Deliberately still a sheet, not a push. AgentPicker is embedded in New Session, the chat
        // header, the ticket launcher and LoopEditor — several of which have no NavigationStack
        // around them, and `.navigationDestination` outside a stack silently does nothing. That
        // would remove the only route to Manage Agents from those screens. The nesting this leaves
        // is one modal, because ManageAgentsView now PUSHES its own editor rather than stacking a
        // third sheet on top.
        .sheet(isPresented: $showManage) { ManageAgentsView(model: model, palette: palette) }
    }
}

/// Manage the agent roster: see native/detected/custom agents and add, edit, or remove custom CLI
/// agents (persisted daemon-side to ~/.oculus/agents.json and registered live).
public struct ManageAgentsView: View {
    @ObservedObject var model: Model
    let palette: OculusPalette
    /// True when this is HOSTED (a macOS Settings tab) rather than presented as a sheet. There is
    /// nothing to dismiss in that case, so the Done button would be inert — it is omitted instead.
    var embedded: Bool = false
    @Environment(\.dismiss) private var dismiss

    @State private var errorText: String?
    @State private var rescanning = false
    /// The custom agent a delete is staged against — one tap used to remove its whole definition
    /// (command, args, models, env) with no undo.
    @State private var pendingDelete: AgentInfo? = nil

    /// The editor. On iOS it is PUSHED onto this view's own stack rather than presented over it:
    /// reached from LoopEditor the chain was four modals deep — Loops, LoopEditor, this, the editor —
    /// with the form at the bottom of three dimming layers.
    ///
    /// On macOS it stays a sheet. A pushed view there gets a toolbar back button that SwiftUI gives
    /// no way to hide, and Back does not run the discard confirmation — which would make it a new,
    /// unguarded exit from a form whose env rows can hold an API key that exists nowhere else.
    private enum Route: Hashable, Identifiable {
        case new
        case edit(AgentInfo)

        var id: String {
            switch self {
            case .new: return "new"
            case .edit(let a): return "edit:" + a.id
            }
        }
    }

    /// Declared on both platforms so the single `NavigationStack(path:)` below compiles everywhere.
    /// On macOS nothing ever appends to it — `open` sets `route` and the sheet takes over.
    @State private var path: [Route] = []
    #if os(macOS)
    @State private var route: Route? = nil
    #endif

    private func open(_ r: Route) {
        #if os(iOS)
        path.append(r)
        #else
        route = r
        #endif
    }

    public init(model: Model, palette: OculusPalette, embedded: Bool = false) {
        self.model = model; self.palette = palette; self.embedded = embedded
    }

    private var native: [AgentInfo] { model.agents.filter { $0.kind == "native" } }
    private var detected: [AgentInfo] { model.agents.filter { $0.kind == "detected" } }
    private var custom: [AgentInfo] { model.agents.filter { $0.kind == "custom" } }

    public var body: some View {
        NavigationStack(path: $path) {
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
                        Button { open(.new) } label: { Label("Add agent…", systemImage: "plus") }
                            .buttonStyle(.bordered)
                        Spacer()
                    }
                    // If nothing is actually available, the daemon can't see your agents on its PATH.
                    if !model.agents.contains(where: { $0.available }) && !model.agents.isEmpty {
                        Label("No agents are available — the daemon isn't finding them on its PATH. Tap Check for agents, then open Daemon Logs to see the PATH it searched.",
                              systemImage: "exclamationmark.triangle.fill")
                            .font(.caption).foregroundStyle(palette.warning)
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
                if !embedded {
                    ToolbarItem(placement: .confirmationAction) { Button("Done") { dismiss() } }
                }
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
                    Button { open(.new) } label: { Label("Add", systemImage: "plus") }
                }
            }
            .task { await model.loadAgents() }
            #if os(iOS)
            .navigationDestination(for: Route.self) { editor($0, pushed: true) }
            #else
            .sheet(item: $route) { editor($0, pushed: false) }
            #endif
            .alert("Couldn’t save agent", isPresented: Binding(get: { errorText != nil }, set: { if !$0 { errorText = nil } })) {
                Button("OK", role: .cancel) { errorText = nil }
            } message: { Text(errorText ?? "") }
            .confirmationDialog(
                "Remove this agent?",
                isPresented: Binding(get: { pendingDelete != nil }, set: { if !$0 { pendingDelete = nil } }),
                titleVisibility: .visible,
                presenting: pendingDelete
            ) { a in
                Button("Remove agent", role: .destructive) {
                    let name = a.name
                    pendingDelete = nil
                    Task { errorText = await model.deleteAgent(name) }
                }
                Button("Cancel", role: .cancel) { pendingDelete = nil }
            } message: { a in
                Text("“\(a.name)” and its definition — command, arguments, models and any environment variables — are removed. Sessions already running on it aren't affected. If you only want it out of the pickers, use the switch instead.")
            }
        }
        #if os(macOS)
        .frame(width: 460, height: 560)
        #endif
    }

    @ViewBuilder private func editor(_ r: Route, pushed: Bool) -> some View {
        switch r {
        case .new:
            CustomAgentEditor(model: model, palette: palette, agent: nil, pushed: pushed) { errorText = $0 }
        case .edit(let a):
            CustomAgentEditor(model: model, palette: palette, agent: a, pushed: pushed) { errorText = $0 }
        }
    }

    private func agentSection(_ title: String, _ items: [AgentInfo], note: String?) -> some View {
        Section {
            if items.isEmpty, title == "Custom" {
                Button { open(.new) } label: { Label("Add a custom agent", systemImage: "plus.circle") }
            }
            // Only custom agents can be removed at all; a swipe on a native or detected one would
            // offer a delete that the trash button correctly doesn't show. It stages the same
            // confirmation the button does.
            ForEach(items) { a in
                if a.editable {
                    agentRow(a).sheetSwipeDelete("Remove") { pendingDelete = a }
                } else {
                    agentRow(a)
                }
            }
        } header: {
            HStack { Text(title); if let note { Spacer(); Text(note).font(.caption2).foregroundStyle(palette.mutedForeground) } }
        }
    }

    private func agentRow(_ a: AgentInfo) -> some View {
        HStack(spacing: 10) {
            Circle().fill(a.available ? palette.success : palette.mutedForeground)
                .frame(width: 8, height: 8)
                .accessibilityLabel(a.available ? "Available" : "Not found on PATH")
            VStack(alignment: .leading, spacing: 1) {
                Text(a.name).font(.callout.weight(.medium)).foregroundStyle(a.hidden ? palette.mutedForeground : palette.foreground)
                if !a.command.isEmpty {
                    Text(([a.command] + a.args).joined(separator: " ")).font(.caption2.monospaced())
                        .foregroundStyle(palette.mutedForeground).lineLimit(1)
                } else if !a.available {
                    Text("command not found on PATH").font(.caption2).foregroundStyle(palette.warning)
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
                // `.help()` sets the HINT, not the label — VoiceOver still announced a bare "Button".
                .accessibilityLabel(isDefault ? "\(a.name) is the default agent. Reset to automatic."
                                              : "Make \(a.name) the default agent")
                .help(isDefault ? "Default agent — tap to reset to auto" : "Set as default agent")
                .sheetTapTarget()
            }
            if a.editable {
                Button { open(.edit(a)) } label: { Image(systemName: "pencil") }
                    .buttonStyle(.plain).foregroundStyle(palette.mutedForeground)
                    .accessibilityLabel("Edit \(a.name)")
                    .sheetTapTarget()
                Button(role: .destructive) { pendingDelete = a } label: { Image(systemName: "trash") }
                    .buttonStyle(.plain).foregroundStyle(palette.destructive)
                    .accessibilityLabel("Remove \(a.name)")
                    .sheetTapTarget()
            }
            // Show/hide in the session pickers (any kind). Hidden agents stay runnable.
            // labelsHidden() keeps the accessibility label while hiding the visual one; without a
            // label this switch announced with no referent in a list of identical switches.
            Toggle("Show \(a.name) in agent pickers",
                   isOn: Binding(get: { !a.hidden }, set: { on in Task { errorText = await model.setAgentVisible(a.name, on) } }))
                .labelsHidden()
        }
        .padding(.vertical, 2)
    }
}

/// Add or edit a custom CLI agent.
struct CustomAgentEditor: View {
    @ObservedObject var model: Model
    let palette: OculusPalette
    /// Pushed onto ManageAgentsView's stack rather than presented over it. The stack supplies the
    /// title and the actions, so the hand-rolled header and footer would both be duplicates — and
    /// the fixed macOS window size would fight the window it is now inside.
    let pushed: Bool
    let onError: (String?) -> Void
    @Environment(\.dismiss) private var dismiss

    @State private var name: String
    @State private var command: String
    @State private var argsText: String
    @State private var modelsText: String
    @State private var envRows: [EnvRow]
    /// What the editor opened with, so an accidental swipe-dismiss can be told apart from a
    /// deliberate one. The env rows here can hold API keys that exist nowhere else.
    private let initial: [String]
    @State private var confirmDiscard = false
    @State private var problem: String? = nil
    @FocusState private var focus: Field?
    private let isNew: Bool

    private enum Field: Hashable { case name, command, args, models }

    struct EnvRow: Identifiable { let id = UUID(); var key = ""; var value = "" }

    init(model: Model, palette: OculusPalette, agent: AgentInfo?, pushed: Bool = false,
         onError: @escaping (String?) -> Void) {
        self.model = model; self.palette = palette; self.pushed = pushed; self.onError = onError
        self.isNew = agent == nil
        _name = State(initialValue: agent?.name ?? "")
        _command = State(initialValue: agent?.command ?? "")
        _argsText = State(initialValue: (agent?.args ?? []).joined(separator: " "))
        _modelsText = State(initialValue: (agent?.models ?? []).joined(separator: ", "))
        let env = agent?.env ?? [:]
        let rows = env.isEmpty ? [EnvRow()] : env.sorted { $0.key < $1.key }.map { EnvRow(key: $0.key, value: $0.value) }
        _envRows = State(initialValue: rows)
        self.initial = [agent?.name ?? "", agent?.command ?? "",
                        (agent?.args ?? []).joined(separator: " "),
                        (agent?.models ?? []).joined(separator: ", "),
                        rows.map { "\($0.key)=\($0.value)" }.joined(separator: "\n")]
    }

    private var current: [String] {
        [name, command, argsText, modelsText, envRows.map { "\($0.key)=\($0.value)" }.joined(separator: "\n")]
    }
    private var dirty: Bool { current != initial }

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
    private var title: String { isNew ? "Add agent" : "Edit \(name)" }

    var body: some View {
        VStack(spacing: 0) {
            // Header. Suppressed when pushed: the navigation bar already shows this title, and its
            // Cancel is the same Cancel.
            if !pushed {
                HStack {
                    Text(title).font(.headline).foregroundStyle(palette.foreground)
                    Spacer()
                    Button("Cancel") { cancel() }.keyboardShortcut(.cancelAction)
                }
                .padding(16)
                Divider().overlay(palette.border)
            }

            ScrollView {
                VStack(alignment: .leading, spacing: 16) {
                    field("Name", required: true) {
                        TextField("my-agent", text: $name).textFieldStyle(.roundedBorder).disabled(!isNew)
                            .plainInput().focused($focus, equals: .name)
                            .submitLabel(.next).onSubmit { focus = .command }
                    }
                    field("Command", required: true) {
                        TextField("codex, or /usr/local/bin/foo", text: $command).textFieldStyle(.roundedBorder).plainInput()
                            .focused($focus, equals: .command)
                            .submitLabel(.next).onSubmit { focus = .args }
                    }
                    field("Arguments", help: "{prompt} = your message · {cwd} = working dir · {model} = chosen model. Omit {prompt} and the message is appended last.") {
                        TextField("exec {prompt}", text: $argsText, axis: .vertical).lineLimit(1...3).textFieldStyle(.roundedBorder).plainInput()
                            .focused($focus, equals: .args)
                        if !parsedArgs.isEmpty {
                            argChips
                        }
                    }
                    field("Models", optional: true, help: "Comma-separated. They appear in the chat-header picker; put {model} in the arguments to pass the chosen one.") {
                        TextField("gpt-5, o3, gemini-2.5-pro", text: $modelsText, axis: .vertical).lineLimit(1...2).textFieldStyle(.roundedBorder).plainInput()
                            .focused($focus, equals: .models)
                    }
                    field("Environment", optional: true, help: "Point the agent at a config file or set keys, e.g. OPENCODE_CONFIG=/path/to/config.json. Stored on the daemon host.") {
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
                        Text("Preview").font(.caption.weight(.semibold)).foregroundStyle(palette.mutedForeground)
                        Text("\(command.isEmpty ? "…" : command) \(parsedArgs.joined(separator: " "))")
                            .font(.system(.footnote, design: .monospaced)).foregroundStyle(palette.foreground)
                            .padding(8).frame(maxWidth: .infinity, alignment: .leading)
                            .background(OculusShape.rounded(6).fill(palette.secondary.opacity(0.5)))
                            .textSelection(.enabled)
                    }
                }
                .padding(16)
            }

            if !pushed { Divider().overlay(palette.border) }
            if let problem {
                Text(problem).font(.footnote).foregroundStyle(palette.destructive)
                    .fixedSize(horizontal: false, vertical: true)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .padding(.horizontal, 16).padding(.top, 12)
            }
            if !pushed {
                HStack {
                    Spacer()
                    // Enabled and validated on tap: a dead Save spanning name AND command couldn't
                    // say which of the two it was waiting for.
                    Button { save() } label: { Text("Save").frame(minWidth: 60) }
                        .buttonStyle(.borderedProminent).tint(palette.primary)
                        .keyboardShortcut(.defaultAction)
                }
                .padding(16)
            }
        }
        .background(palette.background)
        #if os(macOS)
        // A pushed editor sits INSIDE ManageAgentsView's own 460×560 window; its own 500pt width
        // would overflow that by 40pt and force the window wider mid-navigation.
        .frame(width: pushed ? nil : 500, height: pushed ? nil : 560)
        #endif
        #if os(iOS)
        .navigationTitle(pushed ? title : "")
        .navigationBarTitleDisplayMode(.inline)
        // Back would leave WITHOUT the discard confirmation, which is exactly what the guard is for:
        // the env rows here can hold an API key that exists nowhere else. Cancel is the only way out
        // of a pushed editor, and Cancel asks.
        .navigationBarBackButtonHidden(pushed)
        .toolbar { pushedActions }
        #endif
        .onAppear { focus = isNew ? .name : .command }
        .sheetDraftGuard(dirty)
        .confirmationDialog(isNew ? "Discard this agent?" : "Discard changes?",
                            isPresented: $confirmDiscard, titleVisibility: .visible) {
            Button("Discard", role: .destructive) { dismiss() }
            Button("Keep editing", role: .cancel) {}
        } message: {
            Text(envRows.contains { !$0.value.isEmpty }
                 ? "Your changes won't be saved — including anything typed into Environment, which isn't stored anywhere else."
                 : "Your changes won't be saved.")
        }
    }

    #if os(iOS)
    @ToolbarContentBuilder private var pushedActions: some ToolbarContent {
        if pushed {
            ToolbarItem(placement: .cancellationAction) { Button("Cancel") { cancel() } }
            ToolbarItem(placement: .confirmationAction) { Button("Save") { save() } }
        }
    }
    #endif

    private func cancel() {
        if dirty { confirmDiscard = true } else { dismiss() }
    }

    private func save() {
        if name.trimmingCharacters(in: .whitespaces).isEmpty {
            problem = "Give the agent a name — it's how you'll pick it in the session pickers."; return
        }
        if command.trimmingCharacters(in: .whitespaces).isEmpty {
            problem = "Enter the command that runs this agent, e.g. codex or /usr/local/bin/foo."; return
        }
        problem = nil
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
    @ViewBuilder private func field(_ label: String, optional: Bool = false, required: Bool = false,
                                    help: String? = nil,
                                    @ViewBuilder _ content: () -> some View) -> some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack(spacing: 5) {
                Text(label).font(.caption.weight(.semibold)).foregroundStyle(palette.mutedForeground)
                if required { Text("required").font(.caption2).foregroundStyle(palette.mutedForeground.opacity(0.7)) }
                if optional { Text("optional").font(.caption2).foregroundStyle(palette.mutedForeground.opacity(0.7)) }
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

// `plainInput()` now lives in OculusTheme.swift so every technical field in the app can use it,
// not just this one.
