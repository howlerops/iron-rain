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

    /// iOS tab order puts Activity in the center (index 2) as the default launch tab. `tabSurface`
    /// builds the tab bar by iterating this, so the order is defined once here rather than being an
    /// intention a hand-written tab list can silently drift out of.
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
        case .needsYou: return { $0.warning }
        case .failed:   return { $0.destructive }
        case .conflict: return { $0.conflict }
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

    /// Derive the display state for a session from its status, conflict flag, + the Model's error
    /// map. Priority: failed/needs-you (attention) > conflict > running > idle.
    @MainActor static func of(_ session: Session, model: Model) -> AgentState {
        if model.sessionErrors[session.id] != nil { return .failed }
        switch session.status {
        case SessionStatusValue.error, "errored": return .failed
        case SessionStatusValue.awaitingApproval: return .needsYou
        case SessionStatusValue.needsYou: return .needsYou // stuck, nudged, still stuck — a person's call
        default: break
        }
        if session.conflicted == true { return .conflict }
        switch session.status {
        case SessionStatusValue.running: return .running
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
    /// Called when the Sessions destination is re-selected — the host clears the open session so the
    /// full sessions table appears.
    var onShowAllSessions: (() -> Void)? = nil
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
                        Text("NEEDS YOU").font(.caption.weight(.semibold)).tracking(0.8)
                        Spacer()
                        Text("\(model.needsYouCount)")
                            .font(.caption.weight(.bold).monospaced())
                            .foregroundStyle(palette.background)
                            .padding(.horizontal, 5).padding(.vertical, 1)
                            .background(Capsule().fill(palette.warning))
                    }
                    .foregroundStyle(palette.warning)
                    .padding(.horizontal, 10).padding(.vertical, 7)
                    .background(OculusShape.rounded(OculusRadius.sm).fill(palette.warning.opacity(0.12))
                        .overlay(OculusShape.rounded(OculusRadius.sm).strokeBorder(palette.warning.opacity(0.32))))
                }
                .buttonStyle(.plain)
                .accessibilityLabel("\(model.needsYouCount) items need you")
                .accessibilityHint("Opens Activity")
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
        Button {
            // Sessions ALWAYS lands on the table, whether or not you were already there. The nav item
            // names a destination, and the destination for Sessions is every session you have — not
            // whichever conversation happened to be open. The recents list and the pinned
            // active-session bar below make the conversation one click away, so nothing is stranded.
            if d == .sessions { onShowAllSessions?() }
            destination = d
        } label: {
            HStack(spacing: 9) {
                Image(systemName: d.symbol).font(.subheadline).frame(width: 18)
                Text(d.title).font(.footnote.weight(active ? .semibold : .medium))
                Spacer(minLength: 4)
                if let badge = count(d) {
                    Text("\(badge)")
                        .font(.caption.monospaced())
                        .foregroundStyle(d == .activity && model.needsYouCount > 0 ? palette.warning : palette.mutedForeground)
                }
            }
            .foregroundStyle(active ? palette.foreground : palette.mutedForeground)
            .padding(.horizontal, 10).padding(.vertical, 7)
            .background(OculusShape.rounded(OculusRadius.sm).fill(active ? palette.primary.opacity(0.14) : .clear))
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        // The rail is a selection list, not five unrelated buttons — VoiceOver should say which one
        // you are on, the way it does for a real sidebar List.
        .accessibilityAddTraits(active ? [.isButton, .isSelected] : .isButton)
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

/// A sticky search field for the sidebar's session/fleet list — pinned above the scrolling list (so
/// it never scrolls away) with breathing room after the destination rail above it.
struct DeckSearchBar: View {
    @Binding var text: String
    let palette: OculusPalette
    var prompt: String = "Search sessions"

    var body: some View {
        HStack(spacing: 6) {
            Image(systemName: "magnifyingglass").font(.footnote).foregroundStyle(palette.mutedForeground)
            TextField(prompt, text: $text)
                .textFieldStyle(.plain)
                .font(.footnote)
                .plainInput()
            if !text.isEmpty {
                Button { text = "" } label: { Image(systemName: "xmark.circle.fill").font(.caption) }
                    .buttonStyle(.plain).foregroundStyle(palette.mutedForeground)
                    .accessibilityLabel("Clear search")
            }
        }
        .padding(.horizontal, 9).padding(.vertical, 6)
        .background(OculusShape.rounded(OculusRadius.sm).fill(palette.secondary.opacity(0.6))
            .overlay(OculusShape.rounded(OculusRadius.sm).strokeBorder(palette.border)))
        .padding(.horizontal, 10)
        .padding(.top, 10)   // breathing room after the destination rail (the "top section")
        .padding(.bottom, 4)
    }
}

/// A friendly placeholder for a destination whose primary surface lives in the other column.
struct DestinationHint: View {
    let palette: OculusPalette
    let symbol: String
    let title: String
    let message: String
    @Environment(\.inSidebarColumn) private var inSidebarColumn
    var body: some View {
        VStack(spacing: 12) {
            Image(systemName: symbol).font(.largeTitle).foregroundStyle(palette.mutedForeground.opacity(0.5))
            Text(title).font(.title3.weight(.semibold)).foregroundStyle(palette.foreground)
            Text(message).font(.callout).foregroundStyle(palette.mutedForeground)
                .multilineTextAlignment(.center).frame(maxWidth: 360)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .padding(24)
        .surfaceBackground(palette.background, inSidebar: inSidebarColumn)
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
    @Environment(\.inSidebarColumn) private var inSidebarColumn

    var body: some View {
        VStack(spacing: 0) {
            HStack {
                Text("Loops").font(.footnote.weight(.semibold))
                Spacer()
                Button(action: onNew) { Label("New", systemImage: "plus") .font(.footnote.weight(.medium)) }
                    .buttonStyle(.plain).foregroundStyle(palette.primaryText)
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
        .surfaceBackground(palette.background, inSidebar: inSidebarColumn)
    }

    private func row(_ loop: Loop) -> some View {
        let running = model.loopRuns.contains { $0.loopID == loop.id && ($0.status == "running" || $0.status.isEmpty) }
        return HStack(spacing: 9) {
            Image(systemName: "arrow.trianglehead.2.clockwise.rotate.90")
                .font(.footnote).foregroundStyle(loop.enabled ? palette.primary : palette.mutedForeground)
                .frame(width: 16)
            VStack(alignment: .leading, spacing: 2) {
                Text(loop.name).font(.footnote.weight(.medium)).foregroundStyle(palette.foreground).lineLimit(2)
                Text(subtitle(loop)).font(.caption.monospaced()).foregroundStyle(palette.mutedForeground).lineLimit(1)
            }
            Spacer(minLength: 4)
            if running {
                StatusChip(.running, palette: palette, showLabel: false, compact: true)
            } else if !loop.enabled {
                Text("paused").font(.caption2).foregroundStyle(palette.mutedForeground)
            }
        }
        .padding(.horizontal, 8).padding(.vertical, 7)
        .background(OculusShape.rounded(7).fill(selected == loop.id ? palette.secondary : .clear))
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
                Text("Recent runs").font(.caption.weight(.semibold)).tracking(0.6)
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
                                Text(run.issueKey.isEmpty ? run.sessionID : run.issueKey).font(.footnote.monospaced())
                                    .foregroundStyle(palette.foreground)
                                Spacer()
                                Image(systemName: "chevron.right").font(.caption2).foregroundStyle(palette.mutedForeground)
                            }
                            .padding(.horizontal, 12).padding(.vertical, 9)
                            .background(OculusShape.rounded(OculusRadius.sm).fill(palette.secondary.opacity(0.4)))
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
                Image(systemName: state.glyph).font(.caption2)
            }
            if showLabel {
                Text(state.label).font((compact ? Font.caption2 : .caption).weight(.medium))
            }
        }
        .foregroundStyle(tint)
        .padding(.horizontal, showLabel ? 7 : 4)
        .padding(.vertical, compact ? 1 : 2)
        .background(
            OculusShape.rounded(5)
                .fill(tint.opacity(state == .running || state == .needsYou ? 0.14 : 0.0))
                .overlay(OculusShape.rounded(5).strokeBorder(tint.opacity(0.35), lineWidth: showLabel ? 1 : 0))
        )
        // Without the label the chip is a bare glyph — colour and shape carry the whole meaning, which
        // VoiceOver can't see. Say the state out loud instead.
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(state.label)
    }
}
