import SwiftUI
import OculusKit

/// The Activity destination: a persistent, cross-session feed answering "what did my agents just do,
/// and which need me?" — the mental model is an inbox, sorted needs-you first. Each row carries the
/// standard status chip, a timestamp, the owning session, and a tap that jumps straight to that
/// session (a needs-you item lands you in the reply box). Backed by the daemon's ActivityStore, so
/// this same data feeds the Needs-You nav badge, the iOS tab badge, and the bottom ticker.
///
/// Rows whose session is blocked on an approval are ANSWERABLE HERE. Routing was the only thing an
/// inbox row could do, which meant three queued approvals cost three round trips through three full
/// transcript replays to answer questions the row already had the text of. `model.pendingApprovals`
/// holds every waiting request, not just the open session's, so the decision can be sent in place.
struct ActivityView: View {
    @ObservedObject var model: Model
    let palette: OculusPalette
    /// Opens the owning session (jump-to-turn). Provided by the host so macOS and iOS route differently.
    var onOpen: (String) -> Void
    /// The status chip is a dot/glyph with no text in this dense feed. When the user has asked the
    /// system not to rely on colour, spell the state out beside it.
    @Environment(\.accessibilityDifferentiateWithoutColor) private var differentiateWithoutColor
    @Environment(\.inSidebarColumn) private var inSidebarColumn
    /// Whether this destination is actually on screen — see the announcement below.
    @State private var onScreen = false

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
                            sectionHeader("Needs you", count: needsYou.count, tint: palette.warning)
                            Button("Clear") { Task { await model.markActivityRead(needsYou.map(\.id)) } }
                                .font(.caption).buttonStyle(.plain)
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
        .surfaceBackground(palette.background, inSidebar: inSidebarColumn)
        // Approvals arrive (and are answered on other clients) with no focus change to carry them, so
        // a VoiceOver user's inbox silently gains and loses the rows that can actually be acted on.
        // Guarded on visibility because an unselected iOS tab stays alive and still gets onChange —
        // unguarded, Activity and Fleet would both speak the same event.
        .onAppear { onScreen = true }
        .onDisappear { onScreen = false }
        .onChange(of: model.pendingApprovals.count) { count in
            if onScreen { InlineApprovalControls.announceCountChange(count) }
        }
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

    /// Title Case, not ALL CAPS: OS 26 moved every system list header to title case, so a
    /// hand-uppercased header now reads as a deliberate departure from the rest of the platform.
    private func sectionHeader(_ title: String, count: Int?, tint: Color) -> some View {
        HStack(spacing: 6) {
            Text(title)
                .font(.footnote.weight(.semibold))
            if let count { Text("\(count)").font(.footnote.weight(.semibold).monospacedDigit()) }
            Spacer()
        }
        .foregroundStyle(tint)
        .padding(.horizontal, 16).padding(.top, 10).padding(.bottom, 5)
    }

    private func row(_ e: ActivityEvent) -> some View {
        VStack(alignment: .leading, spacing: 0) {
            Button {
                if let sid = e.sessionID { onOpen(sid) }
                if e.needsYou, !e.read { Task { await model.markActivityRead([e.id]) } }
            } label: {
                HStack(alignment: .top, spacing: 10) {
                    StatusChip(state(e), palette: palette, showLabel: differentiateWithoutColor)
                        .padding(.top, 1)
                        .accessibilityLabel(state(e).label)
                    VStack(alignment: .leading, spacing: 2) {
                        Text(e.title)
                            .font(.footnote.weight(e.needsYou && !e.read ? .semibold : .regular))
                            .foregroundStyle(palette.foreground)
                            .lineLimit(2).multilineTextAlignment(.leading)
                        HStack(spacing: 6) {
                            if let p = e.project, !p.isEmpty {
                                Text(projectName(p)).font(.system(.caption, design: .monospaced))
                            }
                            if let d = e.detail, !d.isEmpty {
                                Text("· \(d)").font(.caption).lineLimit(1)
                            }
                            Text("· \(relTime(e.ts))").font(.system(.caption, design: .monospaced))
                        }
                        .foregroundStyle(palette.mutedForeground)
                    }
                    Spacer(minLength: 4)
                    if e.sessionID != nil {
                        Image(systemName: "chevron.right").font(.caption2).foregroundStyle(palette.mutedForeground.opacity(0.6))
                            .accessibilityHidden(true) // decorative: the row's button trait already says it opens
                    }
                }
                .padding(.horizontal, 16).padding(.vertical, 9)
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .accessibilityHint(e.sessionID != nil ? "Opens the session" : "")
            // A sibling of the open-row button, never inside its label: controls nested in a Button's
            // label are swallowed by the outer button, so an inline Approve there would silently open
            // the session instead of answering — the exact round trip this is here to remove.
            if let sid = e.sessionID, let ap = model.pendingApprovals[sid], isApprovalRow(e, sid) {
                approvalStrip(ap, sessionID: sid, event: e)
            }
        }
    }

    /// Whether this row is the one that carries the session's approval controls. A session can own
    /// several unread needs-you events while `pendingApprovals` holds exactly ONE request for it, so
    /// without this the newest request renders on every one of those rows and reads as several
    /// separate requests — answering "one" of which would silently clear the others. Newest row wins
    /// (the feed is newest-first).
    private func isApprovalRow(_ e: ActivityEvent, _ sid: String) -> Bool {
        e.needsYou && !e.read && needsYou.first { $0.sessionID == sid }?.id == e.id
    }

    /// Names the session in every control's accessibility label. A VoiceOver user swiping a list of
    /// rows hears the buttons out of visual context, so a bare "Approve" gives them no way to tell
    /// which agent they just unblocked.
    private func sessionLabel(_ sid: String, project: String?) -> String {
        if let s = model.sessions.first(where: { $0.id == sid }),
           let n = s.subtask ?? s.name ?? s.workspaceName, !n.isEmpty { return n }
        if let p = project, !p.isEmpty { return projectName(p) }
        return "session \(sid.prefix(6))"
    }

    private func approvalStrip(_ ap: ApprovalRequest, sessionID sid: String, event e: ActivityEvent) -> some View {
        InlineApprovalControls(
            approval: ap, palette: palette, sessionName: sessionLabel(sid, project: e.project),
            // Answering clears the row too: without the read-mark the inbox keeps counting a
            // question the user just answered.
            onRespond: { decision in
                Task {
                    await model.respond(decision, for: sid)
                    await model.markActivityRead([e.id])
                }
            },
            onOpen: {
                onOpen(sid)
                Task { await model.markActivityRead([e.id]) }
            })
            .padding(.horizontal, 16).padding(.bottom, 9)
    }

    private var emptyState: some View {
        VStack(spacing: 10) {
            // .largeTitle rather than a fixed 30pt: matches FleetView's empty state and scales.
            Image(systemName: "waveform.path.ecg").font(.largeTitle).foregroundStyle(palette.mutedForeground.opacity(0.5))
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

/// Answer one pending approval from a list surface — an Activity row or a Fleet card — without
/// opening its session.
///
/// Shared by both so the rule for WHEN approving is offered can't drift between them: a safety gate
/// that holds on one screen and not the other is worse than not having it, because the user learns
/// the lenient screen. It is deliberately a reduced version of ChatView's ApprovalCard, never a
/// replacement — no "Always" (a permanent, unscoped rule needs the card's scope wording and its
/// confirmation), no keyboard shortcuts, and no Approve at all when the payload doesn't fit.
struct InlineApprovalControls: View {
    let approval: ApprovalRequest
    let palette: OculusPalette
    /// Human name of the owning session, spoken in every control's label.
    let sessionName: String
    /// Sends a `Decision` for this approval.
    let onRespond: (String) -> Void
    /// Escape hatch to the full card, and the only path to Approve when the payload is truncated.
    let onOpen: () -> Void

    /// Everything the in-session card would put in front of the user: the daemon's one-line summary,
    /// plus the raw arguments when they say something the summary doesn't.
    static func payloadText(_ ap: ApprovalRequest) -> String? {
        let detail = (ap.detail ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
        let args = (ap.input?.prettyJSON ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
        if args.isEmpty || args == detail { return detail.isEmpty ? nil : detail }
        return detail.isEmpty ? args : detail + "\n" + args
    }

    /// Approve is offered only when this surface can show the WHOLE request — the same text the card
    /// would show. A truncated payload is the one case where the list knows less than the card, and
    /// the tail of a command is exactly where the damage hides (`… && rm -rf /`), so those get Deny
    /// and Open instead. Denying on partial information is safe; approving on it is not.
    static func canApproveInline(_ ap: ApprovalRequest) -> Bool {
        guard let p = payloadText(ap) else { return false } // nothing shown is nothing consented to
        return p.count <= 160 && p.split(separator: "\n", omittingEmptySubsequences: false).count <= 3
    }

    private var risky: Bool { ApprovalCard.isRisky(approval) }
    private var inline: Bool { Self.canApproveInline(approval) }
    /// Same accent rule as the in-session card: a write/exec tool or a dangerous-looking payload
    /// turns the whole strip destructive, so the risk is legible before the buttons are.
    private var accent: Color { risky ? palette.destructive : palette.primary }

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack(spacing: 6) {
                Image(systemName: risky ? "exclamationmark.triangle.fill" : "bell.badge.fill")
                    .font(.caption2).foregroundStyle(accent)
                Text(approval.tool).font(.caption.weight(.semibold)).foregroundStyle(palette.foreground)
                Spacer(minLength: 4)
            }
            // A bare "Approve" button is strictly worse than the card, which at least names the tool
            // and what it wants to do. Show the payload or don't offer the decision.
            if let p = Self.payloadText(approval) {
                Text(p)
                    .font(.system(.caption, design: .monospaced))
                    .foregroundStyle(palette.foreground)
                    // No line limit once Approve is on offer: the gate promises the user sees the
                    // whole request, and a clamp would break that promise at large Dynamic Type
                    // sizes exactly where the text wraps most. Rows that only offer Deny may clamp —
                    // they're already declaring the payload doesn't fit.
                    .lineLimit(inline ? nil : 2)
                    .truncationMode(.middle)
                    .multilineTextAlignment(.leading)
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
            HStack(spacing: 8) {
                if inline, risky {
                    // Mirrors ApprovalCard's split: on a risky request the SAFE choice is the
                    // prominent one, so the "big button = go ahead" reflex can't land on the
                    // dangerous action. Nothing here takes a keyboard shortcut — Approve must never
                    // be what a reflexive Return hits, and N rows competing for Escape is worse than
                    // no binding at all.
                    denyButton.buttonStyle(.borderedProminent).tint(palette.primary)
                    approveButton.buttonStyle(.bordered).tint(palette.destructive)
                } else if inline {
                    denyButton.buttonStyle(.bordered).tint(palette.destructive)
                    approveButton.buttonStyle(.borderedProminent).tint(palette.primary)
                } else {
                    denyButton.buttonStyle(.borderedProminent).tint(palette.primary)
                    Button("Review", action: onOpen)
                        .buttonStyle(.bordered).tint(palette.primary)
                        .accessibilityLabel("Review \(approval.tool) approval in \(sessionName)")
                        .accessibilityHint("Too long to show in full here. Approving it needs the session.")
                }
                Spacer(minLength: 0)
            }
            .font(.caption)
            #if os(macOS)
            .controlSize(.small) // keeps the strip row-sized; iOS needs the full-size tap target
            #endif
        }
        .padding(10)
        .background(palette.card, in: OculusShape.rounded(OculusRadius.sm))
        .overlay(OculusShape.rounded(OculusRadius.sm).strokeBorder(accent.opacity(0.4)))
    }

    /// Speaks the waiting-approval total. Shared so Activity and Fleet can't word it differently —
    /// hearing two different sentences for one event reads as two events.
    static func announceCountChange(_ count: Int) {
        announceToAccessibility(count == 0
            ? "No approvals waiting."
            : "\(count) approval\(count == 1 ? "" : "s") waiting for you.")
    }

    private var denyButton: some View {
        Button("Deny") { onRespond(Decision.deny) }
            .accessibilityLabel("Deny \(approval.tool) in \(sessionName)")
    }
    private var approveButton: some View {
        Button("Approve") { onRespond(Decision.allow) }
            .accessibilityLabel("Approve \(approval.tool) in \(sessionName)")
    }
}
