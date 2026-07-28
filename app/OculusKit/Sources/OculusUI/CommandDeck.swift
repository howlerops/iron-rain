import SwiftUI
import OculusKit

/// The five peer destinations of the Command Deck IA. Everything the app can do is reached FROM one
/// of these — nothing hides in a "⋯" menu or a modal sheet. macOS renders them as a persistent nav
/// rail column; iOS renders them as a bottom tab bar (defaulting to Activity — the phone is a triage
/// inbox). Order is deliberate: authoring on the left, monitoring on the right, Activity centered on
/// iOS so the needs-you inbox is the thumb-reachable default.
public enum Destination: Int, CaseIterable, Identifiable {
    case sessions, loops, fleet, issues, activity
    public var id: Int { rawValue }

    var title: String {
        switch self {
        case .sessions: return "Sessions"
        case .loops:    return "Loops"
        case .fleet:    return "Fleet"
        case .issues:   return "Issues"
        case .activity: return "Activity"
        }
    }

    /// SF Symbol for the rail/tab. Chosen to read at a glance and stay distinct from one another.
    var symbol: String {
        switch self {
        case .sessions: return "bubble.left.and.bubble.right"
        case .loops:    return "arrow.trianglehead.2.clockwise.rotate.90"
        case .fleet:    return "square.grid.2x2"
        case .issues:   return "checklist"
        case .activity: return "waveform.path.ecg"
        }
    }

    /// iOS tab order puts Activity in the center (index 2) as the default launch tab.
    static var mobileOrder: [Destination] { [.sessions, .loops, .activity, .fleet, .issues] }
}

// MARK: - Unified status vocabulary

/// The ONE status vocabulary used identically on every session row, fleet tile, and activity item —
/// so state reads the same everywhere. `.failed` is special: its chip is the Reconnect control.
public enum AgentState {
    case running, needsYou, failed, idle, conflict, loop, viewOnly

    var color: (OculusPalette) -> Color {
        switch self {
        case .running:  return { $0.primary }
        case .needsYou: return { _ in Color(hex: 0xE0912A) }
        case .failed:   return { $0.destructive }
        case .conflict: return { _ in Color(hex: 0xA071D6) }
        case .idle, .loop, .viewOnly: return { $0.mutedForeground }
        }
    }
    var glyph: String {
        switch self {
        case .running:  return "circle.fill"
        case .needsYou: return "exclamationmark.triangle.fill"
        case .failed:   return "xmark.octagon.fill"
        case .idle:     return "circle"
        case .conflict: return "arrow.triangle.merge"
        case .loop:     return "arrow.trianglehead.2.clockwise.rotate.90"
        case .viewOnly: return "terminal"
        }
    }
    var label: String {
        switch self {
        case .running:  return "Running"
        case .needsYou: return "Needs you"
        case .failed:   return "Reconnect"
        case .idle:     return "Idle"
        case .conflict: return "Conflict"
        case .loop:     return "Loop run"
        case .viewOnly: return "Terminal"
        }
    }

    /// Derive the display state for a session from its status + the Model's error map.
    @MainActor static func of(_ session: Session, model: Model) -> AgentState {
        if model.sessionErrors[session.id] != nil { return .failed }
        switch session.status {
        case SessionStatusValue.running: return .running
        case SessionStatusValue.awaitingApproval: return .needsYou
        case SessionStatusValue.error, "errored": return .failed
        case SessionStatusValue.stopped: return .idle
        default: return .idle
        }
    }
}

/// The persistent destination rail that sits atop the sidebar (macOS) — five named, badged peer
/// destinations plus a pinned Needs-You inbox row. This is the whole point of the redesign: Loops,
/// Fleet, and Activity are first-glance destinations with live counts, not items hidden in a "⋯"
/// menu or a modal sheet.
struct DestinationRail: View {
    @Binding var destination: Destination
    @ObservedObject var model: Model
    let palette: OculusPalette

    var body: some View {
        VStack(spacing: 2) {
            // Pinned Needs-You inbox: jumps to Activity, filtered to what needs you.
            if model.needsYouCount > 0 {
                Button {
                    destination = .activity
                } label: {
                    HStack(spacing: 8) {
                        Text("NEEDS YOU").font(.system(size: 10.5, weight: .semibold)).tracking(0.8)
                        Spacer()
                        Text("\(model.needsYouCount)")
                            .font(.system(size: 10.5, weight: .bold, design: .monospaced))
                            .foregroundStyle(palette.background)
                            .padding(.horizontal, 5).padding(.vertical, 1)
                            .background(Capsule().fill(Color(hex: 0xE0912A)))
                    }
                    .foregroundStyle(Color(hex: 0xE0912A))
                    .padding(.horizontal, 10).padding(.vertical, 7)
                    .background(RoundedRectangle(cornerRadius: 8).fill(Color(hex: 0xE0912A).opacity(0.12))
                        .overlay(RoundedRectangle(cornerRadius: 8).stroke(Color(hex: 0xE0912A).opacity(0.32))))
                }
                .buttonStyle(.plain)
                .padding(.bottom, 4)
            }
            ForEach(Destination.allCases) { d in
                railRow(d)
            }
        }
        .padding(.horizontal, 8).padding(.top, 8).padding(.bottom, 6)
    }

    @ViewBuilder private func railRow(_ d: Destination) -> some View {
        let active = destination == d
        Button { destination = d } label: {
            HStack(spacing: 9) {
                Image(systemName: d.symbol).font(.system(size: 14)).frame(width: 18)
                Text(d.title).font(.system(size: 13, weight: active ? .semibold : .medium))
                Spacer(minLength: 4)
                if let badge = count(d) {
                    Text("\(badge)")
                        .font(.system(size: 10.5, design: .monospaced))
                        .foregroundStyle(d == .activity && model.needsYouCount > 0 ? Color(hex: 0xE0912A) : palette.mutedForeground)
                }
            }
            .foregroundStyle(active ? palette.foreground : palette.mutedForeground)
            .padding(.horizontal, 10).padding(.vertical, 7)
            .background(RoundedRectangle(cornerRadius: 8).fill(active ? palette.primary.opacity(0.14) : .clear))
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
    }

    /// Live count badge per destination — the "no hunting" signal.
    private func count(_ d: Destination) -> Int? {
        switch d {
        case .sessions: let n = model.sessions.count; return n > 0 ? n : nil
        case .loops:    let n = model.loops.count; return n > 0 ? n : nil
        case .fleet:    return nil
        case .issues:   let n = model.issues.count; return n > 0 ? n : nil
        case .activity: let n = model.needsYouCount; return n > 0 ? n : nil
        }
    }
}

/// A collapsible strip pinned at the top of the chat that keeps WHOLE-FLEET awareness while you work
/// on one session — the "can't watch the fleet while chatting" fix. Shows the other running /
/// needs-you sessions as compact tappable chips; tapping switches to that session in place. Hidden
/// when there's nothing else active, so a single-agent user never sees it.
struct FleetStrip: View {
    @ObservedObject var model: Model
    let palette: OculusPalette
    @State private var collapsed = false

    /// The other sessions worth surfacing (running OR needs-you), excluding the active one. Pure +
    /// deterministic so it can be unit-tested headlessly.
    static func others(sessions: [Session], activeID: String?, errored: Set<String>) -> [Session] {
        sessions.filter { s in
            s.id != activeID && (s.status == SessionStatusValue.running
                                 || s.status == SessionStatusValue.awaitingApproval
                                 || errored.contains(s.id))
        }
    }

    private var others: [Session] {
        Self.others(sessions: model.sessions, activeID: model.sessionID,
                    errored: Set(model.sessionErrors.keys))
    }

    var body: some View {
        if !others.isEmpty {
            VStack(spacing: 0) {
                HStack(spacing: 8) {
                    Button { collapsed.toggle() } label: {
                        HStack(spacing: 5) {
                            Image(systemName: collapsed ? "chevron.right" : "chevron.down").font(.system(size: 8, weight: .bold))
                            Image(systemName: "square.grid.2x2").font(.system(size: 10))
                            Text("Fleet").font(.system(size: 11, weight: .semibold))
                            Text("\(others.count)").font(.system(size: 10, design: .monospaced)).foregroundStyle(palette.mutedForeground)
                        }.foregroundStyle(palette.foreground)
                    }.buttonStyle(.plain)
                    if !collapsed {
                        ScrollView(.horizontal, showsIndicators: false) {
                            HStack(spacing: 6) {
                                ForEach(others, id: \.id) { s in
                                    Button { Task { await model.openSession(s.id) } } label: { chip(s) }
                                        .buttonStyle(.plain)
                                }
                            }
                        }
                    }
                    Spacer(minLength: 0)
                }
                .padding(.horizontal, 12).padding(.vertical, 6)
                Divider().overlay(palette.border)
            }
            .background(palette.card.opacity(0.5))
        }
    }

    private func chip(_ s: Session) -> some View {
        let st = AgentState.of(s, model: model)
        return HStack(spacing: 5) {
            StatusChip(st, palette: palette, showLabel: false, compact: true)
            Text(s.name ?? s.title ?? String(s.id.prefix(6)))
                .font(.system(size: 11)).foregroundStyle(palette.foreground).lineLimit(1)
        }
        .padding(.horizontal, 8).padding(.vertical, 3)
        .background(RoundedRectangle(cornerRadius: 6).fill(palette.secondary.opacity(0.6))
            .overlay(RoundedRectangle(cornerRadius: 6).stroke(st.color(palette).opacity(0.3))))
    }
}

/// A friendly placeholder for a destination whose primary surface lives in the other column.
struct DestinationHint: View {
    let palette: OculusPalette
    let symbol: String
    let title: String
    let message: String
    var body: some View {
        VStack(spacing: 12) {
            Image(systemName: symbol).font(.system(size: 32)).foregroundStyle(palette.mutedForeground.opacity(0.5))
            Text(title).font(.title3.weight(.semibold)).foregroundStyle(palette.foreground)
            Text(message).font(.callout).foregroundStyle(palette.mutedForeground)
                .multilineTextAlignment(.center).frame(maxWidth: 360)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .padding(24)
        .background(palette.background)
    }
}

/// The Loops LIST column: every loop as a compact row with its cadence, next-run hint, and a live
/// status chip — plus a prominent New button. Loops are now a first-glance destination, not a modal.
struct LoopsListColumn: View {
    @ObservedObject var model: Model
    let palette: OculusPalette
    @Binding var selected: String?
    var onOpen: (String) -> Void
    var onNew: () -> Void

    var body: some View {
        VStack(spacing: 0) {
            HStack {
                Text("Loops").font(.system(size: 13, weight: .semibold))
                Spacer()
                Button(action: onNew) { Label("New", systemImage: "plus") .font(.system(size: 12, weight: .medium)) }
                    .buttonStyle(.plain).foregroundStyle(palette.primary)
            }
            .padding(.horizontal, 14).padding(.vertical, 10)
            Divider().overlay(palette.border)
            ScrollView {
                LazyVStack(spacing: 2) {
                    if model.loops.isEmpty {
                        VStack(spacing: 8) {
                            Text("No loops yet").font(.subheadline.weight(.medium)).foregroundStyle(palette.foreground)
                            Text("A loop watches a tracker (or runs on a schedule) and hands new work to an agent — hands-free.")
                                .font(.caption).foregroundStyle(palette.mutedForeground).multilineTextAlignment(.center)
                            Button(action: onNew) { Text("Create a loop") .font(.caption.weight(.semibold)) }
                                .buttonStyle(.borderedProminent).tint(palette.primary).controlSize(.small)
                        }
                        .padding(.horizontal, 18).padding(.top, 34)
                    } else {
                        ForEach(model.loops) { loop in
                            Button { onOpen(loop.id) } label: { row(loop) }
                                .buttonStyle(.plain)
                        }
                    }
                }
                .padding(.vertical, 6).padding(.horizontal, 8)
            }
        }
        .background(palette.background)
    }

    private func row(_ loop: Loop) -> some View {
        let running = model.loopRuns.contains { $0.loopID == loop.id && ($0.status == "running" || $0.status.isEmpty) }
        return HStack(spacing: 9) {
            Image(systemName: "arrow.trianglehead.2.clockwise.rotate.90")
                .font(.system(size: 12)).foregroundStyle(loop.enabled ? palette.primary : palette.mutedForeground)
                .frame(width: 16)
            VStack(alignment: .leading, spacing: 2) {
                Text(loop.name).font(.system(size: 13, weight: .medium)).foregroundStyle(palette.foreground).lineLimit(1)
                Text(subtitle(loop)).font(.system(size: 10.5, design: .monospaced)).foregroundStyle(palette.mutedForeground).lineLimit(1)
            }
            Spacer(minLength: 4)
            if running {
                StatusChip(.running, palette: palette, showLabel: false, compact: true)
            } else if !loop.enabled {
                Text("paused").font(.system(size: 10)).foregroundStyle(palette.mutedForeground)
            }
        }
        .padding(.horizontal, 8).padding(.vertical, 7)
        .background(RoundedRectangle(cornerRadius: 7).fill(selected == loop.id ? palette.secondary : .clear))
        .contentShape(Rectangle())
    }

    private func subtitle(_ loop: Loop) -> String {
        var bits: [String] = []
        if loop.intervalMinutes > 0 {
            let m = loop.intervalMinutes
            bits.append(m % 60 == 0 ? "every \(m/60)h" : "every \(m)m")
        }
        if !loop.provider.isEmpty { bits.append(loop.provider) }
        return bits.isEmpty ? "recurring" : bits.joined(separator: " · ")
    }
}

/// The Loops DETAIL column: the inline editor when creating/editing a loop (its empty state is the
/// template gallery), otherwise an overview of the selected loop's recent runs — no more two stacked
/// modals to reach loop templates.
struct LoopDetail: View {
    @ObservedObject var model: Model
    let palette: OculusPalette
    let loopID: String?
    let editing: Bool
    var onOpenSession: (String) -> Void
    var onDone: () -> Void

    private var loop: Loop? { model.loops.first { $0.id == loopID } }

    var body: some View {
        Group {
            if editing {
                LoopEditor(model: model, palette: palette, loop: loop, onDone: onDone)
            } else if let loop {
                runsOverview(loop)
            } else {
                DestinationHint(palette: palette, symbol: "arrow.trianglehead.2.clockwise.rotate.90", title: "Loops",
                                message: "Pick a loop on the left to see its recent runs, or create one to automate recurring ticket→PR work.")
            }
        }
        .background(palette.background)
    }

    private func runsOverview(_ loop: Loop) -> some View {
        let runs = model.loopRuns.filter { $0.loopID == loop.id }
        return ScrollView {
            VStack(alignment: .leading, spacing: 14) {
                HStack {
                    VStack(alignment: .leading, spacing: 3) {
                        Text(loop.name).font(.title2.weight(.semibold))
                        Text(loop.enabled ? "Active" : "Paused").font(.caption)
                            .foregroundStyle(loop.enabled ? palette.primary : palette.mutedForeground)
                    }
                    Spacer()
                    Button("Edit") { onDone(); }.buttonStyle(.bordered)
                }
                Text("Recent runs").font(.system(size: 11, weight: .semibold)).tracking(0.6)
                    .foregroundStyle(palette.mutedForeground)
                if runs.isEmpty {
                    Text("No runs yet — this loop will start an agent when its next trigger fires.")
                        .font(.callout).foregroundStyle(palette.mutedForeground)
                } else {
                    ForEach(runs) { run in
                        Button { onOpenSession(run.sessionID) } label: {
                            HStack(spacing: 9) {
                                StatusChip(run.status == "running" || run.status.isEmpty ? .running : .idle,
                                           palette: palette, showLabel: false)
                                Text(run.issueKey.isEmpty ? run.sessionID : run.issueKey).font(.system(size: 13, design: .monospaced))
                                    .foregroundStyle(palette.foreground)
                                Spacer()
                                Image(systemName: "chevron.right").font(.system(size: 10)).foregroundStyle(palette.mutedForeground)
                            }
                            .padding(.horizontal, 12).padding(.vertical, 9)
                            .background(RoundedRectangle(cornerRadius: 8).fill(palette.secondary.opacity(0.4)))
                            .contentShape(Rectangle())
                        }
                        .buttonStyle(.plain)
                    }
                }
            }
            .padding(20)
        }
    }
}

/// The single status chip used across the app. `filled` renders a bold pill (running/needs-you);
/// otherwise a quiet outline. When `label` is false it's just the dot+glyph (dense rows).
public struct StatusChip: View {
    let state: AgentState
    let palette: OculusPalette
    var showLabel: Bool = true
    var compact: Bool = false

    public init(_ state: AgentState, palette: OculusPalette, showLabel: Bool = true, compact: Bool = false) {
        self.state = state; self.palette = palette; self.showLabel = showLabel; self.compact = compact
    }

    public var body: some View {
        let tint = state.color(palette)
        HStack(spacing: 4) {
            if state == .running {
                Circle().fill(tint).frame(width: 6, height: 6)
            } else {
                Image(systemName: state.glyph).font(.system(size: compact ? 8 : 9))
            }
            if showLabel {
                Text(state.label).font(.system(size: compact ? 10 : 11, weight: .medium))
            }
        }
        .foregroundStyle(tint)
        .padding(.horizontal, showLabel ? 7 : 4)
        .padding(.vertical, compact ? 1 : 2)
        .background(
            RoundedRectangle(cornerRadius: 5)
                .fill(tint.opacity(state == .running || state == .needsYou ? 0.14 : 0.0))
                .overlay(RoundedRectangle(cornerRadius: 5).stroke(tint.opacity(0.35), lineWidth: showLabel ? 1 : 0))
        )
    }
}
