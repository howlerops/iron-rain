import SwiftUI
import OculusKit

/// The Claude-style session sidebar: branding + status + menu at the top, then
/// New session and the live opencode / claude-code sessions detected on the host.
/// Selection navigates to the chat (a drawer on iPhone, side-by-side on macOS/iPad).
struct SessionSidebar: View {
    @ObservedObject var store: DesktopStore
    @ObservedObject var model: Model
    @Binding var selection: String?
    @Binding var tab: Int // 0 = Sessions, 1 = Issues (macOS mode switch lives in the sidebar)
    @Environment(\.colorScheme) private var scheme
    private var palette: OculusPalette { .current(scheme) }
    @State private var showPairingQR = false

    static let newSessionTag = "__new__"

    private var opencodeSessions: [Discovered] {
        model.discovered.filter { $0.provider == "opencode" && $0.kind == DiscoveredKind.session }
    }
    private var claudeSessions: [Discovered] {
        model.discovered.filter { $0.provider == "claude-code" }
    }

    /// Hub-managed sessions grouped by their project (worktree sessions included),
    /// sorted by group name. Sessions with no project fall under "Sessions".
    private var sessionGroups: [(name: String, sessions: [Session])] {
        // Build a [projectID: name] lookup once so resolving each group's name is
        // O(1) instead of a linear scan of `model.projects` per group.
        let projectNames = Dictionary(model.projects.map { ($0.id, $0.name) },
                                      uniquingKeysWith: { first, _ in first })
        return Dictionary(grouping: model.sessions) { $0.projectID ?? "" }
            .map { pid, ss in
                let name = projectNames[pid] ?? (pid.isEmpty ? "Sessions" : pid)
                return (name, ss)
            }
            .sorted { $0.0 < $1.0 }
    }

    var body: some View {
        VStack(spacing: 0) {
            SidebarHeader(store: store, model: model, palette: palette) { showPairingQR = true }
            Divider().overlay(palette.border)
            #if os(macOS)
            Picker("", selection: $tab) {
                Text("Sessions").tag(0)
                Text("Issues").tag(1)
            }
            .pickerStyle(.segmented).labelsHidden()
            .padding(.horizontal, 10).padding(.vertical, 8)
            Divider().overlay(palette.border)
            #endif
            List(selection: $selection) {
                Label("New session", systemImage: "plus.circle").tag(Self.newSessionTag)

                ForEach(sessionGroups, id: \.name) { group in
                    Section(group.name) {
                        ForEach(group.sessions, id: \.id) { s in
                            row(title: s.workspaceName ?? s.title ?? String(s.id.prefix(8)),
                                subtitle: s.branch ?? s.provider,
                                active: model.sessionID == s.id)
                                .tag(s.id)
                        }
                    }
                }

                if !opencodeSessions.isEmpty {
                    Section("Sessions") {
                        ForEach(opencodeSessions, id: \.sessionID) { d in
                            row(title: d.title ?? d.sessionID ?? "session", subtitle: "opencode",
                                active: model.sessionID == d.sessionID)
                                .tag(d.sessionID ?? "")
                        }
                    }
                }
                if !claudeSessions.isEmpty {
                    Section("claude-code · view-only") {
                        ForEach(claudeSessions, id: \.discoveryID) { d in
                            row(title: (d.cwd as NSString?)?.lastPathComponent ?? "session",
                                subtitle: d.cwd ?? "", active: false)
                                .foregroundStyle(palette.mutedForeground)
                        }
                    }
                }
            }
            .refreshable { await model.discover() }
            .task { await model.discover() }
        }
        .background(palette.background)
        .sheet(isPresented: $showPairingQR) {
            PairingQRView(url: model.pairingURL ?? "", palette: palette) { showPairingQR = false }
        }
    }

    private func row(title: String, subtitle: String, active: Bool) -> some View {
        HStack(spacing: 8) {
            Circle().fill(active ? palette.primary : palette.mutedForeground.opacity(0.4))
                .frame(width: 7, height: 7)
            VStack(alignment: .leading, spacing: 1) {
                Text(title).lineLimit(1)
                if !subtitle.isEmpty {
                    Text(subtitle).font(.caption2).foregroundStyle(palette.mutedForeground).lineLimit(1)
                }
            }
            Spacer()
        }
        .contentShape(Rectangle())
    }
}

/// Unified sidebar header: a single row that switches desktops (tap the name),
/// shows live connection status with a real reason when it can't connect, and hosts
/// the overflow menu. Replaces the old stacked DesktopBar + branding header.
struct SidebarHeader: View {
    @ObservedObject var store: DesktopStore
    @ObservedObject var model: Model
    let palette: OculusPalette
    var onPairPhone: () -> Void

    @State private var showAdd = false
    @State private var renaming = false
    @State private var newName = ""

    var body: some View {
        HStack(spacing: 10) {
            // The brand mark is a sibling of the Menu, NOT inside its label: a resizable
            // image inside a .fixedSize() borderlessButton menu label has no intrinsic
            // size and blows up to fill the window on macOS.
            Image("WolfMark").resizable().scaledToFit().frame(width: 26, height: 26)

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
                VStack(alignment: .leading, spacing: 2) {
                    HStack(spacing: 5) {
                        Text(desktopName).font(.headline).lineLimit(1)
                        Image(systemName: "chevron.up.chevron.down")
                            .font(.caption2).foregroundStyle(palette.mutedForeground)
                    }
                    HStack(spacing: 5) {
                        Circle().fill(statusColor).frame(width: 7, height: 7)
                        Text(statusLabel).font(.caption2)
                            .foregroundStyle(palette.mutedForeground).lineLimit(1)
                    }
                }
                .contentShape(Rectangle())
            }
            .menuStyle(.borderlessButton).fixedSize()

            Spacer()

            Menu {
                if model.pairingURL != nil {
                    Button { onPairPhone() } label: { Label("Pair a phone…", systemImage: "qrcode") }
                }
                Button { Task { await model.discover() } } label: { Label("Refresh sessions", systemImage: "arrow.clockwise") }
                Button(role: .destructive) { model.disconnect() } label: { Label("Disconnect", systemImage: "bolt.horizontal.circle") }
            } label: {
                Image(systemName: "ellipsis.circle").foregroundStyle(palette.mutedForeground)
            }
            .menuStyle(.borderlessButton).fixedSize()
        }
        .padding(.horizontal, 14).padding(.vertical, 10)
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
    private var statusColor: Color {
        if model.pendingApproval != nil { return palette.primary }
        if model.busy { return .green }
        return model.connected ? .green : .red
    }
    private var statusLabel: String {
        if model.pendingApproval != nil { return "Awaiting approval" }
        if model.busy { return "Working…" }
        if model.connected { return "Connected" }
        return model.statusDetail ?? model.status
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
