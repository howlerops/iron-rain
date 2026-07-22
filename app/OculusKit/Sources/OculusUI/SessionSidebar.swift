import SwiftUI
import OculusKit

/// A normalized session for the sidebar list, unifying hub-managed sessions and
/// discovered-on-host sessions into one row model.
private struct SidebarSession: Identifiable {
    let id: String
    let title: String
    let provider: String
    let projectName: String // the natural (project) group this session belongs to
    let branch: String?
    let isRunning: Bool
    let viewOnly: Bool
    /// True when this session is owned by our daemon (started from the app) — it can be
    /// stopped/managed. False for sessions discovered from a terminal (view-only lifecycle).
    let managed: Bool
    let updatedAt: Date?
    var isChild: Bool = false // delegated sub-agent (shown with a ↳ marker)
}

private struct SessionGroup: Identifiable {
    let name: String
    let items: [SidebarSession]
    let showProvider: Bool // only when a group actually mixes providers
    let showProject: Bool  // the "Recent" group spans projects, so show each row's project
    let hasRunning: Bool
    var id: String { name }
}

/// Sidebar list filter — All, only running, only daemon-managed, or only view-only
/// (terminal-owned). Lets you hide the view-only clutter or focus on live work.
private enum SessionFilter: String, CaseIterable, Identifiable {
    case all, running, managed, viewOnly
    var id: String { rawValue }
    var label: String {
        switch self {
        case .all: return "All sessions"
        case .running: return "Running"
        case .managed: return "Managed"
        case .viewOnly: return "View-only"
        }
    }
    var symbol: String {
        switch self {
        case .all: return "square.stack"
        case .running: return "bolt.fill"
        case .managed: return "person.crop.circle.badge.checkmark"
        case .viewOnly: return "eye"
        }
    }
    func matches(_ s: SidebarSession) -> Bool {
        switch self {
        case .all: return true
        case .running: return s.isRunning
        case .managed: return s.managed
        case .viewOnly: return !s.managed
        }
    }
}

/// The session sidebar: a device switcher + status, a Sessions/Issues switch, and the
/// live agent sessions grouped by project. One accent (gold) is used only for state —
/// selection, the running indicator, and primary actions — never for decoration.
struct SessionSidebar: View {
    @ObservedObject var store: DesktopStore
    @ObservedObject var model: Model
    @Binding var selection: String?
    @Environment(\.colorScheme) private var scheme
    private var palette: OculusPalette { .current(scheme) }
    @State private var showPairingQR = false
    @State private var showAddDesktop = false
    @State private var renamingDesktop = false
    @State private var desktopNewName = ""
    /// Driven by `.searchable` on the NavigationSplitView (RootView), per Apple's guidance;
    /// it filters `filteredGroups` here.
    @Binding var searchText: String
    /// Opens the Code detail in review mode for a session's changes (macOS).
    var onReview: ((String) -> Void)? = nil
    /// Opens the New Session sheet straight into "Take over" mode (empty-state action).
    var onTakeOver: (() -> Void)? = nil
    /// macOS: Settings → "Check for updates". The banner (RootView-level) owns the actual check.
    var onCheckForUpdates: (() -> Void)? = nil
    /// Opens the Loops (recurring autonomous workflows) sheet.
    var onOpenLoops: (() -> Void)? = nil
    var onOpenAgents: (() -> Void)? = nil
    @AppStorage("oculus.appearance") private var appearance: Appearance = .system
    @State private var filter: SessionFilter = .all
    @State private var renamingSessionID: String?
    @State private var renameText = ""
    @State private var showFleet = false

    static let newSessionTag = "__new__"

    var body: some View {
        sessionsList
            .overlay {
                if model.connected && searchText.isEmpty && filter == .all && filteredGroups.isEmpty {
                    emptyState
                }
            }
            .tint(palette.primary)
        // The desktop switcher hangs off the title as a `.toolbarTitleMenu` (the
        // Xcode scheme-menu pattern). `.navigationTitle` is also what makes the
        // NavigationSplitView reserve the titlebar top inset for this column.
        .navigationTitle(desktopName)
        #if os(macOS)
        .toolbarTitleMenu { desktopSwitcherMenu }
        // `.searchable(.sidebar)` belongs on the sidebar (NOT the split view): for a
        // selection-based List it's what makes the column reserve the titlebar top inset,
        // so content stops sliding up under the glass titlebar. Now that the sidebar has
        // no other top chrome, nothing competes with it.
        .searchable(text: $searchText, placement: .sidebar, prompt: "Search sessions")
        #else
        .searchable(text: $searchText, prompt: "Search sessions")
        #endif
        .toolbar { sidebarToolbar }
        .sheet(isPresented: $showPairingQR) {
            PairingQRView(url: model.pairingURL ?? "", palette: palette) { showPairingQR = false }
        }
        .sheet(isPresented: $showAddDesktop) {
            AddDesktopView(store: store, palette: palette) { showAddDesktop = false }
        }
        .sheet(isPresented: $showFleet) {
            FleetView(model: model, palette: palette,
                      onOpen: { id in selection = id; showFleet = false },
                      onClose: { showFleet = false })
        }
        .alert("Rename desktop", isPresented: $renamingDesktop) {
            TextField("Name", text: $desktopNewName)
            Button("Save") { if let a = store.active { store.rename(a.id, to: desktopNewName) } }
            Button("Cancel", role: .cancel) {}
        }
        .alert("Rename session", isPresented: Binding(get: { renamingSessionID != nil },
                                                      set: { if !$0 { renamingSessionID = nil } })) {
            TextField("Session name", text: $renameText)
            Button("Save") {
                if let id = renamingSessionID { Task { await model.renameSession(id, to: renameText) } }
                renamingSessionID = nil
            }
            Button("Cancel", role: .cancel) { renamingSessionID = nil }
        } message: {
            Text("Give this session a name. Leave blank to reset to its default title.")
        }
    }

    /// The sidebar body — a plain session `List`, styled by the system as a sidebar. Search
    /// is on the split view; the Sessions/Issues switch is on the detail toolbar. The window
    /// sizing is handled in RootView (windowResizability + detail clamp), so no inset hacks
    /// are needed here.
    private var sessionsList: some View {
        // Selection is drawn by the rows (a soft, elevated gold card), NOT the native List
        // highlight — the system selection is a full-bleed accent-blue rectangle that clashes
        // with the gold theme. Rows are buttons that set `selection`; RootView's onChange opens
        // the session.
        List {
            if !model.connected {
                HStack(spacing: 6) {
                    if model.connecting {
                        ProgressView().controlSize(.mini)          // in progress — not an error
                    } else {
                        Circle().fill(Color.red).frame(width: 6, height: 6) // actually failed/offline
                    }
                    Text(model.connecting ? "Connecting…" : (model.statusDetail ?? model.status))
                        .font(.system(size: 11))
                        .foregroundStyle(palette.mutedForeground)
                        .lineLimit(1)
                    Spacer()
                    if !model.connecting {
                        Button("Retry") { Task { await model.connect() } }
                            .font(.system(size: 11)).buttonStyle(.plain).foregroundStyle(palette.primary)
                    }
                }
                .listRowSeparator(.hidden)
                .listRowBackground(Color.clear)
            }
            ForEach(filteredGroups) { group in
                Section {
                    ForEach(group.items) { item in
                        let selected = model.sessionID == item.id
                        Button { selection = item.id } label: {
                            SessionRow(item: item, active: selected,
                                       showProvider: group.showProvider, showProject: group.showProject,
                                       palette: palette)
                                .padding(.horizontal, 8).padding(.vertical, 5)
                                .frame(maxWidth: .infinity, alignment: .leading)
                                .background(rowSelectionBackground(selected))
                                .contentShape(Rectangle())
                        }
                        .buttonStyle(.plain)
                        .listRowInsets(EdgeInsets(top: 1, leading: 6, bottom: 1, trailing: 6))
                        .listRowSeparator(.hidden)
                        .listRowBackground(Color.clear)
                        .contextMenu { rowMenu(item) }
                    }
                } header: {
                    sectionHeader(group.name, running: group.hasRunning)
                }
            }
        }
        .listStyle(.sidebar)
    }

    /// First-run empty state: no in-app sessions yet, so guide the two ways to get one —
    /// start fresh, or take over a session already running in a terminal.
    private var emptyState: some View {
        VStack(spacing: 14) {
            VStack(spacing: 4) {
                Text("No sessions yet").font(.system(size: 15, weight: .semibold))
                Text("Start an agent on one of your projects, or take over a session already running in a terminal.")
                    .font(.system(size: 12)).foregroundStyle(palette.mutedForeground)
                    .multilineTextAlignment(.center)
            }
            VStack(spacing: 8) {
                Button { selection = Self.newSessionTag } label: {
                    Label("New session", systemImage: "plus").frame(maxWidth: .infinity)
                }
                .buttonStyle(.borderedProminent).tint(palette.primary).controlSize(.large)
                Button { onTakeOver?() } label: {
                    Label("Take over a terminal session", systemImage: "arrow.down.left.circle").frame(maxWidth: .infinity)
                }
                .buttonStyle(.bordered).tint(palette.primary)
            }
            .padding(.top, 2)
        }
        .padding(.horizontal, 22)
        .frame(maxWidth: .infinity)
    }

    /// A soft, elevated selection card in the brand gold — a light gold wash + a faint gold
    /// hairline + a subtle shadow, so the active row reads as raised rather than a jarring
    /// full-blue system highlight.
    @ViewBuilder private func rowSelectionBackground(_ selected: Bool) -> some View {
        if selected {
            // strokeBorder (not stroke) draws the border INSIDE the shape, so its outer half doesn't
            // spill past the row bounds and get clipped by the List cell (the "clipped border" on
            // mobile). Keep the glow subtle so it doesn't clip either.
            RoundedRectangle(cornerRadius: 8)
                .fill(palette.primary.opacity(scheme == .dark ? 0.18 : 0.12))
                .overlay(RoundedRectangle(cornerRadius: 8).strokeBorder(palette.primary.opacity(0.30), lineWidth: 1))
                .shadow(color: palette.primary.opacity(scheme == .dark ? 0.18 : 0.10), radius: 2, y: 1)
        } else {
            Color.clear
        }
    }

    /// The desktop switcher — the list of paired Macs plus add/rename/remove. Hangs off the
    /// navigation title via `.toolbarTitleMenu`, so the title (the active desktop's name)
    /// gains a dropdown chevron, the way Xcode's scheme menu works.
    @ViewBuilder private var desktopSwitcherMenu: some View {
        ForEach(store.models, id: \.id) { m in
            Button { store.selectedID = m.id } label: {
                Label(m.name.isEmpty ? "Desktop" : m.name,
                      systemImage: m.id == store.selectedID ? "checkmark"
                        : (m.connected ? "circle.fill" : "circle"))
            }
        }
        Divider()
        Button { showAddDesktop = true } label: { Label("Add desktop…", systemImage: "plus") }
        if let a = store.active {
            Button { desktopNewName = a.name; renamingDesktop = true } label: { Label("Rename…", systemImage: "pencil") }
            Button(role: .destructive) { store.remove(a.id) } label: { Label("Remove desktop", systemImage: "trash") }
        }
    }

    /// Trailing titlebar actions: the overflow menu and the new-session button.
    @ToolbarContentBuilder private var sidebarToolbar: some ToolbarContent {
        ToolbarItemGroup(placement: .primaryAction) {
            Menu {
                Picker(selection: $filter) {
                    ForEach(SessionFilter.allCases) { f in
                        Label(f.label, systemImage: f.symbol).tag(f)
                    }
                } label: { Text("Filter") }
                .pickerStyle(.inline)
            } label: {
                Image(systemName: filter == .all ? "line.3.horizontal.decrease.circle" : "line.3.horizontal.decrease.circle.fill")
            }
            .help("Filter sessions")
            Menu {
                if model.pairingURL != nil {
                    Button { showPairingQR = true } label: { Label("Pair a phone…", systemImage: "qrcode") }
                }
                Button { Task { await model.discover() } } label: { Label("Refresh sessions", systemImage: "arrow.clockwise") }
                if let onOpenLoops {
                    Button { onOpenLoops() } label: { Label("Loops…", systemImage: "arrow.triangle.2.circlepath") }
                }
                if let onOpenAgents {
                    Button { onOpenAgents() } label: { Label("Agents…", systemImage: "cpu") }
                }
                Picker(selection: $appearance) {
                    ForEach(Appearance.allCases) { a in
                        Label(a.label, systemImage: a.symbol).tag(a)
                    }
                } label: {
                    Label("Appearance", systemImage: "circle.lefthalf.filled")
                }
                #if os(macOS)
                if let onCheckForUpdates {
                    Button { onCheckForUpdates() } label: { Label("Check for updates…", systemImage: "arrow.down.circle") }
                }
                #endif
                Button(role: .destructive) { model.disconnect() } label: { Label("Disconnect", systemImage: "bolt.horizontal.circle") }
            } label: {
                Image(systemName: "ellipsis")
            }
            .help("More options")
            Button { showFleet = true } label: {
                Image(systemName: "square.grid.2x2")
            }
            .help("Agent fleet — all sessions at a glance")
            Button { selection = Self.newSessionTag } label: {
                Image(systemName: "square.and.pencil")
            }
            .help("New session")
        }
    }

    private var desktopName: String {
        let n = store.active?.name ?? model.name
        return n.isEmpty ? "Desktop" : n
    }

    /// `groups`, narrowed by the search field and the active filter. Empty sections are
    /// dropped so the list collapses to just the hits.
    private var filteredGroups: [SessionGroup] {
        let q = searchText.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !q.isEmpty || filter != .all else { return groups }
        return groups.compactMap { g in
            let hits = g.items.filter { item in
                (q.isEmpty || item.title.localizedCaseInsensitiveContains(q)) && filter.matches(item)
            }
            guard !hits.isEmpty else { return nil }
            return SessionGroup(name: g.name, items: hits,
                                showProvider: g.showProvider, showProject: g.showProject,
                                hasRunning: hits.contains { $0.isRunning })
        }
    }

    /// Right-click actions for a row. Managed (daemon-owned) sessions can be stopped, which
    /// ends the agent and removes them. Terminal-owned sessions are view-only — surfaced as a
    /// disabled hint so it's clear why there's nothing to manage.
    @ViewBuilder private func rowMenu(_ item: SidebarSession) -> some View {
        if item.managed {
            Button { renameText = item.title; renamingSessionID = item.id } label: {
                Label("Rename…", systemImage: "pencil")
            }
            if let onReview {
                Button { onReview(item.id) } label: { Label("Review changes", systemImage: "plus.forwardslash.minus") }
            }
            Divider()
            Button(role: .destructive) {
                Task { await model.stopSession(item.id) }
            } label: {
                Label("Delete session", systemImage: "trash")
            }
        } else {
            Label("Started in a terminal · click to resume", systemImage: "terminal")
                .foregroundStyle(palette.mutedForeground)
        }
    }

    private func sectionHeader(_ name: String, running: Bool) -> some View {
        HStack(spacing: 6) {
            Text(name.uppercased())
                .font(.system(size: 11, weight: .bold)).tracking(0.4)
                .foregroundStyle(palette.mutedForeground)
            if running {
                Circle().fill(palette.primary).frame(width: 5, height: 5)
            }
            Spacer()
        }
    }

    // MARK: grouping

    private var groups: [SessionGroup] {
        let projectNames = Dictionary(model.projects.map { ($0.id, $0.name) }, uniquingKeysWith: { a, _ in a })
        let discoveredTitles = Dictionary(model.discovered.compactMap { d -> (String, String)? in
            guard let s = d.sessionID, let t = d.title, !t.isEmpty else { return nil }
            return (s, t)
        }, uniquingKeysWith: { a, _ in a })

        var buckets: [String: [SidebarSession]] = [:]
        var order: [String] = []
        func add(_ key: String, _ item: SidebarSession) {
            if buckets[key] == nil { buckets[key] = []; order.append(key) }
            buckets[key]?.append(item)
        }

        for s in model.sessions {
            let isChild = !(s.parentID?.isEmpty ?? true)
            let title = clean(s.subtask) ?? clean(s.name) ?? s.workspaceName ?? clean(s.title) ?? clean(discoveredTitles[s.id]) ?? "ses \(s.id.prefix(6))"
            let key = s.projectID.flatMap { projectNames[$0] } ?? ((s.projectID?.isEmpty ?? true) ? "On this Mac" : s.projectID!)
            add(key, SidebarSession(id: s.id, title: title, provider: s.provider, projectName: key,
                                    branch: s.branch, isRunning: s.status == SessionStatusValue.running,
                                    viewOnly: false, managed: true, updatedAt: date(s.updatedAt), isChild: isChild))
        }
        // Terminal-owned sessions discovered on the host are intentionally NOT shown here —
        // the sidebar lists only sessions started/opened in the app. Discovered sessions are
        // found on demand via the Add Session search (which attaches them, making them managed).

        // Pull recently-active sessions (active within the window, or running) out of their
        // project buckets into a single "Recent" section at the top. View-only sessions stay
        // in their own section — they're a different interaction class.
        let cutoff = Date().addingTimeInterval(-recentWindow)
        var recent: [SidebarSession] = []
        for key in order where key != "View-only" {
            var kept: [SidebarSession] = []
            for it in buckets[key] ?? [] {
                if it.isRunning || (it.updatedAt.map { $0 >= cutoff } ?? false) {
                    recent.append(it)
                } else {
                    kept.append(it)
                }
            }
            buckets[key] = kept
        }

        func group(_ name: String, _ items: [SidebarSession], showProject: Bool) -> SessionGroup {
            let sorted = items.sorted { a, b in
                if a.isRunning != b.isRunning { return a.isRunning }
                return (a.updatedAt ?? .distantPast) > (b.updatedAt ?? .distantPast)
            }
            return SessionGroup(name: name, items: sorted,
                                showProvider: Set(sorted.map { $0.provider }).count > 1,
                                showProject: showProject,
                                hasRunning: sorted.contains { $0.isRunning })
        }

        var result: [SessionGroup] = []
        if !recent.isEmpty { result.append(group("Recent", recent, showProject: true)) }
        let special = ["On this Mac", "View-only"]
        let projects = order.filter { !special.contains($0) }.sorted()
        let tail = special.filter { !(buckets[$0]?.isEmpty ?? true) }
        for name in projects + tail where !(buckets[name]?.isEmpty ?? true) {
            result.append(group(name, buckets[name] ?? [], showProject: false))
        }
        return result
    }

    /// A session counts as "Recent" if it was active within this window (or is running).
    private let recentWindow: TimeInterval = 24 * 3600

    private func date(_ secs: Int?) -> Date? {
        guard let s = secs, s > 0 else { return nil }
        return Date(timeIntervalSince1970: TimeInterval(s))
    }

    /// Cleans a raw title: strips the "New session - <ISO8601>" pattern and blanks.
    private func clean(_ raw: String?) -> String? {
        guard let t = raw?.trimmingCharacters(in: .whitespacesAndNewlines), !t.isEmpty else { return nil }
        if t.hasPrefix("New session"),
           t.range(of: #"\d{4}-\d{2}-\d{2}T"#, options: .regularExpression) != nil {
            return "New session"
        }
        return t
    }
}

/// One session row: a gold left-bar + gold title when it's the active session, a running
/// dot only while running, provider only when its group mixes providers, branch as a chip.
private struct SessionRow: View {
    let item: SidebarSession
    let active: Bool
    let showProvider: Bool
    let showProject: Bool
    let palette: OculusPalette

    var body: some View {
        HStack(spacing: 9) {
            RoundedRectangle(cornerRadius: 2)
                .fill(active ? palette.primary : Color.clear)
                .frame(width: 3, height: 22)
            VStack(alignment: .leading, spacing: 2) {
                HStack(spacing: 4) {
                    if item.isChild {
                        Image(systemName: "arrow.turn.down.right")
                            .font(.system(size: 10)).foregroundStyle(palette.mutedForeground)
                    }
                    Text(item.title)
                        .font(.system(size: 13, weight: active ? .semibold : .medium))
                        .foregroundStyle(palette.foreground)
                        .lineLimit(1)
                }
                if let sub = secondary {
                    Text(sub)
                        .font(.system(size: 11))
                        .foregroundStyle(palette.mutedForeground)
                        .lineLimit(1)
                }
            }
            Spacer(minLength: 6)
            if let b = item.branch, !b.isEmpty {
                chip(icon: "arrow.triangle.branch", text: b, tint: palette.mutedForeground)
            }
            // A solid chip to distinguish lifecycle at a glance: running (gold, live), or a
            // terminal glyph for sessions started outside the app (discovered — clicking
            // resumes them). Managed idle sessions carry no chip; they're the plain default.
            if item.isRunning {
                chip(icon: "circle.fill", text: "Live", tint: palette.primary, filled: true)
            } else if !item.managed {
                chip(icon: "terminal", text: nil, tint: palette.mutedForeground)
            }
        }
        .padding(.vertical, 3)
        .contentShape(Rectangle())
    }

    private func chip(icon: String, text: String?, tint: Color, filled: Bool = false) -> some View {
        HStack(spacing: 3) {
            Image(systemName: icon).font(.system(size: filled ? 6 : 9))
            if let text { Text(text).font(.system(size: 10, weight: .semibold)).lineLimit(1) }
        }
        .foregroundStyle(filled ? tint : palette.mutedForeground)
        .padding(.horizontal, text == nil ? 5 : 6).padding(.vertical, 2)
        .background(Capsule().fill(tint.opacity(filled ? 0.16 : 0.12)))
    }

    /// Provider (only when its group mixes providers) joined with a compact relative time.
    /// The view-only/managed distinction is carried by the trailing chip, not this line.
    private var secondary: String? {
        var parts: [String] = []
        if showProject { parts.append(item.projectName) } // Recent section spans projects
        if showProvider || item.viewOnly { parts.append(item.provider) }
        if let t = item.updatedAt { parts.append(Self.relative(t)) }
        return parts.isEmpty ? nil : parts.joined(separator: " · ")
    }

    private static let weekdayFmt: DateFormatter = {
        let f = DateFormatter(); f.dateFormat = "EEE"; return f // Mon
    }()
    private static let dateFmt: DateFormatter = {
        let f = DateFormatter(); f.setLocalizedDateFormatFromTemplate("MMMd"); return f // Jul 3
    }()

    static func relative(_ date: Date) -> String {
        let s = Date().timeIntervalSince(date)
        if s < 45 { return "now" }
        if s < 3600 { return "\(Int(s / 60))m ago" }
        if s < 86_400 { return "\(Int(s / 3600))h ago" }
        if Calendar.current.isDateInYesterday(date) { return "Yesterday" }
        if s < 7 * 86_400 { return weekdayFmt.string(from: date) }
        return dateFmt.string(from: date)
    }
}


