import SwiftUI
import OculusKit

/// The Claude-style session sidebar: branding + status + menu at the top, then
/// New session and the live opencode / claude-code sessions detected on the host.
/// Selection navigates to the chat (a drawer on iPhone, side-by-side on macOS/iPad).
struct SessionSidebar: View {
    @ObservedObject var model: Model
    @Binding var selection: String?
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
        Dictionary(grouping: model.sessions) { $0.projectID ?? "" }
            .map { pid, ss in
                let name = model.projects.first { $0.id == pid }?.name ?? (pid.isEmpty ? "Sessions" : pid)
                return (name, ss)
            }
            .sorted { $0.0 < $1.0 }
    }

    var body: some View {
        VStack(spacing: 0) {
            header
            Divider().overlay(palette.border)
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
                        ForEach(Array(opencodeSessions.enumerated()), id: \.offset) { _, d in
                            row(title: d.title ?? d.sessionID ?? "session", subtitle: "opencode",
                                active: model.sessionID == d.sessionID)
                                .tag(d.sessionID ?? "")
                        }
                    }
                }
                if !claudeSessions.isEmpty {
                    Section("claude-code · view-only") {
                        ForEach(Array(claudeSessions.enumerated()), id: \.offset) { _, d in
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

    private var header: some View {
        HStack(spacing: 10) {
            Image("WolfMark").resizable().scaledToFit().frame(width: 26, height: 26)
            VStack(alignment: .leading, spacing: 1) {
                Text("Iron Rain").font(.headline)
                HStack(spacing: 5) {
                    Circle().fill(statusColor).frame(width: 7, height: 7)
                    Text(statusLabel).font(.caption2).foregroundStyle(palette.mutedForeground)
                }
            }
            Spacer()
            Menu {
                if model.pairingURL != nil {
                    Button("Pair a phone…") { showPairingQR = true }
                }
                Button("Refresh sessions") { Task { await model.discover() } }
                Button("Disconnect", role: .destructive) { model.disconnect() }
            } label: {
                Image(systemName: "ellipsis.circle").foregroundStyle(palette.mutedForeground)
            }
            .menuStyle(.borderlessButton).fixedSize()
        }
        .padding(.horizontal, 14).padding(.vertical, 12)
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

    private var statusColor: Color {
        if model.pendingApproval != nil { return palette.primary }
        if model.busy { return .green }
        return model.connected ? palette.mutedForeground : .red
    }
    private var statusLabel: String {
        if model.pendingApproval != nil { return "awaiting approval" }
        if model.busy { return "working…" }
        return model.status
    }
}
