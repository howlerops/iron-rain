import SwiftUI
import OculusKit

/// The agent fleet: a glanceable grid of every live session with its supervision state, cost, and
/// to-do progress — the "Mission Control" for running many agents at once. Tap a card to open it.
struct FleetView: View {
    @ObservedObject var model: Model
    let palette: OculusPalette
    let onOpen: (String) -> Void
    let onClose: () -> Void
    var onFanout: (() -> Void)? = nil // show a "Fan out" button (destination usage)

    private let columns = [GridItem(.adaptive(minimum: 220, maximum: 320), spacing: 12)]

    private var sessions: [Session] {
        // Running first, then most-recently active.
        model.sessions.sorted { a, b in
            let ra = a.status == SessionStatusValue.running, rb = b.status == SessionStatusValue.running
            if ra != rb { return ra }
            return (a.updatedAt ?? 0) > (b.updatedAt ?? 0)
        }
    }
    private var totalCost: Double { model.sessions.reduce(0) { $0 + ($1.costUSD ?? 0) } }
    private var runningCount: Int { model.sessions.filter { $0.status == SessionStatusValue.running }.count }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack {
                VStack(alignment: .leading, spacing: 2) {
                    Text("Agent fleet").font(.headline)
                    Text("\(model.sessions.count) sessions · \(runningCount) running" + (totalCost > 0 ? String(format: " · $%.3f total", totalCost) : ""))
                        .font(.caption2).foregroundStyle(palette.mutedForeground)
                }
                Spacer()
                if let onFanout {
                    Button(action: onFanout) { Label("Fan out", systemImage: "square.grid.2x2") }
                        .help("Race one task across several agents, then merge the winner")
                } else {
                    Button("Done", action: onClose).keyboardShortcut(.cancelAction)
                }
            }
            .padding(.horizontal, 16).padding(.vertical, 12)
            Divider()
            if sessions.isEmpty {
                VStack(spacing: 8) {
                    Image(systemName: "square.grid.2x2").font(.largeTitle).foregroundStyle(palette.mutedForeground)
                    Text("No active sessions").foregroundStyle(palette.mutedForeground)
                }
                .frame(maxWidth: .infinity, maxHeight: .infinity)
            } else {
                ScrollView {
                    LazyVGrid(columns: columns, spacing: 12) {
                        ForEach(sessions) { s in
                            FleetCard(session: s, hb: model.heartbeats[s.id], palette: palette) { onOpen(s.id) }
                        }
                    }
                    .padding(16)
                }
            }
        }
        .frame(minWidth: 520, minHeight: 440)
        .background(palette.background)
    }
}

private struct FleetCard: View {
    let session: Session
    let hb: SessionHeartbeat?
    let palette: OculusPalette
    let onOpen: () -> Void

    private var title: String {
        session.subtask ?? session.name ?? session.workspaceName ?? "session \(session.id.prefix(6))"
    }
    private var isRunning: Bool { session.status == SessionStatusValue.running }

    var body: some View {
        Button(action: onOpen) {
            VStack(alignment: .leading, spacing: 8) {
                HStack(spacing: 6) {
                    Circle().fill(dotColor).frame(width: 8, height: 8)
                    Text(title).font(.subheadline.bold()).lineLimit(1)
                    Spacer()
                    if isRunning {
                        Text("live").font(.caption2.bold()).foregroundStyle(palette.primary)
                    }
                }
                HStack(spacing: 6) {
                    Text(session.provider).font(.caption2)
                        .padding(.horizontal, 6).padding(.vertical, 2)
                        .background(palette.secondary.opacity(0.6), in: Capsule())
                    if let b = session.branch, !b.isEmpty {
                        Label(b, systemImage: "arrow.triangle.branch").font(.caption2).lineLimit(1)
                            .foregroundStyle(palette.mutedForeground)
                    }
                    if session.isWorkspace == true {
                        Label("workspace", systemImage: "square.stack.3d.up").font(.caption2)
                            .foregroundStyle(palette.mutedForeground)
                    }
                }
                if let hb, hb.todosTotal > 0 {
                    ProgressView(value: Double(hb.todosDone), total: Double(hb.todosTotal))
                        .tint(palette.primary)
                    Text("\(stateLabel) · \(hb.todosDone)/\(hb.todosTotal) done")
                        .font(.caption2).foregroundStyle(palette.mutedForeground)
                } else if hb != nil {
                    Text(stateLabel).font(.caption2).foregroundStyle(palette.mutedForeground)
                }
                HStack {
                    if let c = session.costUSD, c > 0 {
                        Text(String(format: "$%.3f", c)).font(.caption2.monospacedDigit())
                            .foregroundStyle(palette.mutedForeground)
                    }
                    Spacer()
                    if let hb, hb.budgetUSD > 0 {
                        Text(String(format: "budget $%.2f", hb.budgetUSD)).font(.caption2)
                            .foregroundStyle(palette.mutedForeground)
                    }
                }
            }
            .padding(12)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(palette.secondary.opacity(0.35), in: RoundedRectangle(cornerRadius: 12))
            .overlay(RoundedRectangle(cornerRadius: 12).strokeBorder(palette.border))
        }
        .buttonStyle(.plain)
    }

    private var dotColor: Color {
        switch hb?.state {
        case "working", "idle_incomplete": return .green
        case "awaiting_input": return .yellow
        case "stalled", "errored", "exhausted": return .orange
        case "done": return palette.mutedForeground
        default: return isRunning ? .green : palette.mutedForeground
        }
    }
    private var stateLabel: String {
        switch hb?.state {
        case "working": return "On track"
        case "idle_incomplete": return "Nudging"
        case "awaiting_input": return "Needs you"
        case "stalled": return "Stalled"
        case "exhausted": return "Budget used"
        case "errored": return "Error"
        case "done": return "Done"
        default: return isRunning ? "Running" : "Idle"
        }
    }
}
