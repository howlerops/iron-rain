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

    public init(model: Model, palette: OculusPalette, onClose: @escaping () -> Void, onOpen: ((String) -> Void)? = nil) {
        self.model = model; self.palette = palette; self.onClose = onClose; self.onOpen = onOpen
    }

    enum SortKey: String, CaseIterable, Identifiable {
        case updated = "Last active", name = "Name", provider = "Provider", status = "Status", cost = "Cost"
        var id: String { rawValue }
    }
    enum Scope: String, CaseIterable, Identifiable {
        case all = "All", worktrees = "Worktrees", stopped = "Stopped"
        var id: String { rawValue }
    }

    @State private var sort: SortKey = .updated
    @State private var ascending = false
    @State private var scope: Scope = .all
    @State private var query = ""
    /// The worktree session pending a delete decision (drives the "remove the worktree too?" dialog).
    @State private var pendingWorktreeDelete: Session?
    /// A plain (non-worktree) session pending delete confirmation.
    @State private var pendingDelete: Session?
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

    public var body: some View {
        VStack(spacing: 0) {
            header
            Divider().overlay(palette.border)
            if !selection.isEmpty { bulkBar; Divider().overlay(palette.border) }
            columnHeader
            Divider().overlay(palette.border.opacity(0.6))
            if rows.isEmpty {
                emptyState
            } else {
                ScrollView {
                    LazyVStack(spacing: 0) {
                        ForEach(rows) { s in
                            row(s)
                            Divider().overlay(palette.border.opacity(0.4))
                        }
                    }
                }
            }
        }
        .frame(minWidth: 640, minHeight: 440)
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
                Text("Manage sessions").font(.headline)
                Text("\(model.sessions.filter { $0.ephemeral != true }.count) total · \(worktreeCount) worktrees")
                    .font(.caption).foregroundStyle(palette.mutedForeground)
                Spacer()
                Picker("", selection: $scope) {
                    ForEach(Scope.allCases) { Text($0.rawValue).tag($0) }
                }
                .pickerStyle(.segmented).labelsHidden().frame(maxWidth: 240)
                Button { onClose() } label: { Image(systemName: "xmark.circle.fill").font(.title3).foregroundStyle(palette.mutedForeground) }
                    .buttonStyle(.plain)
            }
            HStack(spacing: 6) {
                Image(systemName: "magnifyingglass").font(.caption).foregroundStyle(palette.mutedForeground)
                TextField("Filter by name or provider…", text: $query)
                    .textFieldStyle(.plain).font(.callout)
            }
            .padding(.horizontal, 10).padding(.vertical, 7)
            .background(palette.input, in: RoundedRectangle(cornerRadius: 8))
            .overlay(RoundedRectangle(cornerRadius: 8).stroke(palette.border))
        }
        .padding(.horizontal, 16).padding(.vertical, 12)
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
    }

    private func sortButton(_ title: String, _ key: SortKey) -> some View {
        Button {
            if sort == key { ascending.toggle() } else { sort = key; ascending = (key == .name || key == .provider) }
        } label: {
            HStack(spacing: 3) {
                Text(title)
                if sort == key { Image(systemName: ascending ? "chevron.up" : "chevron.down").font(.system(size: 8)) }
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
            VStack(alignment: .leading, spacing: 2) {
                Text(label(s)).font(.callout).lineLimit(1)
                HStack(spacing: 6) {
                    if let b = s.branch, !b.isEmpty {
                        Label(b, systemImage: "arrow.triangle.branch").font(.system(size: 9)).lineLimit(1)
                            .foregroundStyle(palette.primary)
                    }
                    if let p = projectName(s) {
                        Text(p).font(.system(size: 9)).foregroundStyle(palette.mutedForeground).lineLimit(1)
                    }
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            Text(s.provider).font(.caption).foregroundStyle(palette.mutedForeground)
                .frame(width: 90, alignment: .leading).lineLimit(1)
            HStack(spacing: 5) {
                Circle().fill(statusColor(s.status)).frame(width: 6, height: 6)
                Text(statusLabel(s.status)).font(.caption).lineLimit(1)
            }.frame(width: 90, alignment: .leading)
            Text(s.costUSD.map { String(format: "$%.3f", $0) } ?? "—")
                .font(.caption.monospacedDigit()).foregroundStyle(palette.mutedForeground)
                .frame(width: 66, alignment: .trailing)
            Text(age(s.updatedAt)).font(.caption.monospacedDigit()).foregroundStyle(palette.mutedForeground)
                .frame(width: 92, alignment: .trailing)
            rowMenu(s).frame(width: 28)
        }
        .padding(.horizontal, 16).padding(.vertical, 8)
        .contentShape(Rectangle())
        .onTapGesture(count: 2) { open(s) }
    }

    private func rowMenu(_ s: Session) -> some View {
        Menu {
            Button { open(s) } label: { Label("Open", systemImage: "arrow.up.forward.app") }
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
        case SessionStatusValue.running: return .green
        case SessionStatusValue.awaitingApproval: return .yellow
        case SessionStatusValue.error, "errored": return .orange
        case SessionStatusValue.stopped: return palette.mutedForeground
        default: return palette.mutedForeground
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
