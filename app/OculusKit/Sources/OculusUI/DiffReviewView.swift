import SwiftUI
import OculusKit

// MARK: - Diff model

/// The classification of a single unified-diff line, driving its background tint.
private enum DiffLineKind { case add, del, context, meta }

/// One parsed diff line: its kind plus the original text (prefix included).
private struct DiffLineModel: Identifiable {
    let id = UUID()
    let kind: DiffLineKind
    let text: String
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
            } else if hunkHeader != nil {
                let kind: DiffLineKind
                if line.hasPrefix("+") { kind = .add }
                else if line.hasPrefix("-") { kind = .del }
                else if line.hasPrefix("\\") { kind = .meta }   // "\ No newline at end of file"
                else { kind = .context }
                hunkLines.append(DiffLineModel(kind: kind, text: line))
            }
            // else: file-header metadata (index / mode / similarity) — ignored.
        }
        closeFile()

        // Give any file that never resolved a path a readable fallback.
        return files.map { f in
            f.path.isEmpty ? DiffFileModel(path: "(changes)", hunks: f.hunks) : f
        }
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
    /// Format: ``Re: `<path>`[:<hunk header>]\n\n<comment>\n\n```<lang>\n<hunk text>\n``` ``
    static func compose(path: String, hunkHeader: String?, comment: String, body: String) -> String {
        var head = "Re: `\(path)`"
        if let h = hunkHeader, !h.isEmpty { head += ":\(h)" }
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
/// tinted, monospaced, horizontally-scrollable hunks, and an inline "comment → prompt"
/// affordance that turns a review note into the agent's next instruction.
public struct DiffReviewView: View {
    @ObservedObject var model: Model
    let palette: OculusPalette
    @Environment(\.colorScheme) private var scheme
    @State private var refreshing = false

    public init(model: Model, palette: OculusPalette) {
        self.model = model
        self.palette = palette
    }

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
        .clipShape(RoundedRectangle(cornerRadius: 10))
        .overlay(RoundedRectangle(cornerRadius: 10).stroke(palette.border, lineWidth: 1))
    }

    private var totals: (add: Int, del: Int) {
        files.reduce(into: (0, 0)) { acc, f in acc.0 += f.additions; acc.1 += f.deletions }
    }

    private var topBar: some View {
        HStack(spacing: 10) {
            if files.isEmpty {
                Text("No changes").font(.system(size: 12, weight: .medium))
                    .foregroundStyle(palette.mutedForeground)
            } else {
                let t = totals
                Text("\(files.count) file\(files.count == 1 ? "" : "s")")
                    .font(.system(size: 12, weight: .semibold)).foregroundStyle(palette.foreground)
                Text("+\(t.add)").font(.system(size: 12, weight: .semibold, design: .monospaced))
                    .foregroundStyle(Color(hex: 0x2EA043))
                Text("-\(t.del)").font(.system(size: 12, weight: .semibold, design: .monospaced))
                    .foregroundStyle(Color(hex: 0xF85149))
            }
            Spacer()
            Button {
                refreshing = true
                Task { await model.worktreeDiff(); refreshing = false }
            } label: {
                if refreshing { ProgressView().controlSize(.small) }
                else { Label("Refresh", systemImage: "arrow.triangle.branch").font(.system(size: 12, weight: .medium)) }
            }
            .buttonStyle(.plain).foregroundStyle(palette.primary)
        }
        .padding(.horizontal, 12).padding(.vertical, 9)
        .background(palette.card.opacity(0.4))
    }

    private var emptyState: some View {
        VStack(spacing: 10) {
            Image(systemName: "checkmark.circle").font(.system(size: 30)).foregroundStyle(palette.mutedForeground)
            Text("No changes yet — tap Refresh diff.")
                .font(.system(size: 13)).foregroundStyle(palette.mutedForeground)
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
    let palette: OculusPalette
    let theme: CodeTheme
    let onSend: (String) -> Void

    @State private var expanded: Bool
    @State private var commenting = false

    init(file: DiffFileModel, startExpanded: Bool, palette: OculusPalette,
         theme: CodeTheme, onSend: @escaping (String) -> Void) {
        self.file = file
        self.startExpanded = startExpanded
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
                                 palette: palette, theme: theme, onSend: onSend)
                }
            }
        }
        .background(palette.card.opacity(0.3))
        .clipShape(RoundedRectangle(cornerRadius: 8))
        .overlay(RoundedRectangle(cornerRadius: 8).stroke(palette.border, lineWidth: 1))
    }

    private var header: some View {
        HStack(spacing: 8) {
            Button { withAnimation(.easeInOut(duration: 0.15)) { expanded.toggle() } } label: {
                HStack(spacing: 8) {
                    Image(systemName: expanded ? "chevron.down" : "chevron.right")
                        .font(.system(size: 11, weight: .bold)).foregroundStyle(palette.mutedForeground)
                        .frame(width: 12)
                    Text((file.path as NSString).lastPathComponent)
                        .font(.system(size: 13, weight: .semibold, design: .monospaced))
                        .foregroundStyle(palette.foreground).lineLimit(1).truncationMode(.middle)
                    if file.additions > 0 {
                        Text("+\(file.additions)").font(.system(size: 11, weight: .semibold, design: .monospaced))
                            .foregroundStyle(Color(hex: 0x2EA043))
                    }
                    if file.deletions > 0 {
                        Text("-\(file.deletions)").font(.system(size: 11, weight: .semibold, design: .monospaced))
                            .foregroundStyle(Color(hex: 0xF85149))
                    }
                    Spacer(minLength: 0)
                }
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            Button { withAnimation { commenting.toggle() } } label: {
                Image(systemName: commenting ? "bubble.left.fill" : "bubble.left")
                    .font(.system(size: 14)).foregroundStyle(commenting ? palette.primary : palette.mutedForeground)
                    .frame(width: 30, height: 30).contentShape(Rectangle())
            }
            .buttonStyle(.plain).help("Comment on this file")
        }
        .padding(.leading, 10).padding(.trailing, 4).padding(.vertical, 4)
    }
}

// MARK: - Hunk

private struct DiffHunkView: View {
    let hunk: DiffHunkModel
    let filePath: String
    let language: CodeLanguage
    let palette: OculusPalette
    let theme: CodeTheme
    let onSend: (String) -> Void

    @State private var commenting = false

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack(spacing: 6) {
                Text(hunk.header)
                    .font(.system(size: 11, design: .monospaced))
                    .foregroundStyle(palette.mutedForeground).lineLimit(1)
                Spacer(minLength: 0)
                Button { withAnimation { commenting.toggle() } } label: {
                    Image(systemName: commenting ? "bubble.left.fill" : "bubble.left")
                        .font(.system(size: 12)).foregroundStyle(commenting ? palette.primary : palette.mutedForeground)
                        .frame(width: 26, height: 22).contentShape(Rectangle())
                }
                .buttonStyle(.plain).help("Comment on this hunk")
            }
            .padding(.horizontal, 10).padding(.vertical, 4)
            .background(palette.primary.opacity(0.08))

            if commenting {
                CommentComposer(placeholder: "Comment on this hunk…", palette: palette) { text in
                    onSend(DiffPrompt.compose(path: filePath, hunkHeader: hunk.header,
                                              comment: text, body: hunk.promptText()))
                    commenting = false
                }
                .padding(.horizontal, 10).padding(.vertical, 8)
            }

            ScrollView(.horizontal, showsIndicators: true) {
                VStack(alignment: .leading, spacing: 0) {
                    ForEach(hunk.lines) { line in
                        row(line)
                    }
                }
                .fixedSize(horizontal: true, vertical: false)
            }
            .background(theme.background)
        }
    }

    private func row(_ line: DiffLineModel) -> some View {
        let bg: Color
        switch line.kind {
        case .add: bg = Color(hex: 0x2EA043).opacity(0.16)
        case .del: bg = Color(hex: 0xF85149).opacity(0.16)
        case .meta: bg = .clear
        case .context: bg = .clear
        }
        return styledText(line)
            .font(.system(size: 12, design: .monospaced))
            .padding(.horizontal, 10).padding(.vertical, 1)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(bg)
    }

    /// Renders a line: a subtly-colored prefix char followed by syntax-highlighted content.
    private func styledText(_ line: DiffLineModel) -> Text {
        let text = line.text
        if line.kind == .meta || text.isEmpty {
            return Text(text.isEmpty ? " " : text).foregroundColor(palette.mutedForeground)
        }
        let prefixColor: Color
        switch line.kind {
        case .add: prefixColor = Color(hex: 0x2EA043)
        case .del: prefixColor = Color(hex: 0xF85149)
        default: prefixColor = palette.mutedForeground
        }
        // Split off the +/-/space prefix; syntax-color the remaining code.
        let prefix = String(text.prefix(1))
        let content = String(text.dropFirst(1))
        var attr = AttributedString(prefix)
        attr.foregroundColor = prefixColor
        let highlighted = SyntaxHighlighter.attributedString(content, language: language, theme: theme)
        return Text(attr) + Text(highlighted)
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
                    .font(.system(size: 12, weight: .medium)).foregroundStyle(palette.primary)
                    .transition(.opacity)
            } else {
                HStack(alignment: .bottom, spacing: 8) {
                    TextField(placeholder, text: $text, axis: .vertical)
                        .textFieldStyle(.plain)
                        .font(.system(size: 13))
                        .lineLimit(1...4)
                        .padding(8)
                        .background(palette.input)
                        .clipShape(RoundedRectangle(cornerRadius: 6))
                        .overlay(RoundedRectangle(cornerRadius: 6).stroke(palette.border, lineWidth: 1))
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
                        Image(systemName: "paperplane.fill").font(.system(size: 13, weight: .semibold))
                            .frame(width: 34, height: 34)
                    }
                    .buttonStyle(.borderedProminent).tint(palette.primary)
                    .disabled(text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
                }
            }
        }
    }
}
