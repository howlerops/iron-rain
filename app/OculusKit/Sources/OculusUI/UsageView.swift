import SwiftUI
import OculusKit

/// What your agents have actually spent — today, this week, this month, and inside the current
/// rolling window.
///
/// The chat header has always carried a per-session meter, but it died with the session: close a
/// conversation and its cost was gone, and there was nowhere to see the total across everything.
/// The daemon now records every usage event a provider reports, so this is the real ledger rather
/// than a sum of whatever happens to be open.
///
/// Two honest caveats are surfaced rather than hidden. Dollar figures come from the PROVIDER — for a
/// subscription-backed agent nothing is billed per token, so the number is what the work would have
/// cost at API rates, not a bill. And the window resets relative to when usage began, not on a clock
/// boundary, which is why the countdown can look "off" against the hour.
struct UsageView: View {
    @ObservedObject var model: Model
    let palette: OculusPalette
    var onClose: (() -> Void)? = nil

    @State private var now = Date()
    private let tick = Timer.publish(every: 30, on: .main, in: .common).autoconnect()

    var body: some View {
        OculusSheet(
            title: "Usage",
            subtitle: model.usage?.subscription == true
                ? "Estimated cost of the work — subscription agents aren't billed per token."
                : "Tokens and cost across every session, live and finished.",
            palette: palette,
            onClose: onClose
        ) {
            if let r = model.usage {
                totals(r)
                window(r.window, subscription: r.subscription)
                breakdown("By provider", r.providers, icon: "shippingbox")
                breakdown("By model", r.models, icon: "cpu")
                breakdown("By session", r.sessions, icon: "bubble.left.and.bubble.right")
                if r.today.tokens == 0 && r.month.tokens == 0 {
                    SheetEmptyState(icon: "chart.bar",
                                    title: "Nothing recorded yet",
                                    message: "Usage is written as your agents work. Only turns taken since the daemon gained usage tracking appear here — earlier work isn't recoverable.",
                                    palette: palette)
                }
            } else if model.loadingUsage {
                SheetEmptyState(icon: "hourglass", title: "Reading the ledger…",
                                message: "Totalling usage across your sessions.", palette: palette)
            } else {
                SheetEmptyState(icon: "exclamationmark.triangle",
                                title: "Usage unavailable",
                                message: "The daemon didn't return a usage report. If it's running an older build, update it and try again.",
                                palette: palette) {
                    Button("Try again") { Task { await model.loadUsage() } }.buttonStyle(.bordered)
                }
            }
        }
        .task { await model.loadUsage() }
        .onReceive(tick) { now = $0 }
    }

    /// Three money cards side by side — until they can't be. The figures scale with Dynamic Type
    /// rather than staying frozen, so the row is allowed to become a column when three columns of
    /// dollars no longer fit; freezing the type instead would keep the layout and lose the reader.
    private func totals(_ r: UsageReport) -> some View {
        ViewThatFits(in: .horizontal) {
            HStack(spacing: OculusSpace.sm) {
                totalCard("Today", r.today)
                totalCard("This week", r.week)
                totalCard("This month", r.month)
            }
            VStack(spacing: OculusSpace.sm) {
                totalCard("Today", r.today)
                totalCard("This week", r.week)
                totalCard("This month", r.month)
            }
        }
    }

    private func totalCard(_ label: String, _ s: UsageSlice) -> some View {
        SheetCard(palette: palette) {
            Text(label)
                .font(.caption2.weight(.semibold))
                .foregroundStyle(palette.mutedForeground)
            Text(money(s.costUSD))
                .font(.system(.title3, design: .rounded).weight(.semibold).monospacedDigit())
                .foregroundStyle(palette.foreground)
                .lineLimit(1).minimumScaleFactor(0.6)
            Text("\(compact(s.tokens)) tokens")
                .font(.caption2.monospacedDigit())
                .foregroundStyle(palette.mutedForeground)
        }
    }

    /// The rolling window — the thing you actually want before starting a long run.
    @ViewBuilder private func window(_ w: UsageWindow, subscription: Bool) -> some View {
        SheetCard(palette: palette, tint: w.active ? palette.primary : nil) {
            HStack(alignment: .firstTextBaseline) {
                Label("Current \(w.hours)-hour window", systemImage: "clock.arrow.circlepath")
                    .font(.footnote.weight(.medium)).foregroundStyle(palette.foreground)
                Spacer()
                Text(w.active ? resetText(w) : "idle")
                    .font(.caption.weight(.medium).monospacedDigit())
                    .foregroundStyle(w.active ? palette.primary : palette.mutedForeground)
            }
            if w.active {
                HStack(spacing: OculusSpace.md) {
                    Text(money(w.costUSD))
                        .font(.subheadline.weight(.semibold).monospacedDigit())
                        .foregroundStyle(palette.foreground)
                    Text("\(compact(w.tokens)) tokens")
                        .font(.caption.monospacedDigit()).foregroundStyle(palette.mutedForeground)
                    Spacer()
                }
                Text("Started \(clock(w.startedAt)) — the window is anchored to your first activity, not the clock hour.")
                    .font(.caption).foregroundStyle(palette.mutedForeground)
                    .fixedSize(horizontal: false, vertical: true)
            } else {
                Text("No usage in the last \(w.hours) hours. A new window opens with your next turn.")
                    .font(.caption).foregroundStyle(palette.mutedForeground)
                    .fixedSize(horizontal: false, vertical: true)
            }
            if subscription {
                Text("Costs are estimates at API rates. Your subscription agents don't bill per token, and this can't see your plan's own limits.")
                    .font(.caption).foregroundStyle(palette.mutedForeground)
                    .fixedSize(horizontal: false, vertical: true)
            }
        }
    }

    @ViewBuilder private func breakdown(_ title: String, _ slices: [UsageSlice], icon: String) -> some View {
        if !slices.isEmpty {
            let top = slices.map(\.costUSD).max() ?? 0
            VStack(alignment: .leading, spacing: OculusSpace.xs) {
                Text(title).font(.caption.weight(.semibold))
                    .foregroundStyle(palette.mutedForeground)
                SheetCard(palette: palette) {
                    ForEach(slices) { s in
                        VStack(alignment: .leading, spacing: 3) {
                            HStack(spacing: OculusSpace.xs) {
                                Image(systemName: icon).font(.caption2)
                                    .foregroundStyle(palette.mutedForeground)
                                Text(s.label ?? s.key).font(.footnote)
                                    .foregroundStyle(palette.foreground)
                                    .lineLimit(2).minimumScaleFactor(0.8)
                                Spacer(minLength: OculusSpace.sm)
                                Text(money(s.costUSD))
                                    .font(.footnote.monospacedDigit())
                                    .foregroundStyle(palette.foreground)
                                Text(compact(s.tokens))
                                    .font(.caption2.monospacedDigit())
                                    .foregroundStyle(palette.mutedForeground)
                                    .frame(minWidth: 46, alignment: .trailing)
                            }
                            // Three columns — name, dollars, tokens — that only read as a table while
                            // they stay on one line. The type scales, but past AX2 there is no width
                            // left for a session name, so the row stops growing rather than collapsing.
                            .dynamicTypeSize(...DynamicTypeSize.accessibility2)
                            // A bar against the heaviest line makes the distribution readable at a
                            // glance — which agent is eating the budget, not just what each cost.
                            GeometryReader { geo in
                                OculusShape.rounded(2)
                                    .fill(palette.primary.opacity(0.35))
                                    .frame(width: top > 0 ? max(2, geo.size.width * (s.costUSD / top)) : 2)
                            }
                            .frame(height: 3)
                        }
                    }
                }
            }
        }
    }

    private func resetText(_ w: UsageWindow) -> String {
        let remaining = TimeInterval(w.resetsAt) - now.timeIntervalSince1970
        guard remaining > 0 else { return "resetting" }
        let mins = Int(remaining / 60)
        if mins < 60 { return "resets in \(mins)m" }
        return "resets in \(mins / 60)h \(mins % 60)m"
    }

    private func clock(_ ts: Int) -> String {
        let f = DateFormatter(); f.timeStyle = .short; f.dateStyle = .none
        return f.string(from: Date(timeIntervalSince1970: TimeInterval(ts)))
    }

    /// Sub-cent totals still deserve a number — "$0.00" reads as "nothing happened".
    private func money(_ v: Double) -> String {
        if v == 0 { return "$0" }
        if v < 0.01 { return String(format: "$%.4f", v) }
        if v < 10 { return String(format: "$%.2f", v) }
        return String(format: "$%.0f", v)
    }

    private func compact(_ n: Int) -> String {
        if n >= 1_000_000 { return String(format: "%.1fM", Double(n) / 1_000_000) }
        if n >= 1_000 { return String(format: "%.0fk", Double(n) / 1_000) }
        return "\(n)"
    }
}
