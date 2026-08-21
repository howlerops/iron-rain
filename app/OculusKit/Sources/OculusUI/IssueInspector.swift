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
                .foregroundStyle(palette.primaryText)
            if let p = current.priority, p > 0 {
                Text(priorityLabel(p)).font(.caption2.bold())
                    .padding(.horizontal, 6).padding(.vertical, 2)
                    .background(Capsule().fill(priorityColor(p).opacity(0.18)))
                    .foregroundStyle(priorityColor(p))
            }
            Spacer()
            Button { onClose() } label: {
                Image(systemName: "xmark.circle.fill")
                    .frame(width: 44, height: 44).contentShape(Rectangle())
            }
                .buttonStyle(.plain).foregroundStyle(palette.mutedForeground)
                .help("Close")
                .accessibilityLabel("Close inspector")
        }
        .padding(.leading, 18).padding(.trailing, 4)
    }

    // MARK: title

    private var titleSection: some View {
        Group {
            if editingTitle {
                VStack(alignment: .leading, spacing: 8) {
                    TextField("Title", text: $titleDraft, axis: .vertical)
                        .textFieldStyle(.plain).font(.title3.bold())
                        .padding(8).background(palette.input).clipShape(OculusShape.rounded(OculusRadius.sm))
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
                            .frame(minHeight: 44).contentShape(Rectangle())
                    }
                    .buttonStyle(.plain).foregroundStyle(palette.primaryText)
                    .accessibilityLabel("Edit description")
                }
            }
            if editingBody {
                VStack(alignment: .leading, spacing: 8) {
                    TextEditor(text: $bodyDraft)
                        .font(.callout).frame(minHeight: 160)
                        .padding(6).background(palette.input).clipShape(OculusShape.rounded(OculusRadius.sm))
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
                    Image(systemName: "arrow.up.right.square").font(.caption).foregroundStyle(palette.primaryText)
                }
                .padding(10)
                .frame(maxWidth: .infinity, alignment: .leading)
                .background(palette.card.opacity(0.6))
                .clipShape(OculusShape.rounded(OculusRadius.md))
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
                    .padding(6).background(palette.input).clipShape(OculusShape.rounded(OculusRadius.sm))
                    .scrollContentBackground(.hidden)
                    .accessibilityLabel("Add a comment")
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
                        .frame(width: 44, height: 44).contentShape(Rectangle())
                }
                .buttonStyle(.plain).foregroundStyle(palette.mutedForeground)
                .help("Edit comment")
                .accessibilityLabel("Edit comment by \(c.author ?? "someone")")
            }
            if editingCommentID == c.id {
                TextEditor(text: $commentDraft)
                    .font(.callout).frame(minHeight: 60)
                    .padding(6).background(palette.input).clipShape(OculusShape.rounded(OculusRadius.sm))
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
        .clipShape(OculusShape.rounded(OculusRadius.md))
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
    private func priorityColor(_ p: Int) -> Color {
        p == 1 ? palette.destructive : (p == 2 ? palette.warning : palette.mutedForeground)
    }

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

    private func bulletGlyph(_ depth: Int) -> String {
        ["•", "◦", "▪", "–"][min(max(depth, 0), 3)]
    }

    @ViewBuilder private func blockView(_ b: MarkdownBlock) -> some View {
        switch b {
        case .heading(let level, let t):
            inline(t).font(headingFont(level)).bold().padding(.top, level <= 2 ? 4 : 0)
        case .paragraph(let t):
            inline(t).font(.callout).foregroundStyle(palette.foreground.opacity(0.92))
                .fixedSize(horizontal: false, vertical: true)
        case .table(let props):
            // Issue bodies carry tables as often as chat does — same view, same reason.
            TableView(props: props, palette: palette)
        case .quote(let lines):
            HStack(alignment: .top, spacing: 8) {
                OculusShape.rounded(1.5).fill(palette.border).frame(width: 3)
                VStack(alignment: .leading, spacing: 4) {
                    ForEach(Array(lines.enumerated()), id: \.offset) { _, l in
                        inline(l).font(.callout).fixedSize(horizontal: false, vertical: true)
                    }
                }
                .foregroundStyle(palette.mutedForeground)
            }
        case .bullet(let items):
            VStack(alignment: .leading, spacing: 4) {
                ForEach(Array(items.enumerated()), id: \.offset) { _, it in
                    HStack(alignment: .top, spacing: 8) {
                        if let checked = it.checked {
                            Image(systemName: checked ? "checkmark.square.fill" : "square")
                                .font(.callout)
                                .foregroundStyle(checked ? palette.primary : palette.mutedForeground)
                        } else {
                            Text(bulletGlyph(it.depth)).foregroundStyle(palette.mutedForeground)
                        }
                        inline(it.text).font(.callout).fixedSize(horizontal: false, vertical: true)
                    }
                    .padding(.leading, CGFloat(it.depth) * 14)
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
            .background(palette.muted.opacity(0.3)).clipShape(OculusShape.rounded(OculusRadius.sm))
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
                    .clipShape(OculusShape.rounded(OculusRadius.sm))
                    .overlay(OculusShape.rounded(OculusRadius.sm).strokeBorder(palette.border))
            } else if failed {
                Button { if let u = URL(string: url) { openURL(u) } } label: {
                    Label(alt.isEmpty ? "View image" : alt, systemImage: "photo").font(.caption)
                }.buttonStyle(.plain).foregroundStyle(palette.primaryText)
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

/// One bullet-list row: its text, how deeply it is nested, and — for a GFM task item — whether the
/// box is ticked. `checked == nil` means an ordinary bullet, which is why it is an Optional rather
/// than a Bool defaulting to false.
struct ListItem {
    var depth: Int
    var text: String
    var checked: Bool?
}

enum MarkdownBlock {
    case heading(level: Int, text: String)
    case paragraph(String)
    case bullet([ListItem])
    case ordered([(num: Int, text: String)])
    case code(language: String?, text: String)
    case image(alt: String, url: String)
    case rule
    /// A GitHub-flavoured pipe table, carried as the SAME props the generative-UI layer uses so it
    /// renders through the existing `TableView` rather than a second, divergent table renderer.
    case table(TableProps)
    case quote([String])
}

enum MarkdownParser {
    static func parse(_ text: String) -> [MarkdownBlock] {
        var blocks: [MarkdownBlock] = []
        // Bold repair is applied PER LINE and only OUTSIDE fences. Run across the whole document it
        // also rewrote source code: `def f(**kwargs)` in a ```python block picked up an injected
        // newline, and a literal `****` became `**\n\n**` — silently corrupting displayed code, the
        // one place a renderer must reproduce input exactly.
        var lines: [String] = []
        var inFence = false
        for ln in text.replacingOccurrences(of: "\r\n", with: "\n").components(separatedBy: "\n") {
            if ln.trimmingCharacters(in: .whitespaces).hasPrefix("```") {
                inFence.toggle(); lines.append(ln); continue
            }
            if inFence { lines.append(ln); continue }
            // The repair INSERTS newlines, so its output is re-split back into lines.
            lines.append(contentsOf: separateJammedBold(ln).components(separatedBy: "\n"))
        }
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
                // An unterminated fence deliberately runs to EOF (that is what GFM asks for, and it
                // keeps an interrupted answer's tail readable). But text ending exactly at "```swift"
                // yielded an EMPTY code block — a bordered box with nothing in it.
                if !code.isEmpty {
                    blocks.append(.code(language: language, text: code.joined(separator: "\n")))
                }
                i += 1; continue
            }
            if t.isEmpty { flush(); i += 1; continue }
            if let h = heading(t) { flush(); blocks.append(.heading(level: h.0, text: h.1)); i += 1; continue }
            // Setext headings, BEFORE the thematic-break check. "Title" followed by "---" is an H2 in
            // CommonMark; treating the underline as a rule demoted the heading to body text AND drew
            // a spurious divider under it. A non-empty `para` is exactly the disambiguator the spec
            // uses: attached to text it underlines, after a blank line it stays a thematic break.
            if !para.isEmpty, let lvl = setextLevel(t) {
                let title = para.removeLast()
                flush()
                blocks.append(.heading(level: lvl, text: title)); i += 1; continue
            }
            if isThematicBreak(t) { flush(); blocks.append(.rule); i += 1; continue }
            // Tables are checked BEFORE the rule/bullet cases: a table's delimiter row ("|---|---|")
            // starts with a pipe so it never reaches them, but the header must be claimed here or a
            // one-column table would be indistinguishable from a paragraph.
            if let (tbl, next) = table(lines, from: i) {
                flush(); blocks.append(.table(tbl)); i = next; continue
            }
            if t.hasPrefix(">") {
                flush(); var quoted: [String] = []
                while i < lines.count {
                    let q = lines[i].trimmingCharacters(in: .whitespaces)
                    guard q.hasPrefix(">") else { break }
                    quoted.append(String(q.dropFirst()).trimmingCharacters(in: .whitespaces))
                    i += 1
                }
                blocks.append(.quote(quoted)); continue
            }
            // Lists pass the RAW line so the matcher can measure indent for nesting.
            if let it = bullet(line) {
                flush(); var items = [it]; i += 1
                while i < lines.count {
                    if let x = bullet(lines[i]) { items.append(x); i += 1; continue }
                    guard let cont = lazyContinuation(lines[i]) else { break }
                    items[items.count - 1].text += " " + cont; i += 1
                }
                blocks.append(.bullet(items)); continue
            }
            if let it = ordered(t) {
                flush(); var items = [it]; i += 1
                while i < lines.count {
                    if let x = ordered(lines[i].trimmingCharacters(in: .whitespaces)) { items.append(x); i += 1; continue }
                    guard let cont = lazyContinuation(lines[i]) else { break }
                    items[items.count - 1].text += " " + cont; i += 1
                }
                blocks.append(.ordered(items)); continue
            }
            para.append(line); i += 1
        }
        flush()
        return blocks
    }


    /// Parses a GitHub-flavoured pipe table starting at `from`, returning it plus the index to
    /// resume at. Returns nil when the lines aren't a table, which is the common case.
    ///
    /// Tables were the single largest gap in this renderer: with no case for them they fell through
    /// to `.paragraph` and rendered as raw `| col | col |` pipe soup, and agents emit tables
    /// constantly. Rather than grow a second table renderer here, this decodes into `TableProps` so
    /// the result goes through the generative-UI `TableView` that already handles alignment, zebra
    /// striping, column sizing and horizontal overflow.
    ///
    /// A table is only recognised with BOTH a header row and the `|---|:--:|` delimiter beneath it.
    /// Requiring the delimiter is what keeps ordinary prose containing a pipe — a shell command, a
    /// regex alternation — from being swallowed as a one-row table.
    static func table(_ lines: [String], from: Int) -> (TableProps, Int)? {
        guard from + 1 < lines.count else { return nil }
        let head = lines[from].trimmingCharacters(in: .whitespaces)
        let delim = lines[from + 1].trimmingCharacters(in: .whitespaces)
        guard head.contains("|"), isDelimiterRow(delim) else { return nil }

        let labels = splitRow(head)
        let aligns = splitRow(delim).map { cell -> String in
            let l = cell.hasPrefix(":"), r = cell.hasSuffix(":")
            if l && r { return "center" }
            return r ? "right" : "left"
        }
        guard !labels.isEmpty else { return nil }
        let columns = labels.enumerated().map { idx, label in
            TableProps.Column(key: nil, label: label, align: idx < aligns.count ? aligns[idx] : "left")
        }

        // Whether the header opted into GFM's outer pipes. Body rows must agree: without this the
        // loop accepted ANY following line containing a pipe, so prose like "run `cat x | grep y`"
        // after a table was silently swallowed as a row and vanished into the grid.
        let outerPiped = head.hasPrefix("|")
        var rows: [[JSONValue]] = []
        var i = from + 2
        while i < lines.count {
            let r = lines[i].trimmingCharacters(in: .whitespaces)
            guard !r.isEmpty, r.contains("|") else { break }
            guard outerPiped ? r.hasPrefix("|") : splitRow(r).count >= columns.count else { break }
            var cells = splitRow(r).map { JSONValue.string($0) }
            // Ragged rows are common in hand-written and model-written tables alike. Pad or trim to
            // the header width so the Grid stays rectangular instead of dropping cells out of line.
            while cells.count < columns.count { cells.append(.string("")) }
            rows.append(Array(cells.prefix(columns.count)))
            i += 1
        }
        return (TableProps(columns: columns, rows: rows, caption: nil), i)
    }

    /// The heading level a setext underline implies: `===` is H1, `---` is H2. Any run length counts,
    /// which is why "Title" over "--------" used to render its dashes literally.
    private static func setextLevel(_ s: String) -> Int? {
        guard !s.isEmpty else { return nil }
        if s.allSatisfy({ $0 == "=" }) { return 1 }
        if s.allSatisfy({ $0 == "-" }) { return 2 }
        return nil
    }

    /// A thematic break: three or more of the same marker, spaces allowed between them.
    ///
    /// This was an equality test against exactly "---", "***" and "___", so every other spelling the
    /// spec allows fell through to prose — "----" rendered as literal dashes, and "* * *" was matched
    /// by the BULLET rule below it and drew a bullet reading "* *".
    private static func isThematicBreak(_ s: String) -> Bool {
        let bare = s.filter { $0 != " " }
        guard bare.count >= 3, let first = bare.first, "-*_".contains(first) else { return false }
        return bare.allSatisfy { $0 == first }
    }

    /// True for `|---|---|`, `| :--- | ---: |` and friends — at least one dash per cell, and nothing
    /// but dashes, colons, pipes and spaces overall.
    private static func isDelimiterRow(_ s: String) -> Bool {
        guard s.contains("-"), s.contains("|") else { return false }
        guard s.allSatisfy({ $0 == "-" || $0 == ":" || $0 == "|" || $0 == " " }) else { return false }
        let cells = splitRow(s)
        return !cells.isEmpty && cells.allSatisfy { $0.contains("-") }
    }

    /// Splits one table row into trimmed cells, dropping the leading/trailing pipes that GFM allows
    /// but does not require. Escaped `\|` is preserved as a literal pipe rather than splitting.
    private static func splitRow(_ s: String) -> [String] {
        var t = s
        if t.hasPrefix("|") { t.removeFirst() }
        if t.hasSuffix("|") { t.removeLast() }
        let sentinel = "\u{0}"
        return t.replacingOccurrences(of: "\\|", with: sentinel)
            .components(separatedBy: "|")
            .map {
                $0.replacingOccurrences(of: sentinel, with: "|")
                    .trimmingCharacters(in: .whitespaces)
            }
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
            // Reset parity at every line break. This toggle used to run over the WHOLE document, so
            // a single unpaired `**` anywhere — a glob like `**/*.swift`, a Python `**kwargs` —
            // inverted the sense of every `**` after it: the OPENING marker of a later `**Title**`
            // was read as a close, and the repair inserted its newline in the middle of the bold run,
            // destroying it. A genuinely jammed title is always within one line, so line-local parity
            // is both sufficient and self-correcting.
            if chars[i] == "\n" {
                inBold = false
                out.append(chars[i])
                i += 1
                continue
            }
            // A `**` fused to `/` or `*` is a glob, not emphasis — never a lost line break.
            if chars[i] == "*", i + 1 < chars.count, chars[i + 1] == "*",
               (i + 2 < chars.count && (chars[i + 2] == "/" || chars[i + 2] == ".")) ||
               (i > 0 && chars[i - 1] == "/") {
                out += "**"
                i += 2
                continue
            }
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

    /// Matches a bullet marker, carrying the nesting depth from the line's leading indent and the
    /// checkbox state of a `- [ ]` / `- [x]` task item.
    ///
    /// `s` must be the RAW line — the parse loop used to hand this a trimmed one, which threw the
    /// indent away before it could be measured and so flattened every nested list into siblings.
    private static func bullet(_ raw: String) -> ListItem? {
        let indent = leadingIndent(raw)
        let s = raw.trimmingCharacters(in: .whitespaces)
        for p in ["- ", "* ", "+ "] where s.hasPrefix(p) {
            var text = String(s.dropFirst(2))
            // GFM task list. Rendered as literal "[ ] foo" before this, and agents emit checklists
            // constantly, so it was one of the most frequently visible defects.
            var checked: Bool? = nil
            for (box, on) in [("[ ] ", false), ("[x] ", true), ("[X] ", true)] where text.hasPrefix(box) {
                checked = on
                text = String(text.dropFirst(box.count))
                break
            }
            // Two spaces per level is the common convention; four is also standard, so integer
            // division handles both. Capped so a deeply/erratically indented list stays on screen.
            return ListItem(depth: min(indent / 2, 3), text: text, checked: checked)
        }
        return nil
    }

    /// The text of a lazy continuation line — a wrapped list item whose second line carries no
    /// marker — or nil if the line ends the list.
    ///
    /// Without this the collection loop broke on the first unmarked line, so a wrapped bullet became
    /// a detached full-width paragraph wedged between two list items, still carrying its raw leading
    /// spaces and given the wider inter-block gap. It read as a formatting glitch, which it was.
    private static func lazyContinuation(_ raw: String) -> String? {
        let t = raw.trimmingCharacters(in: .whitespaces)
        // A blank line ends the list; so does anything that opens a different block. Checking these
        // explicitly is what keeps a fence or table immediately after a list from being glued onto
        // the previous item.
        guard !t.isEmpty, !t.hasPrefix("```"), !t.hasPrefix(">"), !t.hasPrefix("|"),
              heading(t) == nil, setextLevel(t) == nil, !isThematicBreak(t) else { return nil }
        return t
    }

    /// Leading indent in columns, counting a tab as four.
    private static func leadingIndent(_ s: String) -> Int {
        var n = 0
        for ch in s {
            if ch == " " { n += 1 } else if ch == "\t" { n += 4 } else { break }
        }
        return n
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
