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

    public var body: some View {
        VStack(spacing: 0) {
            header
            Divider().overlay(palette.border)
            if model.approvalRules.isEmpty {
                emptyState
            } else {
                List {
                    if !denies.isEmpty {
                        Section {
                            ForEach(denies, id: \.index) { row($0) }
                        } header: {
                            Text("Never allowed").foregroundStyle(palette.destructive)
                        } footer: {
                            Text("Checked first — a deny always beats an allow.")
                                .font(.caption).foregroundStyle(palette.mutedForeground)
                        }
                    }
                    if !allows.isEmpty {
                        Section("Always allowed") {
                            ForEach(allows, id: \.index) { row($0) }
                        }
                    }
                }
                #if os(macOS)
                .listStyle(.inset)
                #endif
            }
        }
        .frame(minWidth: 460, minHeight: 340)
        .background(palette.background)
        .task { await model.loadApprovalRules() }
    }

    private var header: some View {
        HStack {
            VStack(alignment: .leading, spacing: 2) {
                Text("Approval rules").font(.headline).foregroundStyle(palette.foreground)
                Text("What your agents may do without asking.")
                    .font(.caption).foregroundStyle(palette.mutedForeground)
            }
            Spacer()
            if let onClose {
                Button("Done", action: onClose).keyboardShortcut(.defaultAction)
            }
        }
        .padding(14)
    }

    private func row(_ r: ApprovalRuleInfo) -> some View {
        HStack(alignment: .top, spacing: 10) {
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
        .padding(.vertical, 4)
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

    private var emptyState: some View {
        VStack(spacing: 10) {
            Image(systemName: "checkmark.shield").font(.system(size: 30))
                .foregroundStyle(palette.mutedForeground.opacity(0.5))
            Text("No standing rules").font(.headline).foregroundStyle(palette.foreground)
            Text("Every tool your agents run still asks first. Choosing “Always…” on an approval saves a rule here — scoped to the command, folder, or project you picked.")
                .font(.callout).foregroundStyle(palette.mutedForeground)
                .multilineTextAlignment(.center).frame(maxWidth: 360)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .padding(24)
    }
}
