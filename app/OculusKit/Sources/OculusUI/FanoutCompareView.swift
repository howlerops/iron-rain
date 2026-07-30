import SwiftUI
import OculusKit

/// The payoff screen for a fan-out: every variant's result side by side, with one tap to keep the
/// winner.
///
/// Racing N agents is the easy half — every ADE can do it. The half that decides whether fanning out
/// actually saves time is this one: without it you open N sessions and diff them by hand, and the
/// savings evaporate. Each row is the agent's OWN account of what it did (its handoff record) plus a
/// real diffstat against the worktree's base commit, so nothing here required a second model call.
struct FanoutCompareView: View {
    @ObservedObject var model: Model
    let summary: FanoutSummary
    let palette: OculusPalette
    var onOpenSession: (String) -> Void
    var onClose: () -> Void

    @State private var keeping: String? = nil

    private var succeeded: [FanoutVariantResult] { summary.results.filter { $0.failed != true } }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            header
            Divider().overlay(palette.border)
            ScrollView {
                VStack(alignment: .leading, spacing: 10) {
                    ForEach(summary.results) { r in card(r) }
                }
                .padding(14)
            }
        }
        .frame(minWidth: 560, minHeight: 420)
        .background(palette.background)
    }

    private var header: some View {
        HStack(alignment: .top) {
            VStack(alignment: .leading, spacing: 3) {
                Text("Compare \(summary.results.count) attempts").font(.headline)
                    .foregroundStyle(palette.foreground)
                if let p = summary.prompt, !p.isEmpty {
                    Text(p).font(.caption).foregroundStyle(palette.mutedForeground).lineLimit(2)
                } else {
                    Text("Keep one — the rest are discarded along with their worktrees.")
                        .font(.caption).foregroundStyle(palette.mutedForeground)
                }
            }
            Spacer()
            Button("Done", action: onClose).keyboardShortcut(.cancelAction)
        }
        .padding(14)
    }

    private func card(_ r: FanoutVariantResult) -> some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack(spacing: 8) {
                Text("#\(r.variant + 1)")
                    .font(.system(size: 11, weight: .semibold, design: .monospaced))
                    .padding(.horizontal, 6).padding(.vertical, 2)
                    .background(palette.input).clipShape(RoundedRectangle(cornerRadius: 4))
                if let m = r.model, !m.isEmpty {
                    Text(m).font(.system(size: 11, design: .monospaced)).foregroundStyle(palette.mutedForeground)
                }
                if r.failed == true {
                    Label("failed", systemImage: "exclamationmark.triangle.fill")
                        .font(.system(size: 11)).foregroundStyle(palette.destructive)
                }
                Spacer()
                diffStat(r)
            }

            // The agent's own words about what it did.
            if let t = r.title, !t.isEmpty {
                Text(t).font(.system(size: 13, weight: .medium)).foregroundStyle(palette.foreground)
            }
            if let s = r.summary, !s.isEmpty {
                Text(s).font(.system(size: 12)).foregroundStyle(palette.mutedForeground)
                    .lineLimit(4).multilineTextAlignment(.leading)
            } else if r.title == nil || r.title?.isEmpty == true {
                Text(r.filesChanged == 0
                     ? "No changes — this agent finished without touching the tree."
                     : "No summary written; open the session to see what it did.")
                    .font(.system(size: 12)).italic().foregroundStyle(palette.mutedForeground)
            }

            HStack(spacing: 8) {
                if let d = r.durationSec, d > 0 {
                    Label(durationText(d), systemImage: "clock")
                        .font(.system(size: 10.5)).foregroundStyle(palette.mutedForeground)
                }
                if let b = r.branch, !b.isEmpty {
                    Label(b, systemImage: "arrow.branch")
                        .font(.system(size: 10.5, design: .monospaced))
                        .foregroundStyle(palette.mutedForeground).lineLimit(1)
                }
                Spacer()
                Button("Open") { onOpenSession(r.sessionID) }
                    .buttonStyle(.bordered)
                Button(keeping == r.sessionID ? "Keeping…" : "Keep this") {
                    keeping = r.sessionID
                    Task {
                        await model.resolveFanout(group: summary.group, keep: r.sessionID)
                        onClose()
                    }
                }
                .buttonStyle(.borderedProminent).tint(palette.primary)
                .disabled(keeping != nil || (r.failed == true && succeeded.isEmpty == false))
            }
        }
        .padding(12)
        .background(palette.card)
        .overlay(RoundedRectangle(cornerRadius: 12).stroke(palette.border))
        .clipShape(RoundedRectangle(cornerRadius: 12))
        .opacity(r.failed == true ? 0.72 : 1)
    }

    private func diffStat(_ r: FanoutVariantResult) -> some View {
        HStack(spacing: 6) {
            Text("\(r.filesChanged) file\(r.filesChanged == 1 ? "" : "s")")
                .font(.system(size: 11, design: .monospaced)).foregroundStyle(palette.mutedForeground)
            if r.insertions > 0 {
                Text("+\(r.insertions)").font(.system(size: 11, design: .monospaced))
                    .foregroundStyle(Color(hex: 0x3FB950))
            }
            if r.deletions > 0 {
                Text("−\(r.deletions)").font(.system(size: 11, design: .monospaced))
                    .foregroundStyle(palette.destructive)
            }
        }
    }

    private func durationText(_ secs: Int) -> String {
        if secs < 60 { return "\(secs)s" }
        if secs < 3600 { return "\(secs / 60)m \(secs % 60)s" }
        return "\(secs / 3600)h \((secs % 3600) / 60)m"
    }
}
