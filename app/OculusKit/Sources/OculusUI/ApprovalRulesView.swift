import SwiftUI
import OculusKit

/// The persisted "always allow / never allow" rules, and the one place to revoke them.
///
/// Before this screen a rule could only be CREATED (by answering an approval with Always) and never
/// inspected or taken back — the only way out was hand-editing ~/.oculus/approval-rules.json. Rules
/// are enforced daemon-side for every harness, so this is the honest, complete list of what your
/// agents may do without asking.
public struct ApprovalRulesView: View {
    @ObservedObject var model: Model
    let palette: OculusPalette
    var onClose: (() -> Void)? = nil

    public init(model: Model, palette: OculusPalette, onClose: (() -> Void)? = nil) {
        self.model = model; self.palette = palette; self.onClose = onClose
    }

    private var allows: [ApprovalRuleInfo] { model.approvalRules.filter { $0.action != "deny" } }
    private var denies: [ApprovalRuleInfo] { model.approvalRules.filter { $0.action == "deny" } }

    @State private var query = ""
    /// The rule a delete is staged against. Deleting is not undoable and there is no other copy of
    /// the rule anywhere, so nothing here removes one on a single tap.
    @State private var pendingDelete: ApprovalRuleInfo? = nil

    private func matching(_ rs: [ApprovalRuleInfo]) -> [ApprovalRuleInfo] {
        let q = query.trimmingCharacters(in: .whitespaces).lowercased()
        guard !q.isEmpty else { return rs }
        return rs.filter {
            $0.description.lowercased().contains(q)
            || ($0.tool ?? "").lowercased().contains(q)
            || ($0.pattern ?? "").lowercased().contains(q)
            || ($0.projectName ?? "").lowercased().contains(q)
        }
    }

    public var body: some View {
        OculusSheet(
            title: "Approval rules",
            subtitle: "What your agents may do without asking.",
            palette: palette,
            search: model.approvalRules.count >= 6 ? $query : nil,
            searchPrompt: "Search rules",
            onClose: onClose
        ) {
            if model.approvalRules.isEmpty {
                SheetEmptyState(icon: "checkmark.shield",
                                title: "No standing rules",
                                message: "Every tool your agents run still asks first. Choosing “Always…” on an approval saves a rule here — scoped to the command, folder, or project you picked.",
                                palette: palette)
            } else {
                let denies = matching(model.approvalRules.filter { $0.action == "deny" })
                let allows = matching(model.approvalRules.filter { $0.action != "deny" })
                if denies.isEmpty && allows.isEmpty {
                    SheetEmptyState(icon: "line.3.horizontal.decrease.circle",
                                    title: "Nothing matches",
                                    message: "No rule matching “\(query)”.",
                                    palette: palette) {
                        Button("Clear search") { query = "" }.buttonStyle(.bordered)
                    }
                }
                if !denies.isEmpty {
                    section("Never allowed", tint: palette.destructive,
                            note: "Checked first — a deny always beats an allow.")
                    VStack(spacing: OculusSpace.sm) { ForEach(denies, id: \.index) { row($0) } }
                }
                if !allows.isEmpty {
                    section("Always allowed", tint: palette.mutedForeground, note: nil)
                    VStack(spacing: OculusSpace.sm) { ForEach(allows, id: \.index) { row($0) } }
                }
            }
        }
        .task { await model.loadApprovalRules() }
        // Two dialogs rather than one, because the two directions are not the same decision.
        // Removing a DENY widens what agents may do without asking — the rule that was checked first
        // and beat every allow simply stops existing — so that one names the consequence out loud.
        .confirmationDialog(
            "Remove this block?",
            isPresented: Binding(get: { pendingDelete?.action == "deny" },
                                 set: { if !$0 { pendingDelete = nil } }),
            titleVisibility: .visible,
            presenting: pendingDelete
        ) { r in
            Button("Remove block", role: .destructive) { delete(r) }
            Button("Cancel", role: .cancel) { pendingDelete = nil }
        } message: { r in
            Text("“\(r.description)” is currently never allowed, and a deny beats every allow. Remove it and your agents will be able to do this after asking once — or immediately, if an allow rule already covers it.")
        }
        .confirmationDialog(
            "Remove this rule?",
            isPresented: Binding(get: { pendingDelete != nil && pendingDelete?.action != "deny" },
                                 set: { if !$0 { pendingDelete = nil } }),
            titleVisibility: .visible,
            presenting: pendingDelete
        ) { r in
            Button("Remove rule", role: .destructive) { delete(r) }
            Button("Cancel", role: .cancel) { pendingDelete = nil }
        } message: { r in
            Text("“\(r.description)” will no longer run without asking. The agent will ask you again next time.")
        }
    }

    /// `deleteApprovalRule` returns Void and swallows its own transport error, so a revoke that never
    /// reached the daemon looks exactly like one that did. The daemon answers with the whole rule
    /// list, so a list that didn't shrink is the honest signal that nothing was removed — on a
    /// security surface, silently failing OPEN is the failure worth catching.
    private func delete(_ r: ApprovalRuleInfo) {
        pendingDelete = nil
        let before = model.approvalRules.count
        Task {
            await model.deleteApprovalRule(index: r.index)
            if model.approvalRules.count >= before {
                model.setError("Couldn’t remove the rule",
                               "“\(r.description)” is still in effect. Check the daemon is connected and try again.")
            }
        }
    }

    private func section(_ title: String, tint: Color, note: String?) -> some View {
        VStack(alignment: .leading, spacing: OculusSpace.xxs) {
            Text(title)
                .font(.footnote.weight(.semibold))
                .foregroundStyle(tint)
            if let note {
                Text(note).font(.caption).foregroundStyle(palette.mutedForeground)
            }
        }
        .padding(.top, OculusSpace.xs)
    }

    private func row(_ r: ApprovalRuleInfo) -> some View {
        SheetCard(palette: palette) {
        HStack(alignment: .top, spacing: OculusSpace.sm) {
            Image(systemName: r.action == "deny" ? "hand.raised.fill" : "checkmark.shield.fill")
                .foregroundStyle(r.action == "deny" ? palette.destructive : palette.primary)
                .padding(.top, 2)
                // Not decorative — allow vs deny is the whole meaning of the row, and the section
                // header that carries it visually is a separate element to VoiceOver.
                .accessibilityLabel(r.action == "deny" ? "Never allowed" : "Always allowed")
            VStack(alignment: .leading, spacing: 3) {
                Text(r.description).font(.subheadline).foregroundStyle(palette.foreground)
                    .multilineTextAlignment(.leading)
                HStack(spacing: 6) {
                    if let p = r.provider, !p.isEmpty { tag(p) }
                    if let n = r.projectName, !n.isEmpty { tag(n) }
                    else if let pid = r.projectID, !pid.isEmpty { tag("one project") }
                    if let path = r.pathPrefix, !path.isEmpty { tag(path, mono: true) }
                }
            }
            Spacer(minLength: 6)
            // Destructive role + the destructive colour: rendering a delete in the DE-EMPHASIZED
            // muted grey inverts the signal, telling the eye this is the safe, minor control.
            Button(role: .destructive) {
                pendingDelete = r
            } label: {
                Image(systemName: "trash").foregroundStyle(palette.destructive)
            }
            .buttonStyle(.plain)
            .accessibilityLabel(r.action == "deny" ? "Remove block: \(r.description)"
                                                   : "Remove rule: \(r.description)")
            .help("Remove this rule — the agent will ask again next time.")
            .sheetTapTarget()
        }
        }
    }

    private func tag(_ s: String, mono: Bool = false) -> some View {
        Text(s)
            .font(.system(.caption2, design: mono ? .monospaced : .default))
            .lineLimit(1).truncationMode(.head)
            .padding(.horizontal, 5).padding(.vertical, 1.5)
            .background(palette.input)
            .clipShape(OculusShape.rounded(4))
            .foregroundStyle(palette.mutedForeground)
    }

}
