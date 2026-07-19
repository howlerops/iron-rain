import SwiftUI
import OculusKit

/// State for the built-in editor: the file tree roots, the open file (buffer + base sha for
/// conflict-checked saves), and an optional review diff. All file access goes through the
/// daemon via `Model` (the files live on the host).
@MainActor final class CodeModel: ObservableObject {
    let model: Model

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
    @Published var imageData: Data?         // non-nil → the open file is an image
    @Published var imageMime: String?

    private var loadedSha = ""
    private var reloadTask: Task<Void, Never>?
    private var lspChangeTask: Task<Void, Never>?
    private var lspOpenPath: String?        // path currently open in a language server

    init(model: Model) { self.model = model }

    /// Diagnostics (linting/type errors) for the open file, sorted by position.
    var fileDiagnostics: [LSPDiagnostic] {
        guard let p = openPath else { return [] }
        return (model.diagnostics[p] ?? []).sorted {
            $0.startLine != $1.startLine ? $0.startLine < $1.startLine : $0.startChar < $1.startChar
        }
    }

    private static let imageExts: Set<String> = ["png", "jpg", "jpeg", "gif", "webp", "bmp", "tiff", "tif", "ico", "heic"]
    private func isImage(_ path: String) -> Bool {
        Self.imageExts.contains((path as NSString).pathExtension.lowercased())
    }

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
        lspChangeTask?.cancel()
        closeLSP()          // release the previously-open document
        diffText = nil
        imageData = nil
        imageMime = nil

        // Images render inline rather than as text.
        if isImage(node.path) {
            do {
                let b = try await model.fsReadBytes(node.path)
                openPath = node.path
                fileName = node.name
                imageData = Data(base64Encoded: b.data)
                imageMime = b.mime
                readOnly = true
                dirty = false
                conflict = false
                status = imageData == nil ? "Could not decode image" : nil
            } catch {
                status = "Open failed: \(error.localizedDescription)"
            }
            return
        }

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
            // Hand the document to its language server for diagnostics/types (no-op if none).
            if !readOnly {
                let p = f.path, c = content
                lspOpenPath = p
                Task { await model.lspOpen(p, content: c) }
            }
        } catch {
            status = "Open failed: \(error.localizedDescription)"
        }
    }

    func markEdited() {
        if !dirty { dirty = true }
        // Debounce didChange so we don't flood the language server on every keystroke.
        guard let p = lspOpenPath else { return }
        lspChangeTask?.cancel()
        lspChangeTask = Task { [weak self] in
            try? await Task.sleep(nanoseconds: 400_000_000)
            guard let self, !Task.isCancelled, self.openPath == p else { return }
            await self.model.lspChange(p, content: self.content)
        }
    }

    private func closeLSP() {
        if let p = lspOpenPath {
            lspOpenPath = nil
            Task { await model.lspClose(p) }
        }
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
            } else if let data = code.imageData {
                ImageFileView(data: data, palette: palette, theme: theme)
            } else if code.openPath != nil {
                CodeEditor(text: Binding(get: { code.content }, set: { code.content = $0; code.markEdited() }),
                           language: code.language, theme: theme, editable: !code.readOnly,
                           diagnostics: code.fileDiagnostics)
                if !code.fileDiagnostics.isEmpty {
                    Divider().overlay(palette.border)
                    DiagnosticsBar(diagnostics: code.fileDiagnostics, palette: palette)
                }
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
                diagnosticsCounts
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

    /// Error/warning counts from the language server for the open file.
    @ViewBuilder private var diagnosticsCounts: some View {
        let diags = code.fileDiagnostics
        let errors = diags.filter { $0.severity == 1 }.count
        let warnings = diags.filter { $0.severity == 2 }.count
        if errors > 0 || warnings > 0 {
            HStack(spacing: 8) {
                if errors > 0 {
                    Label("\(errors)", systemImage: "xmark.octagon.fill")
                        .font(.system(size: 11)).foregroundStyle(Color(hex: 0xF85149))
                }
                if warnings > 0 {
                    Label("\(warnings)", systemImage: "exclamationmark.triangle.fill")
                        .font(.system(size: 11)).foregroundStyle(Color(hex: 0xD9A520))
                }
            }
            .labelStyle(.titleAndIcon)
        }
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

/// Renders an image file inline (fetched as raw bytes through the daemon).
struct ImageFileView: View {
    let data: Data
    let palette: OculusPalette
    let theme: CodeTheme

    var body: some View {
        Group {
            if let img = PlatformImage(data: data) {
                ScrollView([.horizontal, .vertical]) {
                    Image(platformImage: img).resizable().scaledToFit().padding(20)
                        .frame(maxWidth: .infinity, maxHeight: .infinity)
                }
            } else {
                VStack(spacing: 8) {
                    Image(systemName: "photo").font(.system(size: 28)).foregroundStyle(palette.mutedForeground)
                    Text("Unsupported image format").font(.system(size: 13)).foregroundStyle(palette.mutedForeground)
                }
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(theme.background)
    }
}

/// A compact problems list under the editor: language-server diagnostics for the open file.
struct DiagnosticsBar: View {
    let diagnostics: [LSPDiagnostic]
    let palette: OculusPalette

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 0) {
                ForEach(diagnostics) { d in
                    HStack(alignment: .top, spacing: 8) {
                        Image(systemName: icon(d.severity)).font(.system(size: 11)).foregroundStyle(color(d.severity))
                            .frame(width: 14)
                        Text("\(d.startLine + 1):\(d.startChar + 1)")
                            .font(.system(size: 11, design: .monospaced)).foregroundStyle(palette.mutedForeground)
                            .frame(width: 54, alignment: .leading)
                        Text(d.message).font(.system(size: 11)).foregroundStyle(palette.foreground)
                            .fixedSize(horizontal: false, vertical: true)
                        Spacer(minLength: 6)
                        if let s = d.source, !s.isEmpty {
                            Text(s).font(.system(size: 10)).foregroundStyle(palette.mutedForeground)
                        }
                    }
                    .padding(.horizontal, 12).padding(.vertical, 4)
                    .frame(maxWidth: .infinity, alignment: .leading)
                }
            }
            .padding(.vertical, 4)
        }
        .frame(maxHeight: 150)
        .background(palette.background)
    }

    private func icon(_ sev: Int) -> String {
        switch sev { case 1: return "xmark.octagon.fill"; case 2: return "exclamationmark.triangle.fill"
        default: return "info.circle.fill" }
    }
    private func color(_ sev: Int) -> Color {
        switch sev { case 1: return Color(hex: 0xF85149); case 2: return Color(hex: 0xD9A520)
        default: return palette.mutedForeground }
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
