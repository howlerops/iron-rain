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
    /// Fires when the user activates an action. The second argument carries a form's collected
    /// values (nil for every other component).
    var onAction: ((UIComponentAction, [String: JSONValue]?) -> Void)? = nil

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            content
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(10)
        .background(palette.secondary.opacity(0.25), in: OculusShape.rounded(OculusRadius.md))
        .overlay(OculusShape.rounded(OculusRadius.md).strokeBorder(palette.border))
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
                    InteractiveView(props: $0, actions: component.actions ?? [], palette: palette,
                                    onAction: { a in onAction?(a, nil) })
                }
            case "form":
                decoded(FormProps.self) {
                    FormView(props: $0, actions: component.actions ?? [], palette: palette, onAction: onAction)
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
            .font(.caption2.weight(.semibold))
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
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @State private var shimmer = false
    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            ForEach(0..<rows, id: \.self) { _ in
                OculusShape.rounded(OculusRadius.sm / 2).fill(palette.muted.opacity(shimmer ? 0.25 : 0.5))
                    .frame(height: 12).frame(maxWidth: .infinity)
            }
        }
        // An indefinitely repeating pulse is precisely what Reduce Motion exists to stop. The bars
        // stay — they still read as "something is loading here" — they just hold still.
        .onAppear {
            guard !reduceMotion else { return }
            withAnimation(.easeInOut(duration: 0.8).repeatForever(autoreverses: true)) { shimmer = true }
        }
        .accessibilityElement(children: .ignore)
        .accessibilityLabel("Loading")
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
        VStack(alignment: .leading, spacing: 6) {
            if let cap = props.caption, !cap.isEmpty {
                Text(cap).font(.footnote.weight(.semibold)).foregroundStyle(palette.foreground)
            }
            ScrollView(.horizontal, showsIndicators: true) {
                // TanStack-style data table: contiguous header bar, zebra rows, hairline-bordered
                // rounded container. Cells stretch to their COLUMN width (maxWidth .infinity — the
                // ideal width still drives column sizing) so backgrounds are continuous, unlike the
                // old per-cell patches.
                Grid(alignment: .topLeading, horizontalSpacing: 0, verticalSpacing: 0) {
                    GridRow {
                        ForEach(Array(cols.enumerated()), id: \.offset) { _, c in
                            Text(c.label)
                                .font(.caption.weight(.semibold))
                                .foregroundStyle(palette.mutedForeground)
                                .padding(.horizontal, 10).padding(.vertical, 7)
                                .frame(minWidth: 70, maxWidth: .infinity, alignment: align(c.align))
                                .background(palette.muted.opacity(0.45))
                        }
                    }
                    ForEach(Array(rows.enumerated()), id: \.offset) { ri, row in
                        GridRow {
                            ForEach(Array(cols.enumerated()), id: \.offset) { ci, c in
                                Text(ci < row.count ? row[ci].displayString : "")
                                    .font(.caption.monospacedDigit())
                                    .foregroundStyle(palette.foreground)
                                    .padding(.horizontal, 10).padding(.vertical, 6)
                                    .frame(minWidth: 70, maxWidth: .infinity, alignment: align(c.align))
                                    .fixedSize(horizontal: false, vertical: true)
                                    .lineLimit(4)
                                    .background(ri % 2 == 1 ? palette.muted.opacity(0.16) : Color.clear)
                            }
                        }
                    }
                }
                .overlay(OculusShape.rounded(OculusRadius.sm).strokeBorder(palette.border))
                .clipShape(OculusShape.rounded(OculusRadius.sm))
                .padding(1) // keep the hairline stroke inside the scroll viewport
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
        switch s { case "done": return palette.success; case "active": return palette.primary
        case "failed": return palette.destructive
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
        .background(tint.opacity(0.12), in: OculusShape.rounded(OculusRadius.sm))
        .overlay(OculusShape.rounded(OculusRadius.sm).strokeBorder(tint.opacity(0.4)))
    }
    private var symbol: String {
        switch props.level { case "error": return "exclamationmark.octagon.fill"; case "warn": return "exclamationmark.triangle.fill"
        case "success": return "checkmark.seal.fill"; default: return "info.circle.fill" }
    }
    private var tint: Color {
        switch props.level { case "error": return palette.destructive; case "warn": return palette.warning
        case "success": return palette.success
        default: return palette.info }
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
            }.foregroundStyle(palette.primaryText)
            if let patch = props.patch, !patch.isEmpty {
                let lines = patch.split(separator: "\n", omittingEmptySubsequences: false).prefix(Self.maxLines)
                VStack(alignment: .leading, spacing: 0) {
                    ForEach(Array(lines.enumerated()), id: \.offset) { _, ln in
                        Text(String(ln)).font(.system(.caption2, design: .monospaced))
                            .foregroundStyle(lineColor(String(ln)))
                            .frame(maxWidth: .infinity, alignment: .leading)
                    }
                }
                // Concentric with the component card it sits in: the gap between the two shapes is
                // the card's own 10pt padding, so the inner radius is derived from that rather than
                // picked by eye — a fixed inner radius is what makes nested corners visibly flare.
                .padding(6)
                .background(palette.background,
                            in: OculusShape.concentric(outer: OculusRadius.md, padding: 10))
            } else {
                Text("Diff available — open the session to review.").font(.caption).foregroundStyle(palette.mutedForeground)
            }
        }
    }
    private func lineColor(_ l: String) -> Color {
        if l.hasPrefix("+") { return palette.diffAdded }
        if l.hasPrefix("-") { return palette.diffRemoved }
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
                        .frame(minHeight: 44)
                        .background(bg(a), in: OculusShape.rounded(OculusRadius.sm))
                        .overlay(OculusShape.rounded(OculusRadius.sm).strokeBorder(border(a)))
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

// MARK: - form

/// A form's declarative field list. This is the INTERPRETER component: instead of adding a compiled
/// case per question shape, the model declares fields and this renders them generically — one
/// catalog entry covering an open-ended space. The field types stay a fixed, daemon-validated set,
/// so the closed-catalog safety model is preserved.
struct FormProps: Decodable {
    struct Option: Decodable, Identifiable {
        let value: String
        let label: String?
        var id: String { value }
    }
    struct Field: Decodable, Identifiable {
        let id: String
        let type: String        // text | textarea | select | toggle | number
        let label: String?
        let placeholder: String?
        let value: String?
        let options: [Option]?
    }
    let title: String?
    let fields: [Field]
    let submitLabel: String?

    enum CodingKeys: String, CodingKey {
        case title, fields
        case submitLabel = "submit_label"
    }
}

/// Renders a form and submits its collected values with the chosen action.
struct FormView: View {
    let props: FormProps
    let actions: [UIComponentAction]
    let palette: OculusPalette
    var onAction: ((UIComponentAction, [String: JSONValue]?) -> Void)? = nil

    /// Values keyed by field id, seeded from each field's default.
    @State private var values: [String: String] = [:]
    @State private var toggles: [String: Bool] = [:]
    @State private var submitted = false

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            if let t = props.title, !t.isEmpty {
                Text(t).font(.callout.bold()).foregroundStyle(palette.foreground)
            }
            ForEach(props.fields) { f in field(f) }
            HStack(spacing: 8) {
                Spacer()
                ForEach(actions) { a in
                    Button(a.label ?? props.submitLabel ?? "Submit") { submit(a) }
                        .buttonStyle(.borderedProminent).tint(palette.primary)
                        .disabled(submitted)
                }
            }
        }
        .opacity(submitted ? 0.6 : 1)          // answered forms read as spent, like the other interactives
        .disabled(submitted)                    // one submission per form: it becomes a user turn
        .onAppear(perform: seedDefaults)
    }

    @ViewBuilder private func field(_ f: FormProps.Field) -> some View {
        // The visible caption is a separate Text, so every control below has to carry the label
        // itself — a bare `Toggle("")` announces as "switch button" with no hint of WHAT it switches.
        let name = f.label ?? f.id
        VStack(alignment: .leading, spacing: 3) {
            if let l = f.label, !l.isEmpty {
                Text(l).font(.caption.weight(.medium)).foregroundStyle(palette.mutedForeground)
            }
            Group {
                switch f.type {
                case "textarea":
                    TextEditor(text: binding(f.id))
                        .font(.footnote)
                        // minHeight, not height: at larger Dynamic Type sizes a fixed 64pt box clipped
                        // the second line of what the user was typing.
                        .frame(minHeight: 64)
                        .overlay(OculusShape.rounded(OculusRadius.sm).strokeBorder(palette.border))
                case "select":
                    Picker(name, selection: binding(f.id)) {
                        ForEach(f.options ?? []) { o in Text(o.label ?? o.value).tag(o.value) }
                    }
                    .labelsHidden().pickerStyle(.menu)
                case "toggle":
                    Toggle(name, isOn: toggleBinding(f.id))
                        .labelsHidden().toggleStyle(.switch).tint(palette.primary)
                case "number":
                    TextField(f.placeholder ?? "", text: binding(f.id))
                        .textFieldStyle(.roundedBorder)
                        #if os(iOS)
                        .keyboardType(.numberPad)
                        #endif
                default:
                    TextField(f.placeholder ?? "", text: binding(f.id)).textFieldStyle(.roundedBorder)
                }
            }
            .accessibilityLabel(name)
        }
    }

    private func binding(_ id: String) -> Binding<String> {
        Binding(get: { values[id] ?? "" }, set: { values[id] = $0 })
    }
    private func toggleBinding(_ id: String) -> Binding<Bool> {
        Binding(get: { toggles[id] ?? false }, set: { toggles[id] = $0 })
    }

    private func seedDefaults() {
        guard values.isEmpty && toggles.isEmpty else { return }
        for f in props.fields {
            if f.type == "toggle" {
                toggles[f.id] = (f.value == "true")
            } else if let v = f.value {
                values[f.id] = v
            } else if f.type == "select", let first = f.options?.first {
                values[f.id] = first.value // a select must always carry a valid value
            }
        }
    }

    private func submit(_ a: UIComponentAction) {
        submitted = true
        var out: [String: JSONValue] = [:]
        for f in props.fields {
            switch f.type {
            case "toggle":
                out[f.label ?? f.id] = .bool(toggles[f.id] ?? false)
            case "number":
                let raw = values[f.id] ?? ""
                if let n = Double(raw) { out[f.label ?? f.id] = .number(n) }
                else if !raw.isEmpty { out[f.label ?? f.id] = .string(raw) }
            default:
                let raw = values[f.id] ?? ""
                if !raw.isEmpty { out[f.label ?? f.id] = .string(raw) }
            }
        }
        onAction?(a, out)
    }
}
