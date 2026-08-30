import SwiftUI
import OculusKit

// Going back to an earlier point in a conversation.
//
// Modelled on pi's own tree selector, because that design is answering a real problem rather than
// decorating one: four turns produce nineteen nodes, since every tool call, file edit, shell run and
// model change is its own node. An unfiltered list is a wall of `[ctx_execute_file: …]` with the
// user's own messages lost inside it, which is why pi ships type filters, a search box and branch
// navigation rather than a plain picker.
//
// What is improved over the terminal version, rather than merely ported:
//
//   - the filters are visible chips, not keystrokes you have to know (ctrl+d/t/u/l/a).
//   - kind is carried by an icon and colour instead of a bracketed prefix, so a row's shape is
//     readable before its text is.
//   - the actions offered come from the session's CAPABILITIES, so opencode shows Rewind and pi does
//     not — rather than one menu that fails on three providers out of four.
//   - what each action does is stated in the row's own terms, because "fork" means different things
//     per provider: opencode creates a new session, pi rebinds the one you are in.

/// One filter chip over node kinds.
private enum ThreadFilter: String, CaseIterable, Identifiable {
    case all, messages, edits, tools
    var id: String { rawValue }
    var label: String {
        switch self {
        case .all: return "All"
        case .messages: return "Messages"
        case .edits: return "Edits"
        case .tools: return "Tools"
        }
    }
    /// Whether a node of this kind passes the filter. Unknown kinds pass everything except the
    /// narrow filters — a provider may report a kind we have never seen, and it should be findable
    /// under All rather than silently absent.
    func accepts(_ kind: String?) -> Bool {
        let k = (kind ?? "").lowercased()
        switch self {
        case .all: return true
        case .messages: return k == "user" || k == "assistant" || k == "branch_summary"
        case .edits: return k == "edit"
        case .tools: return k == "tool" || k == "bash" || k.hasPrefix("ctx_")
        }
    }
}

public struct ThreadView: View {
    @ObservedObject var model: Model
    let palette: OculusPalette
    var onClose: (() -> Void)? = nil

    public init(model: Model, palette: OculusPalette, onClose: (() -> Void)? = nil) {
        self.model = model; self.palette = palette; self.onClose = onClose
    }

    @State private var filter: ThreadFilter = .all
    @State private var query = ""
    /// A rewind staged for confirmation. Rewinding discards everything after the chosen point, and
    /// on opencode it is undoable while on others it is not — so it is never a single tap.
    @State private var pendingRewind: ThreadNode?

    private var caps: ThreadCaps { model.capabilities?.thread ?? ThreadCaps() }

    private var visible: [ThreadNode] {
        let q = query.trimmingCharacters(in: .whitespaces).lowercased()
        return model.threadNodes.filter { n in
            guard filter.accepts(n.kind) else { return false }
            guard !q.isEmpty else { return true }
            return n.preview.lowercased().contains(q)
        }
    }

    public var body: some View {
        OculusSheet(
            title: "Go back",
            subtitle: subtitle,
            palette: palette,
            search: model.threadNodes.count >= 8 ? $query : nil,
            searchPrompt: "Search this conversation",
            onClose: onClose
        ) {
            if model.threadLoading && model.threadNodes.isEmpty {
                ProgressView().frame(maxWidth: .infinity).padding(.vertical, 24)
            } else if let err = model.threadError {
                SheetEmptyState(icon: "exclamationmark.triangle",
                                title: "Couldn't read the history",
                                message: err, palette: palette)
            } else if model.threadNodes.isEmpty {
                SheetEmptyState(icon: "arrow.uturn.backward",
                                title: "Nothing to go back to",
                                message: "This conversation has no earlier point to branch from yet. Send a message first.",
                                palette: palette)
            } else {
                filters
                ForEach(visible) { node in
                    row(node)
                }
                if visible.isEmpty {
                    SheetEmptyState(icon: "line.3.horizontal.decrease.circle",
                                    title: "Nothing matches",
                                    message: "No step matching those filters.",
                                    palette: palette) {
                        Button("Clear") { query = ""; filter = .all }.buttonStyle(.bordered)
                    }
                }
            }
        }
        .task { await model.loadThread() }
        .confirmationDialog(pendingRewind.map { _ in "Rewind to this point?" } ?? "",
                            isPresented: Binding(get: { pendingRewind != nil },
                                                 set: { if !$0 { pendingRewind = nil } }),
                            titleVisibility: .visible) {
            Button("Rewind, discarding what follows", role: .destructive) {
                if let n = pendingRewind { Task { await model.rewindThread(to: n.id) } }
                pendingRewind = nil
            }
            Button("Cancel", role: .cancel) { pendingRewind = nil }
        } message: {
            Text(rewindWarning)
        }
    }

    /// Says what this provider will actually do, rather than a generic line. The two verbs differ per
    /// provider and the difference is the whole point.
    private var subtitle: String {
        if caps.hasRewind && caps.hasFork {
            return "Branch from an earlier step, or rewind this session back to it."
        }
        if caps.hasFork {
            return "Branch the conversation from an earlier step."
        }
        return "Earlier steps in this conversation."
    }

    private var rewindWarning: String {
        caps.hasUnrevert
            ? "Everything after this point is removed from the session. This provider can undo a rewind."
            : "Everything after this point is removed from the session, and it cannot be undone."
    }

    private var filters: some View {
        ScrollView(.horizontal, showsIndicators: false) {
            HStack(spacing: 6) {
                ForEach(ThreadFilter.allCases) { f in
                    Button { filter = f } label: {
                        Text(f.label)
                            .font(.caption.weight(.medium))
                            .padding(.horizontal, 10).padding(.vertical, 5)
                            .background(Capsule().fill(filter == f ? palette.primary : palette.secondary.opacity(0.5)))
                            .foregroundStyle(filter == f ? palette.primaryForeground : palette.foreground)
                    }
                    .buttonStyle(.plain)
                }
            }
            .padding(.vertical, 2)
        }
    }

    @ViewBuilder private func row(_ node: ThreadNode) -> some View {
        let onPath = node.isOnPath || node.isCurrent
        HStack(alignment: .top, spacing: 10) {
            // Depth as real indentation. pi draws ├ │ └ because it only has cells; the same
            // information reads better as space plus a rule.
            if let d = node.depth, d > 0 {
                Rectangle().fill(palette.border)
                    .frame(width: 1)
                    .padding(.leading, CGFloat(d) * 10)
            }
            Image(systemName: icon(node.kind))
                .font(.caption)
                .foregroundStyle(tint(node))
                .frame(width: 16)
            VStack(alignment: .leading, spacing: 3) {
                Text(node.preview)
                    .font(.callout)
                    .lineLimit(2)
                    // Abandoned branches are dimmed, the live line is not. This is the single thing
                    // that makes a tree with two branches readable at a glance.
                    .foregroundStyle(onPath ? palette.foreground : palette.mutedForeground)
                if node.isCurrent {
                    Text("You are here")
                        .font(.caption2.weight(.semibold))
                        .foregroundStyle(palette.primary)
                }
            }
            Spacer(minLength: 8)
            if !node.isCurrent {
                actions(node)
            }
        }
        .padding(.vertical, 7)
        .contentShape(Rectangle())
        .opacity(onPath ? 1 : 0.72)
    }

    /// Only the operations this provider actually supports. A capability manifest is worth nothing
    /// if a control can appear for something that will fail when tapped.
    @ViewBuilder private func actions(_ node: ThreadNode) -> some View {
        HStack(spacing: 6) {
            if caps.hasRewind {
                Button("Rewind") { pendingRewind = node }
                    .buttonStyle(.bordered).controlSize(.small)
            }
            if caps.hasFork {
                Button("Branch") { Task { await model.forkThread(at: node.id) } }
                    .buttonStyle(.borderedProminent).controlSize(.small).tint(palette.primary)
            }
        }
    }

    private func icon(_ kind: String?) -> String {
        switch (kind ?? "").lowercased() {
        case "user": return "person.fill"
        case "assistant": return "sparkle"
        case "edit": return "pencil"
        case "bash": return "terminal"
        case "branch_summary": return "arrow.triangle.branch"
        case "model": return "cpu"
        case "thinking": return "brain"
        default: return "circle.fill"
        }
    }

    private func tint(_ node: ThreadNode) -> Color {
        if node.isCurrent { return palette.primary }
        switch (node.kind ?? "").lowercased() {
        case "edit": return palette.warning
        case "branch_summary": return palette.primary
        default: return palette.mutedForeground
        }
    }
}
