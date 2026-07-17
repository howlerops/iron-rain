import SwiftUI
import OculusKit

/// A normalized session for the sidebar list, unifying hub-managed sessions and
/// discovered-on-host sessions into one row model.
private struct SidebarSession: Identifiable {
    let id: String
    let title: String
    let provider: String
    let branch: String?
    let isRunning: Bool
    let viewOnly: Bool
}

private struct SessionGroup: Identifiable {
    let name: String
    let items: [SidebarSession]
    let showProvider: Bool // only when a group actually mixes providers
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
    @Binding var tab: Int
    @Environment(\.colorScheme) private var scheme
    private var palette: OculusPalette { .current(scheme) }
    @State private var showPairingQR = false

    static let newSessionTag = "__new__"

    var body: some View {
        VStack(spacing: 0) {
            SidebarHeader(store: store, model: model, palette: palette,
                          onPairPhone: { showPairingQR = true },
                          onNewSession: { selection = Self.newSessionTag })
            Divider().overlay(palette.border)
            #if os(macOS)
            SidebarTabPicker(tab: $tab, palette: palette)
                .padding(.horizontal, 12).padding(.top, 8).padding(.bottom, 2)
            #endif
            list
        }
        .background(palette.background)
        .sheet(isPresented: $showPairingQR) {
            PairingQRView(url: model.pairingURL ?? "", palette: palette) { showPairingQR = false }
        }
    }

    private var list: some View {
        List(selection: $selection) {
            ForEach(groups) { group in
                Section {
                    ForEach(group.items) { item in
                        SessionRow(item: item, active: model.sessionID == item.id,
                                   showProvider: group.showProvider, palette: palette)
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
        .refreshable { await model.discover() }
        .task { await model.discover() }
        .overlay {
            if groups.isEmpty {
                Text("No sessions yet")
                    .font(.system(size: 12)).foregroundStyle(palette.mutedForeground)
            }
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
            let item = SidebarSession(id: s.id, title: title, provider: s.provider,
                                      branch: s.branch, isRunning: s.status == SessionStatusValue.running, viewOnly: false)
            let key = s.projectID.flatMap { projectNames[$0] } ?? ((s.projectID?.isEmpty ?? true) ? "On this Mac" : s.projectID!)
            add(key, item)
        }
        for d in model.discovered where d.provider == "opencode" && d.kind == DiscoveredKind.session {
            guard let sid = d.sessionID, !managedIDs.contains(sid) else { continue }
            add("On this Mac", SidebarSession(id: sid, title: clean(d.title) ?? "ses \(sid.prefix(6))",
                                              provider: "opencode", branch: nil, isRunning: false, viewOnly: false))
        }
        for d in model.discovered where d.provider == "claude-code" {
            let name = (d.cwd as NSString?)?.lastPathComponent ?? "session"
            add("View-only", SidebarSession(id: d.discoveryID, title: name, provider: "claude-code",
                                            branch: nil, isRunning: false, viewOnly: true))
        }

        let special = ["On this Mac", "View-only"]
        let projects = order.filter { !special.contains($0) }.sorted()
        let tail = special.filter { buckets[$0] != nil }
        return (projects + tail).map { name in
            let items = buckets[name] ?? []
            return SessionGroup(name: name, items: items,
                                showProvider: Set(items.map { $0.provider }).count > 1,
                                hasRunning: items.contains { $0.isRunning })
        }
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
                if showProvider || item.viewOnly {
                    Text(item.viewOnly ? "\(item.provider) · view-only" : item.provider)
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
}

/// A neutral segmented control (Sessions / Issues). Deliberately NOT gold — gold is
/// reserved for selection/running/actions, so the tab switch stays quiet.
private struct SidebarTabPicker: View {
    @Binding var tab: Int
    let palette: OculusPalette

    var body: some View {
        HStack(spacing: 2) {
            segment("Sessions", 0)
            segment("Issues", 1)
        }
        .padding(2)
        .background(Color.primary.opacity(0.06))
        .clipShape(RoundedRectangle(cornerRadius: 7))
    }

    private func segment(_ title: String, _ index: Int) -> some View {
        Button { tab = index } label: {
            Text(title)
                .font(.system(size: 12, weight: .semibold))
                .frame(maxWidth: .infinity).padding(.vertical, 4)
                .foregroundStyle(tab == index ? palette.foreground : palette.mutedForeground)
                .background {
                    if tab == index {
                        RoundedRectangle(cornerRadius: 5)
                            .fill(palette.background)
                            .shadow(color: .black.opacity(0.12), radius: 1, y: 0.5)
                    }
                }
        }
        .buttonStyle(.plain)
    }
}

/// Unified sidebar header: a compact device switcher (wolf glyph + name + menu), an
/// overflow menu, and a new-session action. The connection reason shows only when the
/// connection is down, so a healthy header stays clean.
struct SidebarHeader: View {
    @ObservedObject var store: DesktopStore
    @ObservedObject var model: Model
    let palette: OculusPalette
    var onPairPhone: () -> Void
    var onNewSession: () -> Void

    @State private var showAdd = false
    @State private var renaming = false
    @State private var newName = ""

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            HStack(spacing: 8) {
                Image("WolfMark").resizable().scaledToFit().frame(width: 18, height: 18)

                Menu {
                    ForEach(store.models, id: \.id) { m in
                        Button { store.selectedID = m.id } label: {
                            Label(m.name.isEmpty ? "Desktop" : m.name,
                                  systemImage: m.id == store.selectedID ? "checkmark"
                                    : (m.connected ? "circle.fill" : "circle"))
                        }
                    }
                    Divider()
                    Button { showAdd = true } label: { Label("Add desktop…", systemImage: "plus") }
                    if let a = store.active {
                        Button { newName = a.name; renaming = true } label: { Label("Rename…", systemImage: "pencil") }
                        Button(role: .destructive) { store.remove(a.id) } label: { Label("Remove desktop", systemImage: "trash") }
                    }
                } label: {
                    HStack(spacing: 4) {
                        Text(desktopName).font(.system(size: 15, weight: .semibold))
                            .foregroundStyle(palette.foreground).lineLimit(1)
                        Image(systemName: "chevron.down").font(.system(size: 9, weight: .bold))
                            .foregroundStyle(palette.mutedForeground)
                    }
                    .contentShape(Rectangle())
                }
                .menuStyle(.borderlessButton).fixedSize(horizontal: false, vertical: true)

                Spacer(minLength: 0)

                Menu {
                    if model.pairingURL != nil {
                        Button { onPairPhone() } label: { Label("Pair a phone…", systemImage: "qrcode") }
                    }
                    Button { Task { await model.discover() } } label: { Label("Refresh sessions", systemImage: "arrow.clockwise") }
                    Button(role: .destructive) { model.disconnect() } label: { Label("Disconnect", systemImage: "bolt.horizontal.circle") }
                } label: {
                    Image(systemName: "ellipsis").font(.system(size: 13, weight: .semibold))
                        .foregroundStyle(palette.mutedForeground)
                        .frame(width: 22, height: 22)
                }
                .menuStyle(.borderlessButton).fixedSize()

                Button { onNewSession() } label: {
                    Image(systemName: "square.and.pencil").font(.system(size: 13, weight: .semibold))
                        .foregroundStyle(palette.primary)
                        .frame(width: 22, height: 22)
                }
                .buttonStyle(.plain)
            }

            if !model.connected {
                HStack(spacing: 5) {
                    Circle().fill(Color.red).frame(width: 6, height: 6)
                    Text(model.statusDetail ?? model.status)
                        .font(.system(size: 11)).foregroundStyle(palette.mutedForeground).lineLimit(1)
                }
            }
        }
        .padding(.horizontal, 12).padding(.top, 10).padding(.bottom, 8)
        .sheet(isPresented: $showAdd) { AddDesktopView(store: store, palette: palette) { showAdd = false } }
        .alert("Rename desktop", isPresented: $renaming) {
            TextField("Name", text: $newName)
            Button("Save") { if let a = store.active { store.rename(a.id, to: newName) } }
            Button("Cancel", role: .cancel) {}
        }
    }

    private var desktopName: String {
        let n = store.active?.name ?? model.name
        return n.isEmpty ? "Desktop" : n
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
