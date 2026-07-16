import SwiftUI
import OculusKit

/// The Claude-style session sidebar: New session, live opencode sessions (tap to
/// open), and claude-code transcripts detected on the host. Persistent on macOS/iPad,
/// a drawer on iPhone (via NavigationSplitView).
struct SessionSidebar: View {
    @ObservedObject var model: Model
    @Environment(\.colorScheme) private var scheme
    private var palette: OculusPalette { .current(scheme) }

    private var opencodeSessions: [Discovered] {
        model.discovered.filter { $0.provider == "opencode" && $0.kind == DiscoveredKind.session }
    }
    private var claudeSessions: [Discovered] {
        model.discovered.filter { $0.provider == "claude-code" }
    }

    var body: some View {
        List {
            Button {
                model.newSession()
            } label: {
                Label("New session", systemImage: "plus")
            }
            .buttonStyle(.plain)
            .listRowBackground(Color.clear)

            if !opencodeSessions.isEmpty {
                Section("Sessions") {
                    ForEach(Array(opencodeSessions.enumerated()), id: \.offset) { _, d in
                        Button {
                            Task { await model.attach(d) }
                        } label: {
                            row(title: d.title ?? d.sessionID ?? "session",
                                subtitle: "opencode",
                                active: model.sessionID == d.sessionID)
                        }
                        .buttonStyle(.plain)
                    }
                }
            }

            if !claudeSessions.isEmpty {
                Section("claude-code (view-only)") {
                    ForEach(Array(claudeSessions.enumerated()), id: \.offset) { _, d in
                        row(title: (d.cwd as NSString?)?.lastPathComponent ?? "session",
                            subtitle: d.cwd ?? "",
                            active: false)
                        .foregroundStyle(palette.mutedForeground)
                    }
                }
            }
        }
        .navigationTitle("Oculus")
        .refreshable { await model.discover() }
        .task { await model.discover() }
        .toolbar {
            ToolbarItem {
                Button { Task { await model.discover() } } label: { Image(systemName: "arrow.clockwise") }
            }
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
