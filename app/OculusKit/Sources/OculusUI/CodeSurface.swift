import SwiftUI
import OculusKit

/// State for the built-in editor: the file tree roots, the open file (buffer + base sha for
/// conflict-checked saves), and an optional review diff. All file access goes through the
/// daemon via `Model` (the files live on the host).
@MainActor final class CodeModel: ObservableObject {
    private let model: Model

    @Published var roots: [FSNode] = []
    @Published var openPath: String?
    @Published var fileName: String = ""
    @Published var content: String = ""     // editable buffer
    @Published var language: CodeLanguage = .plain
    @Published var readOnly = false         // binary/oversized/no-write
    @Published var dirty = false
    @Published var saving = false
    @Published var conflict = false
    @Published var diffText: String?        // non-nil → reviewing changes
    @Published var status: String?

    private var loadedSha = ""
    private var reloadTask: Task<Void, Never>?

    init(model: Model) { self.model = model }

    func loadRoots() async {
        do { roots = (try await model.fsTree(nil)).roots ?? [] }
        catch { status = "\(error.localizedDescription)" }
    }

    func children(of path: String) async -> [FSNode] {
        (try? await model.fsTree(path))?.entries ?? []
    }

    /// Opens a file into the editor (read-only for binary/oversized) and starts a poll that
    /// live-reloads it if the agent changes it on disk while the buffer is clean.
    func open(_ node: FSNode) async {
        reloadTask?.cancel()
        diffText = nil
        do {
            let f = try await model.fsRead(node.path)
            openPath = f.path
            fileName = node.name
            loadedSha = f.sha
            language = CodeLanguage.infer(fromPath: f.path)
            readOnly = (f.binary ?? false) || (f.truncated ?? false)
            content = (f.binary ?? false) ? "(binary file — \(f.size ?? 0) bytes)" : (f.content ?? "")
            dirty = false
            conflict = false
            status = (f.truncated ?? false) ? "Large file — read-only preview" : nil
            startReloadPoll()
        } catch {
            status = "Open failed: \(error.localizedDescription)"
        }
    }

    func markEdited() {
        if !dirty { dirty = true }
    }

    /// Saves the buffer if the on-disk sha still matches (conflict otherwise).
    func save() async {
        guard let path = openPath, dirty, !readOnly else { return }
        saving = true
        defer { saving = false }
        do {
            let res = try await model.fsWrite(path, content: content, baseSha: loadedSha)
            if res.conflict == true {
                conflict = true
                status = "File changed on disk — reload or overwrite"
                return
            }
            loadedSha = res.sha ?? loadedSha
            dirty = false
            conflict = false
            status = "Saved"
        } catch {
            status = "Save failed: \(error.localizedDescription)"
        }
    }

    /// Overwrites despite a conflict (re-reads the current sha, then writes).
    func overwrite() async {
        guard let path = openPath else { return }
        if let cur = try? await model.fsRead(path) { loadedSha = cur.sha }
        conflict = false
        await save()
    }

    /// Discards local edits and reloads from disk.
    func reload() async {
        guard let path = openPath else { return }
        if let f = try? await model.fsRead(path) {
            loadedSha = f.sha
            content = f.content ?? content
            dirty = false
            conflict = false
            status = nil
        }
    }

    /// Shows a session's changes (or an in-root path's) as a diff.
    func review(sessionID: String? = nil, path: String? = nil) async {
        do { diffText = try await model.fsDiff(sessionID: sessionID, path: path) }
        catch { status = "Diff failed: \(error.localizedDescription)" }
    }

    func closeDiff() { diffText = nil }

    private func startReloadPoll() {
        let path = openPath
        reloadTask = Task { [weak self] in
            while !Task.isCancelled {
                try? await Task.sleep(nanoseconds: 2_500_000_000)
                guard let self, self.openPath == path else { return }
                if self.dirty || self.readOnly { continue }
                guard let f = try? await self.model.fsRead(path ?? "") else { continue }
                if f.sha != self.loadedSha, !self.dirty {
                    self.loadedSha = f.sha
                    self.content = f.content ?? self.content
                    self.status = "Reloaded (changed on disk)"
                }
            }
        }
    }

    deinit { reloadTask?.cancel() }
}

/// The Code detail surface: a file tree on the left, the editor or a review diff on the right.
struct CodeSurface: View {
    @ObservedObject var model: Model
    @StateObject private var code: CodeModel
    @Environment(\.colorScheme) private var scheme
    private var palette: OculusPalette { .current(scheme) }
    private var theme: CodeTheme { .current(scheme) }
    /// When set, open in review mode for this session's changes.
    let reviewSessionID: String?

    init(model: Model, reviewSessionID: String? = nil) {
        self.model = model
        self.reviewSessionID = reviewSessionID
        _code = StateObject(wrappedValue: CodeModel(model: model))
    }

    var body: some View {
        HStack(spacing: 0) {
            FileTreeView(code: code)
                .frame(width: 240)
                .background(palette.background)
            Divider().overlay(palette.border)
            editorPane
                .frame(maxWidth: .infinity, maxHeight: .infinity)
        }
        .task {
            await code.loadRoots()
            if let sid = reviewSessionID { await code.review(sessionID: sid) }
        }
    }

    @ViewBuilder private var editorPane: some View {
        VStack(spacing: 0) {
            editorToolbar
            Divider().overlay(palette.border)
            if let diff = code.diffText {
                DiffView(diff: diff, palette: palette, theme: theme)
            } else if code.openPath != nil {
                CodeEditor(text: Binding(get: { code.content }, set: { code.content = $0; code.markEdited() }),
                           language: code.language, theme: theme, editable: !code.readOnly)
            } else {
                emptyState
            }
        }
    }

    private var editorToolbar: some View {
        HStack(spacing: 10) {
            if code.diffText != nil {
                Label("Reviewing changes", systemImage: "plus.forwardslash.minus")
                    .font(.system(size: 12, weight: .medium)).foregroundStyle(palette.mutedForeground)
                Spacer()
                Button("Close diff") { code.closeDiff() }.font(.system(size: 12))
            } else {
                Text(code.fileName.isEmpty ? "No file open" : code.fileName)
                    .font(.system(size: 12, weight: .semibold)).lineLimit(1)
                if code.dirty { Circle().fill(palette.primary).frame(width: 6, height: 6) }
                if let s = code.status {
                    Text(s).font(.system(size: 11)).foregroundStyle(palette.mutedForeground).lineLimit(1)
                }
                Spacer()
                if code.conflict {
                    Button("Reload") { Task { await code.reload() } }.font(.system(size: 12))
                    Button("Overwrite") { Task { await code.overwrite() } }
                        .font(.system(size: 12)).foregroundStyle(palette.destructive)
                }
                Button { Task { await code.save() } } label: {
                    if code.saving { ProgressView().controlSize(.small) } else { Text("Save") }
                }
                .disabled(!code.dirty || code.readOnly || code.saving)
                .keyboardShortcut("s", modifiers: .command)
            }
        }
        .padding(.horizontal, 12).padding(.vertical, 7)
        .background(palette.background)
    }

    private var emptyState: some View {
        VStack(spacing: 8) {
            Image(systemName: "chevron.left.forwardslash.chevron.right")
                .font(.system(size: 30)).foregroundStyle(palette.mutedForeground)
            Text("Select a file to edit, or review a session's changes")
                .font(.system(size: 13)).foregroundStyle(palette.mutedForeground)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(theme.background)
    }
}

/// Lazy file tree: roots from fs.tree, directories expand on demand.
struct FileTreeView: View {
    @ObservedObject var code: CodeModel
    @Environment(\.colorScheme) private var scheme
    private var palette: OculusPalette { .current(scheme) }

    var body: some View {
        List {
            ForEach(code.roots) { root in
                DirNode(code: code, node: root, depth: 0)
            }
        }
        .listStyle(.sidebar)
    }
}

private struct DirNode: View {
    @ObservedObject var code: CodeModel
    let node: FSNode
    let depth: Int
    @State private var expanded = false
    @State private var children: [FSNode] = []
    @State private var loaded = false
    @Environment(\.colorScheme) private var scheme
    private var palette: OculusPalette { .current(scheme) }

    var body: some View {
        Button {
            expanded.toggle()
        } label: {
            HStack(spacing: 5) {
                Image(systemName: expanded ? "chevron.down" : "chevron.right")
                    .font(.system(size: 9, weight: .bold)).foregroundStyle(palette.mutedForeground)
                    .frame(width: 10)
                Image(systemName: "folder").font(.system(size: 11)).foregroundStyle(palette.primary)
                Text(node.name).font(.system(size: 12)).lineLimit(1)
                Spacer()
            }
            .padding(.leading, CGFloat(depth) * 10)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .onChange(of: expanded) { on in
            if on && !loaded { loaded = true; Task { children = await code.children(of: node.path) } }
        }
        if expanded {
            ForEach(children) { child in
                if child.dir {
                    DirNode(code: code, node: child, depth: depth + 1)
                } else {
                    FileRow(code: code, node: child, depth: depth + 1)
                }
            }
        }
    }
}

private struct FileRow: View {
    @ObservedObject var code: CodeModel
    let node: FSNode
    let depth: Int
    @Environment(\.colorScheme) private var scheme
    private var palette: OculusPalette { .current(scheme) }

    var body: some View {
        Button {
            Task { await code.open(node) }
        } label: {
            HStack(spacing: 5) {
                Image(systemName: "doc.text").font(.system(size: 11)).foregroundStyle(palette.mutedForeground)
                Text(node.name).font(.system(size: 12))
                    .foregroundStyle(code.openPath == node.path ? palette.primary : palette.foreground)
                    .lineLimit(1)
                Spacer()
            }
            .padding(.leading, CGFloat(depth) * 10 + 15)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
    }
}

/// Renders a unified diff with per-line add/remove backgrounds.
struct DiffView: View {
    let diff: String
    let palette: OculusPalette
    let theme: CodeTheme

    private var lines: [(Int, String)] {
        Array(diff.split(separator: "\n", omittingEmptySubsequences: false).map(String.init).enumerated())
    }

    var body: some View {
        if diff.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
            VStack(spacing: 6) {
                Image(systemName: "checkmark.circle").font(.system(size: 28)).foregroundStyle(palette.mutedForeground)
                Text("No changes").font(.system(size: 13)).foregroundStyle(palette.mutedForeground)
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity)
            .background(theme.background)
        } else {
            ScrollView([.vertical, .horizontal]) {
                VStack(alignment: .leading, spacing: 0) {
                    ForEach(lines, id: \.0) { _, line in
                        row(line)
                    }
                }
                .padding(.vertical, 6)
                .frame(minWidth: 0, maxWidth: .infinity, alignment: .leading)
            }
            .background(theme.background)
        }
    }

    private func row(_ line: String) -> some View {
        let (bg, fg): (Color, Color)
        if line.hasPrefix("+") && !line.hasPrefix("+++") {
            bg = Color(hex: 0x2EA043).opacity(0.16); fg = theme.plain
        } else if line.hasPrefix("-") && !line.hasPrefix("---") {
            bg = Color(hex: 0xF85149).opacity(0.16); fg = theme.plain
        } else if line.hasPrefix("@@") {
            bg = palette.primary.opacity(0.10); fg = palette.mutedForeground
        } else if line.hasPrefix("diff ") || line.hasPrefix("index ") || line.hasPrefix("+++") || line.hasPrefix("---") {
            bg = .clear; fg = palette.mutedForeground
        } else {
            bg = .clear; fg = theme.plain
        }
        return Text(line.isEmpty ? " " : line)
            .font(.system(size: 12, design: .monospaced))
            .foregroundStyle(fg)
            .padding(.horizontal, 10).padding(.vertical, 0.5)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(bg)
    }
}
