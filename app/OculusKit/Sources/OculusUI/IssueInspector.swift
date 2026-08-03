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
    @State private var attachments: [IssueAttachment] = []
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
                    editRow
                    Divider().overlay(palette.border)
                    descriptionSection
                    if !attachments.isEmpty {
                        Divider().overlay(palette.border)
                        attachmentsSection
                    }
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

            assigneeMenu
            Spacer()
        }
    }

    /// Assignee picker — the team's assignable members (fetched live), plus Unassign.
    private var assigneeMenu: some View {
        Menu {
            ForEach(model.members(for: current)) { u in
                Button { Task { await saveAssignee(u.id) } } label: {
                    if u.id == current.assigneeID { Label(u.name, systemImage: "checkmark") } else { Text(u.name) }
                }
            }
            if model.members(for: current).isEmpty { Text("No assignable users").foregroundStyle(palette.mutedForeground) }
            Divider()
            Button { Task { await saveAssignee("") } } label: { Text("Unassign") }
        } label: {
            chip(current.assignee?.isEmpty == false ? current.assignee! : "Unassigned", systemImage: "person")
        }
        .menuStyle(.borderlessButton).fixedSize()
    }

    /// Second meta row: sprint/cycle · labels · estimate · due date — all editable.
    private var editRow: some View {
        HStack(spacing: 10) {
            sprintMenu
            labelsMenu
            estimateMenu
            dueDateControl
            Spacer()
        }
    }

    private var sprintMenu: some View {
        Menu {
            ForEach(model.cycles(for: current)) { c in
                Button { Task { await saveCycle(c.id) } } label: {
                    let mark = c.id == current.cycleID
                    if mark { Label(cycleLabel(c), systemImage: "checkmark") } else { Text(cycleLabel(c)) }
                }
            }
            if model.cycles(for: current).isEmpty { Text("No sprints").foregroundStyle(palette.mutedForeground) }
            Divider()
            Button { Task { await saveCycle("") } } label: { Text("Remove from sprint") }
        } label: {
            let label = current.cycleLabel ?? (current.sprintName?.isEmpty == false ? current.sprintName! : "No sprint")
            chip(label, systemImage: "flag.checkered")
        }
        .menuStyle(.borderlessButton).fixedSize()
    }

    private var labelsMenu: some View {
        Menu {
            ForEach(model.labels(for: current)) { l in
                Button { Task { await toggleLabel(l) } } label: {
                    if currentLabelIDs.contains(l.id) { Label(l.name, systemImage: "checkmark") } else { Text(l.name) }
                }
            }
            if model.labels(for: current).isEmpty { Text("No labels").foregroundStyle(palette.mutedForeground) }
        } label: {
            let names = (current.labels ?? []).map(\.name)
            chip(names.isEmpty ? "Labels" : names.joined(separator: ", "), systemImage: "tag")
        }
        .menuStyle(.borderlessButton).fixedSize()
    }

    private var estimateMenu: some View {
        Menu {
            ForEach([1, 2, 3, 5, 8, 13], id: \.self) { pts in
                Button { Task { await saveEstimate(Double(pts)) } } label: {
                    if Int(current.estimate ?? 0) == pts { Label("\(pts) pts", systemImage: "checkmark") } else { Text("\(pts) pts") }
                }
            }
            Divider()
            Button { Task { await saveEstimate(0) } } label: { Text("No estimate") }
        } label: {
            let e = current.estimate ?? 0
            chip(e > 0 ? "\(Int(e)) pts" : "Estimate", systemImage: "gauge.medium")
        }
        .menuStyle(.borderlessButton).fixedSize()
    }

    private var dueDateControl: some View {
        HStack(spacing: 4) {
            chip(current.dueDate?.isEmpty == false ? current.dueDate! : "Due date", systemImage: "calendar")
                .overlay {
                    DatePicker("", selection: dueDateBinding, displayedComponents: .date)
                        .labelsHidden().blendMode(.destinationOver) // invisible hit-target over the chip
                }
            if current.dueDate?.isEmpty == false {
                Button { Task { await saveDueDate("") } } label: { Image(systemName: "xmark.circle.fill").font(.caption2) }
                    .buttonStyle(.plain).foregroundStyle(palette.mutedForeground)
            }
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

    // MARK: attachments

    @ViewBuilder private var attachmentsSection: some View {
        VStack(alignment: .leading, spacing: 10) {
            Text("Attachments (\(attachments.count))")
                .font(.caption.bold()).foregroundStyle(palette.mutedForeground)
            ForEach(attachments) { a in attachmentRow(a) }
        }
    }

    @ViewBuilder private func attachmentRow(_ a: IssueAttachment) -> some View {
        if a.isImage {
            VStack(alignment: .leading, spacing: 4) {
                // Auth-gated tracker CDNs: fetch through the daemon (reuses the markdown image path).
                TrackerImage(model: model, provider: current.provider, url: a.url, alt: a.filename, palette: palette)
                Text(a.filename).font(.caption2).foregroundStyle(palette.mutedForeground).lineLimit(1)
            }
        } else {
            Button { if let u = URL(string: a.url) { openURL(u) } } label: {
                HStack(spacing: 8) {
                    Image(systemName: "paperclip").font(.caption).foregroundStyle(palette.mutedForeground)
                    VStack(alignment: .leading, spacing: 1) {
                        Text(a.filename).font(.caption).foregroundStyle(palette.foreground).lineLimit(1)
                        if let size = a.size, size > 0 {
                            Text(byteLabel(size)).font(.caption2).foregroundStyle(palette.mutedForeground)
                        }
                    }
                    Spacer()
                    Image(systemName: "arrow.up.right.square").font(.caption).foregroundStyle(palette.primary)
                }
                .padding(10)
                .frame(maxWidth: .infinity, alignment: .leading)
                .background(palette.card.opacity(0.6))
                .clipShape(RoundedRectangle(cornerRadius: 10))
            }
            .buttonStyle(.plain)
        }
    }

    private func byteLabel(_ n: Int) -> String {
        ByteCountFormatter.string(fromByteCount: Int64(n), countStyle: .file)
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
                Button { openURL(url) } label: { Label(openInLabel(for: current.provider), systemImage: "arrow.up.right.square") }
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
            self.attachments = detail.attachments ?? []
            self.states = (try? await s) ?? []
            // Warm the editor pickers (assignee/labels/sprint) in the background — cached per team.
            Task { await model.loadMembers(for: detail.issue) }
            Task { await model.loadLabels(for: detail.issue) }
            Task { await model.loadCycles(for: detail.issue) }
        } catch {
            loadError = error.localizedDescription
        }
    }

    // These mutate the real tracker (Linear/Jira). On failure, surface the reason and DON'T apply
    // the change locally — a fake "saved" that silently vanished on refresh was worse than an error.
    private func saveTitle() async {
        let t = titleDraft.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !t.isEmpty else { return }
        editingTitle = false
        do { current = try await model.updateIssue(current, title: t) }
        catch { model.actionError = "Couldn’t update the title.\n\n\(error.localizedDescription)" }
    }

    private func saveBody() async {
        savingBody = true; defer { savingBody = false }
        editingBody = false
        do { current = try await model.updateIssue(current, description: bodyDraft) }
        catch { model.actionError = "Couldn’t update the description.\n\n\(error.localizedDescription)" }
    }

    private func saveStatus(_ s: IssueState) async {
        do { current = try await model.updateIssue(current, stateID: s.id) }
        catch { model.actionError = "Couldn’t change the status.\n\n\(error.localizedDescription)" }
    }

    private func savePriority(_ p: Int) async {
        do { current = try await model.updateIssue(current, priority: p) }
        catch { model.actionError = "Couldn’t change the priority.\n\n\(error.localizedDescription)" }
    }

    private func saveAssignee(_ id: String) async {
        do { current = try await model.updateIssue(current, assigneeID: id) }
        catch { model.actionError = "Couldn’t change the assignee.\n\n\(error.localizedDescription)" }
    }

    private func saveCycle(_ id: String) async {
        do { current = try await model.updateIssue(current, cycleID: id) }
        catch { model.actionError = "Couldn’t change the sprint.\n\n\(error.localizedDescription)" }
    }

    /// Adds or removes a label, sending the full new set (both providers replace, not merge).
    private func toggleLabel(_ l: IssueLabel) async {
        var ids = currentLabelIDs
        if ids.contains(l.id) { ids.removeAll { $0 == l.id } } else { ids.append(l.id) }
        do { current = try await model.updateIssue(current, labelIDs: ids) }
        catch { model.actionError = "Couldn’t change the labels.\n\n\(error.localizedDescription)" }
    }

    private func saveEstimate(_ pts: Double) async {
        do { current = try await model.updateIssue(current, estimate: pts) }
        catch { model.actionError = "Couldn’t change the estimate.\n\n\(error.localizedDescription)" }
    }

    private func saveDueDate(_ iso: String) async {
        do { current = try await model.updateIssue(current, dueDate: iso) }
        catch { model.actionError = "Couldn’t change the due date.\n\n\(error.localizedDescription)" }
    }

    // MARK: editor helpers

    private var currentLabelIDs: [String] { (current.labels ?? []).map(\.id) }
    private func cycleLabel(_ c: IssueCycle) -> String {
        if !c.name.isEmpty { return c.name }
        if let n = c.number, n > 0 { return "Cycle \(n)" }
        return c.id
    }
    /// Binds the DatePicker to the issue's ISO due date (YYYY-MM-DD), saving on change.
    private var dueDateBinding: Binding<Date> {
        Binding(
            get: { Self.isoFormatter.date(from: current.dueDate ?? "") ?? Date() },
            set: { newDate in Task { await saveDueDate(Self.isoFormatter.string(from: newDate)) } }
        )
    }
    private static let isoFormatter: DateFormatter = {
        let f = DateFormatter(); f.calendar = Calendar(identifier: .iso8601)
        f.dateFormat = "yyyy-MM-dd"; f.timeZone = TimeZone(identifier: "UTC")
        return f
    }()

    private func postComment() async {
        let body = newComment.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !body.isEmpty else { return }
        postingComment = true; defer { postingComment = false }
        do { _ = try await model.addComment(current, body: body) }
        catch {
            model.actionError = "Couldn’t post the comment.\n\n\(error.localizedDescription)"
            return
        }
        newComment = ""
        // Re-fetch so the new comment shows with its real id/author/timestamp (the add mutation
        // returns only success). Fall back to an optimistic append only on a failed RE-FETCH — the
        // comment did post, we just couldn't reload the thread.
        if let d = try? await model.issueDetail(issue) {
            comments = d.comments
        } else {
            comments.append(IssueComment(id: UUID().uuidString, author: "You", body: body, createdAt: nil))
        }
    }

    private func saveComment(_ c: IssueComment) async {
        let body = commentDraft
        editingCommentID = nil
        do {
            try await model.editComment(provider: current.provider, commentID: c.id, body: body)
            if let i = comments.firstIndex(where: { $0.id == c.id }) { comments[i].body = body }
        } catch {
            model.actionError = "Couldn’t edit the comment.\n\n\(error.localizedDescription)"
        }
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
                ForEach(Array(items.enumerated()), id: \.offset) { _, it in
                    HStack(alignment: .top, spacing: 8) {
                        Text("\(it.num).").foregroundStyle(palette.mutedForeground).monospacedDigit()
                        inline(it.text).font(.callout).fixedSize(horizontal: false, vertical: true)
                    }
                }
            }
        case .code(_, let c):
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
                    .overlay(RoundedRectangle(cornerRadius: 8).strokeBorder(palette.border))
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
    case ordered([(num: Int, text: String)])
    case code(language: String?, text: String)
    case image(alt: String, url: String)
    case rule
}

enum MarkdownParser {
    static func parse(_ text: String) -> [MarkdownBlock] {
        var blocks: [MarkdownBlock] = []
        let lines = separateJammedBold(text.replacingOccurrences(of: "\r\n", with: "\n"))
            .components(separatedBy: "\n")
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
                let language = fenceLanguage(t)
                var code: [String] = []; i += 1
                while i < lines.count, !lines[i].trimmingCharacters(in: .whitespaces).hasPrefix("```") { code.append(lines[i]); i += 1 }
                blocks.append(.code(language: language, text: code.joined(separator: "\n"))); i += 1; continue
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


    /// Restores line breaks that were lost between a bold run and what follows it.
    ///
    /// Agents emit reasoning-step titles as bold runs (`**Confirming repository paths**`) and some
    /// providers stream them with NO separator at all, so a turn arrives as one unbroken string:
    /// `**Title**The filesystem tool batch is aborting...` and, where two titles are adjacent,
    /// `paths****Inspecting`. Rendered faithfully that is a wall of text with words fused to
    /// headings — which is exactly what it looked like.
    ///
    /// Both shapes are unambiguous artifacts rather than meaningful markdown: `****` is an empty
    /// bold (never intentional), and a bold run butting straight into a capital letter is a lost
    /// break. Repairing them here fixes every provider at once, and fixes history too, since the
    /// durable transcript stores the raw text and this runs at render time.
    ///
    /// Deliberately conservative — it will not touch `**bold**, then prose` or `**bold**s`, because
    /// those continue a sentence rather than starting one.
    static func separateJammedBold(_ s: String) -> String {
        guard s.contains("**") else { return s }
        var out = ""
        out.reserveCapacity(s.count + 16)
        let chars = Array(s)
        var i = 0
        // Tracks whether we're inside a bold run, so a CLOSING `**` can be told from an opening one.
        var inBold = false
        while i < chars.count {
            if chars[i] == "*", i + 1 < chars.count, chars[i + 1] == "*" {
                // Four in a row: two bold runs collided. Break between them.
                if i + 3 < chars.count, chars[i + 2] == "*", chars[i + 3] == "*" {
                    out += "**\n\n**"
                    i += 4
                    continue
                }
                out += "**"
                i += 2
                inBold.toggle()
                // A bold run just CLOSED and the next character starts a new sentence — the break
                // between heading and body was dropped upstream.
                if !inBold, i < chars.count, chars[i].isUppercase {
                    out += "\n"
                }
                continue
            }
            out.append(chars[i])
            i += 1
        }
        return out
    }

    private static func heading(_ s: String) -> (Int, String)? {
        var n = 0
        for c in s { if c == "#" { n += 1 } else { break } }
        guard n >= 1, n <= 6, s.count > n else { return nil }
        let after = s.index(s.startIndex, offsetBy: n)
        guard s[after] == " " else { return nil }
        return (n, String(s[after...]).trimmingCharacters(in: .whitespaces))
    }

    private static func fenceLanguage(_ s: String) -> String? {
        let raw = String(s.dropFirst(3)).trimmingCharacters(in: .whitespacesAndNewlines)
        guard !raw.isEmpty else { return nil }
        return raw.components(separatedBy: .whitespaces).first
    }

    private static func bullet(_ s: String) -> String? {
        for p in ["- ", "* ", "+ "] where s.hasPrefix(p) { return String(s.dropFirst(2)) }
        return nil
    }

    private static func ordered(_ s: String) -> (num: Int, text: String)? {
        guard let dot = s.firstIndex(of: ".") else { return nil }
        let num = s[s.startIndex..<dot]
        guard !num.isEmpty, num.allSatisfy(\.isNumber), let n = Int(num) else { return nil }
        let after = s.index(after: dot)
        guard after < s.endIndex, s[after] == " " else { return nil }
        // Keep the SOURCE number: LLM output often interleaves lists with bullets/paragraphs, which
        // splits one logical list into several blocks — renumbering each block from 1 made every
        // item render as "1.".
        return (n, String(s[s.index(after: after)...]))
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
