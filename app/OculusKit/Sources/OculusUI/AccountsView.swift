import SwiftUI
import OculusKit

/// Accounts & Usage: keep multiple credentials per agent (personal / work logins or API keys) and
/// hot-swap which one new sessions use, alongside a per-provider token/cost usage meter. Env keys
/// are stored 0600 on the daemon host; this view is the switcher + meter.
struct AccountsView: View {
    @ObservedObject var model: Model
    let palette: OculusPalette
    /// Optional so this view can also be HOSTED rather than presented — inside a macOS Settings tab
    /// there is no sheet to close, and a Done button there would be inert. Nil simply omits it.
    var onClose: (() -> Void)? = nil

    @State private var quota: [String: AccountQuota] = [:]
    @State private var checking: Set<String> = []
    /// The account a delete is staged against. Deleting one destroys the API keys stored with it and
    /// there is no copy anywhere else, so it never happens on a single tap.
    @State private var pendingDelete: Account? = nil

    /// The one child screen. On iOS it is pushed rather than presented: this view is itself already
    /// a sheet, and a modal over a modal buries the credential form behind two dimming layers.
    private enum Route: Hashable, Identifiable {
        case add
        var id: String { "add" }
    }

    #if os(iOS)
    @State private var path: [Route] = []
    #else
    @State private var route: Route? = nil
    #endif

    private var byProvider: [(provider: String, accounts: [Account])] {
        let groups = Dictionary(grouping: model.accounts, by: { $0.provider })
        return groups.keys.sorted().map { (provider: $0, accounts: groups[$0] ?? []) }
    }

    /// The native list is only the right shape once there is something to list. With no accounts the
    /// sheet is one centred empty state, which is not a list row.
    private var usesList: Bool {
        #if os(iOS)
        return !model.accounts.isEmpty
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
                .navigationDestination(for: Route.self) { _ in
                    AddAccountSheet(model: model, palette: palette, pushed: true, onClose: closeChild)
                }
        }
        #else
        core.sheet(item: $route) { _ in
            AddAccountSheet(model: model, palette: palette, onClose: closeChild)
        }
        #endif
    }

    private var core: some View {
        // Was a hand-rolled header + a hardcoded 520×440 frame. The frame was the real bug: this
        // sheet is reachable on iPhone, where 520pt is wider than the device and the page scrolled
        // sideways. OculusSheet already decides sizing per platform.
        OculusSheet(
            title: "Accounts & Usage",
            subtitle: "Credentials per agent, and what they've spent.",
            palette: palette,
            actions: AnyView(addButton),
            onClose: onClose,
            scrolls: !usesList
        ) {
            content
        }
        .task { await model.loadAccounts() }
        .confirmationDialog(
            "Remove this account?",
            isPresented: Binding(get: { pendingDelete != nil }, set: { if !$0 { pendingDelete = nil } }),
            titleVisibility: .visible,
            presenting: pendingDelete
        ) { a in
            Button("Remove account", role: .destructive) { delete(a) }
            Button("Cancel", role: .cancel) { pendingDelete = nil }
        } message: { a in
            Text(deleteWarning(a))
        }
    }

    @ViewBuilder private var content: some View {
        #if os(iOS)
        if usesList { accountList } else { cardBody }
        #else
        cardBody
        #endif
    }

    private func openAdd() {
        #if os(iOS)
        path.append(.add)
        #else
        route = .add
        #endif
    }

    private func closeChild() {
        #if os(iOS)
        if !path.isEmpty { path.removeLast() }
        #else
        route = nil
        #endif
    }

    private var addButton: some View {
        Button { openAdd() } label: { Label("Add", systemImage: "plus") }
    }

    // MARK: - Body variants

    /// The macOS shape: a scrolling column of tinted rows under hand-drawn section labels.
    @ViewBuilder private var cardBody: some View {
        usageSection
        if model.accounts.isEmpty {
            emptyState
        } else {
            ForEach(byProvider, id: \.provider) { group in
                providerSection(group.provider, group.accounts)
            }
        }
    }

    #if os(iOS)
    /// The iOS shape: the platform's grouped list. The provider names become real section headers
    /// instead of hand-drawn captions, and the swipe restores the delete gesture the hand-rolled
    /// rows had no way to offer.
    private var accountList: some View {
        List {
            Section("Usage") { usageRows }
            ForEach(byProvider, id: \.provider) { group in
                Section(group.provider.capitalized) {
                    ForEach(group.accounts) { a in
                        accountRow(a, provider: group.provider, inList: true)
                            .sheetSwipeDelete("Remove") { pendingDelete = a }
                    }
                }
            }
        }
        .sheetListChrome(palette)
    }
    #endif

    /// The keys are the part that can't be recovered — an ANTHROPIC_API_KEY lives here and in the
    /// provider's console, and the console won't show it to you twice either.
    private func deleteWarning(_ a: Account) -> String {
        let keys = (a.env ?? [:]).keys.sorted()
        let base = "“\(a.name)” will be removed from \(a.provider)."
        guard !keys.isEmpty else { return base + " Sessions already running aren't affected." }
        return base + " The credentials it holds (\(keys.joined(separator: ", "))) are deleted with it and can't be recovered — you'd have to fetch them from the provider again."
    }

    /// `deleteAccount` returns Void and swallows its error, so a delete that never reached the daemon
    /// looks exactly like one that did. The daemon answers with the whole account list, so an account
    /// that's still there is the honest signal nothing happened.
    private func delete(_ a: Account) {
        pendingDelete = nil
        Task {
            await model.deleteAccount(a.id)
            if model.accounts.contains(where: { $0.id == a.id }) {
                model.setError("Couldn’t remove \(a.name)",
                               "It's still stored. Check the daemon is connected and try again.")
            }
        }
    }

    private func usageLine(_ u: ProviderUsage) -> some View {
        HStack(spacing: OculusSpace.sm) {
            // A fixed 110pt column clipped the provider name once the text scaled; a minimum lets it
            // grow with Dynamic Type instead of truncating.
            Text(u.provider).font(.subheadline.weight(.medium))
                .frame(minWidth: 110, alignment: .leading)
            Text("\(u.sessions) session\(u.sessions == 1 ? "" : "s")")
                .font(.caption.monospaced()).foregroundStyle(palette.mutedForeground)
            Spacer(minLength: OculusSpace.xs)
            Text("\(fmtTokens(u.inputTokens + u.outputTokens)) tok")
                .font(.caption.monospaced()).foregroundStyle(palette.mutedForeground)
            if u.costUSD > 0 {
                Text(String(format: "$%.3f", u.costUSD))
                    .font(.footnote.weight(.semibold).monospaced())
                    .foregroundStyle(palette.primary)
            }
        }
    }

    private var sortedUsage: [ProviderUsage] { model.providerUsage.sorted { $0.costUSD > $1.costUSD } }

    /// List form: the section header carries the "Usage" label, so the rows are bare.
    @ViewBuilder private var usageRows: some View {
        if model.providerUsage.isEmpty {
            Text("No usage yet this session.").font(.footnote).foregroundStyle(palette.mutedForeground)
        } else {
            ForEach(sortedUsage) { u in usageLine(u) }
        }
    }

    /// Card form: the label and the row tint have to be drawn by hand, since there is no List to
    /// draw them.
    private var usageSection: some View {
        VStack(alignment: .leading, spacing: OculusSpace.sm) {
            Text("Usage").font(.caption.weight(.semibold)).foregroundStyle(palette.mutedForeground)
            if model.providerUsage.isEmpty {
                Text("No usage yet this session.").font(.footnote).foregroundStyle(palette.mutedForeground)
            } else {
                ForEach(sortedUsage) { u in
                    usageLine(u)
                        .padding(.vertical, 6).padding(.horizontal, OculusSpace.sm)
                        .background(OculusShape.rounded(OculusRadius.sm).fill(palette.secondary.opacity(0.4)))
                }
            }
        }
    }

    private func providerSection(_ provider: String, _ accounts: [Account]) -> some View {
        VStack(alignment: .leading, spacing: OculusSpace.xs) {
            // Title Case, not SHOUTED: OS 26 moved section headers to Title Case systemwide, so a
            // hand-uppercased header now reads as a departure from every native list.
            Text(provider.capitalized).font(.caption.weight(.semibold))
                .foregroundStyle(palette.mutedForeground)
            ForEach(accounts) { a in
                accountRow(a, provider: provider, inList: false)
                    .padding(.vertical, 7).padding(.horizontal, OculusSpace.sm)
                    .background(OculusShape.rounded(OculusRadius.sm)
                        .fill(palette.secondary.opacity(a.active == true ? 0.6 : 0.3)))
            }
        }
    }

    /// One account, shared by both shapes. `inList` drops the trash button: in a List the delete is
    /// the swipe (which stages the same confirmation), and a second target in an already-crowded row
    /// is one more thing to hit by accident.
    private func accountRow(_ a: Account, provider: String, inList: Bool) -> some View {
        VStack(alignment: .leading, spacing: OculusSpace.xs) {
            HStack(spacing: OculusSpace.sm) {
                Image(systemName: a.active == true ? "largecircle.fill.circle" : "circle")
                    .foregroundStyle(a.active == true ? palette.primary : palette.mutedForeground)
                    .accessibilityHidden(true)
                VStack(alignment: .leading, spacing: 1) {
                    Text(a.name).font(.subheadline.weight(.medium)).foregroundStyle(palette.foreground)
                    if let env = a.env, !env.isEmpty {
                        Text(env.keys.sorted().joined(separator: ", "))
                            .font(.caption2.monospaced()).foregroundStyle(palette.mutedForeground).lineLimit(1)
                    }
                }
                Spacer(minLength: OculusSpace.xs)
                Button {
                    checking.insert(a.id)
                    Task { quota[a.id] = await model.accountQuota(a.id); checking.remove(a.id) }
                } label: {
                    if checking.contains(a.id) { ProgressView().controlSize(.small) }
                    else { Text("Quota").font(.caption) }
                }
                .buttonStyle(.bordered)
                #if os(macOS)
                .controlSize(.small)
                #endif
                .accessibilityLabel("Check remaining quota for \(a.name)")
                if a.active == true {
                    Text("Active").font(.caption2.weight(.bold)).foregroundStyle(palette.primary)
                } else {
                    Button("Use") { Task { await model.activateAccount(provider: provider, id: a.id) } }
                        .buttonStyle(.bordered)
                        #if os(macOS)
                        .controlSize(.small)
                        #endif
                        .accessibilityLabel("Use \(a.name) for new \(provider) sessions")
                }
                if !inList {
                    // Destructive role AND the destructive colour: this was an 11pt glyph in the
                    // de-emphasized muted grey, which told the eye it was the minor, safe control.
                    Button(role: .destructive) { pendingDelete = a } label: {
                        Image(systemName: "trash").font(.caption)
                    }
                    .buttonStyle(.plain).foregroundStyle(palette.destructive)
                    .accessibilityLabel("Remove account \(a.name)")
                    .sheetTapTarget()
                }
            }
            if let q = quota[a.id] { quotaRow(q) }
        }
    }

    private func quotaRow(_ q: AccountQuota) -> some View {
        Group {
            if !q.available {
                Text(q.note ?? "Quota not available (subscription login or no API key).")
                    .font(.caption2).foregroundStyle(palette.mutedForeground)
            } else {
                HStack(spacing: OculusSpace.sm) {
                    if q.requestsRemaining >= 0 {
                        Text("\(q.requestsRemaining) req left").font(.caption2.monospaced())
                    }
                    if q.tokensRemaining >= 0 {
                        Text("\(fmtTokens(q.tokensRemaining)) tok left").font(.caption2.monospaced())
                    }
                    if let r = q.resetInSeconds, r > 0 {
                        Text("resets in \(r < 60 ? "\(r)s" : "\(r/60)m")").font(.caption2.monospaced())
                    }
                }
                .foregroundStyle(palette.primary)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }

    /// The bare two-sentence version left the user reading about a feature with no way to start it —
    /// the Add button is in the header, which is exactly where an empty screen doesn't look.
    private var emptyState: some View {
        SheetEmptyState(icon: "person.2.badge.key",
                        title: "No accounts yet",
                        message: "Add a named credential — an API key, or a config-dir env var — for an agent, then hot-swap which one new sessions use without editing any files.",
                        palette: palette) {
            Button { openAdd() } label: { Label("Add an account", systemImage: "plus") }
                .buttonStyle(.borderedProminent).tint(palette.primary)
        }
    }

    private func fmtTokens(_ n: Int) -> String {
        if n >= 1_000_000 { return String(format: "%.1fM", Double(n) / 1_000_000) }
        if n >= 1_000 { return String(format: "%.1fk", Double(n) / 1_000) }
        return "\(n)"
    }
}

/// Add/edit an account: provider + name + a small env-var table (KEY=value pairs).
struct AddAccountSheet: View {
    @ObservedObject var model: Model
    let palette: OculusPalette
    /// Pushed onto a navigation stack rather than presented. The stack supplies the title bar, so
    /// the scaffold header and the in-content button row would both be duplicates of it.
    var pushed: Bool = false
    var onClose: () -> Void

    @State private var provider = ""
    @State private var name = ""
    @State private var rows: [EnvRow] = [EnvRow()]
    @State private var problem: String? = nil
    @State private var confirmDiscard = false
    @FocusState private var focus: Field?

    private enum Field: Hashable { case name, key(Int), value(Int) }

    struct EnvRow: Identifiable { let id = UUID(); var key = ""; var value = "" }

    /// Anything typed is unsaved work — and the values here are API keys, which exist nowhere else
    /// once the sheet closes.
    private var dirty: Bool {
        !name.trimmingCharacters(in: .whitespaces).isEmpty
        || rows.contains { !$0.key.isEmpty || !$0.value.isEmpty }
    }

    var body: some View {
        form
            .onAppear {
                if provider.isEmpty { provider = model.providers.first ?? "" }
                focus = .name
            }
            .sheetDraftGuard(dirty)
            .confirmationDialog("Discard this account?", isPresented: $confirmDiscard, titleVisibility: .visible) {
                Button("Discard", role: .destructive, action: onClose)
                Button("Keep editing", role: .cancel) {}
            } message: {
                Text("Nothing is saved — including any key you've typed, which isn't stored anywhere else yet.")
            }
            #if os(iOS)
            .navigationTitle(pushed ? "Add account" : "")
            .navigationBarTitleDisplayMode(.inline)
            // Back would leave WITHOUT the discard confirmation, which is the one thing the guard
            // exists to prevent: an API key typed here is stored nowhere else yet. Cancel is the only
            // way out of a pushed form, and Cancel asks.
            .navigationBarBackButtonHidden(pushed)
            .toolbar { pushedActions }
            #endif
    }

    #if os(iOS)
    @ToolbarContentBuilder private var pushedActions: some ToolbarContent {
        if pushed {
            ToolbarItem(placement: .cancellationAction) { Button("Cancel") { cancel() } }
            ToolbarItem(placement: .confirmationAction) { Button("Save") { save() } }
        }
    }
    #endif

    private var form: some View {
        OculusSheet(title: "Add account",
                    subtitle: "A named credential you can switch to per session.",
                    palette: palette,
                    showsHeader: !pushed) {
            HStack(spacing: OculusSpace.md) {
                VStack(alignment: .leading, spacing: OculusSpace.xs) {
                    fieldLabel("Agent", required: true)
                    Picker("", selection: $provider) {
                        Text("—").tag("")
                        ForEach(model.providers, id: \.self) { Text($0).tag($0) }
                    }
                    .labelsHidden().frame(minWidth: 130)
                    .accessibilityLabel("Agent this account belongs to")
                }
                VStack(alignment: .leading, spacing: OculusSpace.xs) {
                    fieldLabel("Name", required: true)
                    TextField("e.g. Work", text: $name).textFieldStyle(.roundedBorder)
                        .frame(minWidth: 160)
                        .focused($focus, equals: .name)
                        .submitLabel(.next)
                        .onSubmit { focus = .key(0) }
                }
            }
            fieldLabel("Env overrides (API keys / config dirs)")
            ForEach(rows.indices, id: \.self) { i in
                HStack(spacing: OculusSpace.sm) {
                    // Technical fields: an ANTHROPIC_API_KEY typed here used to come back as
                    // "Anthropic_api_key" because iOS autocapitalized the first letter.
                    TextField("KEY", text: $rows[i].key).textFieldStyle(.roundedBorder)
                        .frame(minWidth: 150).plainInput()
                        .focused($focus, equals: .key(i))
                        .submitLabel(.next).onSubmit { focus = .value(i) }
                    TextField("value", text: $rows[i].value).textFieldStyle(.roundedBorder)
                        .plainInput()
                        .focused($focus, equals: .value(i))
                        .submitLabel(i == rows.count - 1 ? .done : .next)
                        .onSubmit { if i < rows.count - 1 { focus = .key(i + 1) } else { focus = nil } }
                }
            }
            Button { rows.append(EnvRow()) } label: { Label("Add variable", systemImage: "plus").font(.caption) }
                .buttonStyle(.plain).foregroundStyle(palette.primary)

            if let problem {
                Text(problem).font(.footnote).foregroundStyle(palette.destructive)
                    .fixedSize(horizontal: false, vertical: true)
            }

            if !pushed {
                HStack(spacing: OculusSpace.sm) {
                    Spacer()
                    Button("Cancel") { cancel() }.keyboardShortcut(.cancelAction)
                    // Enabled and validated on tap: a dead Save can't say whether the agent or the
                    // name is the thing that's missing.
                    Button("Save account") { save() }
                        .buttonStyle(.borderedProminent).tint(palette.primary)
                        .keyboardShortcut(.defaultAction)
                }
            }
        }
    }

    private func fieldLabel(_ t: String, required: Bool = false) -> some View {
        HStack(spacing: OculusSpace.xs) {
            Text(t).font(.caption.weight(.semibold))
            if required { Text("required").font(.caption2).opacity(0.8) }
        }
        .foregroundStyle(palette.mutedForeground)
    }

    private func cancel() {
        if dirty { confirmDiscard = true } else { onClose() }
    }

    private func save() {
        if provider.isEmpty { problem = "Pick which agent this credential is for."; return }
        if name.trimmingCharacters(in: .whitespaces).isEmpty {
            problem = "Give the account a name — it's what you'll pick between, e.g. “Work”."
            return
        }
        problem = nil
        var env: [String: String] = [:]
        for r in rows where !r.key.trimmingCharacters(in: .whitespaces).isEmpty { env[r.key] = r.value }
        Task {
            await model.upsertAccount(Account(provider: provider, name: name, env: env.isEmpty ? nil : env))
            // `upsertAccount` returns Void and swallows its error. The daemon answers with the whole
            // list, so an account that isn't in it didn't save — closing on that would silently drop
            // a typed API key.
            if model.accounts.contains(where: { $0.provider == provider && $0.name == name }) {
                onClose()
            } else {
                problem = "Couldn't save this account. Check the daemon is connected — your entries are still here."
            }
        }
    }
}
