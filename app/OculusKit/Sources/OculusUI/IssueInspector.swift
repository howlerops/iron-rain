import SwiftUI
import OculusKit

#if os(macOS)
import AppKit
typealias PlatformImage = NSImage
extension Image { init(platformImage img: PlatformImage) { self.init(nsImage: img) } }
#else
import UIKit
typealias PlatformImage = UIImage
extension Image { init(platformImage img: PlatformImage) { self.init(uiImage: img) } }
#endif

// MARK: - Slide-out inspector panel

/// A trailing inspector for a ticket: full markdown body with inline images, editable
/// title/description/status/priority, and threaded comments. Slides in from the right
/// (see IssuesView) instead of a modal so the board stays visible behind it.
struct IssueInspectorPanel: View {
    @ObservedObject var model: Model
    let issue: Issue
    let palette: OculusPalette
    let onStart: () -> Void
    let onClose: () -> Void
    @Environment(\.openURL) private var openURL

    @State private var detail: IssueDetail?
    @State private var states: [IssueState] = []
    @State private var loadError: String?

    // Current field values (seeded from detail/issue, mutated by edits).
    @State private var current: Issue
    @State private var comments: [IssueComment] = []

    // Edit state.
    @State private var editingTitle = false
    @State private var titleDraft = ""
    @State private var editingBody = false
    @State private var bodyDraft = ""
    @State private var savingBody = false
    @State private var newComment = ""
    @State private var postingComment = false
    @State private var editingCommentID: String?
    @State private var commentDraft = ""

    init(model: Model, issue: Issue, palette: OculusPalette, onStart: @escaping () -> Void, onClose: @escaping () -> Void) {
        self.model = model; self.issue = issue; self.palette = palette; self.onStart = onStart; self.onClose = onClose
        _current = State(initialValue: issue)
    }

    var body: some View {
        VStack(spacing: 0) {
            header
            Divider().overlay(palette.border)
            ScrollView {
                VStack(alignment: .leading, spacing: 16) {
                    titleSection
                    metaSection
                    Divider().overlay(palette.border)
                    descriptionSection
                    Divider().overlay(palette.border)
                    commentsSection
                }
                .padding(18)
                .frame(maxWidth: .infinity, alignment: .leading)
            }
            Divider().overlay(palette.border)
            footer
        }
        .background(palette.background)
        .overlay(alignment: .leading) { Rectangle().fill(palette.border).frame(width: 1) }
        .task(id: issue.id) { await load() }
    }

    // MARK: header

    private var header: some View {
        HStack(spacing: 8) {
            Text(current.key).font(.system(.caption, design: .monospaced).bold())
                .foregroundStyle(palette.primary)
            if let p = current.priority, p > 0 {
                Text(priorityLabel(p)).font(.caption2.bold())
                    .padding(.horizontal, 6).padding(.vertical, 2)
                    .background(Capsule().fill(priorityColor(p).opacity(0.18)))
                    .foregroundStyle(priorityColor(p))
            }
            Spacer()
            Button { onClose() } label: { Image(systemName: "xmark.circle.fill") }
                .buttonStyle(.plain).foregroundStyle(palette.mutedForeground)
                .help("Close")
        }
        .padding(.horizontal, 18).padding(.vertical, 14)
    }

    // MARK: title

    private var titleSection: some View {
        Group {
            if editingTitle {
                VStack(alignment: .leading, spacing: 8) {
                    TextField("Title", text: $titleDraft, axis: .vertical)
                        .textFieldStyle(.plain).font(.title3.bold())
                        .padding(8).background(palette.input).clipShape(RoundedRectangle(cornerRadius: 8))
                    HStack {
                        Button("Save") { Task { await saveTitle() } }
                            .buttonStyle(.borderedProminent).tint(palette.primary)
                            .disabled(titleDraft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
                        Button("Cancel") { editingTitle = false }.buttonStyle(.bordered)
                    }.controlSize(.small)
                }
            } else {
                HStack(alignment: .top, spacing: 8) {
                    Text(current.title).font(.title3.bold()).textSelection(.enabled)
                        .frame(maxWidth: .infinity, alignment: .leading)
                    Button { titleDraft = current.title; editingTitle = true } label: {
                        Image(systemName: "pencil").font(.caption)
                    }.buttonStyle(.plain).foregroundStyle(palette.mutedForeground)
                }
            }
        }
    }

    // MARK: meta (status + priority + assignee)

    private var metaSection: some View {
        HStack(spacing: 10) {
            // Status picker (from the team's workflow states; falls back to a static chip).
            if !states.isEmpty {
                Menu {
                    ForEach(states) { s in
                        Button { Task { await saveStatus(s) } } label: {
                            if s.name == current.status { Label(s.name, systemImage: "checkmark") } else { Text(s.name) }
                        }
                    }
                } label: { chip(current.status, systemImage: "circle.fill") }
                .menuStyle(.borderlessButton).fixedSize()
            } else {
                chip(current.status, systemImage: "circle.fill")
            }
            // Priority picker.
            Menu {
                ForEach([1, 2, 3, 4], id: \.self) { p in
                    Button { Task { await savePriority(p) } } label: {
                        if current.priority == p { Label(priorityLabel(p), systemImage: "checkmark") } else { Text(priorityLabel(p)) }
                    }
                }
                Divider()
                Button { Task { await savePriority(0) } } label: { Text("No priority") }
            } label: {
                chip(current.priority.map(priorityLabel) ?? "No priority", systemImage: "flag.fill")
            }
            .menuStyle(.borderlessButton).fixedSize()

            if let a = current.assignee, !a.isEmpty { chip(a, systemImage: "person") }
            Spacer()
        }
    }

    // MARK: description

    private var descriptionSection: some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack {
                Text("Description").font(.caption.bold()).foregroundStyle(palette.mutedForeground)
                Spacer()
                if !editingBody {
                    Button { bodyDraft = current.body ?? ""; editingBody = true } label: {
                        Label("Edit", systemImage: "pencil").font(.caption)
                    }.buttonStyle(.plain).foregroundStyle(palette.primary)
                }
            }
            if editingBody {
                VStack(alignment: .leading, spacing: 8) {
                    TextEditor(text: $bodyDraft)
                        .font(.callout).frame(minHeight: 160)
                        .padding(6).background(palette.input).clipShape(RoundedRectangle(cornerRadius: 8))
                        .scrollContentBackground(.hidden)
                    HStack {
                        Button { Task { await saveBody() } } label: {
                            if savingBody { ProgressView().controlSize(.small) } else { Text("Save") }
                        }.buttonStyle(.borderedProminent).tint(palette.primary).disabled(savingBody)
                        Button("Cancel") { editingBody = false }.buttonStyle(.bordered)
                    }.controlSize(.small)
                }
            } else if let body = current.body, !body.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
                IssueMarkdownView(text: body, model: model, provider: current.provider, palette: palette)
            } else if detail == nil && loadError == nil {
                HStack(spacing: 6) { ProgressView().controlSize(.small); Text("Loading…").font(.caption).foregroundStyle(palette.mutedForeground) }
            } else {
                Text("No description.").font(.callout).foregroundStyle(palette.mutedForeground)
            }
        }
    }

    // MARK: comments

    private var commentsSection: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("Comments\(comments.isEmpty ? "" : " (\(comments.count))")")
                .font(.caption.bold()).foregroundStyle(palette.mutedForeground)

            if detail == nil && loadError == nil {
                HStack(spacing: 6) { ProgressView().controlSize(.small); Text("Loading…").font(.caption).foregroundStyle(palette.mutedForeground) }
            }
            ForEach(comments) { c in commentRow(c) }

            // Composer.
            VStack(alignment: .leading, spacing: 8) {
                TextEditor(text: $newComment)
                    .font(.callout).frame(minHeight: 60)
                    .padding(6).background(palette.input).clipShape(RoundedRectangle(cornerRadius: 8))
                    .scrollContentBackground(.hidden)
                    .overlay(alignment: .topLeading) {
                        if newComment.isEmpty {
                            Text("Add a comment…").font(.callout).foregroundStyle(palette.mutedForeground)
                                .padding(.horizontal, 11).padding(.vertical, 14).allowsHitTesting(false)
                        }
                    }
                HStack {
                    Spacer()
                    Button { Task { await postComment() } } label: {
                        if postingComment { ProgressView().controlSize(.small) } else { Label("Comment", systemImage: "paperplane.fill") }
                    }
                    .buttonStyle(.borderedProminent).tint(palette.primary).controlSize(.small)
                    .disabled(postingComment || newComment.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
                }
            }
            .padding(.top, 4)
        }
    }

    private func commentRow(_ c: IssueComment) -> some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack(spacing: 6) {
                Text(c.author ?? "Someone").font(.caption.bold()).foregroundStyle(palette.foreground)
                if let d = c.createdAt { Text(relativeDate(d)).font(.caption2).foregroundStyle(palette.mutedForeground) }
                Spacer()
                Button { commentDraft = c.body; editingCommentID = c.id } label: {
                    Image(systemName: "pencil").font(.caption2)
                }.buttonStyle(.plain).foregroundStyle(palette.mutedForeground)
            }
            if editingCommentID == c.id {
                TextEditor(text: $commentDraft)
                    .font(.callout).frame(minHeight: 60)
                    .padding(6).background(palette.input).clipShape(RoundedRectangle(cornerRadius: 8))
                    .scrollContentBackground(.hidden)
                HStack {
                    Button("Save") { Task { await saveComment(c) } }
                        .buttonStyle(.borderedProminent).tint(palette.primary)
                    Button("Cancel") { editingCommentID = nil }.buttonStyle(.bordered)
                }.controlSize(.small)
            } else {
                IssueMarkdownView(text: c.body, model: model, provider: current.provider, palette: palette)
            }
        }
        .padding(12)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(palette.card.opacity(0.6))
        .clipShape(RoundedRectangle(cornerRadius: 10))
    }

    // MARK: footer

    private var footer: some View {
        HStack(spacing: 10) {
            if let u = current.url, let url = URL(string: u) {
                Button { openURL(url) } label: { Label("Open in Linear", systemImage: "arrow.up.right.square") }
                    .buttonStyle(.bordered)
            }
            Spacer()
            Button { onStart() } label: { Label("Start agent", systemImage: "play.circle.fill") }
                .buttonStyle(.borderedProminent).tint(palette.primary)
        }
        .padding(.horizontal, 18).padding(.vertical, 12)
    }

    // MARK: data

    private func load() async {
        do {
            async let d = model.issueDetail(issue)
            async let s = model.issueStates(issue)
            let detail = try await d
            self.detail = detail
            self.current = detail.issue
            self.comments = detail.comments
            self.states = (try? await s) ?? []
        } catch {
            loadError = error.localizedDescription
        }
    }

    private func saveTitle() async {
        let t = titleDraft.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !t.isEmpty else { return }
        editingTitle = false
        if let up = try? await model.updateIssue(current, title: t) { current = up } else { current.title = t }
    }

    private func saveBody() async {
        savingBody = true; defer { savingBody = false }
        if let up = try? await model.updateIssue(current, description: bodyDraft) { current = up } else { current.body = bodyDraft }
        editingBody = false
    }

    private func saveStatus(_ s: IssueState) async {
        if let up = try? await model.updateIssue(current, stateID: s.id) { current = up } else { current.status = s.name }
    }

    private func savePriority(_ p: Int) async {
        if let up = try? await model.updateIssue(current, priority: p) { current = up } else { current.priority = p }
    }

    private func postComment() async {
        let body = newComment.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !body.isEmpty else { return }
        postingComment = true; defer { postingComment = false }
        if let c = try? await model.addComment(current, body: body) {
            comments.append(c)
            newComment = ""
        }
    }

    private func saveComment(_ c: IssueComment) async {
        let body = commentDraft
        try? await model.editComment(provider: current.provider, commentID: c.id, body: body)
        if let i = comments.firstIndex(where: { $0.id == c.id }) { comments[i].body = body }
        editingCommentID = nil
    }

    // MARK: bits

    private func chip(_ text: String, systemImage: String) -> some View {
        Label(text, systemImage: systemImage)
            .font(.caption).foregroundStyle(palette.mutedForeground)
            .padding(.horizontal, 8).padding(.vertical, 3)
            .background(Capsule().fill(palette.muted.opacity(0.4)))
    }

    private func priorityLabel(_ p: Int) -> String {
        switch p { case 1: return "Urgent"; case 2: return "High"; case 3: return "Medium"; case 4: return "Low"; default: return "No priority" }
    }
    private func priorityColor(_ p: Int) -> Color { p == 1 ? .red : (p == 2 ? .orange : palette.mutedForeground) }

    /// Best-effort relative date from an ISO-8601 string (Linear returns e.g. 2026-07-19T…Z).
    private func relativeDate(_ iso: String) -> String {
        let f = ISO8601DateFormatter()
        f.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        let date = f.date(from: iso) ?? ISO8601DateFormatter().date(from: iso)
        guard let date else { return "" }
        let rel = RelativeDateTimeFormatter(); rel.unitsStyle = .abbreviated
        return rel.localizedString(for: date, relativeTo: Date())
    }
}

// MARK: - Markdown renderer (headings, lists, code, links, inline images)

/// A pragmatic block-level markdown renderer good enough for Linear/Jira ticket bodies:
/// headings, bullet/numbered lists, fenced code, horizontal rules, inline emphasis + links
/// (via AttributedString), and — the reason this exists — inline images fetched through the
/// daemon (SwiftUI's built-in markdown ignores images, and tracker CDNs are auth-gated).
struct IssueMarkdownView: View {
    let text: String
    @ObservedObject var model: Model
    let provider: String
    let palette: OculusPalette

    var body: some View {
        let blocks = MarkdownParser.parse(text)
        VStack(alignment: .leading, spacing: 10) {
            ForEach(Array(blocks.enumerated()), id: \.offset) { _, b in blockView(b) }
        }
        .tint(palette.primary)
        .frame(maxWidth: .infinity, alignment: .leading)
    }

    @ViewBuilder private func blockView(_ b: MarkdownBlock) -> some View {
        switch b {
        case .heading(let level, let t):
            inline(t).font(headingFont(level)).bold().padding(.top, level <= 2 ? 4 : 0)
        case .paragraph(let t):
            inline(t).font(.callout).foregroundStyle(palette.foreground.opacity(0.92))
                .fixedSize(horizontal: false, vertical: true)
        case .bullet(let items):
            VStack(alignment: .leading, spacing: 4) {
                ForEach(Array(items.enumerated()), id: \.offset) { _, it in
                    HStack(alignment: .top, spacing: 8) {
                        Text("•").foregroundStyle(palette.mutedForeground)
                        inline(it).font(.callout).fixedSize(horizontal: false, vertical: true)
                    }
                }
            }
        case .ordered(let items):
            VStack(alignment: .leading, spacing: 4) {
                ForEach(Array(items.enumerated()), id: \.offset) { idx, it in
                    HStack(alignment: .top, spacing: 8) {
                        Text("\(idx + 1).").foregroundStyle(palette.mutedForeground).monospacedDigit()
                        inline(it).font(.callout).fixedSize(horizontal: false, vertical: true)
                    }
                }
            }
        case .code(let c):
            ScrollView(.horizontal, showsIndicators: false) {
                Text(c).font(.system(.caption, design: .monospaced)).textSelection(.enabled)
                    .padding(10).frame(maxWidth: .infinity, alignment: .leading)
            }
            .background(palette.muted.opacity(0.3)).clipShape(RoundedRectangle(cornerRadius: 8))
        case .image(let alt, let url):
            TrackerImage(model: model, provider: provider, url: url, alt: alt, palette: palette)
        case .rule:
            Divider().overlay(palette.border)
        }
    }

    private func inline(_ s: String) -> Text {
        if let attr = try? AttributedString(markdown: s, options: .init(interpretedSyntax: .inlineOnlyPreservingWhitespace)) {
            return Text(attr)
        }
        return Text(s)
    }

    private func headingFont(_ l: Int) -> Font {
        switch l { case 1: return .title2; case 2: return .title3; case 3: return .headline; default: return .subheadline }
    }
}

/// An auth-gated tracker image loaded through the daemon (which holds the API token).
/// Shows a spinner while loading and a tappable link fallback on failure.
struct TrackerImage: View {
    @ObservedObject var model: Model
    let provider: String
    let url: String
    let alt: String
    let palette: OculusPalette
    @Environment(\.openURL) private var openURL
    @State private var image: PlatformImage?
    @State private var failed = false

    var body: some View {
        Group {
            if let image {
                Image(platformImage: image).resizable().scaledToFit()
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .clipShape(RoundedRectangle(cornerRadius: 8))
                    .overlay(RoundedRectangle(cornerRadius: 8).stroke(palette.border))
            } else if failed {
                Button { if let u = URL(string: url) { openURL(u) } } label: {
                    Label(alt.isEmpty ? "View image" : alt, systemImage: "photo").font(.caption)
                }.buttonStyle(.plain).foregroundStyle(palette.primary)
            } else {
                HStack(spacing: 6) {
                    ProgressView().controlSize(.small)
                    Text(alt.isEmpty ? "Loading image…" : alt).font(.caption).foregroundStyle(palette.mutedForeground)
                }
                .task { await load() }
            }
        }
    }

    private func load() async {
        do {
            let data = try await model.issueImage(provider: provider, url: url)
            if let img = PlatformImage(data: data) { image = img } else { failed = true }
        } catch { failed = true }
    }
}

// MARK: - Parser

enum MarkdownBlock {
    case heading(level: Int, text: String)
    case paragraph(String)
    case bullet([String])
    case ordered([String])
    case code(String)
    case image(alt: String, url: String)
    case rule
}

enum MarkdownParser {
    static func parse(_ text: String) -> [MarkdownBlock] {
        var blocks: [MarkdownBlock] = []
        let lines = text.replacingOccurrences(of: "\r\n", with: "\n").components(separatedBy: "\n")
        var i = 0
        var para: [String] = []
        func flush() {
            if !para.isEmpty { blocks.append(contentsOf: splitImages(para.joined(separator: "\n"))); para = [] }
        }
        while i < lines.count {
            let line = lines[i]
            let t = line.trimmingCharacters(in: .whitespaces)
            if t.hasPrefix("```") {
                flush()
                var code: [String] = []; i += 1
                while i < lines.count, !lines[i].trimmingCharacters(in: .whitespaces).hasPrefix("```") { code.append(lines[i]); i += 1 }
                blocks.append(.code(code.joined(separator: "\n"))); i += 1; continue
            }
            if t.isEmpty { flush(); i += 1; continue }
            if let h = heading(t) { flush(); blocks.append(.heading(level: h.0, text: h.1)); i += 1; continue }
            if t == "---" || t == "***" || t == "___" { flush(); blocks.append(.rule); i += 1; continue }
            if let it = bullet(t) {
                flush(); var items = [it]; i += 1
                while i < lines.count, let x = bullet(lines[i].trimmingCharacters(in: .whitespaces)) { items.append(x); i += 1 }
                blocks.append(.bullet(items)); continue
            }
            if let it = ordered(t) {
                flush(); var items = [it]; i += 1
                while i < lines.count, let x = ordered(lines[i].trimmingCharacters(in: .whitespaces)) { items.append(x); i += 1 }
                blocks.append(.ordered(items)); continue
            }
            para.append(line); i += 1
        }
        flush()
        return blocks
    }

    private static func heading(_ s: String) -> (Int, String)? {
        var n = 0
        for c in s { if c == "#" { n += 1 } else { break } }
        guard n >= 1, n <= 6, s.count > n else { return nil }
        let after = s.index(s.startIndex, offsetBy: n)
        guard s[after] == " " else { return nil }
        return (n, String(s[after...]).trimmingCharacters(in: .whitespaces))
    }

    private static func bullet(_ s: String) -> String? {
        for p in ["- ", "* ", "+ "] where s.hasPrefix(p) { return String(s.dropFirst(2)) }
        return nil
    }

    private static func ordered(_ s: String) -> String? {
        guard let dot = s.firstIndex(of: ".") else { return nil }
        let num = s[s.startIndex..<dot]
        guard !num.isEmpty, num.allSatisfy(\.isNumber) else { return nil }
        let after = s.index(after: dot)
        guard after < s.endIndex, s[after] == " " else { return nil }
        return String(s[s.index(after: after)...])
    }

    /// Pulls standalone `![alt](url)` images out of a paragraph into their own blocks,
    /// keeping surrounding text as paragraphs.
    private static func splitImages(_ s: String) -> [MarkdownBlock] {
        guard let re = try? NSRegularExpression(pattern: "!\\[([^\\]]*)\\]\\(([^)\\s]+)[^)]*\\)") else { return [.paragraph(s)] }
        let ns = s as NSString
        let matches = re.matches(in: s, range: NSRange(location: 0, length: ns.length))
        if matches.isEmpty { return [.paragraph(s)] }
        var out: [MarkdownBlock] = []
        var loc = 0
        for m in matches {
            if m.range.location > loc {
                let seg = ns.substring(with: NSRange(location: loc, length: m.range.location - loc))
                if !seg.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty { out.append(.paragraph(seg)) }
            }
            out.append(.image(alt: ns.substring(with: m.range(at: 1)), url: ns.substring(with: m.range(at: 2))))
            loc = m.range.location + m.range.length
        }
        if loc < ns.length {
            let seg = ns.substring(with: NSRange(location: loc, length: ns.length - loc))
            if !seg.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty { out.append(.paragraph(seg)) }
        }
        return out
    }
}
