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
    /// Whether this destination is actually on screen — see the announcement below.
    @State private var onScreen = false

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
                            FleetCard(session: s, hb: model.heartbeats[s.id],
                                      approval: model.pendingApprovals[s.id], palette: palette,
                                      onOpen: { onOpen(s.id) },
                                      onRespond: { decision in Task { await model.respond(decision, for: s.id) } })
                        }
                    }
                    .padding(16)
                }
            }
        }
        // A minimum size is a macOS idea — this view is also the iOS Fleet TAB ROOT, and asking a
        // 393pt phone for 520pt forced the tab content wider than the screen and let the whole page
        // scroll sideways. Same bug (and same fix) as SheetScaffold.swift:56-63.
        #if os(macOS)
        .frame(minWidth: 520, minHeight: 440)
        #else
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        #endif
        .background(palette.background)
        // Cards gain and lose their answerable controls with no focus change to carry it, so a
        // VoiceOver user has no way to notice an agent started waiting on them. Guarded on
        // visibility: an unselected iOS tab stays alive and still receives onChange, and Activity
        // announces the same event — unguarded the user hears it twice.
        .onAppear { onScreen = true }
        .onDisappear { onScreen = false }
        .onChange(of: model.pendingApprovals.count) { count in
            if onScreen { InlineApprovalControls.announceCountChange(count) }
        }
    }
}

private struct FleetCard: View {
    let session: Session
    let hb: SessionHeartbeat?
    /// This session's pending approval, when it's blocked on one. Answerable on the card: making
    /// "Needs you" only a label you have to travel to is what left a whole grid of blocked agents
    /// unresolvable from the one screen that shows all of them at once.
    let approval: ApprovalRequest?
    let palette: OculusPalette
    let onOpen: () -> Void
    let onRespond: (String) -> Void

    private var title: String {
        session.subtask ?? session.name ?? session.workspaceName ?? "session \(session.id.prefix(6))"
    }
    private var isRunning: Bool { session.status == SessionStatusValue.running }

    var body: some View {
        // The card chrome (padding, fill, border) wraps BOTH the open-button and the approval
        // controls. The controls can't live inside the button's label — a nested button never gets
        // the tap, the outer one does — so they're a sibling and the chrome moved out to hold them.
        VStack(alignment: .leading, spacing: 8) {
            Button(action: onOpen) { cardSummary }
                .buttonStyle(.plain)
                .accessibilityHint("Opens this session")
            if let approval {
                InlineApprovalControls(
                    approval: approval, palette: palette, sessionName: title,
                    onRespond: onRespond, onOpen: onOpen)
            }
        }
        .padding(12)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(palette.secondary.opacity(0.35), in: OculusShape.rounded(OculusRadius.md))
        .overlay(OculusShape.rounded(OculusRadius.md).strokeBorder(palette.border))
    }

    private var cardSummary: some View {
        VStack(alignment: .leading, spacing: 8) {
            // The state used to be a bare coloured dot, with the word "live" appearing only while
            // running — so every other state (needs you, stalled, budget used) was carried by hue
            // alone and vanished for anyone who can't separate green from amber from orange.
            // The glyph now changes with the state and the label is always present.
            HStack(spacing: 6) {
                Image(systemName: stateSymbol).font(.caption2).foregroundStyle(stateColor)
                Text(title).font(.subheadline.bold()).lineLimit(1)
                Spacer(minLength: 4)
                Text(stateLabel).font(.caption2.bold()).foregroundStyle(stateColor)
            }
            .accessibilityElement(children: .combine)
            .accessibilityLabel("\(title), \(stateLabel)")
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
            // The state itself now lives in the header, so this line is only the to-do progress.
            if let hb, hb.todosTotal > 0 {
                ProgressView(value: Double(hb.todosDone), total: Double(hb.todosTotal))
                    .tint(palette.primary)
                Text("\(hb.todosDone)/\(hb.todosTotal) done")
                    .font(.caption2).foregroundStyle(palette.mutedForeground)
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
        // The chrome moved to the wrapper, so the label no longer fills the card on its own — without
        // this the tappable area shrinks to the text and the card stops opening where it looks like
        // it should.
        .frame(maxWidth: .infinity, alignment: .leading)
        .contentShape(Rectangle())
    }

    private var stateColor: Color {
        switch hb?.state {
        case "working", "idle_incomplete": return palette.success
        case "awaiting_input": return palette.warning
        case "stalled", "exhausted": return palette.warning
        case "errored": return palette.destructive
        case "done": return palette.mutedForeground
        default: return isRunning ? palette.success : palette.mutedForeground
        }
    }

    /// A distinct glyph per state, so the card is readable without colour.
    private var stateSymbol: String {
        switch hb?.state {
        case "working": return "bolt.fill"
        case "idle_incomplete": return "hourglass"
        case "awaiting_input": return "questionmark.circle.fill"
        case "stalled": return "pause.circle"
        case "exhausted": return "dollarsign.circle.fill"
        case "errored": return "exclamationmark.triangle.fill"
        case "done": return "checkmark.circle.fill"
        default: return isRunning ? "bolt.fill" : "pause.circle"
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
