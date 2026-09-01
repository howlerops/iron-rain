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

    /// `.adaptive` packs as many columns as `minimum` allows and only then divides the width, so the
    /// minimum is what the cards actually END UP at — the maximum is a ceiling for the leftovers, not
    /// a target. At 220 a maximised window fitted five columns and every card sat at its floor width:
    /// a name clipped to one line, a branch that never fit, and "budget $5.00" pushed to caption2.
    /// 300 buys four columns at ~340pt on the same window, which is where the card's second and third
    /// lines start reading. Unchanged on a phone and in a narrow split view — both were, and remain,
    /// a single column, since neither has room for two at either figure.
    private let columns = [GridItem(.adaptive(minimum: 300, maximum: 420), spacing: OculusSpace.md)]

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
                // No "Agent fleet" heading here. Both call sites are navigation destinations that
                // already carry `.navigationTitle("Fleet")`, so the window chrome said "Fleet" and the
                // pane immediately said "Agent fleet" underneath it — the same label twice, two lines
                // apart, with the actual information (how many, how many running, what it has cost)
                // demoted to caption2 beneath them both. The counts ARE the header.
                Text("\(model.sessions.count) sessions · \(runningCount) running" + (totalCost > 0 ? String(format: " · $%.3f total", totalCost) : ""))
                    .font(.subheadline).foregroundStyle(palette.mutedForeground)
                Spacer()
                if let onFanout {
                    Button(action: onFanout) { Label("Fan out", systemImage: "square.grid.2x2") }
                        .help("Race one task across several agents, then merge the winner")
                } else {
                    Button("Done", action: onClose).keyboardShortcut(.cancelAction)
                }
            }
            .padding(.horizontal, OculusSpace.lg).padding(.vertical, OculusSpace.md)
            Divider()
            if sessions.isEmpty {
                VStack(spacing: 8) {
                    Image(systemName: "square.grid.2x2").font(.largeTitle).foregroundStyle(palette.mutedForeground)
                    Text("No active sessions").foregroundStyle(palette.mutedForeground)
                }
                .frame(maxWidth: .infinity, maxHeight: .infinity)
            } else {
                ScrollView {
                    LazyVGrid(columns: columns, alignment: .leading, spacing: OculusSpace.md) {
                        ForEach(sessions) { s in
                            FleetCard(session: s, hb: model.heartbeats[s.id],
                                      approval: model.pendingApprovals[s.id], palette: palette,
                                      onOpen: { onOpen(s.id) },
                                      onRespond: { decision in Task { await model.respond(decision, for: s.id) } })
                        }
                    }
                    .padding(OculusSpace.lg)
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

    /// Same fallback chain the sidebar and the sessions table use. It used to stop at
    /// `workspaceName`, which is nil for every session that isn't a workspace — so a plain session
    /// fell straight through to `"session \(id.prefix(6))"` and the card was headed "session pi_0c2".
    /// That reads as a name that got truncated by a narrow card; it isn't, it's the id. `title` (the
    /// agent's own summary of the turn) and `folderName` (folder · branch, derived from the working
    /// tree) both name the work, and one of them is almost always there.
    private var title: String {
        session.subtask ?? session.name ?? session.title ?? session.workspaceName
            ?? session.folderName ?? "session \(session.id.prefix(6))"
    }
    private var isRunning: Bool { session.status == SessionStatusValue.running }

    /// Compact relative age of the last activity — the one thing that separates "four agents working"
    /// from "one working and three abandoned yesterday", and there is now width for it.
    private var age: String? {
        guard let ts = session.updatedAt, ts > 0 else { return nil }
        let secs = max(0, Int(Date().timeIntervalSince1970) - ts)
        switch secs {
        case 0..<60: return "\(secs)s ago"
        case 60..<3600: return "\(secs / 60)m ago"
        case 3600..<86400: return "\(secs / 3600)h ago"
        default: return "\(secs / 86400)d ago"
        }
    }

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
        .padding(OculusSpace.md)
        // A floor, not a fixed height: a card carrying approval controls still grows. Without it a
        // grid of quiet sessions is a row of ~70pt slivers under an otherwise empty window, which is
        // what made the fleet read as a strip of chips rather than as tiles.
        .frame(maxWidth: .infinity, minHeight: 96, alignment: .topLeading)
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
                // Two lines now that the card is ~340pt rather than ~220pt wide. A subtask is a
                // sentence ("Fix the sidebar overflow on macOS 26"), and one line of it at this width
                // was an ellipsis in the middle of the only thing that says which agent this is.
                Text(title).font(.subheadline.bold()).lineLimit(2).multilineTextAlignment(.leading)
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
            HStack(spacing: OculusSpace.xs) {
                if let c = session.costUSD, c > 0 {
                    Text(String(format: "$%.3f", c)).font(.caption2.monospacedDigit())
                        .foregroundStyle(palette.mutedForeground)
                }
                if let hb, hb.budgetUSD > 0 {
                    // "of $5.00", not "budget $5.00": beside the spend it reads as the pair it is,
                    // and it stops being a second isolated number at the far corner of the card.
                    Text(String(format: "of $%.2f", hb.budgetUSD)).font(.caption2)
                        .foregroundStyle(palette.mutedForeground)
                }
                Spacer(minLength: OculusSpace.xs)
                if let age {
                    Text(age).font(.caption2.monospacedDigit()).foregroundStyle(palette.mutedForeground)
                }
            }
            .accessibilityElement(children: .combine)
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
        case "awaiting_input", "needs_you": return palette.warning
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
        case "needs_you": return "hand.raised.fill"
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
        // Distinct from awaiting_input: that is the agent asking a question, this is the daemon
        // giving up after spending its nudges on a turn that never got moving.
        case "needs_you": return "Stuck — needs you"
        case "stalled": return "Stalled"
        case "exhausted": return "Budget used"
        case "errored": return "Error"
        case "done": return "Done"
        default: return isRunning ? "Running" : "Idle"
        }
    }
}
