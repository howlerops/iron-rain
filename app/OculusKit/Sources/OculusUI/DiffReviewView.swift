import SwiftUI
import OculusKit

// MARK: - Diff model

/// The classification of a single unified-diff line, driving its background tint.
private enum DiffLineKind { case add, del, context, meta }

/// One parsed diff line: its kind, the original text (prefix included), and the file line numbers
/// it occupies on each side.
///
/// The numbers are what make a review comment addressable. Without them the only things a reviewer
/// could point at were "this file" and "this hunk", so a note about one wrong line arrived at the
/// agent attached to forty lines of context and it had to guess which.
private struct DiffLineModel: Identifiable {
    let id = UUID()
    let kind: DiffLineKind
    let text: String
    /// Line number in the pre-change file (nil for added lines).
    let oldLine: Int?
    /// Line number in the post-change file (nil for deleted lines).
    let newLine: Int?

    /// The number a comment should cite — the new file's, falling back to the old file's for a
    /// deletion (which no longer exists in the new file).
    var citableLine: Int? { newLine ?? oldLine }
}

/// A single hunk (`@@ … @@` block) with its lines.
private struct DiffHunkModel: Identifiable {
    let id = UUID()
    let header: String
    let lines: [DiffLineModel]

    /// The hunk reconstructed as text (header + lines), capped to `maxLines` for prompts.
    func promptText(maxLines: Int = 40) -> String {
        var out = [header]
        out.append(contentsOf: lines.map(\.text))
        if out.count > maxLines {
            out = Array(out.prefix(maxLines))
            out.append("… (\(lines.count + 1 - maxLines) more lines)")
        }
        return out.joined(separator: "\n")
    }
}

/// One file's section of the diff: its path and hunks (add/del counts are derived).
private struct DiffFileModel: Identifiable {
    let id = UUID()
    let path: String
    let hunks: [DiffHunkModel]

    var additions: Int { hunks.reduce(0) { $0 + $1.lines.filter { $0.kind == .add }.count } }
    var deletions: Int { hunks.reduce(0) { $0 + $1.lines.filter { $0.kind == .del }.count } }

    /// All hunks concatenated, capped for a file-level prompt.
    func promptText(maxLines: Int = 40) -> String {
        var out: [String] = []
        for h in hunks {
            out.append(h.header)
            out.append(contentsOf: h.lines.map(\.text))
        }
        if out.count > maxLines {
            let extra = out.count - maxLines
            out = Array(out.prefix(maxLines))
            out.append("… (\(extra) more lines)")
        }
        return out.joined(separator: "\n")
    }
}

/// Parses a unified git diff into per-file sections. Splits on `diff --git a/… b/…`,
/// falling back to `+++ b/<path>` for the file path when no git header is present.
private enum DiffParser {
    static func parse(_ diff: String) -> [DiffFileModel] {
        var files: [DiffFileModel] = []
        var curPath: String? = nil
        var curHunks: [DiffHunkModel] = []
        var hunkHeader: String? = nil
        var hunkLines: [DiffLineModel] = []

        func closeHunk() {
            if let h = hunkHeader {
                curHunks.append(DiffHunkModel(header: h, lines: hunkLines))
            }
            hunkHeader = nil
            hunkLines = []
        }
        func closeFile() {
            closeHunk()
            if let p = curPath { files.append(DiffFileModel(path: p, hunks: curHunks)) }
            curPath = nil
            curHunks = []
        }

        // Running file line numbers for the hunk being parsed, seeded from its `@@` header.
        var oldNo = 0
        var newNo = 0

        let rawLines = diff.split(separator: "\n", omittingEmptySubsequences: false).map(String.init)
        for line in rawLines {
            if line.hasPrefix("diff --git") {
                closeFile()
                curPath = gitPath(from: line) ?? ""   // may be filled in by a later +++ line
            } else if line.hasPrefix("+++ ") {
                // Fall back to the `+++ b/<path>` header when `diff --git` gave no path.
                if curPath == nil || (curPath?.isEmpty ?? true), let p = plusPath(from: line) {
                    curPath = p
                }
                // `+++` never counts as an addition line.
            } else if line.hasPrefix("--- ") {
                continue
            } else if line.hasPrefix("@@") {
                if curPath == nil { curPath = "(changes)" }
                closeHunk()
                hunkHeader = line
                let starts = hunkStarts(from: line)
                oldNo = starts.old
                newNo = starts.new
            } else if hunkHeader != nil {
                let kind: DiffLineKind
                if line.hasPrefix("+") { kind = .add }
                else if line.hasPrefix("-") { kind = .del }
                else if line.hasPrefix("\\") { kind = .meta }   // "\ No newline at end of file"
                else { kind = .context }
                // Only the side a line exists on advances: an addition consumes a new-file line,
                // a deletion an old-file one, context both, and the "\ No newline" marker neither.
                var old: Int? = nil
                var new: Int? = nil
                switch kind {
                case .add: new = newNo; newNo += 1
                case .del: old = oldNo; oldNo += 1
                case .context: old = oldNo; new = newNo; oldNo += 1; newNo += 1
                case .meta: break
                }
                hunkLines.append(DiffLineModel(kind: kind, text: line, oldLine: old, newLine: new))
            }
            // else: file-header metadata (index / mode / similarity) — ignored.
        }
        closeFile()

        // Give any file that never resolved a path a readable fallback.
        return files.map { f in
            f.path.isEmpty ? DiffFileModel(path: "(changes)", hunks: f.hunks) : f
        }
    }

    /// The old/new starting line numbers from an `@@ -12,7 +14,9 @@ trailing context` header.
    ///
    /// Only the span BETWEEN the two `@@` markers is scanned: git appends the enclosing function
    /// signature after the second `@@`, and that text routinely contains `-` and `+` tokens which
    /// would otherwise be mistaken for the range spec.
    private static func hunkStarts(from header: String) -> (old: Int, new: Int) {
        guard let open = header.range(of: "@@"),
              let close = header.range(of: "@@", range: open.upperBound..<header.endIndex)
        else { return (1, 1) }
        var old = 1
        var new = 1
        for token in header[open.upperBound..<close.lowerBound].split(separator: " ") {
            guard let mark = token.first, mark == "-" || mark == "+" else { continue }
            let digits = token.dropFirst().prefix { $0.isNumber }
            guard let n = Int(digits) else { continue }
            if mark == "-" { old = n } else { new = n }
        }
        return (old, new)
    }

    /// Extracts the "b/…" path from a `diff --git a/x b/x` header.
    private static func gitPath(from line: String) -> String? {
        guard let range = line.range(of: " b/") else { return nil }
        let p = String(line[range.upperBound...]).trimmingCharacters(in: .whitespaces)
        return p.isEmpty ? nil : p
    }

    /// Extracts the path from a `+++ b/path` line (nil for /dev/null).
    private static func plusPath(from line: String) -> String? {
        var p = String(line.dropFirst(4)).trimmingCharacters(in: .whitespaces)
        if p == "/dev/null" { return nil }
        if p.hasPrefix("b/") { p.removeFirst(2) }
        return p.isEmpty ? nil : p
    }
}

// MARK: - Prompt composition

private enum DiffPrompt {
    /// Composes the "address this" prompt sent to the agent.
    /// Format: ``Re: `<path>[:<line>]`[ <hunk header>]\n\n<comment>\n\n```<lang>\n<hunk text>\n``` ``
    ///
    /// `line` is the file line a reviewer tapped. It is defaulted so existing file- and hunk-level
    /// callers are unaffected, but without it the prompt cannot express "this line is wrong" — the
    /// agent receives the note attached to the whole hunk and has to infer the target.
    static func compose(path: String, hunkHeader: String?, comment: String, body: String,
                        line: Int? = nil) -> String {
        var head = "Re: `\(path)"
        if let line { head += ":\(line)" }
        head += "`"
        if let h = hunkHeader, !h.isEmpty { head += " \(h)" }
        let lang = fenceLanguage(forPath: path)
        return head + "\n\n" + comment.trimmingCharacters(in: .whitespacesAndNewlines)
            + "\n\n```\(lang)\n" + body + "\n```"
    }

    /// A markdown code-fence language hint from the file extension.
    static func fenceLanguage(forPath path: String) -> String {
        switch (path as NSString).pathExtension.lowercased() {
        case "swift": return "swift"
        case "go": return "go"
        case "js", "jsx", "mjs", "cjs": return "javascript"
        case "ts", "tsx": return "typescript"
        case "py": return "python"
        case "rs": return "rust"
        case "c", "h": return "c"
        case "cpp", "cc", "hpp": return "cpp"
        case "m": return "objectivec"
        case "java": return "java"
        case "kt": return "kotlin"
        case "json": return "json"
        case "md", "markdown": return "markdown"
        case "sh", "bash", "zsh": return "bash"
        case "yml", "yaml": return "yaml"
        default: return ""
        }
    }
}

// MARK: - DiffReviewView

/// A rich, phone-friendly diff reviewer: per-file collapsible sections with add/remove
/// tinted, monospaced hunks carrying old/new line numbers, and an inline "comment → prompt"
/// affordance that turns a review note — on a file, a hunk, or a single tapped line — into the
/// agent's next instruction.
public struct DiffReviewView: View {
    @ObservedObject var model: Model
    let palette: OculusPalette
    @Environment(\.colorScheme) private var scheme
    #if os(iOS)
    @Environment(\.horizontalSizeClass) private var hSize
    #endif
    @State private var refreshing = false
    /// nil = follow the width default; set once the reviewer picks explicitly.
    @State private var wrapOverride: Bool?

    public init(model: Model, palette: OculusPalette) {
        self.model = model
        self.palette = palette
    }

    private var compact: Bool {
        #if os(iOS)
        return hSize == .compact
        #else
        return false
        #endif
    }

    /// Soft wrap defaults ON at compact width. A 390pt phone leaves ~48 monospaced characters, so
    /// horizontally scrolling hunks meant two drags per real line — and that pan fought both the
    /// vertical transcript scroll and text selection. Wrapping trades the code grid for actually
    /// being able to read the change; on a wide window the grid wins and wrap stays off.
    private var wrapLines: Bool { wrapOverride ?? compact }

    private var theme: CodeTheme { .current(scheme) }
    private var files: [DiffFileModel] {
        guard let d = model.lastDiff, !d.isEmpty else { return [] }
        return DiffParser.parse(d)
    }

    public var body: some View {
        VStack(spacing: 0) {
            topBar
            Divider().overlay(palette.border)
            if files.isEmpty {
                emptyState
            } else {
                ScrollView {
                    LazyVStack(spacing: 10) {
                        ForEach(Array(files.enumerated()), id: \.element.id) { idx, file in
                            DiffFileCard(file: file,
                                         startExpanded: idx < 3,
                                         wrapLines: wrapLines,
                                         palette: palette,
                                         theme: theme,
                                         onSend: { prompt in Task { await model.send(prompt) } })
                        }
                    }
                    .padding(10)
                }
            }
        }
        .background(palette.background)
        .clipShape(OculusShape.rounded(OculusRadius.md))
        .overlay(OculusShape.rounded(OculusRadius.md).strokeBorder(palette.border, lineWidth: 1))
    }

    private var totals: (add: Int, del: Int) {
        files.reduce(into: (0, 0)) { acc, f in acc.0 += f.additions; acc.1 += f.deletions }
    }

    private var topBar: some View {
        HStack(spacing: 10) {
            if files.isEmpty {
                Text("No changes").font(.footnote.weight(.medium))
                    .foregroundStyle(palette.mutedForeground)
            } else {
                let t = totals
                Text("\(files.count) file\(files.count == 1 ? "" : "s")")
                    .font(.footnote.weight(.semibold)).foregroundStyle(palette.foreground)
                Text("+\(t.add)").font(.system(.footnote, design: .monospaced).weight(.semibold))
                    .foregroundStyle(palette.diffAdded)
                Text("-\(t.del)").font(.system(.footnote, design: .monospaced).weight(.semibold))
                    .foregroundStyle(palette.diffRemoved)
            }
            Spacer()
            if !files.isEmpty {
                Button { wrapOverride = !wrapLines } label: {
                    Image(systemName: wrapLines ? "arrow.turn.down.left" : "arrow.left.and.right")
                        .font(.footnote.weight(.medium))
                        .frame(width: 44, height: 44).contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                .foregroundStyle(wrapLines ? palette.primary : palette.mutedForeground)
                .help(wrapLines ? "Turn off soft wrap" : "Wrap long lines")
                .accessibilityLabel("Soft wrap")
                .accessibilityValue(wrapLines ? "On" : "Off")
            }
            Button {
                refreshing = true
                Task { await model.worktreeDiff(); refreshing = false }
            } label: {
                if refreshing { ProgressView().controlSize(.small) }
                else { Label("Refresh", systemImage: "arrow.triangle.branch").font(.footnote.weight(.medium)) }
            }
            .buttonStyle(.plain).foregroundStyle(palette.primaryText)
            .accessibilityLabel("Refresh diff")
        }
        // 44pt tall so the icon-only wrap toggle can carry a full-size touch target without the
        // bar growing taller than a standard toolbar when it is absent.
        .frame(minHeight: 44)
        .padding(.horizontal, 8)
        .background(palette.card.opacity(0.4))
    }

    private var emptyState: some View {
        VStack(spacing: 10) {
            Image(systemName: "checkmark.circle").font(.largeTitle).foregroundStyle(palette.mutedForeground)
            Text("No changes yet — tap Refresh diff.")
                .font(.footnote).foregroundStyle(palette.mutedForeground)
            Button {
                refreshing = true
                Task { await model.worktreeDiff(); refreshing = false }
            } label: {
                Label("Refresh diff", systemImage: "arrow.triangle.branch")
            }
            .buttonStyle(.borderedProminent).tint(palette.primary).controlSize(.small)
            .disabled(refreshing)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .padding(24)
    }
}

// MARK: - File card

private struct DiffFileCard: View {
    let file: DiffFileModel
    let startExpanded: Bool
    let wrapLines: Bool
    let palette: OculusPalette
    let theme: CodeTheme
    let onSend: (String) -> Void

    @State private var expanded: Bool
    @State private var commenting = false

    init(file: DiffFileModel, startExpanded: Bool, wrapLines: Bool, palette: OculusPalette,
         theme: CodeTheme, onSend: @escaping (String) -> Void) {
        self.file = file
        self.startExpanded = startExpanded
        self.wrapLines = wrapLines
        self.palette = palette
        self.theme = theme
        self.onSend = onSend
        _expanded = State(initialValue: startExpanded)
    }

    private var language: CodeLanguage { .infer(fromPath: file.path) }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            header
            if commenting {
                CommentComposer(placeholder: "Comment on this file…", palette: palette) { text in
                    onSend(DiffPrompt.compose(path: file.path, hunkHeader: nil,
                                              comment: text, body: file.promptText()))
                    commenting = false
                }
                .padding(.horizontal, 10).padding(.bottom, 8)
            }
            if expanded {
                ForEach(file.hunks) { hunk in
                    Divider().overlay(palette.border)
                    DiffHunkView(hunk: hunk, filePath: file.path, language: language,
                                 wrapLines: wrapLines, palette: palette, theme: theme, onSend: onSend)
                }
            }
        }
        .background(palette.card.opacity(0.3))
        .clipShape(OculusShape.rounded(OculusRadius.sm))
        .overlay(OculusShape.rounded(OculusRadius.sm).strokeBorder(palette.border, lineWidth: 1))
    }

    private var header: some View {
        HStack(spacing: 8) {
            Button { withAnimation(.easeInOut(duration: 0.15)) { expanded.toggle() } } label: {
                HStack(spacing: 8) {
                    Image(systemName: expanded ? "chevron.down" : "chevron.right")
                        .font(.caption.weight(.bold)).foregroundStyle(palette.mutedForeground)
                        .frame(width: 12)
                    Text((file.path as NSString).lastPathComponent)
                        .font(.system(.footnote, design: .monospaced).weight(.semibold))
                        .foregroundStyle(palette.foreground).lineLimit(1).truncationMode(.middle)
                    if file.additions > 0 {
                        Text("+\(file.additions)").font(.system(.caption, design: .monospaced).weight(.semibold))
                            .foregroundStyle(palette.diffAdded)
                    }
                    if file.deletions > 0 {
                        Text("-\(file.deletions)").font(.system(.caption, design: .monospaced).weight(.semibold))
                            .foregroundStyle(palette.diffRemoved)
                    }
                    Spacer(minLength: 0)
                }
                .frame(minHeight: 44)
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .accessibilityLabel(Text("\(file.path), \(file.additions) added, \(file.deletions) removed"))
            .accessibilityHint(expanded ? "Collapse file" : "Expand file")
            Button { withAnimation { commenting.toggle() } } label: {
                Image(systemName: commenting ? "bubble.left.fill" : "bubble.left")
                    .font(.subheadline).foregroundStyle(commenting ? palette.primary : palette.mutedForeground)
                    .frame(width: 44, height: 44).contentShape(Rectangle())
            }
            .buttonStyle(.plain).help("Comment on this file")
            .accessibilityLabel("Comment on this file")
        }
        .padding(.leading, 10).padding(.trailing, 4)
    }
}

// MARK: - Hunk

private struct DiffHunkView: View {
    let hunk: DiffHunkModel
    let filePath: String
    let language: CodeLanguage
    let wrapLines: Bool
    let palette: OculusPalette
    let theme: CodeTheme
    let onSend: (String) -> Void

    @State private var commenting = false
    /// The tapped line the composer is scoped to; nil means the comment covers the whole hunk.
    @State private var commentLine: DiffLineModel?

    /// Diff body type stays at a fixed size: code is read on a character grid, and letting Dynamic
    /// Type resize it would break the alignment between the marker column, the gutter and the code.
    private let bodySize: CGFloat = 12
    /// Advance width of SF Mono at `bodySize`, used only to size the number gutter.
    private let monoAdvance: CGFloat = 7.2

    private var gutterDigits: Int {
        let widest = hunk.lines.reduce(0) { max($0, max($1.oldLine ?? 0, $1.newLine ?? 0)) }
        return max(String(widest).count, 2)
    }
    private var numberColumnWidth: CGFloat { CGFloat(gutterDigits) * (monoAdvance - 1.2) }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack(spacing: 6) {
                Text(hunk.header)
                    .font(.system(.caption, design: .monospaced))
                    .foregroundStyle(palette.mutedForeground).lineLimit(1)
                Spacer(minLength: 0)
                Button { toggleHunkComment() } label: {
                    Image(systemName: commenting && commentLine == nil ? "bubble.left.fill" : "bubble.left")
                        .font(.footnote)
                        .foregroundStyle(commenting && commentLine == nil ? palette.primary : palette.mutedForeground)
                        .frame(width: 44, height: 44).contentShape(Rectangle())
                }
                .buttonStyle(.plain).help("Comment on this hunk")
                .accessibilityLabel("Comment on this hunk")
            }
            .frame(minHeight: 44)
            .padding(.leading, 10)
            .background(palette.primary.opacity(0.08))

            if commenting {
                CommentComposer(placeholder: composerPlaceholder, palette: palette) { text in
                    onSend(DiffPrompt.compose(path: filePath, hunkHeader: hunk.header,
                                              comment: text, body: hunk.promptText(),
                                              line: commentLine?.citableLine))
                    commenting = false
                    commentLine = nil
                }
                .padding(.horizontal, 10).padding(.vertical, 8)
            }

            if wrapLines {
                lines
            } else {
                ScrollView(.horizontal, showsIndicators: true) {
                    lines.fixedSize(horizontal: true, vertical: false)
                }
            }
        }
    }

    private var lines: some View {
        VStack(alignment: .leading, spacing: 0) {
            ForEach(hunk.lines) { line in
                row(line)
            }
        }
        .background(theme.background)
    }

    private var composerPlaceholder: String {
        if let n = commentLine?.citableLine { return "Comment on line \(n)…" }
        return "Comment on this hunk…"
    }

    private func toggleHunkComment() {
        withAnimation {
            if commenting && commentLine == nil { commenting = false }
            else { commentLine = nil; commenting = true }
        }
    }

    /// A tap on a line scopes the composer to it, so the prompt can carry `path:line`. Tapping the
    /// same line again closes the composer rather than leaving it stranded open.
    private func selectLine(_ line: DiffLineModel) {
        withAnimation {
            if commenting, commentLine?.id == line.id { commenting = false; commentLine = nil }
            else { commentLine = line; commenting = true }
        }
    }

    private func row(_ line: DiffLineModel) -> some View {
        let selected = commentLine?.id == line.id
        return Button { selectLine(line) } label: {
            HStack(alignment: .top, spacing: 0) {
                gutter(line)
                // The +/-/space marker gets its own column so it is never swallowed by a wrapped
                // continuation line, and so wrapped text hangs under the code rather than under
                // the gutter — otherwise a wrapped line is indistinguishable from a new one.
                Text(String(line.text.prefix(1)))
                    .foregroundColor(markerColor(line))
                    .frame(width: monoAdvance + 3, alignment: .leading)
                content(line)
            }
            .font(.system(size: bodySize, design: .monospaced))
            .padding(.vertical, 1)
            .padding(.trailing, 8)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(rowBackground(line, selected: selected))
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .accessibilityLabel(Text(accessibilityText(line)))
        .accessibilityHint(selected ? "Close the comment box" : "Comment on this line")
    }

    /// Old/new file line numbers. Both sides are shown because "which line in the file I have
    /// checked out" and "which line the agent will edit" are different questions during a review.
    private func gutter(_ line: DiffLineModel) -> some View {
        HStack(spacing: 4) {
            Text(line.oldLine.map(String.init) ?? "")
                .frame(width: numberColumnWidth, alignment: .trailing)
            Text(line.newLine.map(String.init) ?? "")
                .frame(width: numberColumnWidth, alignment: .trailing)
        }
        .font(.system(size: bodySize - 2, design: .monospaced))
        .foregroundStyle(palette.mutedForeground.opacity(0.7))
        .padding(.horizontal, 6)
        .accessibilityHidden(true)   // the row's own label already names the line
    }

    @ViewBuilder private func content(_ line: DiffLineModel) -> some View {
        let body = String(line.text.dropFirst(1))
        let text: Text = {
            if line.kind == .meta || line.text.isEmpty {
                return Text(line.text.isEmpty ? " " : line.text).foregroundColor(palette.mutedForeground)
            }
            return Text(SyntaxHighlighter.attributedString(body, language: language, theme: theme))
        }()
        if wrapLines {
            text.fixedSize(horizontal: false, vertical: true)
                .frame(maxWidth: .infinity, alignment: .leading)
        } else {
            text.lineLimit(1).fixedSize(horizontal: true, vertical: false)
        }
    }

    private func markerColor(_ line: DiffLineModel) -> Color {
        switch line.kind {
        case .add: return palette.diffAdded
        case .del: return palette.diffRemoved
        default: return palette.mutedForeground
        }
    }

    private func rowBackground(_ line: DiffLineModel, selected: Bool) -> Color {
        if selected { return palette.primary.opacity(0.22) }
        switch line.kind {
        case .add: return palette.diffAdded.opacity(0.16)
        case .del: return palette.diffRemoved.opacity(0.16)
        case .meta, .context: return .clear
        }
    }

    /// VoiceOver has no access to the colour tint or the marker column's shape, so the kind and the
    /// line number have to be spoken.
    private func accessibilityText(_ line: DiffLineModel) -> String {
        let kind: String
        switch line.kind {
        case .add: kind = "Added"
        case .del: kind = "Removed"
        case .context: kind = "Unchanged"
        case .meta: kind = "Note"
        }
        let number = line.citableLine.map { " line \($0)" } ?? ""
        return "\(kind)\(number), \(line.text.dropFirst(1))"
    }
}

// MARK: - Comment composer

/// A small inline composer: a text field plus Send, with a transient "Sent to agent"
/// confirmation after dispatch.
private struct CommentComposer: View {
    let placeholder: String
    let palette: OculusPalette
    let onSend: (String) -> Void

    @State private var text = ""
    @State private var sent = false

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            if sent {
                Label("Sent to agent", systemImage: "checkmark.circle.fill")
                    .font(.footnote.weight(.medium)).foregroundStyle(palette.primaryText)
                    .transition(.opacity)
                    .accessibilityAddTraits(.isStaticText)
            } else {
                HStack(alignment: .bottom, spacing: 8) {
                    TextField(placeholder, text: $text, axis: .vertical)
                        .textFieldStyle(.plain)
                        .font(.footnote)
                        .lineLimit(1...4)
                        .padding(8)
                        .background(palette.input)
                        .clipShape(OculusShape.rounded(OculusRadius.sm))
                        .overlay(OculusShape.rounded(OculusRadius.sm).strokeBorder(palette.border, lineWidth: 1))
                        .accessibilityLabel(placeholder)
                        #if os(iOS)
                        .textInputAutocapitalization(.sentences)
                        #endif
                    Button {
                        let t = text.trimmingCharacters(in: .whitespacesAndNewlines)
                        guard !t.isEmpty else { return }
                        onSend(t)
                        text = ""
                        withAnimation { sent = true }
                        Task { try? await Task.sleep(nanoseconds: 2_000_000_000); withAnimation { sent = false } }
                    } label: {
                        Image(systemName: "paperplane.fill").font(.footnote.weight(.semibold))
                            .frame(width: 44, height: 44)
                    }
                    .buttonStyle(.borderedProminent).tint(palette.primary)
                    .disabled(text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
                    .accessibilityLabel("Send comment to agent")
                }
            }
        }
    }
}
