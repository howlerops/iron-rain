import SwiftUI
import OculusKit

// Generative-UI rendering (see the generative-UI plan). The daemon projects a normalized
// `UIComponent` (from tool events or a fenced ```iron:ui``` block); this file is the CLIENT-owned
// registry that switches the component's `type` to a fixed native view. Nothing here is
// model-authored — the model supplies only inert `props`. Unknown component / bad schema / decode
// failure degrades to `fallbackText` rendered as markdown, never a crash. Every component wears an
// "agent" provenance chip so a model can't forge a system dialog.

/// The registry: switches a UIComponent to its native view, with a running skeleton, an error/unknown
/// fallback, and the provenance chip. `onAction` fires when the user activates an interactive action.
struct UIComponentView: View {
    let component: UIComponent
    let palette: OculusPalette
    var onAction: ((UIComponentAction) -> Void)? = nil

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            content
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(10)
        .background(palette.secondary.opacity(0.25), in: RoundedRectangle(cornerRadius: 10))
        .overlay(RoundedRectangle(cornerRadius: 10).stroke(palette.border))
        .overlay(alignment: .topTrailing) { provenanceChip }
    }

    @ViewBuilder private var content: some View {
        if component.status == "running" {
            SkeletonView(kind: component.component, palette: palette)
        } else if component.status == "error" {
            fallback
        } else {
            switch component.component {
            case "table":     decoded(TableProps.self) { TableView(props: $0, palette: palette) }
            case "checklist", "plan": decoded(ChecklistProps.self) { ChecklistView(props: $0, palette: palette) }
            case "callout":   decoded(CalloutProps.self) { CalloutView(props: $0, palette: palette) }
            case "diff":      decoded(DiffProps.self) { DiffCardView(props: $0, palette: palette) }
            case "choice", "confirm":
                decoded(InteractiveProps.self) {
                    InteractiveView(props: $0, actions: component.actions ?? [], palette: palette, onAction: onAction)
                }
            default:          fallback   // unknown component → visible markdown fallback
            }
        }
    }

    /// Decodes the component's typed props and hands them to `view`; on any decode failure (bad shape,
    /// newer schema) it falls back to markdown so a malformed payload never breaks the transcript.
    @ViewBuilder private func decoded<P: Decodable, V: View>(_ type: P.Type, @ViewBuilder _ view: (P) -> V) -> some View {
        if let p = component.props?.decoded(type) { view(p) } else { fallback }
    }

    private var fallback: some View {
        // Reuse the chat markdown renderer so the fallback reads like normal assistant text.
        ChatMarkdownView(text: component.fallbackText.isEmpty ? "*(unsupported UI component)*" : component.fallbackText, palette: palette)
    }

    private var provenanceChip: some View {
        Text("agent")
            .font(.system(size: 8, weight: .semibold))
            .foregroundStyle(palette.mutedForeground)
            .padding(.horizontal, 4).padding(.vertical, 1)
            .background(Capsule().fill(palette.muted.opacity(0.5)))
            .padding(6)
    }
}

// MARK: - Skeleton (status: running)

private struct SkeletonView: View {
    let kind: String
    let palette: OculusPalette
    @State private var shimmer = false
    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            ForEach(0..<rows, id: \.self) { _ in
                RoundedRectangle(cornerRadius: 4).fill(palette.muted.opacity(shimmer ? 0.25 : 0.5))
                    .frame(height: 12).frame(maxWidth: .infinity)
            }
        }
        .onAppear { withAnimation(.easeInOut(duration: 0.8).repeatForever(autoreverses: true)) { shimmer = true } }
    }
    private var rows: Int { kind == "callout" ? 1 : (kind == "table" ? 4 : 3) }
}

// MARK: - table

struct TableProps: Decodable {
    struct Column: Decodable { let key: String?; let label: String; let align: String? }
    let columns: [Column]
    let rows: [[JSONValue]]
    let caption: String?
}

private struct TableView: View {
    let props: TableProps
    let palette: OculusPalette
    private static let maxRows = 500, maxCols = 20

    var body: some View {
        let cols = Array(props.columns.prefix(Self.maxCols))
        let rows = Array(props.rows.prefix(Self.maxRows))
        VStack(alignment: .leading, spacing: 0) {
            if let cap = props.caption, !cap.isEmpty {
                Text(cap).font(.caption.bold()).foregroundStyle(palette.mutedForeground).padding(.bottom, 4)
            }
            ScrollView(.horizontal, showsIndicators: true) {
                VStack(alignment: .leading, spacing: 0) {
                    HStack(spacing: 0) {
                        ForEach(Array(cols.enumerated()), id: \.offset) { _, c in
                            Text(c.label).font(.caption.bold()).foregroundStyle(palette.foreground)
                                .frame(minWidth: 70, alignment: align(c.align)).padding(.horizontal, 8).padding(.vertical, 5)
                        }
                    }
                    .background(palette.muted.opacity(0.3))
                    Divider().overlay(palette.border)
                    ForEach(Array(rows.enumerated()), id: \.offset) { _, row in
                        HStack(spacing: 0) {
                            ForEach(Array(cols.enumerated()), id: \.offset) { ci, c in
                                Text(ci < row.count ? row[ci].displayString : "")
                                    .font(.caption.monospacedDigit()).foregroundStyle(palette.foreground)
                                    .frame(minWidth: 70, alignment: align(c.align)).padding(.horizontal, 8).padding(.vertical, 4)
                                    .lineLimit(1)
                            }
                        }
                        Divider().overlay(palette.border.opacity(0.3))
                    }
                }
            }
            if props.rows.count > Self.maxRows {
                Text("+\(props.rows.count - Self.maxRows) more rows").font(.caption2).foregroundStyle(palette.mutedForeground).padding(.top, 4)
            }
        }
    }
    private func align(_ a: String?) -> Alignment { a == "right" ? .trailing : (a == "center" ? .center : .leading) }
}

// MARK: - checklist / plan

struct ChecklistProps: Decodable {
    struct Item: Decodable { let id: String?; let text: String; let status: String? }
    let title: String?
    let items: [Item]
}

private struct ChecklistView: View {
    let props: ChecklistProps
    let palette: OculusPalette
    var body: some View {
        VStack(alignment: .leading, spacing: 5) {
            if let t = props.title, !t.isEmpty { Text(t).font(.caption.bold()).foregroundStyle(palette.foreground) }
            ForEach(Array(props.items.prefix(200).enumerated()), id: \.offset) { _, it in
                HStack(alignment: .top, spacing: 7) {
                    Image(systemName: symbol(it.status)).font(.caption).foregroundStyle(color(it.status)).padding(.top, 1)
                    Text(it.text).font(.callout).foregroundStyle(palette.foreground)
                        .strikethrough(it.status == "done", color: palette.mutedForeground)
                    Spacer(minLength: 0)
                }
            }
        }
    }
    private func symbol(_ s: String?) -> String {
        switch s { case "done": return "checkmark.circle.fill"; case "active": return "circle.dotted"
        case "failed": return "xmark.circle.fill"; default: return "circle" }
    }
    private func color(_ s: String?) -> Color {
        switch s { case "done": return .green; case "active": return palette.primary; case "failed": return .orange
        default: return palette.mutedForeground }
    }
}

// MARK: - callout

struct CalloutProps: Decodable { let level: String?; let title: String?; let body: String }

private struct CalloutView: View {
    let props: CalloutProps
    let palette: OculusPalette
    var body: some View {
        HStack(alignment: .top, spacing: 8) {
            Image(systemName: symbol).foregroundStyle(tint).font(.callout).padding(.top, 1)
            VStack(alignment: .leading, spacing: 2) {
                if let t = props.title, !t.isEmpty { Text(t).font(.callout.bold()).foregroundStyle(palette.foreground) }
                ChatMarkdownView(text: props.body, palette: palette)
            }
        }
        .padding(8)
        .background(tint.opacity(0.12), in: RoundedRectangle(cornerRadius: 8))
        .overlay(RoundedRectangle(cornerRadius: 8).stroke(tint.opacity(0.4)))
    }
    private var symbol: String {
        switch props.level { case "error": return "exclamationmark.octagon.fill"; case "warn": return "exclamationmark.triangle.fill"
        case "success": return "checkmark.seal.fill"; default: return "info.circle.fill" }
    }
    private var tint: Color {
        switch props.level { case "error": return .red; case "warn": return .orange; case "success": return .green
        default: return palette.primary }
    }
}

// MARK: - diff

struct DiffProps: Decodable { let path: String?; let patch: String?; let ref: String? }

private struct DiffCardView: View {
    let props: DiffProps
    let palette: OculusPalette
    private static let maxLines = 200
    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack(spacing: 6) {
                Image(systemName: "plus.forwardslash.minus").font(.caption)
                Text(props.path ?? "changes").font(.caption.bold().monospaced()).lineLimit(1)
            }.foregroundStyle(palette.primary)
            if let patch = props.patch, !patch.isEmpty {
                let lines = patch.split(separator: "\n", omittingEmptySubsequences: false).prefix(Self.maxLines)
                VStack(alignment: .leading, spacing: 0) {
                    ForEach(Array(lines.enumerated()), id: \.offset) { _, ln in
                        Text(String(ln)).font(.system(.caption2, design: .monospaced))
                            .foregroundStyle(lineColor(String(ln)))
                            .frame(maxWidth: .infinity, alignment: .leading)
                    }
                }
                .padding(6).background(palette.background, in: RoundedRectangle(cornerRadius: 6))
            } else {
                Text("Diff available — open the session to review.").font(.caption).foregroundStyle(palette.mutedForeground)
            }
        }
    }
    private func lineColor(_ l: String) -> Color {
        if l.hasPrefix("+") { return .green }
        if l.hasPrefix("-") { return .red }
        if l.hasPrefix("@@") { return palette.primary }
        return palette.mutedForeground
    }
}

// MARK: - choice / confirm (interactive)

struct InteractiveProps: Decodable { let prompt: String?; let title: String?; let multiple: Bool? }

private struct InteractiveView: View {
    let props: InteractiveProps
    let actions: [UIComponentAction]
    let palette: OculusPalette
    var onAction: ((UIComponentAction) -> Void)? = nil
    @State private var chosen: String?   // simple single-select feedback

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            if let t = props.title, !t.isEmpty { Text(t).font(.callout.bold()) }
            if let p = props.prompt, !p.isEmpty { ChatMarkdownView(text: p, palette: palette) }
            VStack(spacing: 6) {
                ForEach(actions) { a in
                    Button {
                        chosen = a.id
                        onAction?(a)
                    } label: {
                        HStack {
                            Text(a.label ?? a.id).font(.callout.weight(.medium))
                            Spacer()
                            if chosen == a.id { Image(systemName: "checkmark").font(.caption) }
                        }
                        .foregroundStyle(fg(a))
                        .padding(.horizontal, 12).padding(.vertical, 8)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .background(bg(a), in: RoundedRectangle(cornerRadius: 8))
                        .overlay(RoundedRectangle(cornerRadius: 8).stroke(border(a)))
                    }
                    .buttonStyle(.plain)
                    .disabled(chosen != nil)
                }
            }
            if chosen != nil {
                Text("Sent — the agent will continue.").font(.caption2).foregroundStyle(palette.mutedForeground)
            }
        }
    }
    private func fg(_ a: UIComponentAction) -> Color { a.style == "destructive" ? .white : palette.foreground }
    private func bg(_ a: UIComponentAction) -> Color {
        if a.style == "destructive" { return palette.destructive.opacity(0.9) }
        return chosen == a.id ? palette.primary.opacity(0.18) : palette.muted.opacity(0.35)
    }
    private func border(_ a: UIComponentAction) -> Color { a.style == "destructive" ? .clear : palette.border }
}

// MARK: - helpers

extension JSONValue {
    /// A compact, human-facing rendering of a JSON scalar for table cells (numbers lose a trailing .0).
    var displayString: String {
        switch self {
        case .null: return ""
        case .bool(let b): return b ? "true" : "false"
        case .number(let n): return n == n.rounded() ? String(Int(n)) : String(n)
        case .string(let s): return s
        case .array(let a): return a.map { $0.displayString }.joined(separator: ", ")
        case .object: return "{…}"
        }
    }
}
