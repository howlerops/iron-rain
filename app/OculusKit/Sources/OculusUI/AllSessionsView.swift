import SwiftUI
import OculusKit

/// A management surface for EVERY session the daemon knows — a sortable, filterable table for
/// cleaning up old work. Unlike the sidebar (grouped by project, tuned for switching), this is a
/// flat "housekeeping" view: sort by age/cost/status, spot stale worktrees, and delete in one place.
/// Deleting a worktree session prompts whether to also remove the worktree directory (git worktree
/// remove), so an old branch's checkout doesn't silently linger on disk.
public struct AllSessionsView: View {
    @ObservedObject var model: Model
    let palette: OculusPalette
    let onClose: () -> Void
    /// Open a session as the active one (closes this sheet). Supplied by the host so it can also
    /// switch the deck to the Sessions destination.
    var onOpen: ((String) -> Void)? = nil
    /// Rendered INSIDE the detail column rather than as a sheet: no Done button, no fixed size.
    /// This is what the Sessions destination shows when no conversation is open — every session you
    /// have ever had, not just the recent handful the sidebar lists.
    var embedded: Bool = false

    public init(model: Model, palette: OculusPalette, onClose: @escaping () -> Void, onOpen: ((String) -> Void)? = nil, embedded: Bool = false) {
        self.model = model; self.palette = palette; self.onClose = onClose; self.onOpen = onOpen
        self.embedded = embedded
    }

    enum SortKey: String, CaseIterable, Identifiable {
        case updated = "Last active", name = "Name", provider = "Provider", status = "Status", cost = "Cost"
        var id: String { rawValue }
    }
    enum Scope: String, CaseIterable, Identifiable {
        /// What is actually going on right now: running, waiting on you, or errored — and top-level
        /// only. Everything finished, idle, or spawned by another agent is hidden.
        ///
        /// This is the default because the table's job is "what is happening", and a heavy week
        /// buries that under a hundred finished sessions plus every sub-agent each of them spawned.
        /// `All` is one tap away and still shows the lot.
        case active = "Active"
        case all = "All", worktrees = "Worktrees", stopped = "Stopped"
        var id: String { rawValue }
    }

    /// Sessions worth calling active: doing something, blocked on you, or broken.
    ///
    /// Deliberately NOT "not idle": `idle` is the resting state of a session you finished with, and
    /// including it puts the whole history back. A session you are reading is reachable from the
    /// recents list and the switcher regardless of this filter.
    private static func isActive(_ s: Session) -> Bool {
        switch s.status {
        case SessionStatusValue.running,
             SessionStatusValue.awaitingApproval,
             SessionStatusValue.error, "errored":
            return true
        default:
            return false
        }
    }

    /// A session spawned BY another agent rather than by you. Noise in a list of your own work —
    /// a single fan-out can add a dozen.
    private static func isChild(_ s: Session) -> Bool {
        !(s.parentID ?? "").isEmpty
    }

    @State private var hovered: String?
    @State private var sort: SortKey = .updated
    @State private var ascending = false
    @State private var scope: Scope = .active
    @State private var query = ""
    /// The worktree session pending a delete decision (drives the "remove the worktree too?" dialog).
    @State private var pendingWorktreeDelete: Session?
    /// A plain (non-worktree) session pending delete confirmation.
    @State private var pendingDelete: Session?
    /// A fan-out variant chosen as the winner, pending "end the race, discard the others" confirmation.
    @State private var pendingFanoutKeep: Session?
    /// Multi-select for bulk management (checkbox column + bulk action bar).
    @State private var selection: Set<String> = []
    /// Drives the bulk-delete confirmation (worktree-aware).
    @State private var pendingBulkDelete = false

    /// Selected sessions that actually still exist, and how many carry a worktree (drives the
    /// worktree-aware bulk delete prompt).
    private var selectedSessions: [Session] { rows.filter { selection.contains($0.id) } }
    private var selectedWorktreeCount: Int { selectedSessions.filter { $0.branch?.isEmpty == false }.count }

    private var rows: [Session] {
        var out = model.sessions.filter { $0.ephemeral != true }
        switch scope {
        case .active: out = out.filter { Self.isActive($0) && !Self.isChild($0) }
        case .all: break
        case .worktrees: out = out.filter { ($0.branch?.isEmpty == false) }
        case .stopped: out = out.filter { $0.status == SessionStatusValue.stopped }
        }
        if !query.isEmpty {
            out = out.filter { label($0).localizedCaseInsensitiveContains(query) || ($0.provider.localizedCaseInsensitiveContains(query)) }
        }
        out.sort { a, b in
            let r: Bool
            switch sort {
            case .updated: r = (a.updatedAt ?? 0) < (b.updatedAt ?? 0)
            case .name: r = label(a).localizedCaseInsensitiveCompare(label(b)) == .orderedAscending
            case .provider: r = a.provider.localizedCaseInsensitiveCompare(b.provider) == .orderedAscending
            case .status: r = a.status < b.status
            case .cost: r = (a.costUSD ?? 0) < (b.costUSD ?? 0)
            }
            return ascending ? r : !r
        }
        return out
    }

    private var worktreeCount: Int { model.sessions.filter { $0.branch?.isEmpty == false }.count }

    /// Terminal sessions available to adopt.
    private var terminalCandidates: [TakeoverCandidate] {
        TerminalTakeover.candidates(discovered: model.discovered, managed: model.sessions)
    }

    /// Those the daemon confirmed are actually running, as opposed to merely recent.
    private var liveTerminalCandidates: [TakeoverCandidate] { terminalCandidates.filter(\.live) }

    @ViewBuilder private var terminalSection: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack(spacing: 6) {
                // Title Case (OS 26 dropped all-caps section headers), and the count is of what is
                // actually LIVE — the candidate list also carries recent-but-exited sessions, which
                // are still worth offering to resume but are not "running".
                Text(liveTerminalCandidates.isEmpty ? "Recent terminal sessions" : "Running in a terminal")
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(palette.mutedForeground)
                Text("\(liveTerminalCandidates.isEmpty ? terminalCandidates.count : liveTerminalCandidates.count)")
                    .font(.system(.caption2, design: .monospaced).weight(.semibold))
                    .foregroundStyle(palette.mutedForeground)
                Spacer()
                Button { Task { await model.discover() } } label: {
                    Label("Rescan", systemImage: "arrow.clockwise").font(.caption)
                }
                .buttonStyle(.plain).foregroundStyle(palette.primaryText)
            }
            ForEach(terminalCandidates) { c in
                Button {
                    guard let d = model.discovered.first(where: { $0.discoveryID == c.id }) else { return }
                    Task { await model.attach(d); onOpen?(c.sessionID) }
                } label: {
                    HStack(spacing: 9) {
                        Image(systemName: c.provider == "claude-code" ? "terminal" : "bolt.horizontal.circle")
                            .font(.footnote).foregroundStyle(palette.primaryText)
                        VStack(alignment: .leading, spacing: 1) {
                            // Which terminal session this is, is the whole decision being made here —
                            // it wraps rather than truncating. The subtitle is a path, so it keeps
                            // middle truncation, where the tail is what identifies it.
                            Text(c.title).font(.footnote.weight(.medium))
                                .foregroundStyle(palette.foreground).lineLimit(2)
                            Text(c.subtitle).font(.caption)
                                .foregroundStyle(palette.mutedForeground)
                                .lineLimit(1).truncationMode(.middle)
                        }
                        Spacer(minLength: 6)
                        if c.live {
                            Text("Live").font(.caption2.weight(.semibold))
                                .foregroundStyle(palette.primaryText)
                                .padding(.horizontal, 6).padding(.vertical, 2)
                                .background(Capsule().fill(palette.primary.opacity(0.16)))
                        }
                        Text("Continue").font(.caption.weight(.medium))
                            .foregroundStyle(palette.primaryText)
                    }
                    .padding(.vertical, 5).contentShape(Rectangle())
                }
                .buttonStyle(.plain)
            }
        }
        .padding(.horizontal, 14).padding(.vertical, 10)
        .frame(maxWidth: .infinity, alignment: .leading)
    }

    public var body: some View {
        VStack(spacing: 0) {
            header
            Divider().overlay(palette.border)
            if !selection.isEmpty { bulkBar; Divider().overlay(palette.border) }
            if rows.isEmpty && terminalCandidates.isEmpty {
                emptyState
            } else {
                // Everything below the filters scrolls. The session column header belongs to the
                // rows, not terminal-only results, or it creates an empty table band above terminals.
                ScrollView {
                    LazyVStack(spacing: 0) {
                        if !terminalCandidates.isEmpty {
                            terminalSection
                            Divider().overlay(palette.border)
                        }
                        if !rows.isEmpty {
                            columnHeader
                            Divider().overlay(palette.border.opacity(0.6))
                            ForEach(rows) { s in
                                row(s)
                                Divider().overlay(palette.border.opacity(0.4))
                            }
                        }
                    }
                }
                .scrollIndicators(.automatic)
            }
        }
        .modifier(SheetSizing(embedded: embedded))
        .background(palette.background)
        .foregroundStyle(palette.foreground)
        // Worktree delete: offer to remove the on-disk checkout too, not just the session record.
        .confirmationDialog(
            "Delete this worktree session?",
            isPresented: Binding(get: { pendingWorktreeDelete != nil }, set: { if !$0 { pendingWorktreeDelete = nil } }),
            titleVisibility: .visible,
            presenting: pendingWorktreeDelete
        ) { s in
            Button("Remove worktree & session", role: .destructive) {
                Task { await model.removeWorktree(s.id, force: true) }
            }
            Button("Delete session only") {
                Task { await model.stopSession(s.id) }
            }
            Button("Cancel", role: .cancel) {}
        } message: { s in
            Text("“\(label(s))” is on branch \(s.branch ?? "?"). Removing the worktree runs `git worktree remove` and deletes its checkout from disk. Deleting the session only leaves the worktree in place.")
        }
        .confirmationDialog(
            "End this fan-out?",
            isPresented: Binding(get: { pendingFanoutKeep != nil }, set: { if !$0 { pendingFanoutKeep = nil } }),
            titleVisibility: .visible,
            presenting: pendingFanoutKeep
        ) { s in
            Button("Keep this, discard the others", role: .destructive) {
                if let g = s.fanoutGroup { Task { await model.resolveFanout(group: g, keep: s.id, force: true) } }
            }
            Button("Cancel", role: .cancel) {}
        } message: { s in
            Text("Keeps “\(label(s))” and removes every other agent in this fan-out — deleting their sessions and worktree checkouts (including uncommitted changes). This can’t be undone.")
        }
        .confirmationDialog(
            "Delete this session?",
            isPresented: Binding(get: { pendingDelete != nil }, set: { if !$0 { pendingDelete = nil } }),
            titleVisibility: .visible,
            presenting: pendingDelete
        ) { s in
            Button("Delete", role: .destructive) { Task { await model.stopSession(s.id) } }
            Button("Cancel", role: .cancel) {}
        } message: { s in
            Text("“\(label(s))” will be removed. This ends the agent if it's still running.")
        }
        // Bulk delete — one confirmation for the whole selection, worktree-aware.
        .confirmationDialog(
            "Delete \(selection.count) session\(selection.count == 1 ? "" : "s")?",
            isPresented: $pendingBulkDelete, titleVisibility: .visible
        ) {
            if selectedWorktreeCount > 0 {
                Button("Delete & remove \(selectedWorktreeCount) worktree\(selectedWorktreeCount == 1 ? "" : "s")", role: .destructive) {
                    bulkDelete(removeWorktrees: true)
                }
                Button("Delete sessions only", role: .destructive) { bulkDelete(removeWorktrees: false) }
            } else {
                Button("Delete", role: .destructive) { bulkDelete(removeWorktrees: false) }
            }
            Button("Cancel", role: .cancel) {}
        } message: {
            Text(selectedWorktreeCount > 0
                 ? "\(selectedWorktreeCount) of \(selection.count) carry a git worktree. Removing worktrees runs `git worktree remove` and deletes their checkouts from disk."
                 : "This ends any agents that are still running.")
        }
    }

    /// The action bar shown while a selection is active: count, select-all/none, clean-up-stopped,
    /// and the worktree-aware bulk delete.
    private var bulkBar: some View {
        HStack(spacing: 12) {
            Text("\(selection.count) selected").font(.caption.bold())
            Button("Select all") { selection = Set(rows.map { $0.id }) }.font(.caption).buttonStyle(.plain)
            Button("None") { selection = [] }.font(.caption).buttonStyle(.plain)
            Spacer()
            let stopped = rows.filter { $0.status == SessionStatusValue.stopped }
            if !stopped.isEmpty {
                Button { selection = Set(stopped.map { $0.id }) } label: {
                    Label("Select stopped (\(stopped.count))", systemImage: "moon.zzz").font(.caption)
                }.buttonStyle(.plain).foregroundStyle(palette.mutedForeground)
            }
            Button(role: .destructive) { pendingBulkDelete = true } label: {
                Label("Delete \(selection.count)", systemImage: "trash").font(.caption.bold())
            }.buttonStyle(.plain).foregroundStyle(palette.destructive)
        }
        .padding(.horizontal, 16).padding(.vertical, 8)
        .background(palette.primary.opacity(0.08))
    }

    /// Deletes every selected session; worktree sessions either have their checkout removed (git
    /// worktree remove) or just the record dropped, per the user's choice in the confirmation.
    private func bulkDelete(removeWorktrees: Bool) {
        let targets = selectedSessions
        selection = []
        Task {
            for s in targets {
                if removeWorktrees, s.branch?.isEmpty == false {
                    await model.removeWorktree(s.id, force: true)
                } else {
                    await model.stopSession(s.id)
                }
            }
        }
    }

    private var header: some View {
        VStack(spacing: 10) {
            HStack(spacing: 10) {
                Text("All sessions").font(.subheadline.weight(.semibold))
                Text("\(model.sessions.filter { $0.ephemeral != true }.count) total · \(worktreeCount) worktrees")
                    .font(.caption).foregroundStyle(palette.mutedForeground)
                Spacer()
                Spacer(minLength: 0)
                if !embedded {
                    Button { onClose() } label: {
                        Image(systemName: "xmark.circle.fill").font(.title3)
                            .foregroundStyle(palette.mutedForeground)
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel("Close")
                }
            }
            HStack(spacing: 6) {
                Image(systemName: "magnifyingglass").font(.caption).foregroundStyle(palette.mutedForeground)
                TextField("Filter by name or provider…", text: $query)
                    .textFieldStyle(.plain).font(.callout)
                    .plainInput() // provider ids and branch names must not be autocapitalized
            }
            .padding(.horizontal, 10).padding(.vertical, 7)
            .background(palette.input, in: OculusShape.rounded(OculusRadius.sm))
            .overlay(OculusShape.rounded(OculusRadius.sm).strokeBorder(palette.border))
            scopeChips
        }
        .padding(.horizontal, 16).padding(.vertical, 12)
    }

    /// Filter chips carrying live counts — "Stopped 3" tells you whether it is worth clicking before
    /// you click it, which a bare segmented control cannot.
    private var scopeChips: some View {
        // Three capsules that no longer share a line once the type is large. They scroll rather than
        // clamp, because a filter you cannot reach is worse than a filter you have to swipe to.
        ViewThatFits(in: .horizontal) {
            chipRow
            ScrollView(.horizontal, showsIndicators: false) { chipRow }
        }
    }

    private var chipRow: some View {
        HStack(spacing: 6) {
            ForEach(Scope.allCases) { sc in
                let n = count(for: sc)
                let active = scope == sc
                Button { scope = sc } label: {
                    HStack(spacing: 5) {
                        Text(sc.rawValue).font(.caption.weight(active ? .semibold : .regular))
                        if n > 0 {
                            Text("\(n)").font(.system(.caption2, design: .monospaced).weight(.semibold))
                                .opacity(0.75)
                        }
                    }
                    .foregroundStyle(active ? palette.primaryForeground : palette.mutedForeground)
                    .padding(.horizontal, 9).padding(.vertical, 4)
                    .background(Capsule().fill(active ? palette.primary : palette.muted.opacity(0.5)))
                    .contentShape(Capsule())
                }
                .buttonStyle(.plain)
            }
            Spacer(minLength: 0)
        }
        .animation(.easeOut(duration: 0.14), value: scope)
    }

    private func count(for sc: Scope) -> Int {
        let base = model.sessions.filter { $0.ephemeral != true }
        switch sc {
        case .active: return base.filter { Self.isActive($0) && !Self.isChild($0) }.count
        case .all: return base.count
        case .worktrees: return base.filter { $0.branch?.isEmpty == false }.count
        case .stopped: return base.filter { $0.status == SessionStatusValue.stopped }.count
        }
    }

    private var columnHeader: some View {
        HStack(spacing: 10) {
            Color.clear.frame(width: 22) // checkbox column
            sortButton("Name", .name).frame(maxWidth: .infinity, alignment: .leading)
            sortButton("Provider", .provider).frame(width: 90, alignment: .leading)
            sortButton("Status", .status).frame(width: 90, alignment: .leading)
            sortButton("Cost", .cost).frame(width: 66, alignment: .trailing)
            sortButton("Last active", .updated).frame(width: 92, alignment: .trailing)
            Color.clear.frame(width: 28) // actions column
        }
        .font(.caption2.bold())
        .foregroundStyle(palette.mutedForeground)
        .padding(.horizontal, 16).padding(.vertical, 7)
        .background(palette.secondary.opacity(0.25))
        .dynamicTypeSize(...DynamicTypeSize.accessibility2) // must track `row`'s clamp or the columns drift
    }

    private func sortButton(_ title: String, _ key: SortKey) -> some View {
        Button {
            if sort == key { ascending.toggle() } else { sort = key; ascending = (key == .name || key == .provider) }
        } label: {
            HStack(spacing: 3) {
                Text(title)
                if sort == key { Image(systemName: ascending ? "chevron.up" : "chevron.down").font(.caption2) }
            }
        }
        .buttonStyle(.plain)
    }

    private func row(_ s: Session) -> some View {
        HStack(spacing: 10) {
            Button {
                if selection.contains(s.id) { selection.remove(s.id) } else { selection.insert(s.id) }
            } label: {
                Image(systemName: selection.contains(s.id) ? "checkmark.circle.fill" : "circle")
                    .foregroundStyle(selection.contains(s.id) ? palette.primary : palette.mutedForeground)
                    .font(.body)
            }
            .buttonStyle(.plain).frame(width: 22)
            .accessibilityLabel(selection.contains(s.id) ? "Deselect \(label(s))" : "Select \(label(s))")
            // The name is a real Button, not just part of the row's tap gesture. Opening a session is
            // the primary action of this screen, and a bare `.onTapGesture` is invisible to
            // VoiceOver — the name was announced as static text with no way to act on it. The row
            // keeps its full-width tap target below for pointer users.
            Button { open(s) } label: {
                VStack(alignment: .leading, spacing: 2) {
                    // The name is what you are scanning for; it shrinks and takes a second line
                    // before it will truncate.
                    Text(label(s)).font(.callout).lineLimit(2).minimumScaleFactor(0.85)
                    HStack(spacing: 6) {
                        if let b = s.branch, !b.isEmpty {
                            Label(b, systemImage: "arrow.triangle.branch").font(.caption2).lineLimit(1)
                                .foregroundStyle(palette.primaryText)
                        }
                        if let p = projectName(s) {
                            Text(p).font(.caption2).foregroundStyle(palette.mutedForeground).lineLimit(1)
                        }
                    }
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .accessibilityHint("Opens this session")
            .frame(maxWidth: .infinity, alignment: .leading)
            Text(s.provider).font(.caption).foregroundStyle(palette.mutedForeground)
                .frame(width: 90, alignment: .leading).lineLimit(1)
            // A pill, not a dot beside grey text. Status is the column you scan down when you are
            // looking for the session that needs you, and it has to read at a glance.
            HStack(spacing: 4) {
                Image(systemName: statusSymbol(s.status)).font(.caption2)
                Text(statusLabel(s.status))
                    .font(.caption.weight(.medium)).lineLimit(1)
            }
            .foregroundStyle(statusColor(s.status))
            .padding(.horizontal, 7).padding(.vertical, 2.5)
            .background(Capsule().fill(statusColor(s.status).opacity(0.14)))
            .frame(width: 90, alignment: .leading)
            Text(s.costUSD.map { String(format: "$%.3f", $0) } ?? "—")
                .font(.caption.monospacedDigit()).foregroundStyle(palette.mutedForeground)
                .frame(width: 66, alignment: .trailing)
            Text(age(s.updatedAt)).font(.caption.monospacedDigit()).foregroundStyle(palette.mutedForeground)
                .frame(width: 92, alignment: .trailing)
            rowMenu(s).frame(width: 28)
        }
        // A genuine table: the provider/status/cost/age columns only read as columns because every
        // row pins them to the same widths. The type scales up to AX2 and the row grows taller with
        // it; beyond that there is no width left for the name, so growth stops rather than the
        // columns sliding out of alignment. `columnHeader` carries the same clamp.
        .dynamicTypeSize(...DynamicTypeSize.accessibility2)
        .padding(.horizontal, 16).padding(.vertical, 9)
        .background(hovered == s.id ? palette.muted.opacity(0.35) : .clear)
        .contentShape(Rectangle())
        // SINGLE click opens. Double-click-to-open is a Finder convention that nothing else in this
        // app uses, and a row that looks clickable and does nothing reads as broken.
        .onTapGesture { open(s) }
        .onHover { hovered = $0 ? s.id : (hovered == s.id ? nil : hovered) }
        .help("Open this session")
    }

    private func rowMenu(_ s: Session) -> some View {
        Menu {
            Button { open(s) } label: { Label("Open", systemImage: "arrow.up.forward.app") }
            if let g = s.fanoutGroup, !g.isEmpty {
                Divider()
                Button { pendingFanoutKeep = s } label: {
                    Label("Keep this · end fan-out…", systemImage: "flag.checkered")
                }
            }
            Divider()
            if let b = s.branch, !b.isEmpty {
                Button(role: .destructive) { pendingWorktreeDelete = s } label: {
                    Label("Delete & remove worktree…", systemImage: "trash")
                }
                Button(role: .destructive) { pendingDelete = s } label: {
                    Label("Delete session only", systemImage: "trash.slash")
                }
            } else {
                Button(role: .destructive) { pendingDelete = s } label: { Label("Delete…", systemImage: "trash") }
            }
        } label: {
            Image(systemName: "ellipsis").font(.caption).foregroundStyle(palette.mutedForeground)
                .frame(width: 28, height: 22).contentShape(Rectangle())
        }
        .menuIndicator(.hidden)
        .fixedSize()
        .accessibilityLabel("Actions for \(label(s))")
    }

    private var emptyState: some View {
        VStack(spacing: 8) {
            Image(systemName: "tray").font(.largeTitle).foregroundStyle(palette.mutedForeground)
            Text(scope == .all ? "No sessions" : "No \(scope.rawValue.lowercased()) sessions")
                .foregroundStyle(palette.mutedForeground)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }

    private func open(_ s: Session) {
        if let onOpen { onOpen(s.id) } else { Task { await model.openSession(s.id) } }
        onClose()
    }

    // MARK: formatting

    private func label(_ s: Session) -> String {
        if let n = s.name, !n.isEmpty { return n }
        if let t = s.title, !t.isEmpty { return t }
        if let st = s.subtask, !st.isEmpty { return st }
        if let f = s.folderName { return f } // auto-name from the working tree (folder · branch)
        return "Session \(s.id.prefix(6))"
    }
    private func projectName(_ s: Session) -> String? {
        s.workspaceName ?? s.projectID.flatMap { id in model.projects.first(where: { $0.id == id })?.name }
    }
    private func statusColor(_ status: String) -> Color {
        switch status {
        case SessionStatusValue.running: return palette.success
        case SessionStatusValue.awaitingApproval: return palette.warning
        case SessionStatusValue.error, "errored": return palette.destructive
        case SessionStatusValue.stopped: return palette.mutedForeground
        default: return palette.mutedForeground
        }
    }
    /// A glyph per status, so the pill is legible without colour (the dot alone was hue-only).
    private func statusSymbol(_ status: String) -> String {
        switch status {
        case SessionStatusValue.running: return "circle.fill"
        case SessionStatusValue.awaitingApproval: return "exclamationmark.triangle.fill"
        case SessionStatusValue.error, "errored": return "xmark.octagon.fill"
        case SessionStatusValue.stopped: return "pause.circle"
        case SessionStatusValue.done: return "checkmark.circle.fill"
        default: return "circle"
        }
    }
    private func statusLabel(_ status: String) -> String {
        switch status {
        case SessionStatusValue.running: return "running"
        case SessionStatusValue.idle: return "idle"
        case SessionStatusValue.done: return "done"
        case SessionStatusValue.awaitingApproval: return "needs you"
        case SessionStatusValue.error, "errored": return "error"
        case SessionStatusValue.stopped: return "stopped"
        default: return status
        }
    }
    /// Compact relative age from a unix-seconds timestamp, using a fixed reference captured once per
    /// render so all rows share one "now" (no per-row Date() churn).
    private func age(_ ts: Int?) -> String {
        guard let ts, ts > 0 else { return "—" }
        let secs = max(0, Int(Date().timeIntervalSince1970) - ts)
        switch secs {
        case 0..<60: return "\(secs)s"
        case 60..<3600: return "\(secs / 60)m"
        case 3600..<86400: return "\(secs / 3600)h"
        default: return "\(secs / 86400)d"
        }
    }
}


/// A sheet needs a minimum size; a pane embedded in the detail column must take whatever it is given.
///
/// …and a minimum size is a macOS idea either way. The non-embedded path IS reachable on iOS (the
/// sidebar's ⋯ → "Manage sessions…"), where demanding 640pt forced the sheet wider than the phone and
/// let the page scroll sideways. Same bug and same fix as SheetScaffold.swift:56-63.
private struct SheetSizing: ViewModifier {
    let embedded: Bool
    func body(content: Content) -> some View {
        #if os(macOS)
        if embedded {
            content.frame(maxWidth: .infinity, maxHeight: .infinity)
        } else {
            content.frame(minWidth: 640, minHeight: 440)
        }
        #else
        content.frame(maxWidth: .infinity, maxHeight: .infinity)
        #endif
    }
}
