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
    }

    private func section(_ title: String, tint: Color, note: String?) -> some View {
        VStack(alignment: .leading, spacing: OculusSpace.xxs) {
            Text(title.uppercased())
                .font(.system(size: 10, weight: .semibold)).tracking(0.8)
                .foregroundStyle(tint)
            if let note {
                Text(note).font(.system(size: 11)).foregroundStyle(palette.mutedForeground)
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
            VStack(alignment: .leading, spacing: 3) {
                Text(r.description).font(.system(size: 13)).foregroundStyle(palette.foreground)
                    .multilineTextAlignment(.leading)
                HStack(spacing: 6) {
                    if let p = r.provider, !p.isEmpty { tag(p) }
                    if let n = r.projectName, !n.isEmpty { tag(n) }
                    else if let pid = r.projectID, !pid.isEmpty { tag("one project") }
                    if let path = r.pathPrefix, !path.isEmpty { tag(path, mono: true) }
                }
            }
            Spacer(minLength: 6)
            Button {
                Task { await model.deleteApprovalRule(index: r.index) }
            } label: {
                Image(systemName: "trash").foregroundStyle(palette.mutedForeground)
            }
            .buttonStyle(.plain)
            .help("Remove this rule — the agent will ask again next time.")
        }
        }
    }

    private func tag(_ s: String, mono: Bool = false) -> some View {
        Text(s)
            .font(.system(size: 10, design: mono ? .monospaced : .default))
            .lineLimit(1).truncationMode(.head)
            .padding(.horizontal, 5).padding(.vertical, 1.5)
            .background(palette.input)
            .clipShape(RoundedRectangle(cornerRadius: 4))
            .foregroundStyle(palette.mutedForeground)
    }

}
