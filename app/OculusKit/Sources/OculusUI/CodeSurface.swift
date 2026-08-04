import SwiftUI
import OculusKit

/// State for the built-in editor: the file tree roots, the open file (buffer + base sha for
/// conflict-checked saves), and an optional review diff. All file access goes through the
/// daemon via `Model` (the files live on the host).
/// A caret target (0-based line/char) the editor should scroll to and select.
struct EditorTarget: Equatable { let line: Int; let char: Int }

/// One open editor tab — the full buffered state of a file so switching tabs keeps edits.
struct OpenTab: Identifiable, Equatable {
    let id: String  // == path
    var path: String
    var name: String
    var content: String
    var loadedSha: String
    var language: CodeLanguage
    var readOnly: Bool
    var dirty: Bool
    var imageData: Data?
    var imageMime: String?
    var lspOpen: Bool
}

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
    @Published var scrollTarget: EditorTarget?  // editor moves the caret here, then clears it
    @Published var serverSuggestion: LSPServerInfo?  // non-nil → offer to install a language server
    @Published var installing = false
    @Published var formatOnSave = false
    @Published var formatting = false
    // References / rename / symbols / search.
    @Published var references: [LSPLocation] = []
    @Published var showReferences = false
    @Published var renaming = false
    @Published var symbols: [LSPSymbol] = []
    @Published var searchQuery = ""
    @Published var searchResults: [FSSearchHit] = []
    @Published var searchRegex = false
    @Published var searching = false
    // Open tabs (multiple files).
    @Published var tabs: [OpenTab] = []
    @Published var activeTabID: String?
    /// Bumped every time something becomes worth SHOWING (a file opened, a tab activated, a diff
    /// loaded). The compact layout pushes its detail off this rather than off `openPath`, because
    /// re-opening the file you already had open leaves `openPath` unchanged — and then tapping the
    /// file you just came back from would do nothing at all.
    @Published private(set) var revealCount = 0
    var scopeSessionID: String?  // set by the view, for workspace-scoped search
    private var dismissedLangs: Set<String> = []

    private var loadedSha = ""
    private var reloadTask: Task<Void, Never>?
    private var lspChangeTask: Task<Void, Never>?
    private var lspOpenPath: String?        // path currently open in a language server
    private var caretLine = 0
    private var caretChar = 0

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

    /// Loads the file-tree roots. With sessionID, scoped to that session's workspace folder(s).
    func loadRoots(sessionID: String? = nil) async {
        do { roots = (try await model.fsTree(nil, sessionID: sessionID)).roots ?? [] }
        catch { status = "\(error.localizedDescription)" }
    }

    func children(of path: String) async -> [FSNode] {
        (try? await model.fsTree(path))?.entries ?? []
    }

    /// Opens a file into the editor (read-only for binary/oversized) and starts a poll that
    /// live-reloads it if the agent changes it on disk while the buffer is clean.
    func open(_ node: FSNode) async { await openFile(path: node.path, name: node.name) }

    func openFile(path nodePath: String, name nodeName: String) async {
        // Already open in a tab → just activate it (no reload, keeps unsaved edits).
        if tabs.contains(where: { $0.id == nodePath }) { activateTab(nodePath); return }
        persistActiveTab()  // stash the outgoing buffer into its tab

        reloadTask?.cancel()
        lspChangeTask?.cancel()
        diffText = nil
        imageData = nil
        imageMime = nil
        serverSuggestion = nil

        // Images render inline rather than as text.
        if isImage(nodePath) {
            do {
                let b = try await model.fsReadBytes(nodePath)
                openPath = nodePath
                fileName = nodeName
                imageData = Data(base64Encoded: b.data)
                imageMime = b.mime
                readOnly = true
                dirty = false
                conflict = false
                lspOpenPath = nil
                status = imageData == nil ? "Could not decode image" : nil
                registerActiveTab()
            } catch {
                status = "Open failed: \(error.localizedDescription)"
            }
            return
        }

        do {
            let f = try await model.fsRead(nodePath)
            openPath = f.path
            fileName = nodeName
            loadedSha = f.sha
            language = CodeLanguage.infer(fromPath: f.path)
            readOnly = (f.binary ?? false) || (f.truncated ?? false)
            content = (f.binary ?? false) ? "(binary file — \(f.size ?? 0) bytes)" : (f.content ?? "")
            dirty = false
            conflict = false
            status = (f.truncated ?? false) ? "Large file — read-only preview" : nil
            startReloadPoll()
            // Hand the document to its language server for diagnostics/types (no-op if none),
            // and — if the file's language has no server installed — offer to install one.
            symbols = []
            lspOpenPath = nil
            if !readOnly {
                let p = f.path, c = content
                lspOpenPath = p
                Task { await model.lspOpen(p, content: c); await self.loadSymbols() }
                Task { [weak self] in
                    guard let info = await self?.model.lspServerInfo(p) else { return }
                    guard let self, self.openPath == p, !info.language.isEmpty, !info.installed,
                          !self.dismissedLangs.contains(info.language) else { return }
                    self.serverSuggestion = info
                }
            }
            registerActiveTab()
        } catch {
            status = "Open failed: \(error.localizedDescription)"
        }
    }

    // MARK: tabs (multiple open files)

    private func snapshotLive() -> OpenTab? {
        guard let p = openPath else { return nil }
        return OpenTab(id: p, path: p, name: fileName, content: content, loadedSha: loadedSha,
                       language: language, readOnly: readOnly, dirty: dirty,
                       imageData: imageData, imageMime: imageMime, lspOpen: lspOpenPath == p)
    }

    /// Save the active buffer's live state back into its tab (before switching away).
    private func persistActiveTab() {
        guard let snap = snapshotLive() else { return }
        if let i = tabs.firstIndex(where: { $0.id == snap.id }) { tabs[i] = snap } // else: not a tab yet
    }

    /// Register (or refresh) the current live buffer as the active tab.
    private func registerActiveTab() {
        guard let snap = snapshotLive() else { return }
        if let i = tabs.firstIndex(where: { $0.id == snap.id }) { tabs[i] = snap } else { tabs.append(snap) }
        activeTabID = snap.id
        revealCount += 1
    }

    /// Switch to an already-open tab, hydrating the editor from its buffered state.
    func activateTab(_ id: String) {
        guard id != activeTabID, let tab = tabs.first(where: { $0.id == id }) else { return }
        persistActiveTab()
        reloadTask?.cancel()
        lspChangeTask?.cancel()
        diffText = nil
        serverSuggestion = nil
        openPath = tab.path
        fileName = tab.name
        content = tab.content
        loadedSha = tab.loadedSha
        language = tab.language
        readOnly = tab.readOnly
        dirty = tab.dirty
        imageData = tab.imageData
        imageMime = tab.imageMime
        lspOpenPath = tab.lspOpen ? tab.path : nil
        conflict = false
        activeTabID = tab.id
        symbols = []
        revealCount += 1
        if !readOnly { startReloadPoll(); Task { await loadSymbols() } }
    }

    /// Close a tab (releases its language-server doc); switches to a neighbor if it was active.
    func closeTab(_ id: String) {
        if let t = tabs.first(where: { $0.id == id }) { Task { await model.lspClose(t.path) } }
        let wasActive = activeTabID == id
        tabs.removeAll { $0.id == id }
        guard wasActive else { return }
        if let next = tabs.last {
            activeTabID = nil // force activateTab to hydrate
            activateTab(next.id)
        } else {
            clearEditor()
        }
    }

    private func clearEditor() {
        reloadTask?.cancel()
        openPath = nil; fileName = ""; content = ""; loadedSha = ""; dirty = false
        readOnly = false; conflict = false; imageData = nil; imageMime = nil
        lspOpenPath = nil; activeTabID = nil; symbols = []; showReferences = false
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

    /// The caret moved: remember it (the go-to-definition button acts at the caret).
    func caretMoved(line: Int, char: Int) {
        caretLine = line; caretChar = char
    }

    /// Type/doc info at a position, for the mouse-hover popover ("" if none or no server).
    func hover(line: Int, char: Int) async -> String {
        guard let p = lspOpenPath else { return "" }
        return await model.lspHover(p, line: line, character: char)
    }

    /// Autocomplete suggestions at a position (for the completion popup).
    func complete(line: Int, char: Int) async -> [LSPCompletionItem] {
        guard let p = lspOpenPath else { return [] }
        return await model.lspComplete(p, line: line, character: char)
    }

    /// Formats the whole buffer via the language server (syncs the server first).
    func format() async {
        guard let p = lspOpenPath, !readOnly else { return }
        formatting = true
        defer { formatting = false }
        await model.lspChange(p, content: content) // ensure the server sees the current text
        if let formatted = await model.lspFormat(p, content: content) {
            content = formatted
            markEdited()
            status = "Formatted"
        }
    }

    /// Installs the suggested language server, then re-opens the file so it takes effect.
    func installServer() async {
        guard let p = openPath, serverSuggestion != nil else { return }
        installing = true
        defer { installing = false }
        let result = await model.lspInstall(p)
        if result?.installed == true {
            serverSuggestion = nil
            status = "Language server installed"
            lspOpenPath = p
            await model.lspOpen(p, content: content) // start it now
        } else {
            status = result?.message ?? "Install failed"
        }
    }

    /// Dismisses the install suggestion and stops offering it for this language this session.
    func dismissSuggestion() {
        if let lang = serverSuggestion?.language { dismissedLangs.insert(lang) }
        serverSuggestion = nil
    }

    /// Jumps to the definition of the symbol at the caret (opens the target file if needed).
    func jumpToDefinition() async {
        guard let p = lspOpenPath else { return }
        guard let def = await model.lspDefinition(p, line: caretLine, character: caretChar) else {
            status = "No definition found"; return
        }
        if def.path != openPath {
            await openFile(path: def.path, name: (def.path as NSString).lastPathComponent)
        }
        scrollTarget = EditorTarget(line: def.line, char: def.character)
    }

    /// The editor consumed a scroll target (moved the caret) — clear it.
    func consumeScrollTarget() { scrollTarget = nil }

    // MARK: references / rename / symbols / search

    /// Opens a file at a location and scrolls to it (shared by definition/references/search).
    func openAt(path: String, line: Int, character: Int = 0) async {
        if path != openPath {
            await openFile(path: path, name: (path as NSString).lastPathComponent)
        } else {
            revealCount += 1   // already the open file: still a request to look at it
        }
        scrollTarget = EditorTarget(line: line, char: character)
    }

    /// Finds all references to the symbol at the caret (shown in the bottom panel).
    func findReferences() async {
        guard let p = lspOpenPath else { return }
        references = await model.lspReferences(p, line: caretLine, character: caretChar)
        showReferences = !references.isEmpty
        if references.isEmpty { status = "No references found" }
    }

    /// Renames the symbol at the caret across the workspace, then reloads the buffer.
    func rename(to newName: String) async {
        guard let p = lspOpenPath, !newName.trimmingCharacters(in: .whitespaces).isEmpty else { return }
        renaming = true
        defer { renaming = false }
        let files = await model.lspRename(p, line: caretLine, character: caretChar, newName: newName)
        if files.isEmpty { status = "Rename failed or nothing to rename"; return }
        await reload() // pick up the change in the current buffer
        status = "Renamed across \(files.count) file\(files.count == 1 ? "" : "s")"
    }

    /// Loads the document outline (symbols) for the open file.
    func loadSymbols() async {
        guard let p = lspOpenPath else { symbols = []; return }
        symbols = await model.lspSymbols(p)
    }

    /// Runs a workspace text search scoped to this session.
    func runSearch() async {
        let q = searchQuery.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !q.isEmpty else { searchResults = []; return }
        searching = true
        defer { searching = false }
        searchResults = await model.fsSearch(q, sessionID: scopeSessionID, regex: searchRegex)
    }

    /// Saves the buffer if the on-disk sha still matches (conflict otherwise).
    func save() async {
        guard let path = openPath, dirty, !readOnly else { return }
        if formatOnSave, lspOpenPath == path {
            await model.lspChange(path, content: content)
            if let formatted = await model.lspFormat(path, content: content) { content = formatted }
        }
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
        do {
            diffText = try await model.fsDiff(sessionID: sessionID, path: path)
            revealCount += 1
        }
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
    @Environment(\.accessibilityDifferentiateWithoutColor) private var differentiateWithoutColor
    private var palette: OculusPalette { .current(scheme) }
    private var theme: CodeTheme { .current(scheme) }
    /// The session whose workspace folder(s) scope the file tree (nil → browse all roots).
    let sessionID: String?
    /// When set, open in review mode for this session's changes.
    let reviewSessionID: String?

    @State private var sidebarMode: SidebarMode = .files
    @State private var renameText = ""
    @State private var showRename = false
    #if os(iOS)
    @Environment(\.horizontalSizeClass) private var hSize
    /// Compact only: whether the editor/diff is pushed on top of the file tree.
    @State private var showDetail = false
    #endif

    enum SidebarMode: Hashable { case files, search, outline }

    init(model: Model, sessionID: String? = nil, reviewSessionID: String? = nil) {
        self.model = model
        self.sessionID = sessionID
        self.reviewSessionID = reviewSessionID
        _code = StateObject(wrappedValue: CodeModel(model: model))
    }

    var body: some View {
        layout
            .task {
                code.scopeSessionID = sessionID
                await code.loadRoots(sessionID: sessionID)
                if let sid = reviewSessionID { await code.review(sessionID: sid) }
            }
            .alert("Rename symbol", isPresented: $showRename) {
                TextField("New name", text: $renameText).plainInput()
                Button("Rename") { let n = renameText; Task { await code.rename(to: n) } }
                Button("Cancel", role: .cancel) {}
            } message: { Text("Renames every reference across the workspace.") }
    }

    @ViewBuilder private var layout: some View {
        #if os(iOS)
        if hSize == .compact { compactLayout } else { splitLayout }
        #else
        splitLayout
        #endif
    }

    /// Mac and regular-width iPad: tree and editor side by side.
    private var splitLayout: some View {
        HStack(spacing: 0) {
            sidebar
                .frame(width: 250)
                .background(palette.background)
            Divider().overlay(palette.border)
            editorPane
                .frame(maxWidth: .infinity, maxHeight: .infinity)
        }
    }

    #if os(iOS)
    /// Phone (and iPad in Slide Over): the 250pt tree left about 140pt of editor on a 390pt screen,
    /// which is not an editor. The tree becomes the whole screen and the editor/diff is PUSHED onto
    /// the navigation stack this surface is already sitting on — so back goes tree → chat, and each
    /// view gets the full width. No nested NavigationStack: that would hide the outer back button
    /// and strand the user in Code with no way to the transcript.
    private var compactLayout: some View {
        sidebar
            .background(palette.background)
            .navigationDestination(isPresented: $showDetail) {
                editorPane
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
                    .background(palette.background)
                    .navigationTitle(detailTitle)
                    .navigationBarTitleDisplayMode(.inline)
            }
            .onChange(of: code.revealCount) { _ in showDetail = true }
    }

    private var detailTitle: String {
        if code.diffText != nil { return "Changes" }
        return code.fileName.isEmpty ? "Editor" : code.fileName
    }
    #endif

    private var sidebar: some View {
        VStack(spacing: 0) {
            sessionContextHeader
            Divider().overlay(palette.border)
            Picker("Sidebar", selection: $sidebarMode) {
                Image(systemName: "folder").tag(SidebarMode.files)
                    .accessibilityLabel("Files")
                Image(systemName: "magnifyingglass").tag(SidebarMode.search)
                    .accessibilityLabel("Search")
                Image(systemName: "list.bullet.indent").tag(SidebarMode.outline)
                    .accessibilityLabel("Outline")
            }
            .pickerStyle(.segmented).labelsHidden()
            .padding(.horizontal, 8).padding(.vertical, 6)
            Divider().overlay(palette.border)
            switch sidebarMode {
            case .files: FileTreeView(code: code)
            case .search: SearchPanel(code: code, palette: palette)
            case .outline: OutlinePanel(code: code, palette: palette)
            }
        }
    }

    /// Shows which session's workspace the editor is scoped to — its active directory, worktree
    /// branch, or cross-repo workspace — so the Code tab reads as "this session's files", not a
    /// generic global browser. Falls back to a clear "all projects" hint when no session is open.
    @ViewBuilder private var sessionContextHeader: some View {
        let s = model.sessions.first { $0.id == sessionID }
        HStack(spacing: 7) {
            // Back to the chat transcript. macOS ONLY: there the Code surface replaces the detail
            // column, which has no navigation stack, so this is the only way back. On iOS the
            // surface is PUSHED and the navigation bar already draws a back button — rendering this
            // too gave the same screen two identical back controls.
            #if os(macOS)
            Button { model.codeReviewTarget = nil } label: {
                Label("Chat", systemImage: "chevron.left").font(.caption)
            }
            .buttonStyle(.plain).foregroundStyle(palette.primaryText)
            .accessibilityLabel("Back to chat")
            Divider().frame(height: 14)
            #endif
            Image(systemName: s == nil ? "folder" : "chevron.left.forwardslash.chevron.right")
                .font(.caption).foregroundStyle(s == nil ? palette.mutedForeground : palette.primary)
                .accessibilityHidden(true)
            VStack(alignment: .leading, spacing: 1) {
                Text(scopeTitle(s)).font(.caption.bold()).lineLimit(1)
                Text(scopeSubtitle(s)).font(.caption2).foregroundStyle(palette.mutedForeground)
                    .lineLimit(1).truncationMode(.middle)
            }
            Spacer(minLength: 0)
            if reviewSessionID != nil {
                Text("review").font(.caption2.bold()).foregroundStyle(palette.primaryText)
                    .padding(.horizontal, 6).padding(.vertical, 2)
                    .background(palette.primary.opacity(0.15), in: Capsule())
            }
        }
        .padding(.horizontal, 10).padding(.vertical, 7)
        .background(palette.secondary.opacity(0.4))
    }

    private func scopeTitle(_ s: Session?) -> String {
        guard let s else { return "All projects" }
        return s.name ?? s.workspaceName ?? s.title ?? "Session \(s.id.prefix(6))"
    }

    private func scopeSubtitle(_ s: Session?) -> String {
        guard let s else { return "browsing every registered project — open a session to edit its files" }
        if s.isWorkspace == true { return "cross-repo workspace · edit any member repo" }
        if let b = s.branch, !b.isEmpty { return "worktree · \(b)" }
        if let cwd = s.cwd, !cwd.isEmpty { return cwd }
        return s.provider
    }

    @ViewBuilder private var editorPane: some View {
        VStack(spacing: 0) {
            if !code.tabs.isEmpty {
                EditorTabBar(code: code, palette: palette)
                Divider().overlay(palette.border)
            }
            editorToolbar
            Divider().overlay(palette.border)
            if let info = code.serverSuggestion {
                ServerInstallBanner(info: info, installing: code.installing, palette: palette,
                                    onInstall: { Task { await code.installServer() } },
                                    onDismiss: { code.dismissSuggestion() })
                Divider().overlay(palette.border)
            }
            if let diff = code.diffText {
                DiffView(diff: diff, palette: palette, theme: theme)
            } else if let data = code.imageData {
                ImageFileView(data: data, palette: palette, theme: theme)
            } else if code.openPath != nil {
                CodeEditor(text: Binding(get: { code.content }, set: { code.content = $0; code.markEdited() }),
                           language: code.language, theme: theme, palette: palette, editable: !code.readOnly,
                           diagnostics: code.fileDiagnostics,
                           scrollTarget: code.scrollTarget,
                           onCaret: { line, char in code.caretMoved(line: line, char: char) },
                           onConsumedScroll: { code.consumeScrollTarget() },
                           hoverProvider: { line, char in await code.hover(line: line, char: char) },
                           completionProvider: { line, char in await code.complete(line: line, char: char) })
                if !code.fileDiagnostics.isEmpty {
                    Divider().overlay(palette.border)
                    DiagnosticsBar(diagnostics: code.fileDiagnostics, palette: palette)
                }
                if code.showReferences {
                    Divider().overlay(palette.border)
                    ReferencesPanel(code: code, palette: palette)
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
                    .font(.footnote.weight(.medium)).foregroundStyle(palette.mutedForeground)
                Spacer()
                Button("Close diff") { code.closeDiff() }.font(.footnote)
            } else {
                Text(code.fileName.isEmpty ? "No file open" : code.fileName)
                    .font(.footnote.weight(.semibold)).lineLimit(1)
                dirtyIndicator
                diagnosticsCounts
                if let s = code.status {
                    Text(s).font(.caption).foregroundStyle(palette.mutedForeground).lineLimit(1)
                }
                Spacer()
                if !code.readOnly {
                    toolbarIcon("arrow.uturn.forward.square", label: "Jump to definition (caret)") {
                        Task { await code.jumpToDefinition() }
                    }
                    toolbarIcon("text.magnifyingglass", label: "Find references (caret)") {
                        Task { await code.findReferences() }
                    }
                    toolbarIcon("pencil.and.outline", label: "Rename symbol (caret)") {
                        renameText = ""; showRename = true
                    }
                    Button { Task { await code.format() } } label: {
                        Group {
                            if code.formatting { ProgressView().controlSize(.small) }
                            else { Image(systemName: "text.alignleft") }
                        }
                        .frame(width: 44, height: 44).contentShape(Rectangle())
                    }
                    .font(.footnote).help("Format document (⌥⇧F)")
                    .buttonStyle(.plain).foregroundStyle(palette.mutedForeground)
                    .keyboardShortcut("f", modifiers: [.option, .shift])
                    .accessibilityLabel("Format document")
                    Menu {
                        Toggle("Format on save", isOn: $code.formatOnSave)
                    } label: {
                        Image(systemName: "ellipsis.circle").frame(width: 44, height: 44)
                            .contentShape(Rectangle())
                    }
                        .menuStyle(.borderlessButton).fixedSize()
                        .font(.footnote).foregroundStyle(palette.mutedForeground)
                        .accessibilityLabel("Editor options")
                }
                if code.conflict {
                    Button("Reload") { Task { await code.reload() } }.font(.footnote)
                    Button("Overwrite") { Task { await code.overwrite() } }
                        .font(.footnote).foregroundStyle(palette.destructive)
                }
                Button { Task { await code.save() } } label: {
                    if code.saving { ProgressView().controlSize(.small) } else { Text("Save") }
                }
                .disabled(!code.dirty || code.readOnly || code.saving)
                .keyboardShortcut("s", modifiers: .command)
            }
        }
        .padding(.horizontal, 12)
        .frame(minHeight: 44)   // the icon-only actions carry full-size touch targets
        .background(palette.background)
    }

    /// An icon-only toolbar action. Factored out so the accessibility label, the touch target and
    /// the hover help can never again be added to one button and forgotten on the next.
    private func toolbarIcon(_ symbol: String, label: String, action: @escaping () -> Void) -> some View {
        Button(action: action) {
            Image(systemName: symbol)
                .frame(width: 44, height: 44).contentShape(Rectangle())
        }
        .font(.footnote).help(label)
        .buttonStyle(.plain).foregroundStyle(palette.mutedForeground)
        .accessibilityLabel(label)
    }

    /// Unsaved-changes marker. The bare gold dot carried the whole meaning in colour, which is
    /// invisible under Differentiate Without Color and unspoken by VoiceOver.
    @ViewBuilder private var dirtyIndicator: some View {
        if code.dirty {
            if differentiateWithoutColor {
                Text("Unsaved").font(.caption2.weight(.semibold))
                    .foregroundStyle(palette.foreground)
                    .padding(.horizontal, 5).padding(.vertical, 1)
                    .overlay(OculusShape.rounded(4).strokeBorder(palette.foreground.opacity(0.5)))
            } else {
                Circle().fill(palette.primary).frame(width: 6, height: 6)
                    .accessibilityLabel("Unsaved changes")
            }
        }
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
                        .font(.caption).foregroundStyle(palette.destructive)
                        .accessibilityLabel("\(errors) error\(errors == 1 ? "" : "s")")
                }
                if warnings > 0 {
                    Label("\(warnings)", systemImage: "exclamationmark.triangle.fill")
                        .font(.caption).foregroundStyle(palette.warning)
                        .accessibilityLabel("\(warnings) warning\(warnings == 1 ? "" : "s")")
                }
            }
            .labelStyle(.titleAndIcon)
        }
    }

    private var emptyState: some View {
        VStack(spacing: 8) {
            Image(systemName: "chevron.left.forwardslash.chevron.right")
                .font(.largeTitle).foregroundStyle(palette.mutedForeground)
                .accessibilityHidden(true)
            Text("Select a file to edit, or review a session's changes")
                .font(.footnote).foregroundStyle(palette.mutedForeground)
                .multilineTextAlignment(.center)
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
                    .font(.caption2.weight(.bold)).foregroundStyle(palette.mutedForeground)
                    .frame(width: 10)
                Image(systemName: "folder").font(.caption).foregroundStyle(palette.primaryText)
                Text(node.name).font(.footnote).lineLimit(1)
                Spacer()
            }
            .padding(.leading, CGFloat(depth) * 10)
            .frame(minHeight: codeHitTarget)
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
                Image(systemName: "doc.text").font(.caption).foregroundStyle(palette.mutedForeground)
                Text(node.name).font(.footnote)
                    .foregroundStyle(code.openPath == node.path ? palette.primary : palette.foreground)
                    .lineLimit(1)
                Spacer()
            }
            .padding(.leading, CGFloat(depth) * 10 + 15)
            .frame(minHeight: codeHitTarget)
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
                    Image(systemName: "photo").font(.largeTitle).foregroundStyle(palette.mutedForeground)
                    Text("Unsupported image format").font(.footnote).foregroundStyle(palette.mutedForeground)
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
                        Image(systemName: icon(d.severity)).font(.caption).foregroundStyle(color(d.severity))
                            .frame(width: 14)
                        Text("\(d.startLine + 1):\(d.startChar + 1)")
                            .font(.system(.caption, design: .monospaced)).foregroundStyle(palette.mutedForeground)
                            .frame(minWidth: 54, alignment: .leading)
                        Text(d.message).font(.caption).foregroundStyle(palette.foreground)
                            .fixedSize(horizontal: false, vertical: true)
                        Spacer(minLength: 6)
                        if let s = d.source, !s.isEmpty {
                            Text(s).font(.caption2).foregroundStyle(palette.mutedForeground)
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
        switch sev { case 1: return palette.destructive; case 2: return palette.warning
        default: return palette.info }
    }
}

/// A VSCode-style banner offering to install the language server for the open file's language.
struct ServerInstallBanner: View {
    let info: LSPServerInfo
    let installing: Bool
    let palette: OculusPalette
    let onInstall: () -> Void
    let onDismiss: () -> Void

    var body: some View {
        HStack(spacing: 10) {
            Image(systemName: "wand.and.stars").font(.footnote).foregroundStyle(palette.primaryText)
            if info.installable {
                Text("Install **\(info.installLabel)** for \(info.language.capitalized) language support (autocomplete, diagnostics, types)?")
                    .font(.footnote).foregroundStyle(palette.foreground)
            } else {
                Text("\(info.language.capitalized) language support needs **\(info.installLabel)**.")
                    .font(.footnote).foregroundStyle(palette.foreground)
            }
            Spacer()
            if info.installable {
                Button(action: onInstall) {
                    if installing { ProgressView().controlSize(.small) } else { Text("Install") }
                }
                .buttonStyle(.borderedProminent).tint(palette.primary).controlSize(.small)
                .disabled(installing)
            }
            Button(action: onDismiss) {
                Image(systemName: "xmark")
                    .frame(width: codeHitTarget, height: codeHitTarget).contentShape(Rectangle())
            }
                .buttonStyle(.plain).font(.caption).foregroundStyle(palette.mutedForeground)
                .disabled(installing)
                .help("Dismiss")
                .accessibilityLabel("Dismiss language-server suggestion")
        }
        .padding(.horizontal, 12).padding(.vertical, 8)
        .background(palette.primary.opacity(0.08))
    }
}

/// The minimum hit target for the editor's dense icon-only chrome. 44pt is the touch floor; a
/// pointer does not need it, and forcing it on macOS would inflate the tab bar and toolbar.
#if os(macOS)
private let codeHitTarget: CGFloat = 24
#else
private let codeHitTarget: CGFloat = 44
#endif

/// A horizontal bar of open-file tabs above the editor (VSCode-style).
struct EditorTabBar: View {
    @ObservedObject var code: CodeModel
    let palette: OculusPalette

    var body: some View {
        ScrollView(.horizontal, showsIndicators: false) {
            HStack(spacing: 0) {
                ForEach(code.tabs) { tab in
                    let isActive = tab.id == code.activeTabID
                    // The active tab's dirty state is live; others use their stored snapshot.
                    let dirty = isActive ? code.dirty : tab.dirty
                    HStack(spacing: 6) {
                        // A Button, not an onTapGesture: activating a tab was the only way to switch
                        // files, and as a tap gesture VoiceOver exposed it as plain static text with
                        // no button trait and no way to trigger it.
                        Button { code.activateTab(tab.id) } label: {
                            Text(tab.name).font(.footnote.weight(isActive ? .semibold : .regular))
                                .foregroundStyle(isActive ? palette.foreground : palette.mutedForeground)
                                .lineLimit(1)
                                .frame(minHeight: codeHitTarget)
                                .contentShape(Rectangle())
                        }
                        .buttonStyle(.plain)
                        .accessibilityLabel(tab.name)
                        .accessibilityValue(dirty ? "Unsaved changes" : "")
                        .accessibilityAddTraits(isActive ? [.isButton, .isSelected] : .isButton)
                        Button { code.closeTab(tab.id) } label: {
                            // Fixed, not Dynamic Type: the dot and the × are deliberately different
                            // sizes so "unsaved" reads as a dot rather than a small close button,
                            // and both are centred in a fixed hit target.
                            Image(systemName: dirty ? "circle.fill" : "xmark")
                                .font(.system(size: dirty ? 7 : 9))
                                .frame(width: codeHitTarget, height: codeHitTarget)
                                .contentShape(Rectangle())
                        }
                        .buttonStyle(.plain).foregroundStyle(palette.mutedForeground)
                        .help(dirty ? "Close (unsaved changes)" : "Close tab")
                        .accessibilityLabel("Close \(tab.name)")
                    }
                    .padding(.leading, 12).padding(.trailing, 2)
                    .background(isActive ? palette.background : palette.card.opacity(0.4))
                    .overlay(alignment: .bottom) {
                        if isActive { Rectangle().fill(palette.primary).frame(height: 2) }
                    }
                    Divider().frame(height: 18).overlay(palette.border)
                }
            }
        }
        .background(palette.card.opacity(0.25))
    }
}

/// Workspace text-search panel (left sidebar).
struct SearchPanel: View {
    @ObservedObject var code: CodeModel
    let palette: OculusPalette

    var body: some View {
        VStack(spacing: 0) {
            HStack(spacing: 6) {
                Image(systemName: "magnifyingglass").font(.caption).foregroundStyle(palette.mutedForeground)
                TextField("Search workspace…", text: $code.searchQuery)
                    .textFieldStyle(.plain).font(.footnote)
                    .onSubmit { Task { await code.runSearch() } }
                    .submitLabel(.search)
                    .plainInput()
                Button { code.searchRegex.toggle(); Task { await code.runSearch() } } label: {
                    Text(".*").font(.caption.weight(.bold))
                        .frame(width: codeHitTarget, height: codeHitTarget).contentShape(Rectangle())
                }
                .buttonStyle(.plain).help("Regular expression")
                .foregroundStyle(code.searchRegex ? palette.primary : palette.mutedForeground)
                .accessibilityLabel("Regular expression")
                .accessibilityValue(code.searchRegex ? "On" : "Off")
            }
            .padding(.leading, 10)
            Divider().overlay(palette.border)
            if code.searching {
                ProgressView().controlSize(.small).frame(maxWidth: .infinity).padding(.top, 12)
                Spacer()
            } else if code.searchResults.isEmpty {
                Text(code.searchQuery.isEmpty ? "Type to search files" : "No results")
                    .font(.caption).foregroundStyle(palette.mutedForeground)
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
            } else {
                List(code.searchResults) { hit in
                    Button {
                        Task { await code.openAt(path: hit.path, line: max(hit.line - 1, 0), character: max(hit.col - 1, 0)) }
                    } label: {
                        VStack(alignment: .leading, spacing: 1) {
                            HStack(spacing: 4) {
                                Text((hit.path as NSString).lastPathComponent)
                                    .font(.caption.weight(.medium)).foregroundStyle(palette.primaryText)
                                Text(":\(hit.line)").font(.caption2).foregroundStyle(palette.mutedForeground)
                            }
                            Text(hit.text).font(.system(.caption, design: .monospaced))
                                .foregroundStyle(palette.foreground).lineLimit(1)
                        }
                        .frame(maxWidth: .infinity, alignment: .leading).contentShape(Rectangle())
                    }
                    .buttonStyle(.plain)
                }
                .listStyle(.plain)
            }
        }
    }
}

/// Document-symbol outline panel (left sidebar).
struct OutlinePanel: View {
    @ObservedObject var code: CodeModel
    let palette: OculusPalette

    var body: some View {
        if code.symbols.isEmpty {
            VStack(spacing: 8) {
                Text(code.openPath == nil ? "Open a file" : "No symbols")
                    .font(.caption).foregroundStyle(palette.mutedForeground)
                if code.openPath != nil {
                    Button("Reload") { Task { await code.loadSymbols() } }.font(.caption)
                }
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity)
        } else {
            List {
                ForEach(code.symbols) { sym in SymbolRow(code: code, sym: sym, depth: 0, palette: palette) }
            }
            .listStyle(.plain)
        }
    }
}

struct SymbolRow: View {
    @ObservedObject var code: CodeModel
    let sym: LSPSymbol
    let depth: Int
    let palette: OculusPalette

    var body: some View {
        Button {
            Task { await code.openAt(path: code.openPath ?? "", line: sym.line, character: sym.character) }
        } label: {
            HStack(spacing: 5) {
                Image(systemName: symbolIcon(sym.kind)).font(.caption2).foregroundStyle(palette.primaryText).frame(width: 13)
                Text(sym.name).font(.footnote).lineLimit(1)
                if let d = sym.detail, !d.isEmpty {
                    Text(d).font(.caption2).foregroundStyle(palette.mutedForeground).lineLimit(1)
                }
                Spacer()
            }
            .padding(.leading, CGFloat(depth) * 12)
            .frame(minHeight: codeHitTarget)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        if let children = sym.children {
            ForEach(children) { child in SymbolRow(code: code, sym: child, depth: depth + 1, palette: palette) }
        }
    }
}

/// LSP SymbolKind → an SF Symbol letter tile.
func symbolIcon(_ kind: Int) -> String {
    switch kind {
    case 5: return "c.square"        // class
    case 23: return "s.square"       // struct
    case 11: return "i.square"       // interface
    case 10: return "e.square"       // enum
    case 6, 9, 12: return "f.square" // method / constructor / function
    case 7, 8: return "p.square"     // property / field
    case 13, 14: return "v.square"   // variable / constant
    case 2, 3, 4: return "m.square"  // module / namespace / package
    default: return "circle"
    }
}

/// Find-references results (bottom panel of the editor).
struct ReferencesPanel: View {
    @ObservedObject var code: CodeModel
    let palette: OculusPalette

    var body: some View {
        VStack(spacing: 0) {
            HStack {
                Text("References (\(code.references.count))")
                    .font(.caption.weight(.semibold)).foregroundStyle(palette.mutedForeground)
                Spacer()
                Button { code.showReferences = false } label: {
                    Image(systemName: "xmark")
                        .frame(width: codeHitTarget, height: codeHitTarget).contentShape(Rectangle())
                }
                    .buttonStyle(.plain).font(.caption2).foregroundStyle(palette.mutedForeground)
                    .help("Close references")
                    .accessibilityLabel("Close references")
            }
            .padding(.leading, 12)
            ScrollView {
                VStack(alignment: .leading, spacing: 0) {
                    ForEach(code.references) { ref in
                        Button {
                            Task { await code.openAt(path: ref.path, line: ref.line, character: ref.character) }
                        } label: {
                            HStack(spacing: 6) {
                                Text((ref.path as NSString).lastPathComponent)
                                    .font(.caption.weight(.medium)).foregroundStyle(palette.primaryText)
                                Text(":\(ref.line + 1):\(ref.character + 1)")
                                    .font(.system(.caption2, design: .monospaced)).foregroundStyle(palette.mutedForeground)
                                Spacer()
                            }
                            .padding(.horizontal, 12).padding(.vertical, 3).contentShape(Rectangle())
                        }
                        .buttonStyle(.plain)
                    }
                }
            }
            .frame(maxHeight: 160)
        }
        .background(palette.background)
    }
}

/// Renders a unified diff with per-line add/remove backgrounds, old/new line numbers, and a soft
/// wrap toggle that defaults ON at compact width.
struct DiffView: View {
    let diff: String
    let palette: OculusPalette
    let theme: CodeTheme
    #if os(iOS)
    @Environment(\.horizontalSizeClass) private var hSize
    #endif
    /// nil = follow the width default; set once the reviewer picks explicitly.
    @State private var wrapOverride: Bool?

    /// One rendered diff row: the raw text plus the file line numbers it occupies.
    private struct Row: Identifiable {
        let id: Int
        let text: String
        let oldLine: Int?
        let newLine: Int?
    }

    private var compact: Bool {
        #if os(iOS)
        return hSize == .compact
        #else
        return false
        #endif
    }
    /// At 390pt a monospaced diff shows ~48 characters, so most real lines needed two horizontal
    /// drags — a pan that also fought the vertical scroll. Wrapping is the readable default there.
    private var wrapLines: Bool { wrapOverride ?? compact }

    /// The diff lines, numbered by walking each `@@` header's ranges.
    private var rows: [Row] {
        var out: [Row] = []
        var oldNo = 0
        var newNo = 0
        var inHunk = false
        for (i, raw) in diff.split(separator: "\n", omittingEmptySubsequences: false).map(String.init).enumerated() {
            if raw.hasPrefix("@@") {
                inHunk = true
                let s = Self.hunkStarts(from: raw)
                oldNo = s.old; newNo = s.new
                out.append(Row(id: i, text: raw, oldLine: nil, newLine: nil))
            } else if !inHunk || raw.hasPrefix("\\") {
                out.append(Row(id: i, text: raw, oldLine: nil, newLine: nil))
            } else if raw.hasPrefix("+") {
                out.append(Row(id: i, text: raw, oldLine: nil, newLine: newNo)); newNo += 1
            } else if raw.hasPrefix("-") {
                out.append(Row(id: i, text: raw, oldLine: oldNo, newLine: nil)); oldNo += 1
            } else {
                out.append(Row(id: i, text: raw, oldLine: oldNo, newLine: newNo)); oldNo += 1; newNo += 1
            }
        }
        return out
    }

    /// Old/new start lines from `@@ -12,7 +14,9 @@ context`. Only the span between the two `@@`
    /// markers is scanned — git's trailing function signature routinely contains `-`/`+` tokens.
    private static func hunkStarts(from header: String) -> (old: Int, new: Int) {
        guard let open = header.range(of: "@@"),
              let close = header.range(of: "@@", range: open.upperBound..<header.endIndex)
        else { return (1, 1) }
        var old = 1, new = 1
        for token in header[open.upperBound..<close.lowerBound].split(separator: " ") {
            guard let mark = token.first, mark == "-" || mark == "+" else { continue }
            guard let n = Int(token.dropFirst().prefix { $0.isNumber }) else { continue }
            if mark == "-" { old = n } else { new = n }
        }
        return (old, new)
    }

    private var numberColumnWidth: CGFloat {
        let widest = rows.reduce(0) { max($0, max($1.oldLine ?? 0, $1.newLine ?? 0)) }
        return CGFloat(max(String(widest).count, 2)) * 6
    }

    var body: some View {
        if diff.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
            VStack(spacing: 6) {
                Image(systemName: "checkmark.circle").font(.largeTitle)
                    .foregroundStyle(palette.mutedForeground).accessibilityHidden(true)
                Text("No changes").font(.footnote).foregroundStyle(palette.mutedForeground)
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity)
            .background(theme.background)
        } else {
            VStack(spacing: 0) {
                wrapBar
                Divider().overlay(palette.border)
                if wrapLines {
                    ScrollView(.vertical) { rowStack(width: numberColumnWidth) }
                } else {
                    ScrollView([.vertical, .horizontal]) { rowStack(width: numberColumnWidth) }
                }
            }
            .background(theme.background)
        }
    }

    private func rowStack(width: CGFloat) -> some View {
        VStack(alignment: .leading, spacing: 0) {
            ForEach(rows) { row in
                self.row(row, numberWidth: width)
            }
        }
        .padding(.vertical, 6)
        .frame(minWidth: 0, maxWidth: .infinity, alignment: .leading)
    }

    private var wrapBar: some View {
        HStack {
            Spacer()
            Button { wrapOverride = !wrapLines } label: {
                Image(systemName: wrapLines ? "arrow.turn.down.left" : "arrow.left.and.right")
                    .font(.footnote)
                    .frame(width: codeHitTarget, height: codeHitTarget).contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .foregroundStyle(wrapLines ? palette.primary : palette.mutedForeground)
            .help(wrapLines ? "Turn off soft wrap" : "Wrap long lines")
            .accessibilityLabel("Soft wrap")
            .accessibilityValue(wrapLines ? "On" : "Off")
        }
        .padding(.trailing, 4)
        .background(palette.background)
    }

    private func row(_ row: Row, numberWidth: CGFloat) -> some View {
        let line = row.text
        let (bg, fg): (Color, Color)
        if line.hasPrefix("+") && !line.hasPrefix("+++") {
            bg = palette.diffAdded.opacity(0.16); fg = theme.plain
        } else if line.hasPrefix("-") && !line.hasPrefix("---") {
            bg = palette.diffRemoved.opacity(0.16); fg = theme.plain
        } else if line.hasPrefix("@@") {
            bg = palette.primary.opacity(0.10); fg = palette.mutedForeground
        } else if line.hasPrefix("diff ") || line.hasPrefix("index ") || line.hasPrefix("+++") || line.hasPrefix("---") {
            bg = .clear; fg = palette.mutedForeground
        } else {
            bg = .clear; fg = theme.plain
        }
        // Code keeps a fixed size: it is read on a character grid, and Dynamic Type would break the
        // alignment between the gutter, the +/- marker column and the code.
        return HStack(alignment: .top, spacing: 0) {
            HStack(spacing: 4) {
                Text(row.oldLine.map(String.init) ?? "").frame(width: numberWidth, alignment: .trailing)
                Text(row.newLine.map(String.init) ?? "").frame(width: numberWidth, alignment: .trailing)
            }
            .font(.system(size: 10, design: .monospaced))
            .foregroundStyle(palette.mutedForeground.opacity(0.7))
            .padding(.horizontal, 6)
            .accessibilityHidden(true)
            // The marker sits in its own column so a wrapped continuation hangs under the code
            // rather than under the gutter, where it would read as a separate diff line.
            Text(line.isEmpty ? " " : String(line.prefix(1)))
                .frame(width: 10, alignment: .leading)
            wrapped(String(line.dropFirst(1)))
        }
        .font(.system(size: 12, design: .monospaced))
        .foregroundStyle(fg)
        .padding(.trailing, 8).padding(.vertical, 0.5)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(bg)
    }

    @ViewBuilder private func wrapped(_ text: String) -> some View {
        if wrapLines {
            Text(text.isEmpty ? " " : text)
                .fixedSize(horizontal: false, vertical: true)
                .frame(maxWidth: .infinity, alignment: .leading)
        } else {
            Text(text.isEmpty ? " " : text).lineLimit(1).fixedSize(horizontal: true, vertical: false)
        }
    }
}
