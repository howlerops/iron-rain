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
    @State private var combining = false

    /// The comparison as it stands NOW, not as it was when this sheet opened.
    ///
    /// `summary` is a value captured at presentation time, which was fine while a group's results
    /// were final the moment they appeared. Combining changes that: it adds an attempt to a group
    /// whose comparison is already on screen, and the daemon rebroadcasts the summary when it lands.
    /// Reading the frozen copy would leave the user watching a screen that can never show the thing
    /// they just asked for.
    private var live: FanoutSummary {
        if let s = model.fanoutSummary, s.group == summary.group { return s }
        return summary
    }

    private var succeeded: [FanoutVariantResult] { live.results.filter { $0.failed != true } }
    /// Ordered so the combined attempt leads: it read the others, so it is the first alternative
    /// worth considering. Sorting it by diffstat would place it wherever the merge happened to land.
    private var ordered: [FanoutVariantResult] {
        live.results.sorted { a, b in (a.isSynthesis == true ? 0 : 1) < (b.isSynthesis == true ? 0 : 1) }
    }

    var body: some View {
        OculusSheet(
            title: "Compare \(live.results.count) attempts",
            subtitle: (live.prompt?.isEmpty == false)
                ? live.prompt
                : "Keep one — the rest are discarded along with their worktrees.",
            palette: palette,
            onClose: onClose
        ) {
            VStack(spacing: OculusSpace.sm) {
                ForEach(ordered) { r in card(r) }
                combineFooter
            }
        }
    }

    /// The combine affordance acts on the SET, so it sits below the cards rather than inside one —
    /// every control in a card is about that attempt alone. Quieter than the variant cards on
    /// purpose: this is an escape hatch for "none of these is quite right", not a fourth attempt.
    @ViewBuilder private var combineFooter: some View {
        // Mirror the daemon's own `len(peers) < 2` guard so the button can't produce a server error.
        if succeeded.count >= 2 && !succeeded.contains(where: { $0.isSynthesis == true }) {
            SheetCard(palette: palette) {
                HStack(alignment: .top, spacing: OculusSpace.sm) {
                    Image(systemName: "arrow.triangle.merge").foregroundStyle(palette.mutedForeground)
                    VStack(alignment: .leading, spacing: OculusSpace.xxs) {
                        Text(combining ? "Combining…" : "Not sure? Combine the best of these.")
                            .font(.footnote.weight(.medium)).foregroundStyle(palette.foreground)
                        // The time cost is always-visible text, not a tooltip: it spends a full
                        // agent turn, and iOS has no hover to discover a `.help()` in.
                        Text(combining
                             ? "A fresh agent is reading all \(succeeded.count) attempts. It'll appear above as another attempt when it's done."
                             : "A fresh agent reads all \(succeeded.count) attempts and writes one merged version as a new attempt — a few minutes, and a full turn. The others aren't touched.")
                            .font(.caption).foregroundStyle(palette.mutedForeground)
                            .fixedSize(horizontal: false, vertical: true)
                    }
                    Spacer(minLength: OculusSpace.sm)
                    if combining {
                        ProgressView().controlSize(.small)
                    } else {
                        Button("Combine") {
                            combining = true
                            Task {
                                if await model.synthesizeFanout(group: live.group) == false { combining = false }
                            }
                        }
                        .buttonStyle(.bordered).controlSize(.small)
                        .disabled(keeping != nil)
                    }
                }
            }
        }
    }

    private func card(_ r: FanoutVariantResult) -> some View {
        SheetCard(palette: palette) {
            HStack(spacing: 8) {
                Text("#\(r.variant + 1)")
                    .font(.system(.caption, design: .monospaced).weight(.semibold))
                    .padding(.horizontal, 6).padding(.vertical, 2)
                    .background(palette.input).clipShape(OculusShape.rounded(4))
                if let m = r.model, !m.isEmpty {
                    Text(m).font(.system(.caption, design: .monospaced)).foregroundStyle(palette.mutedForeground)
                }
                if r.isSynthesis == true {
                    // Named, not merely tinted: a colour alone competes with the failed treatment
                    // and the model badge already in this row, and this is the one fact that
                    // changes how you read the diff below it.
                    Label("combined", systemImage: "arrow.triangle.merge")
                        .font(.system(.caption, design: .monospaced).weight(.semibold))
                        .padding(.horizontal, 6).padding(.vertical, 2)
                        .background(palette.accent).foregroundStyle(palette.accentForeground)
                        .clipShape(OculusShape.rounded(4))
                }
                if r.failed == true {
                    Label("failed", systemImage: "exclamationmark.triangle.fill")
                        .font(.caption).foregroundStyle(palette.destructive)
                }
                Spacer()
                diffStat(r)
            }

            // WHICH attempts it read. A combination of 2 of 6 is a different object from one of all
            // 6, and the diffstat above can't tell them apart.
            if r.isSynthesis == true, let src = r.sourceVariants, !src.isEmpty {
                Text("Combined #" + src.map(String.init).joined(separator: ", #"))
                    .font(.caption).foregroundStyle(palette.mutedForeground)
            }

            // The agent's own words about what it did.
            if let t = r.title, !t.isEmpty {
                Text(t).font(.footnote.weight(.medium)).foregroundStyle(palette.foreground)
            }
            if let s = r.summary, !s.isEmpty {
                Text(s).font(.footnote).foregroundStyle(palette.mutedForeground)
                    .lineLimit(4).multilineTextAlignment(.leading)
            } else if r.title == nil || r.title?.isEmpty == true {
                Text(r.filesChanged == 0
                     ? "No changes — this agent finished without touching the tree."
                     : "No summary written; open the session to see what it did.")
                    .font(.footnote).italic().foregroundStyle(palette.mutedForeground)
            }

            // Metadata and the two actions share a line only while both fit; the choice between
            // variants is the whole point of this screen, so the buttons get the width first.
            ViewThatFits(in: .horizontal) {
                HStack(spacing: 8) { meta(r); Spacer(); actions(r) }
                VStack(alignment: .leading, spacing: OculusSpace.xs) {
                    HStack(spacing: 8) { meta(r); Spacer() }
                    HStack(spacing: 8) { Spacer(); actions(r) }
                }
            }
        }
        .opacity(r.failed == true ? 0.72 : 1)
    }

    @ViewBuilder private func meta(_ r: FanoutVariantResult) -> some View {
        if let d = r.durationSec, d > 0 {
            Label(durationText(d), systemImage: "clock")
                .font(.caption).foregroundStyle(palette.mutedForeground)
        }
        if let b = r.branch, !b.isEmpty {
            Label(b, systemImage: "arrow.branch")
                .font(.system(.caption, design: .monospaced))
                .foregroundStyle(palette.mutedForeground)
                .lineLimit(1).minimumScaleFactor(0.8)
        }
    }

    @ViewBuilder private func actions(_ r: FanoutVariantResult) -> some View {
        Button("Open") { onOpenSession(r.sessionID) }
            .buttonStyle(.bordered).controlSize(.small)
        Button(keeping == r.sessionID ? "Keeping…" : "Keep this") {
            keeping = r.sessionID
            Task {
                await model.resolveFanout(group: summary.group, keep: r.sessionID)
                onClose()
            }
        }
        .buttonStyle(.borderedProminent).tint(palette.primary).controlSize(.small)
        .disabled(keeping != nil || (r.failed == true && succeeded.isEmpty == false))
    }

    private func diffStat(_ r: FanoutVariantResult) -> some View {
        HStack(spacing: 6) {
            Text("\(r.filesChanged) file\(r.filesChanged == 1 ? "" : "s")")
                .font(.system(.caption, design: .monospaced)).foregroundStyle(palette.mutedForeground)
            if r.insertions > 0 {
                Text("+\(r.insertions)").font(.system(.caption, design: .monospaced))
                    .foregroundStyle(palette.diffAdded)
            }
            if r.deletions > 0 {
                // diffRemoved, not destructive: a deletion in a diffstat is a fact about the change,
                // not a dangerous action.
                Text("−\(r.deletions)").font(.system(.caption, design: .monospaced))
                    .foregroundStyle(palette.diffRemoved)
            }
        }
        // The diffstat trails the variant number on one line; it is a glanceable stat, not prose,
        // and past AX2 it would push the variant badge off the card.
        .dynamicTypeSize(...DynamicTypeSize.accessibility2)
        .accessibilityElement(children: .combine)
        .accessibilityLabel("\(r.filesChanged) files changed, \(r.insertions) added, \(r.deletions) removed")
    }

    private func durationText(_ secs: Int) -> String {
        if secs < 60 { return "\(secs)s" }
        if secs < 3600 { return "\(secs / 60)m \(secs % 60)s" }
        return "\(secs / 3600)h \((secs % 3600) / 60)m"
    }
}
