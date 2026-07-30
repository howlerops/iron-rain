import SwiftUI
import OculusKit

/// The Activity destination: a persistent, cross-session feed answering "what did my agents just do,
/// and which need me?" — the mental model is an inbox, sorted needs-you first. Each row carries the
/// standard status chip, a timestamp, the owning session, and a tap that jumps straight to that
/// session (a needs-you item lands you in the reply box). Backed by the daemon's ActivityStore, so
/// this same data feeds the Needs-You nav badge, the iOS tab badge, and the bottom ticker.
struct ActivityView: View {
    @ObservedObject var model: Model
    let palette: OculusPalette
    /// Opens the owning session (jump-to-turn). Provided by the host so macOS and iOS route differently.
    var onOpen: (String) -> Void

    private var needsYou: [ActivityEvent] { model.activityFeed.filter { $0.needsYou && !$0.read } }
    private var rest: [ActivityEvent] { model.activityFeed.filter { !($0.needsYou && !$0.read) } }

    /// "Needs you" items pointing at a session that no longer exists — stale errors from deleted /
    /// husk sessions. They're un-actionable (tapping opens nothing), so they're auto-dismissed on
    /// appear rather than sitting in the count forever.
    private var orphanedNeedsYou: [ActivityEvent] {
        let live = Set(model.sessions.map { $0.id })
        return needsYou.filter { e in
            guard let sid = e.sessionID else { return false }
            return !live.contains(sid)
        }
    }

    var body: some View {
        ScrollView {
            LazyVStack(alignment: .leading, spacing: 0) {
                if model.activityFeed.isEmpty {
                    emptyState
                } else {
                    if !needsYou.isEmpty {
                        HStack {
                            sectionHeader("Needs you", count: needsYou.count, tint: Color(hex: 0xE0912A))
                            Button("Clear") { Task { await model.markActivityRead(needsYou.map(\.id)) } }
                                .font(.system(size: 11)).buttonStyle(.plain)
                                .foregroundStyle(palette.mutedForeground).padding(.trailing, 16)
                        }
                        ForEach(needsYou) { row($0) }
                        Divider().overlay(palette.border).padding(.vertical, 6)
                    }
                    if !rest.isEmpty {
                        sectionHeader("Recent", count: nil, tint: palette.mutedForeground)
                        ForEach(rest) { row($0) }
                    }
                }
            }
            .padding(.vertical, 8)
        }
        .background(palette.background)
        .task(id: orphanedNeedsYou.map(\.id)) {
            let ids = orphanedNeedsYou.map(\.id)
            if !ids.isEmpty { await model.markActivityRead(ids) }
        }
        .toolbar {
            if !needsYou.isEmpty {
                ToolbarItem(placement: .primaryAction) {
                    Button("Mark all read") { Task { await model.markActivityRead() } }
                        .font(.callout)
                }
            }
        }
    }

    private func sectionHeader(_ title: String, count: Int?, tint: Color) -> some View {
        HStack(spacing: 6) {
            Text(title.uppercased())
                .font(.system(size: 10.5, weight: .semibold)).tracking(1.0)
            if let count { Text("\(count)").font(.system(size: 10.5, weight: .semibold, design: .monospaced)) }
            Spacer()
        }
        .foregroundStyle(tint)
        .padding(.horizontal, 16).padding(.top, 10).padding(.bottom, 5)
    }

    private func row(_ e: ActivityEvent) -> some View {
        Button {
            if let sid = e.sessionID { onOpen(sid) }
            if e.needsYou, !e.read { Task { await model.markActivityRead([e.id]) } }
        } label: {
            HStack(alignment: .top, spacing: 10) {
                StatusChip(state(e), palette: palette, showLabel: false)
                    .padding(.top, 1)
                VStack(alignment: .leading, spacing: 2) {
                    Text(e.title)
                        .font(.system(size: 13, weight: e.needsYou && !e.read ? .semibold : .regular))
                        .foregroundStyle(palette.foreground)
                        .lineLimit(2).multilineTextAlignment(.leading)
                    HStack(spacing: 6) {
                        if let p = e.project, !p.isEmpty {
                            Text(projectName(p)).font(.system(size: 10.5, design: .monospaced))
                        }
                        if let d = e.detail, !d.isEmpty {
                            Text("· \(d)").font(.system(size: 10.5)).lineLimit(1)
                        }
                        Text("· \(relTime(e.ts))").font(.system(size: 10.5, design: .monospaced))
                    }
                    .foregroundStyle(palette.mutedForeground)
                }
                Spacer(minLength: 4)
                if e.sessionID != nil {
                    Image(systemName: "chevron.right").font(.system(size: 10)).foregroundStyle(palette.mutedForeground.opacity(0.6))
                }
            }
            .padding(.horizontal, 16).padding(.vertical, 9)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
    }

    private var emptyState: some View {
        VStack(spacing: 10) {
            Image(systemName: "waveform.path.ecg").font(.system(size: 30)).foregroundStyle(palette.mutedForeground.opacity(0.5))
            Text("No activity yet").font(.headline).foregroundStyle(palette.foreground)
            Text("Finished turns, questions from your agents, errors, and loop runs show up here — newest first, needs-you at the top.")
                .font(.callout).foregroundStyle(palette.mutedForeground)
                .multilineTextAlignment(.center).frame(maxWidth: 340)
        }
        .frame(maxWidth: .infinity).padding(.top, 60).padding(.horizontal, 24)
    }

    private func state(_ e: ActivityEvent) -> AgentState {
        switch e.kind {
        case "needs_input": return .needsYou
        case "error":       return .failed
        case "loop_run", "loop_pr": return .loop
        case "finished":    return .idle
        default:            return .idle
        }
    }
    private func projectName(_ path: String) -> String { (path as NSString).lastPathComponent }
    private func relTime(_ ts: Int) -> String {
        let secs = Int(Date().timeIntervalSince1970) - ts
        if secs < 60 { return "\(max(secs, 0))s" }
        if secs < 3600 { return "\(secs / 60)m" }
        if secs < 86400 { return "\(secs / 3600)h" }
        return "\(secs / 86400)d"
    }
}
