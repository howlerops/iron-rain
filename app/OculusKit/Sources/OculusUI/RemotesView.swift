import SwiftUI
import OculusKit

/// SSH remote worktrees: register a beefy remote box (an ssh target + a repo path) and inspect its
/// worktree — git status/diff — over SSH from the app. Reachability is probed on load. Running a full
/// agent session ON the remote builds on this transport and is the larger next step.
struct RemotesView: View {
    @ObservedObject var model: Model
    let palette: OculusPalette
    /// Optional so this view can also be HOSTED rather than presented — inside a macOS Settings tab
    /// there is no sheet to close, and a Done button there would be inert. Nil simply omits it.
    var onClose: (() -> Void)? = nil

    @State private var status: [String: RemoteStatus] = [:]
    @State private var loading: Set<String> = []
    /// The host a delete is staged against — one tap used to remove it outright, with no undo.
    @State private var pendingDelete: RemoteHost? = nil

    /// The child screens. On iOS they are PUSHED: this view is itself a sheet, and a modal over a
    /// modal buries the form behind two dimming layers. `RemoteHost` isn't Hashable, so the run route
    /// carries the host's id and the host is looked up — which is also more correct, since the daemon
    /// replaces the whole host list on every mutation.
    private enum Route: Hashable, Identifiable {
        case add
        case run(String)

        var id: String {
            switch self {
            case .add: return "add"
            case .run(let hostID): return "run:" + hostID
            }
        }
    }

    #if os(iOS)
    @State private var path: [Route] = []
    #else
    @State private var route: Route? = nil
    #endif

    /// The native list is only the right shape once there is something to list. With no hosts the
    /// sheet is one centred empty state, which is not a list row.
    private var usesList: Bool {
        #if os(iOS)
        return !model.remotes.isEmpty
        #else
        return false
        #endif
    }

    var body: some View {
        #if os(iOS)
        NavigationStack(path: $path) {
            core
                // The scaffold draws this screen's title and its Done button; a navigation bar over
                // it would be a second, empty title.
                .toolbar(.hidden, for: .navigationBar)
                .navigationDestination(for: Route.self) { child($0) }
        }
        #else
        core.sheet(item: $route) { child($0) }
        #endif
    }

    private var core: some View {
        // Was a hand-rolled header plus a hardcoded 540×440 frame. On iPhone — where this sheet is
        // reachable — 540pt is wider than the screen and forced the page to scroll sideways.
        OculusSheet(
            title: "Remote hosts",
            subtitle: "Run and inspect a worktree on another box over SSH.",
            palette: palette,
            actions: AnyView(addButton),
            onClose: onClose,
            scrolls: !usesList
        ) {
            content
        }
        .task { await model.loadRemotes() }
        .confirmationDialog(
            "Remove this host?",
            isPresented: Binding(get: { pendingDelete != nil }, set: { if !$0 { pendingDelete = nil } }),
            titleVisibility: .visible,
            presenting: pendingDelete
        ) { h in
            Button("Remove host", role: .destructive) { delete(h) }
            Button("Cancel", role: .cancel) { pendingDelete = nil }
        } message: { h in
            Text("“\(h.name)” (\(h.sshTarget)) is removed from this list, along with any port forwards set up for it. Nothing on the remote machine is touched — you'd just have to register it again.")
        }
    }

    @ViewBuilder private var content: some View {
        #if os(iOS)
        if usesList { hostList } else { cardBody }
        #else
        cardBody
        #endif
    }

    // MARK: - Routing

    private func open(_ r: Route) {
        #if os(iOS)
        path.append(r)
        #else
        route = r
        #endif
    }

    private func closeChild() {
        #if os(iOS)
        if !path.isEmpty { path.removeLast() }
        #else
        route = nil
        #endif
    }

    private var pushes: Bool {
        #if os(iOS)
        return true
        #else
        return false
        #endif
    }

    @ViewBuilder private func child(_ r: Route) -> some View {
        switch r {
        case .add:
            AddRemoteSheet(model: model, palette: palette, pushed: pushes, onClose: closeChild)
        case .run(let hostID):
            if let host = model.remotes.first(where: { $0.id == hostID }) {
                // Starting the run has always closed this whole sheet as well — the session it
                // starts is what you go and look at next, not this list.
                RemoteRunSheet(model: model, palette: palette, host: host, pushed: pushes) {
                    closeChild(); onClose?()
                }
            }
        }
    }

    private var addButton: some View {
        Button { open(.add) } label: { Label("Add", systemImage: "plus") }
    }

    // MARK: - Body variants

    /// The macOS shape: a scrolling column of bordered cards.
    @ViewBuilder private var cardBody: some View {
        Text(sshNote)
            .font(.footnote).foregroundStyle(palette.mutedForeground)
            .fixedSize(horizontal: false, vertical: true)
        if model.remotes.isEmpty {
            emptyState
        } else {
            ForEach(model.remotes) { host in
                hostBody(host, inList: false)
                    .padding(OculusSpace.md)
                    .background(OculusShape.rounded(OculusRadius.md).fill(palette.secondary.opacity(0.4))
                        .overlay(OculusShape.rounded(OculusRadius.md).strokeBorder(palette.border)))
            }
        }
    }

    #if os(iOS)
    /// The iOS shape: the platform's grouped list. The SSH caveat becomes the section footer — which
    /// is exactly where iOS puts a "before this works, set that up" note — and the swipe restores the
    /// delete gesture the hand-rolled cards had no way to offer.
    private var hostList: some View {
        List {
            Section {
                ForEach(model.remotes) { host in
                    hostBody(host, inList: true)
                        .sheetSwipeDelete("Remove") { pendingDelete = host }
                }
            } header: {
                Text("Hosts")
            } footer: {
                Text(sshNote)
            }
        }
        .sheetListChrome(palette)
    }
    #endif

    /// Computed, not stored: a stored property would join `RemotesView`'s memberwise initializer, and
    /// the call sites in DesktopViews/SettingsScene pass positionally-ordered arguments to it.
    private var sshNote: String { "Uses your ~/.ssh keys/config (BatchMode — set up key auth first)." }

    /// The bare "No remote hosts yet." left the only way forward in a header the eye doesn't visit
    /// when the page is empty.
    private var emptyState: some View {
        SheetEmptyState(icon: "server.rack",
                        title: "No remote hosts",
                        message: "Register an SSH target and a repo path on it, and you can check that worktree — or start an agent session on that machine — without leaving the app.",
                        palette: palette) {
            Button { open(.add) } label: { Label("Add a host", systemImage: "plus") }
                .buttonStyle(.borderedProminent).tint(palette.primary)
        }
    }

    /// `deleteRemote` returns Void and swallows its error. The daemon answers with the whole host
    /// list, so a host that's still in it is the honest signal the delete never landed.
    private func delete(_ host: RemoteHost) {
        pendingDelete = nil
        Task {
            await model.deleteRemote(host.id)
            if model.remotes.contains(where: { $0.id == host.id }) {
                model.setError("Couldn’t remove \(host.name)",
                               "It's still registered. Check the daemon is connected and try again.")
            }
        }
    }

    /// One host, shared by both shapes. `inList` drops the trash button: in a List the delete is the
    /// swipe (which stages the same confirmation), and a second target in an already-crowded row is
    /// one more thing to hit by accident.
    private func hostBody(_ host: RemoteHost, inList: Bool) -> some View {
        VStack(alignment: .leading, spacing: OculusSpace.sm) {
            HStack(spacing: OculusSpace.sm) {
                Circle().fill(host.reachable == true ? palette.success : palette.destructive)
                    .frame(width: 7, height: 7)
                    .accessibilityHidden(true)
                Text(host.name).font(.subheadline.weight(.semibold)).foregroundStyle(palette.foreground)
                Text(host.sshTarget).font(.caption.monospaced()).foregroundStyle(palette.mutedForeground)
                Spacer(minLength: OculusSpace.xs)
                Text(host.reachable == true ? "reachable" : "unreachable")
                    .font(.caption2.weight(.medium))
                    .foregroundStyle(host.reachable == true ? palette.success : palette.destructive)
                if !inList {
                    // Destructive role and colour: this was an 11pt glyph in the de-emphasized muted
                    // grey, which reads as the minor control rather than the irreversible one.
                    Button(role: .destructive) { pendingDelete = host } label: {
                        Image(systemName: "trash").font(.caption)
                    }
                    .buttonStyle(.plain).foregroundStyle(palette.destructive)
                    .accessibilityLabel("Remove host \(host.name)")
                    .sheetTapTarget()
                }
            }
            Text(host.remotePath).font(.caption.monospaced()).foregroundStyle(palette.mutedForeground)
            if let fwds = host.forwards, !fwds.isEmpty {
                Label(fwds.map { "localhost:\($0.localPort) → :\($0.remotePort)" }.joined(separator: ", "),
                      systemImage: "arrow.left.arrow.right")
                    .font(.caption2.monospaced()).foregroundStyle(palette.primary)
            }
            HStack {
                Button {
                    loading.insert(host.id)
                    Task { status[host.id] = await model.remoteStatus(host.id); loading.remove(host.id) }
                } label: {
                    if loading.contains(host.id) { ProgressView().controlSize(.small) }
                    else { Label("Check worktree", systemImage: "arrow.clockwise") }
                }
                .buttonStyle(.bordered)
                #if os(macOS)
                .controlSize(.small)
                #endif
                Button { open(.run(host.id)) } label: { Label("Run agent here", systemImage: "play.circle") }
                    .buttonStyle(.bordered)
                    #if os(macOS)
                    .controlSize(.small)
                    #endif
                    .disabled(host.reachable != true)
            }
            if let st = status[host.id] {
                if let err = st.error, !err.isEmpty {
                    remoteError(host, err)
                } else if st.status.isEmpty {
                    Text("Clean — no uncommitted changes.").font(.caption)
                        .foregroundStyle(palette.mutedForeground)
                } else {
                    ScrollView(.horizontal, showsIndicators: false) {
                        Text(st.status).font(.caption.monospaced()).foregroundStyle(palette.foreground)
                            .textSelection(.enabled)
                    }
                    .frame(maxHeight: 80)
                }
            }
        }
    }

    /// ssh's own stderr, which was three clipped red lines you could neither copy nor act on. It is
    /// nearly always one of two things, so say which, and put the retry next to it.
    private func remoteError(_ host: RemoteHost, _ err: String) -> some View {
        VStack(alignment: .leading, spacing: OculusSpace.xs) {
            Text("SSH couldn't read that worktree. Usually key auth isn't set up for this target, or the path doesn't exist on the remote.")
                .font(.caption).foregroundStyle(palette.foreground)
                .fixedSize(horizontal: false, vertical: true)
            Text(err).font(.caption.monospaced()).foregroundStyle(palette.destructive)
                .lineLimit(6).fixedSize(horizontal: false, vertical: true)
                .textSelection(.enabled)
            Button {
                loading.insert(host.id)
                Task { status[host.id] = await model.remoteStatus(host.id); loading.remove(host.id) }
            } label: { Label("Try again", systemImage: "arrow.clockwise") }
            .buttonStyle(.bordered)
            #if os(macOS)
            .controlSize(.small)
            #endif
        }
    }
}

/// Compose a remote agent run: the agent command to run on the box + the first prompt.
struct RemoteRunSheet: View {
    @ObservedObject var model: Model
    let palette: OculusPalette
    let host: RemoteHost
    /// Pushed onto a navigation stack rather than presented. The stack supplies the title bar, so
    /// the scaffold header and the in-content button row would both be duplicates of it.
    var pushed: Bool = false
    var onDone: () -> Void

    @State private var command = "opencode run"
    @State private var prompt = ""
    @State private var problem: String? = nil
    @State private var confirmDiscard = false
    @FocusState private var promptFocused: Bool

    /// The typed task is the unsaved work here — the command has a sensible default.
    private var dirty: Bool { !prompt.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty }

    var body: some View {
        form
            .onAppear { promptFocused = true }
            .sheetDraftGuard(dirty)
            .confirmationDialog("Discard this task?", isPresented: $confirmDiscard, titleVisibility: .visible) {
                Button("Discard", role: .destructive) { onDone() }
                Button("Keep editing", role: .cancel) {}
            } message: {
                Text("What you've typed won't be kept.")
            }
            #if os(iOS)
            .navigationTitle(pushed ? "Run on \(host.name)" : "")
            .navigationBarTitleDisplayMode(.inline)
            // Back would leave WITHOUT the discard confirmation — and the task here is several
            // sentences someone wrote by hand. Cancel is the only way out, and Cancel asks.
            .navigationBarBackButtonHidden(pushed)
            .toolbar { pushedActions }
            #endif
    }

    #if os(iOS)
    @ToolbarContentBuilder private var pushedActions: some ToolbarContent {
        if pushed {
            ToolbarItem(placement: .cancellationAction) { Button("Cancel") { cancel() } }
            ToolbarItem(placement: .confirmationAction) { Button("Run") { run() } }
        }
    }
    #endif

    private var form: some View {
        OculusSheet(title: "Run agent on \(host.name)",
                    subtitle: "Runs in \(host.remotePath) over SSH and streams back here.",
                    palette: palette,
                    showsHeader: !pushed) {
            Text("Runs `\(command)` in \(host.remotePath) over SSH and streams it back. The agent must be installed on the remote.")
                .font(.caption).foregroundStyle(palette.mutedForeground)
                .fixedSize(horizontal: false, vertical: true)
            VStack(alignment: .leading, spacing: OculusSpace.xs) {
                fieldLabel("Agent command", required: true)
                TextField("opencode run / claude -p / codex exec", text: $command)
                    .textFieldStyle(.roundedBorder).plainInput()
                    .submitLabel(.next).onSubmit { promptFocused = true }
            }
            VStack(alignment: .leading, spacing: OculusSpace.xs) {
                fieldLabel("Task", required: true)
                TextEditor(text: $prompt).frame(minHeight: 80).focused($promptFocused)
                    .padding(6)
                    // Concentric with the padded container it sits in, so the corners stay parallel
                    // instead of flaring.
                    .background(OculusShape.rounded(OculusRadius.sm).fill(palette.secondary.opacity(0.5)))
                    .overlay(OculusShape.rounded(OculusRadius.sm).strokeBorder(palette.border))
            }
            if let problem {
                Text(problem).font(.footnote).foregroundStyle(palette.destructive)
                    .fixedSize(horizontal: false, vertical: true)
            }
            if !pushed {
                HStack(spacing: OculusSpace.sm) {
                    Spacer()
                    Button("Cancel") { cancel() }.keyboardShortcut(.cancelAction)
                    Button { run() } label: { Label("Run remotely", systemImage: "play.circle") }
                        .buttonStyle(.borderedProminent).tint(palette.primary)
                        .keyboardShortcut(.defaultAction)
                }
            }
        }
    }

    private func cancel() {
        if dirty { confirmDiscard = true } else { onDone() }
    }

    /// Validated on tap and named: a dead button spanning two fields can't say which one it wants.
    private func run() {
        if command.trimmingCharacters(in: .whitespaces).isEmpty {
            problem = "Enter the agent command to run on \(host.name)."; return
        }
        if prompt.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
            problem = "Describe the task for the agent."; return
        }
        problem = nil
        // `remoteRun` surfaces its own failure through model.actionError, so closing here is safe.
        Task { await model.remoteRun(hostID: host.id, agentCommand: command, prompt: prompt); onDone() }
    }

    private func fieldLabel(_ t: String, required: Bool = false) -> some View {
        HStack(spacing: OculusSpace.xs) {
            Text(t).font(.caption.weight(.semibold))
            if required { Text("required").font(.caption2).opacity(0.8) }
        }
        .foregroundStyle(palette.mutedForeground)
    }
}

struct AddRemoteSheet: View {
    @ObservedObject var model: Model
    let palette: OculusPalette
    /// Pushed onto a navigation stack rather than presented — see `RemoteRunSheet.pushed`.
    var pushed: Bool = false
    var onClose: () -> Void

    @State private var name = ""
    @State private var target = ""
    @State private var path = ""
    @State private var devPort = ""
    @State private var problem: String? = nil
    @State private var confirmDiscard = false
    @FocusState private var focus: Field?

    private enum Field: Hashable { case name, target, path, port }

    private var dirty: Bool { !(name.isEmpty && target.isEmpty && path.isEmpty && devPort.isEmpty) }

    var body: some View {
        form
            .onAppear { focus = .name }
            .sheetDraftGuard(dirty)
            .confirmationDialog("Discard this host?", isPresented: $confirmDiscard, titleVisibility: .visible) {
                Button("Discard", role: .destructive, action: onClose)
                Button("Keep editing", role: .cancel) {}
            } message: {
                Text("What you've typed won't be kept.")
            }
            #if os(iOS)
            .navigationTitle(pushed ? "Add remote host" : "")
            .navigationBarTitleDisplayMode(.inline)
            // Back would leave WITHOUT the discard confirmation. Cancel is the only way out, and
            // Cancel asks.
            .navigationBarBackButtonHidden(pushed)
            .toolbar { pushedActions }
            #endif
    }

    #if os(iOS)
    @ToolbarContentBuilder private var pushedActions: some ToolbarContent {
        if pushed {
            ToolbarItem(placement: .cancellationAction) { Button("Cancel") { cancel() } }
            ToolbarItem(placement: .confirmationAction) { Button("Add") { save() } }
        }
    }
    #endif

    private var form: some View {
        OculusSheet(title: "Add remote host",
                    subtitle: "An SSH target and a repo path on it.",
                    palette: palette,
                    showsHeader: !pushed) {
            field("Name", "Build box", $name, focus: .name, next: .target, required: true)
            // SSH targets, paths and ports are technical: autocapitalizing the first letter of a
            // hostname or a path silently produces something that won't connect.
            field("SSH target", "user@host or an ~/.ssh/config alias", $target, focus: .target, next: .path, required: true)
            field("Remote path", "/home/you/project", $path, focus: .path, next: .port, required: true)
            field("Dev server port", "3000 — tunneled to localhost for Design Mode", $devPort,
                  focus: .port, next: nil, numeric: true)

            if let problem {
                Text(problem).font(.footnote).foregroundStyle(palette.destructive)
                    .fixedSize(horizontal: false, vertical: true)
            }

            if !pushed {
                HStack(spacing: OculusSpace.sm) {
                    Spacer()
                    Button("Cancel") { cancel() }.keyboardShortcut(.cancelAction)
                    Button("Add host") { save() }
                        .buttonStyle(.borderedProminent).tint(palette.primary)
                        .keyboardShortcut(.defaultAction)
                }
            }
        }
    }

    private func cancel() {
        if dirty { confirmDiscard = true } else { onClose() }
    }

    private func save() {
        if name.trimmingCharacters(in: .whitespaces).isEmpty {
            problem = "Give the host a name — it's the label you'll pick it by."; return
        }
        if target.trimmingCharacters(in: .whitespaces).isEmpty {
            problem = "Enter the SSH target, e.g. you@buildbox or an ~/.ssh/config alias."; return
        }
        if path.trimmingCharacters(in: .whitespaces).isEmpty {
            problem = "Enter the repo path on that machine, e.g. /home/you/project."; return
        }
        problem = nil
        var fwds: [PortForward]? = nil
        if let p = Int(devPort.trimmingCharacters(in: .whitespaces)), p > 0 {
            fwds = [PortForward(localPort: p, remotePort: p)]
        }
        Task {
            await model.upsertRemote(RemoteHost(name: name, sshTarget: target, remotePath: path, forwards: fwds))
            // `upsertRemote` returns Void and swallows its error; the daemon answers with the whole
            // list, so a host that isn't in it didn't save. Closing on that would drop the entry.
            if model.remotes.contains(where: { $0.name == name && $0.sshTarget == target }) {
                onClose()
            } else {
                problem = "Couldn't save this host. Check the daemon is connected — your entries are still here."
            }
        }
    }

    @ViewBuilder
    private func field(_ label: String, _ placeholder: String, _ text: Binding<String>,
                       focus f: Field, next: Field?, required: Bool = false,
                       numeric: Bool = false) -> some View {
        VStack(alignment: .leading, spacing: OculusSpace.xs) {
            HStack(spacing: OculusSpace.xs) {
                Text(label).font(.caption.weight(.semibold))
                if required { Text("required").font(.caption2).opacity(0.8) }
                else { Text("optional").font(.caption2).opacity(0.8) }
            }
            .foregroundStyle(palette.mutedForeground)
            TextField(placeholder, text: text).textFieldStyle(.roundedBorder)
                .plainInput()
                .focused($focus, equals: f)
                .submitLabel(next == nil ? .done : .next)
                .onSubmit { focus = next }
                #if os(iOS)
                .keyboardType(numeric ? .numberPad : .default)
                #endif
        }
    }
}
