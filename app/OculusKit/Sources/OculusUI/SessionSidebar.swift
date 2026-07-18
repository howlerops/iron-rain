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
    let updatedAt: Date?
}

private struct SessionGroup: Identifiable {
    let name: String
    let items: [SidebarSession]
    let showProvider: Bool // only when a group actually mixes providers
    let showProject: Bool  // the "Recent" group spans projects, so show each row's project
    let hasRunning: Bool
    var id: String { name }
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

    static let newSessionTag = "__new__"

    var body: some View {
        sessionsList
        #if os(macOS)
        // TEMP DEBUG: show the sidebar List's measured height + safe-area insets so we can
        // see whether the scroll container is taller than the window (the overflow).
        .overlay(alignment: .topLeading) {
            GeometryReader { geo in
                Text("H=\(Int(geo.size.height)) sT=\(Int(geo.safeAreaInsets.top)) sB=\(Int(geo.safeAreaInsets.bottom)) win=\(Int(NSApp.windows.first(where: { $0.isVisible })?.frame.height ?? 0))")
                    .font(.system(size: 10, weight: .bold, design: .monospaced))
                    .padding(4)
                    .background(Color.yellow)
                    .foregroundStyle(Color.black)
                    .allowsHitTesting(false)
            }
            .allowsHitTesting(false)
        }
        #endif
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
        .task { await model.discover() }
        .sheet(isPresented: $showPairingQR) {
            PairingQRView(url: model.pairingURL ?? "", palette: palette) { showPairingQR = false }
        }
        .sheet(isPresented: $showAddDesktop) {
            AddDesktopView(store: store, palette: palette) { showAddDesktop = false }
        }
        .alert("Rename desktop", isPresented: $renamingDesktop) {
            TextField("Name", text: $desktopNewName)
            Button("Save") { if let a = store.active { store.rename(a.id, to: desktopNewName) } }
            Button("Cancel", role: .cancel) {}
        }
    }

    /// The sidebar body — a plain session `List`, modeled on Apple's macOS 26
    /// NavigationSplitView sample: the system insets and styles it. No wrapping VStack,
    /// safeAreaInset, or on-List `.searchable` (search is on the split view; the
    /// Sessions/Issues switch is on the detail toolbar). A tiny clear spacer row is the
    /// inset technique from Apple's "extend scrolling under a sidebar" guide, so the first
    /// real row clears the macOS 26 glass titlebar edge instead of being clipped by it.
    private var sessionsList: some View {
        List(selection: $selection) {
            Color.clear
                .frame(height: 1)
                .listRowInsets(EdgeInsets())
                .listRowSeparator(.hidden)
                .listRowBackground(Color.clear)
            if !model.connected {
                HStack(spacing: 5) {
                    Circle().fill(Color.red).frame(width: 6, height: 6)
                    Text(model.statusDetail ?? model.status)
                        .font(.system(size: 11))
                        .foregroundStyle(palette.mutedForeground)
                        .lineLimit(1)
                    Spacer()
                }
                .listRowSeparator(.hidden)
                .listRowBackground(Color.clear)
            }
            ForEach(filteredGroups) { group in
                Section {
                    ForEach(group.items) { item in
                        SessionRow(item: item, active: model.sessionID == item.id,
                                   showProvider: group.showProvider, showProject: group.showProject,
                                   palette: palette)
                            .tag(item.id)
                            .listRowBackground(
                                model.sessionID == item.id
                                    ? palette.primary.opacity(scheme == .dark ? 0.16 : 0.10)
                                    : Color.clear
                            )
                    }
                } header: {
                    sectionHeader(group.name, running: group.hasRunning)
                }
            }
        }
        .listStyle(.sidebar)
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
                if model.pairingURL != nil {
                    Button { showPairingQR = true } label: { Label("Pair a phone…", systemImage: "qrcode") }
                }
                Button { Task { await model.discover() } } label: { Label("Refresh sessions", systemImage: "arrow.clockwise") }
                Button(role: .destructive) { model.disconnect() } label: { Label("Disconnect", systemImage: "bolt.horizontal.circle") }
            } label: {
                Image(systemName: "ellipsis")
            }
            Button { selection = Self.newSessionTag } label: {
                Image(systemName: "square.and.pencil")
            }
            #if os(macOS)
            // TEMP DEBUG: dump the AppKit view tree with frames to the Xcode console.
            Button { Self.dumpViewTree() } label: { Image(systemName: "ladybug") }
            #endif
        }
    }

    #if os(macOS)
    /// TEMP DEBUG: walk the key window's NSView tree and print each view's class + frame,
    /// flagging any scroll/clip view taller than the window so we can see the overflow.
    static func dumpViewTree() {
        guard let win = NSApp.windows.first(where: { $0.isVisible && $0.contentView != nil }),
              let root = win.contentView else { print("DUMP: no window"); return }
        let winH = win.frame.height
        print("=== VIEW TREE (window height=\(Int(winH))) ===")
        func walk(_ v: NSView, _ depth: Int) {
            let cls = String(describing: type(of: v))
            // Only print the structurally interesting views to keep it readable.
            let interesting = cls.contains("Scroll") || cls.contains("Clip") || cls.contains("Split")
                || cls.contains("Table") || cls.contains("Hosting") || cls.contains("Sidebar") || depth < 4
            if interesting {
                let f = v.frame
                let flag = f.height > winH + 2 ? "  <<< TALLER THAN WINDOW" : ""
                print("\(String(repeating: "· ", count: depth))\(cls) \(Int(f.origin.x)),\(Int(f.origin.y)) \(Int(f.width))x\(Int(f.height))\(flag)")
            }
            for sub in v.subviews { walk(sub, depth + 1) }
        }
        walk(root, 0)
        print("=== END ===")
    }
    #endif

    private var desktopName: String {
        let n = store.active?.name ?? model.name
        return n.isEmpty ? "Desktop" : n
    }

    /// `groups`, narrowed to rows whose title matches the search field. Empty sections are
    /// dropped so search collapses the list to just the hits.
    private var filteredGroups: [SessionGroup] {
        let q = searchText.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !q.isEmpty else { return groups }
        return groups.compactMap { g in
            let hits = g.items.filter { $0.title.localizedCaseInsensitiveContains(q) }
            guard !hits.isEmpty else { return nil }
            return SessionGroup(name: g.name, items: hits,
                                showProvider: g.showProvider, showProject: g.showProject,
                                hasRunning: hits.contains { $0.isRunning })
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
        let managedIDs = Set(model.sessions.map { $0.id })
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
            let title = s.workspaceName ?? clean(s.title) ?? clean(discoveredTitles[s.id]) ?? "ses \(s.id.prefix(6))"
            let key = s.projectID.flatMap { projectNames[$0] } ?? ((s.projectID?.isEmpty ?? true) ? "On this Mac" : s.projectID!)
            add(key, SidebarSession(id: s.id, title: title, provider: s.provider, projectName: key,
                                    branch: s.branch, isRunning: s.status == SessionStatusValue.running,
                                    viewOnly: false, updatedAt: date(s.updatedAt)))
        }
        for d in model.discovered where d.provider == "opencode" && d.kind == DiscoveredKind.session {
            guard let sid = d.sessionID, !managedIDs.contains(sid) else { continue }
            add("On this Mac", SidebarSession(id: sid, title: clean(d.title) ?? "ses \(sid.prefix(6))",
                                              provider: "opencode", projectName: "On this Mac", branch: nil,
                                              isRunning: false, viewOnly: false, updatedAt: date(d.updatedAt)))
        }
        for d in model.discovered where d.provider == "claude-code" {
            let name = (d.cwd as NSString?)?.lastPathComponent ?? "session"
            add("View-only", SidebarSession(id: d.discoveryID, title: name, provider: "claude-code",
                                            projectName: "View-only", branch: nil, isRunning: false,
                                            viewOnly: true, updatedAt: date(d.updatedAt)))
        }

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
                Text(item.title)
                    .font(.system(size: 13, weight: active ? .semibold : .medium))
                    .foregroundStyle(active ? palette.primary : palette.foreground)
                    .lineLimit(1)
                if let sub = secondary {
                    Text(sub)
                        .font(.system(size: 11))
                        .foregroundStyle(palette.mutedForeground)
                        .lineLimit(1)
                }
            }
            Spacer(minLength: 6)
            if let b = item.branch, !b.isEmpty {
                HStack(spacing: 3) {
                    Image(systemName: "arrow.triangle.branch").font(.system(size: 9))
                    Text(b).font(.system(size: 10, weight: .medium)).lineLimit(1)
                }
                .foregroundStyle(palette.mutedForeground)
                .padding(.horizontal, 6).padding(.vertical, 2)
                .background(Capsule().fill(palette.mutedForeground.opacity(0.12)))
            }
            if item.isRunning {
                Circle().fill(palette.primary).frame(width: 6, height: 6)
                    .overlay(Circle().stroke(palette.primary.opacity(0.25), lineWidth: 3))
                    .padding(.trailing, 2)
            }
        }
        .padding(.vertical, 3)
        .contentShape(Rectangle())
    }

    /// Provider (only when its group mixes providers, or view-only) joined with a compact
    /// relative time. Nil → the row is a single clean line of just the title.
    private var secondary: String? {
        var parts: [String] = []
        if showProject { parts.append(item.projectName) } // Recent section spans projects
        if item.viewOnly { parts.append("\(item.provider) · view-only") }
        else if showProvider { parts.append(item.provider) }
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


private extension Discovered {
    /// A stable composite identity for a discovered artifact, used to key ForEach
    /// so live host re-discovery (insert/remove/reorder) associates rows to the
    /// right data instead of to a positional array offset.
    var discoveryID: String {
        [provider, kind, sessionID, cwd, path].compactMap { $0 }.joined(separator: "|")
    }
}
