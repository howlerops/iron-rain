import SwiftUI
import OculusKit
import UniformTypeIdentifiers
#if os(iOS)
import PhotosUI
import UIKit
#elseif os(macOS)
import AppKit
#endif

/// The sticky bottom composer, styled like the Claude Code / opencode input: a single
/// rounded container with the text field on top and a toolbar row (attach · voice …
/// send) along the bottom. iOS + macOS.
struct Composer: View {
    @ObservedObject var model: Model
    /// The per-session store (Model.drafts). Read on open, written on send / session switch /
    /// idle — NOT on every keystroke.
    @Binding var draft: String
    /// What you're typing, held locally.
    ///
    /// This used to be bound straight through to `model.drafts`, so every keystroke published on the
    /// Model and invalidated everything observing it — including the whole transcript. On a phone with
    /// a long conversation open that is the typing lag: each character re-evaluated hundreds of rows.
    /// Keeping the in-progress text local means a keystroke re-renders the composer and nothing else.
    @State private var entry: String = ""
    /// The session `entry` belongs to, so a switch can flush it to the RIGHT session's draft.
    @State private var entrySession: String? = nil
    @State private var flushTask: Task<Void, Never>? = nil
    let palette: OculusPalette

    @StateObject private var dictator = SpeechDictator()
    @StateObject private var voice = VoiceController()
    /// Whether the message field holds first responder.
    ///
    /// A plain Bool rather than `@FocusState`: SwiftUI's focus system cannot make the text view
    /// inside a representable first responder, so `.focused()` would light the ring while the
    /// keyboard stayed down — and the slash button would drop a "/" into a field you then have to
    /// tap yourself. ComposerTextView drives the responder from this and reports back, so it also
    /// goes false when the field loses focus to something else.
    @State private var focused = false
    /// What was already typed when dictation started. The recognizer's transcript REPLACES the text
    /// it's appended to, so without an anchor, tapping the mic to finish a half-typed message
    /// silently deleted the typed half.
    @State private var dictationPrefix = ""
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    /// The action buttons' drawn diameter, scaled with the glyph's text style so the circle can't
    /// clip its own arrow at large accessibility sizes.
    @ScaledMetric(relativeTo: .subheadline) private var actionDiameter: CGFloat = 28
    /// The composer's growth cap, scaled so accessibility sizes get several lines instead of two
    /// clipped ones — still bounded, or the input eats the transcript.
    @ScaledMetric(relativeTo: .body) private var scaledEntryHeight: CGFloat = 160
    @ScaledMetric(relativeTo: .footnote) private var scaledPaletteHeight: CGFloat = 220
    /// Decoded thumbnails, memoized by each attachment's base64 payload so the
    /// image is decoded once (not on every keystroke that re-evaluates `body`).
    @State private var thumbCache: [String: Image] = [:]
    @State private var showFileImporter = false // both platforms: attach documents
    @State private var showPhotoPicker = false  // iOS: photo library
    /// Highlighted row in the slash-command popup (Tab completes it, ↑/↓ move it).
    @State private var cmdIndex = 0
    // Shell-style prompt history: ↑/↓ recall previously SENT prompts when the slash popup is closed.
    // historyIndex nil = not browsing (editing a fresh entry); 0 = most recent sent; up counts back.
    @State private var historyIndex: Int? = nil
    @State private var historyStash = "" // the in-progress entry, preserved so ↓ past the newest restores it
    #if os(iOS)
    @State private var photoItem: PhotosPickerItem?
    #endif

    /// HIG's 44pt minimum, grown (never shrunk) with Dynamic Type.
    private var actionTarget: CGFloat { max(44, actionDiameter) }
    private var fieldMaxHeight: CGFloat { min(scaledEntryHeight, 300) }
    private var paletteMaxHeight: CGFloat { min(scaledPaletteHeight, 340) }

    /// The command palette is active while the entry is a single "/token" or "$token" (no space
    /// yet). It filters to commands with the matching prefix — so codex "$" skills and "/" commands
    /// each appear under their own trigger.
    private var commandMatches: [SlashCommand] {
        guard let first = entry.first, first == "/" || first == "$",
              !entry.dropFirst().contains(" "), !model.commands.isEmpty else { return [] }
        let prefix = String(first)
        let q = entry.dropFirst().lowercased()
        return model.commands.filter { ($0.prefix ?? "/") == prefix && (q.isEmpty || $0.name.lowercased().hasPrefix(q)) }
    }

    var body: some View {
        VStack(spacing: 0) {
            if !commandMatches.isEmpty { commandPalette }
            Divider().overlay(palette.border)
            VStack(alignment: .leading, spacing: 10) {
                if !model.pendingImages.isEmpty { attachmentChips }
                if !model.pendingFiles.isEmpty { fileChips }
                bangBanner
                messageField

                // Spacing is small because every control below now carries a 44pt tap target: the
                // old 14pt gap on top of those targets would push the row past a phone's width and
                // read as scattered. The glyphs still look spaced — each is centred in its target.
                HStack(spacing: 2) {
                    attachButton
                    if !model.commands.isEmpty { slashButton }
                    micButton
                    voiceButton
                    if dictator.isRecording {
                        Text("Listening…").font(.caption).foregroundStyle(palette.primary)
                            .padding(.leading, 6).lineLimit(1)
                    } else if voice.active {
                        Text(voice.speaking ? "Speaking…" : (voice.listening ? "Listening…" : "Voice mode"))
                            .font(.caption).foregroundStyle(palette.primary)
                            .padding(.leading, 6).lineLimit(1)
                    }
                    Spacer(minLength: 0)
                    interruptButton
                    sendButton
                }
            }
            .padding(12)
            .background(palette.input)
            .overlay(OculusShape.rounded(18).strokeBorder(focused ? palette.primary.opacity(0.5) : palette.border))
            .clipShape(OculusShape.rounded(18))
            .padding(12)
        }
        .background(palette.background)
        // Dictation APPENDS to what's already typed. It used to assign the transcript straight over
        // `entry`, so typing half a message and finishing it by voice destroyed the typed half.
        .onChange(of: dictator.transcript) { newValue in
            guard dictator.isRecording else { return }
            entry = newValue.isEmpty ? dictationPrefix : dictationPrefix + newValue
        }
        // Design Mode (or any tool) injects context into the entry via model.draftInsert.
        .onChange(of: model.draftInsert) { text in
            guard !text.isEmpty else { return }
            entry = entry.isEmpty ? text : entry + "\n\n" + text
            model.draftInsert = ""
        }
        // Voice mode: a finished turn (busy → false) → speak the agent's reply, which then resumes
        // listening. Wire the send closure once the view appears.
        .onChange(of: model.busy) { nowBusy in
            if voice.active && !nowBusy {
                voice.speak(model.messages.last(where: { $0.role == .assistant })?.text ?? "")
            }
        }
        // Adopt the stored draft on open, and whenever something OUTSIDE the composer writes one
        // (a suggested prompt, an injected snippet, a session switch). Because typing no longer
        // writes back on every keystroke, this fires only for those real external changes.
        .onChange(of: draft) { stored in
            if stored != entry { entry = stored }
        }
        .onChange(of: model.sessionID) { newID in
            flushTask?.cancel()
            // Flush to the session being LEFT, not the one arriving — otherwise a half-typed message
            // lands in the wrong conversation.
            if let prev = entrySession, model.drafts[prev] != entry { model.drafts[prev] = entry }
            entrySession = newID
            entry = model.drafts[newID ?? ""] ?? ""
        }
        .onChange(of: entry) { _ in scheduleFlush() }
        .onDisappear { flushTask?.cancel(); flushNow() }
        .onAppear {
            entrySession = model.sessionID
            entry = draft
            refreshThumbs()
            // Voice deliberately does NOT go through the `!` escape: speech recognition would have to
            // hear an exclamation mark to trigger it, and a misheard one would run an arbitrary
            // command on the host with no chance to read it first. Spoken input is always a prompt.
            voice.onUtterance = { text in
                guard !text.isEmpty else { return }
                Task { await model.send(text) }
            }
        }
        .onDisappear { voice.stop() }
        .onChange(of: model.pendingImages) { _ in refreshThumbs() }
        .fileImporter(isPresented: $showFileImporter, allowedContentTypes: Self.attachTypes, allowsMultipleSelection: true) { result in
            if case let .success(urls) = result { for u in urls { handlePickedFile(u) } }
        }
    }

    /// Images plus common document types (PDF, Word, RTF, Markdown, text, JSON, CSV, source).
    static let attachTypes: [UTType] = {
        var t: [UTType] = [.image, .pdf, .plainText, .text, .rtf, .json, .commaSeparatedText, .sourceCode, .html]
        for id in ["org.openxmlformats.wordprocessingml.document", "com.microsoft.word.doc",
                   "net.daringfireball.markdown", "public.markdown"] {
            if let u = UTType(id) { t.append(u) }
        }
        return t
    }()

    /// Attach a picked file: images go through the image path; everything else has its text
    /// extracted (PDF/Word/RTF/Markdown/text/…) and attached as a document.
    private func handlePickedFile(_ url: URL) {
        let scoped = url.startAccessingSecurityScopedResource()
        defer { if scoped { url.stopAccessingSecurityScopedResource() } }
        let imageExts: Set<String> = ["png", "jpg", "jpeg", "gif", "heic", "heif", "webp", "bmp", "tiff"]
        if imageExts.contains(url.pathExtension.lowercased()) {
            if let data = try? Data(contentsOf: url), let jpeg = toJPEG(data) {
                model.attachImage(mime: "image/jpeg", data: jpeg)
            }
        } else if let text = extractDocumentText(from: url) {
            model.attachFile(name: url.lastPathComponent, text: text)
        }
    }

    /// Autocomplete of the agent's slash commands (built-in + custom), shown above the input when
    /// the entry begins with "/". Tapping inserts "/name " so you can add args, then send.
    private var commandPalette: some View {
        ScrollViewReader { proxy in
            ScrollView {
                VStack(spacing: 0) {
                    ForEach(Array(commandMatches.enumerated()), id: \.element.id) { idx, cmd in
                        let selected = idx == clampedCmdIndex
                        Button { complete(cmd) } label: {
                            HStack(spacing: 8) {
                                Text("\(cmd.glyph)\(cmd.name)")
                                    .font(.system(.footnote, design: .monospaced).weight(.semibold))
                                    .foregroundStyle(palette.primary)
                                if let d = cmd.description, !d.isEmpty {
                                    Text(d).font(.caption).foregroundStyle(palette.mutedForeground).lineLimit(1)
                                }
                                Spacer(minLength: 6)
                                if selected {
                                    Text("tab").font(.system(.caption2, design: .monospaced).weight(.semibold))
                                        .foregroundStyle(palette.mutedForeground)
                                        .padding(.horizontal, 4).padding(.vertical, 1)
                                        .background(OculusShape.rounded(3).fill(palette.muted.opacity(0.5)))
                                }
                                if cmd.isCustom {
                                    Text("custom").font(.caption2.weight(.semibold)).foregroundStyle(palette.mutedForeground)
                                        .padding(.horizontal, 5).padding(.vertical, 1)
                                        .background(Capsule().fill(palette.muted.opacity(0.45)))
                                }
                            }
                            .padding(.horizontal, 14).padding(.vertical, 8)
                            .frame(maxWidth: .infinity, alignment: .leading)
                            .background(selected ? palette.primary.opacity(0.14) : .clear)
                            .contentShape(Rectangle())
                        }
                        .buttonStyle(.plain)
                        .id(idx)
                        if cmd.id != commandMatches.last?.id { Divider().overlay(palette.border.opacity(0.5)) }
                    }
                }
            }
            .frame(maxHeight: paletteMaxHeight)
            .onChange(of: clampedCmdIndex) { i in
                withAnimation(reduceMotion ? nil : .linear(duration: 0.08)) { proxy.scrollTo(i, anchor: .center) }
            }
        }
        .background(palette.input)
        // 18, matching the composer below it. These are stacked SIBLING surfaces, and two adjacent
        // panels rounded 12 and 18 read as one of them being wrong even though neither is nested.
        .overlay(OculusShape.rounded(18).strokeBorder(palette.border))
        .clipShape(OculusShape.rounded(18))
        .padding(.horizontal, 12)
        .padding(.bottom, 6)
    }

    /// The highlighted command index, clamped to the current matches.
    private var clampedCmdIndex: Int {
        guard !commandMatches.isEmpty else { return 0 }
        return min(max(0, cmdIndex), commandMatches.count - 1)
    }

    /// Completes a command into the entry (full name + trailing space, which closes the popup).
    private func complete(_ cmd: SlashCommand) {
        entry = "\(cmd.glyph)\(cmd.name) "
        focused = true
    }

    /// The message input: a scrollable, auto-growing multiline editor (ComposerTextView) so long
    /// messages stay editable (it scrolls past ~8 lines) and Enter/Shift+Enter behave reliably —
    /// Enter sends, Shift+Enter inserts a newline. A SwiftUI overlay draws the placeholder.
    @ViewBuilder private var messageField: some View {
        ZStack(alignment: .topLeading) {
            if entry.isEmpty {
                Text("Message the agent…")
                    .font(.body).foregroundStyle(palette.mutedForeground)
                    .padding(.top, 7).padding(.leading, 2).allowsHitTesting(false)
            }
            ComposerTextView(
                text: $entry, isFocused: $focused, maxHeight: fieldMaxHeight,
                onSubmit: { submit() },
                // Tab completes the highlighted command; ↑/↓ move the highlight. Each only consumes
                // the key while the popup is open, so normal typing/tabbing is unaffected.
                onTab: { completeSelected() },
                onMoveUp: { moveSelection(-1) || recallHistory(-1) },
                onMoveDown: { moveSelection(1) || recallHistory(1) }
            )
        }
        // Reset the highlight to the top whenever the set of matches changes (new keystroke).
        .onChange(of: entry) { _ in
            cmdIndex = 0
            if !settingFromHistory { historyIndex = nil } // a manual edit ends history browsing
        }
    }

    /// Completes the highlighted slash command when the popup is open. Returns true (consume the
    /// Tab) only in that case; otherwise Tab does nothing special.
    private func completeSelected() -> Bool {
        guard !commandMatches.isEmpty else { return false }
        complete(commandMatches[clampedCmdIndex])
        return true
    }

    @State private var settingFromHistory = false

    /// The prompts this session has SENT, newest last — the ↑/↓ recall list.
    ///
    /// `!` commands are in it too, re-prefixed so recalling one gives back exactly what was typed.
    /// This is the recall people already have in a shell, and a command you ran once is the thing
    /// you're most likely to run again (`!git status` after every turn).
    private var sentPrompts: [String] {
        model.messages.compactMap { m in
            switch m.role {
            case .user: return m.text
            case .shell: return "!" + m.text
            default: return nil
            }
        }
    }

    /// Shell-style history recall. delta -1 = older (↑), +1 = newer (↓). Returns true to CONSUME the
    /// arrow only when it actually moved through history — so in a multi-line entry the arrows still
    /// navigate text normally until you're at the top/bottom with nothing to recall. Runs only when
    /// the slash popup is closed (moveSelection is tried first).
    private func recallHistory(_ delta: Int) -> Bool {
        let hist = sentPrompts
        guard !hist.isEmpty else { return false }
        switch historyIndex {
        case nil:
            guard delta < 0 else { return false } // ↓ with no active recall = let the caret move
            // Only hijack ↑ from an empty/at-top entry, so editing a long message isn't disrupted.
            if !entry.isEmpty && entry != hist.last { return false }
            historyStash = entry
            historyIndex = 0
        case .some(let i):
            let next = i - delta // delta -1 (older) increases the back-index
            if next < 0 { // ↓ past the newest → restore the stashed in-progress entry
                historyIndex = nil
                setDraftFromHistory(historyStash)
                return true
            }
            if next >= hist.count { return true } // ↑ past the oldest → stay put (consumed)
            historyIndex = next
        }
        if let idx = historyIndex {
            setDraftFromHistory(hist[hist.count - 1 - idx])
        }
        return true
    }

    /// Sets the entry from history WITHOUT the onChange handler treating it as a manual edit.
    private func setDraftFromHistory(_ text: String) {
        settingFromHistory = true
        entry = text
        DispatchQueue.main.async { settingFromHistory = false }
    }

    /// Moves the popup highlight by `delta`, wrapping around. Returns true (consume the arrow) only
    /// while the popup is open.
    private func moveSelection(_ delta: Int) -> Bool {
        guard !commandMatches.isEmpty else { return false }
        let n = commandMatches.count
        cmdIndex = ((clampedCmdIndex + delta) % n + n) % n
        return true
    }

    /// Persists the in-progress text to the per-session store once typing pauses.
    ///
    /// Deliberately debounced: the store is `@Published`, so writing it re-renders every observer.
    /// Once per pause preserves the draft across session switches and quits without paying that cost
    /// on every character.
    private func scheduleFlush() {
        flushTask?.cancel()
        flushTask = Task { @MainActor in
            try? await Task.sleep(nanoseconds: 700_000_000)
            guard !Task.isCancelled else { return }
            flushNow()
        }
    }

    private func flushNow() {
        let key = entrySession ?? model.sessionID
        if model.drafts[key ?? ""] != entry { model.drafts[key ?? ""] = entry }
    }

    // MARK: the `!` escape

    /// What the current entry would do if you pressed Enter right now. Computed in one place so the
    /// banner, the send button and submit() can never disagree about it — a send button that says
    /// "message" while submit() runs a shell command is the worst possible version of this feature.
    private var parsed: BangCommand.Parsed { BangCommand.parse(entry) }
    private var isShellEntry: Bool { if case .shell = parsed { return true }; return false }

    /// Explains the escape BEFORE Enter is pressed: what a `!` line is about to do, how to opt out,
    /// and — for a non-owner — why it won't work. A mode you only discover by triggering it is a
    /// mode that eats a message you meant for the agent.
    @ViewBuilder private var bangBanner: some View {
        switch parsed {
        case .shell(let cmd):
            // Three different reasons this line won't behave like a message, each said out loud.
            // A disabled send button with no sentence next to it is the failure mode here.
            let blocked = model.knownNonOwner
            let text = blocked ? model.ownerOnlyReason
                : model.runBusy ? "Another run is still going in this session — wait for it to finish."
                : "Runs `\(cmd)` on the host. The agent won't see it. \(BangCommand.escapeHint)"
            HStack(spacing: 6) {
                Image(systemName: blocked ? "lock" : "terminal")
                    .font(.caption2)
                    .foregroundStyle(blocked ? palette.destructive : palette.primary)
                Text(text)
                    .font(.caption2)
                    .foregroundStyle(blocked ? palette.destructive : palette.mutedForeground)
                    .lineLimit(2).fixedSize(horizontal: false, vertical: true)
                Spacer(minLength: 0)
            }
        case .nothing:
            // A bare "!" that does nothing on Enter would read as a broken send button.
            HStack(spacing: 6) {
                Image(systemName: "terminal").font(.caption2).foregroundStyle(palette.mutedForeground)
                Text("Type a command after ! to run it on the host. \(BangCommand.escapeHint)")
                    .font(.caption2).foregroundStyle(palette.mutedForeground)
                    .lineLimit(2).fixedSize(horizontal: false, vertical: true)
                Spacer(minLength: 0)
            }
        case .prompt:
            EmptyView()
        }
    }

    private func submit() {
        // Enter with the popup open completes the highlighted command instead of sending.
        if !commandMatches.isEmpty { _ = completeSelected(); return }
        guard canSend else { return }
        switch parsed {
        case .nothing:
            return // bare "!" — keep it in the field (the banner says what to type next)
        case .shell(let cmd):
            // The daemon is the authority on whether this is allowed; the client only avoids
            // offering a control it already knows will be refused.
            guard !model.knownNonOwner else { return }
            clearEntry()
            // Attachments are deliberately LEFT pending: they were staged for the agent, and a
            // shell command is not a message to the agent. Clearing them here would silently
            // discard work (a screenshot the user just captured) for an unrelated action.
            Task { await model.runShell(cmd) }
        case .prompt(let text):
            clearEntry()
            Task { await model.send(text) }
        }
    }

    /// Clears the in-progress entry and its persisted draft after a send/run.
    private func clearEntry() {
        entry = ""
        flushTask?.cancel()
        model.drafts[entrySession ?? model.sessionID ?? ""] = ""
        historyIndex = nil; historyStash = ""
        if dictator.isRecording { dictator.stop() }
        dictationPrefix = "" // the sent text is gone; a later dictation must not re-add it
    }

    private var canSend: Bool {
        switch parsed {
        case .nothing:
            return false // a bare "!" has nothing to run and nothing to say
        case .shell:
            return !model.knownNonOwner && !model.runBusy
        case .prompt:
            return !entry.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
                || !model.pendingImages.isEmpty || !model.pendingFiles.isEmpty
        }
    }

    private var attachmentChips: some View {
        ScrollView(.horizontal, showsIndicators: false) {
            HStack(spacing: 8) {
                // Keyed by the attachment's own value (Hashable) rather than the array
                // offset, so inserting/removing a chip doesn't shift every other chip's
                // identity. Removal is by value, so a stale captured index can't delete
                // the wrong item.
                ForEach(model.pendingImages, id: \.self) { img in
                    HStack(spacing: 5) {
                        attachmentThumb(img)
                        Text("Image \(imageNumber(img))").font(.caption2)
                        Button { model.pendingImages.removeAll { $0 == img } } label: {
                            // Drawn at .footnote in a 32pt target: an 11pt glyph was a ~11pt target.
                            // Not the full 44 — that would make every chip 44pt tall and turn the
                            // attachment strip into the largest thing in the composer.
                            Image(systemName: "xmark.circle.fill").font(.footnote)
                                .composerTarget(32, circular: true)
                        }
                        .buttonStyle(.plain)
                        .accessibilityLabel("Remove image \(imageNumber(img))")
                    }
                    .padding(.horizontal, 8).padding(.vertical, 5)
                    .background(palette.muted.opacity(0.3)).clipShape(Capsule())
                }
            }
        }
    }

    private func imageNumber(_ img: ImageAttachment) -> Int {
        (model.pendingImages.firstIndex(of: img) ?? 0) + 1
    }

    @ViewBuilder private func attachmentThumb(_ img: ImageAttachment) -> some View {
        // Reads the memoized thumbnail; never decodes base64 / builds a UIImage
        // inside `body` (which re-evaluates on every keystroke).
        if let image = thumbCache[img.data] {
            image.resizable().scaledToFill().frame(width: 18, height: 18).clipShape(OculusShape.rounded(4))
        } else {
            Image(systemName: "photo").font(.caption2)
        }
    }

    /// Rebuilds `thumbCache` for the current attachments, decoding only newly
    /// added ones and dropping thumbnails for removed attachments.
    private func refreshThumbs() {
        var next: [String: Image] = [:]
        for img in model.pendingImages {
            if let existing = thumbCache[img.data] {
                next[img.data] = existing
            } else if let data = Data(base64Encoded: img.data), let image = platformImage(data) {
                next[img.data] = image
            }
        }
        thumbCache = next
    }

    private func platformImage(_ data: Data) -> Image? {
        #if os(iOS)
        return UIImage(data: data).map { Image(uiImage: $0) }
        #elseif os(macOS)
        return NSImage(data: data).map { Image(nsImage: $0) }
        #else
        return nil
        #endif
    }

    /// Converts arbitrary image data (HEIC/PNG/…) to JPEG, which every provider accepts.
    private func toJPEG(_ data: Data) -> Data? {
        #if os(iOS)
        return UIImage(data: data)?.jpegData(compressionQuality: 0.8)
        #elseif os(macOS)
        guard let img = NSImage(data: data), let tiff = img.tiffRepresentation,
              let rep = NSBitmapImageRep(data: tiff) else { return nil }
        return rep.representation(using: .jpeg, properties: [.compressionFactor: 0.8])
        #else
        return nil
        #endif
    }

    /// Interrupt the current turn so you can redirect the agent (mid-run steering).
    ///
    /// It occupies its slot at ALL times, hidden while idle. Inserting it when a run started slid
    /// send ~42pt left, so the follow-up tap that corrects a just-sent message — the most common
    /// rhythm on a phone — landed on STOP instead. Send is deliberately still live during a run
    /// (queued follow-ups are the point of mid-run steering), so the two stay separate controls.
    private var interruptButton: some View {
        let live = model.busy && model.sessionID != nil
        return Button { Task { await model.interrupt() } } label: {
            Image(systemName: "stop.fill")
                .font(.footnote.weight(.bold))
                .foregroundStyle(palette.primaryForeground)
                .frame(width: actionDiameter, height: actionDiameter)
                .background(palette.destructive)
                .clipShape(Circle())
                .composerTarget(actionTarget, circular: true)
        }
        .buttonStyle(.plain)
        .help("Interrupt the agent")
        .accessibilityLabel("Stop the agent")
        .opacity(live ? 1 : 0)
        .allowsHitTesting(live)
        .accessibilityHidden(!live)
    }

    private var sendHelp: String {
        isShellEntry
        ? (model.knownNonOwner ? model.ownerOnlyReason
           : (model.runBusy ? "Another run is still going." : "Run this command on the host"))
        : "Send message"
    }

    private var sendButton: some View {
        Button(action: submit) {
            // The glyph changes with the DESTINATION. A `!` line doesn't go to the agent, and an
            // unchanged send arrow would be the only thing on screen still claiming it does.
            Image(systemName: isShellEntry ? "terminal.fill" : "arrow.up")
                .font(.subheadline.weight(.bold))
                .glyphSwap(!reduceMotion)
                .foregroundStyle(canSend ? palette.primaryForeground : palette.mutedForeground)
                .frame(width: actionDiameter, height: actionDiameter)
                .background(canSend ? palette.primary : palette.muted.opacity(0.4))
                .clipShape(Circle())
                .composerTarget(actionTarget, circular: true)
        }
        .buttonStyle(.plain)
        .disabled(!canSend)
        .help(sendHelp)
        // The label has to carry the destination too: VoiceOver never sees the glyph swap, so
        // without it "Send message" is announced for a line that runs on the host.
        .accessibilityLabel(isShellEntry ? "Run command on host" : "Send message")
    }

    /// Opens the slash-command palette — especially handy on iPhone where "type /" isn't obvious.
    private var slashButton: some View {
        Button {
            if entry.isEmpty {
                entry = "/"
            } else if !entry.hasPrefix("/") && !entry.hasPrefix("$") {
                entry = "/" + entry
            }
            focused = true
        } label: {
            Image(systemName: "slash.circle")
                .font(.body)
                .foregroundStyle(commandMatches.isEmpty ? palette.mutedForeground : palette.primary)
                .composerTarget(actionTarget)
        }
        .buttonStyle(.plain)
        .help("Slash commands")
        .accessibilityLabel("Slash commands")
    }

    private var micButton: some View {
        Button {
            if dictator.isRecording { dictator.stop() } else { startDictation() }
        } label: {
            Image(systemName: dictator.isRecording ? "mic.fill" : "mic")
                .font(.body)
                .foregroundStyle(dictator.isRecording ? palette.primary : palette.mutedForeground)
                .composerTarget(actionTarget)
        }
        .buttonStyle(.plain)
        .help("Dictate your message")
        .accessibilityLabel(dictator.isRecording ? "Stop dictating" : "Dictate your message")
    }

    /// Anchors the transcript to whatever is already typed, then starts listening. Captured once per
    /// start (not per transcript update) so repeated start/stop cycles append once each rather than
    /// compounding the earlier dictation.
    private func startDictation() {
        let base = entry
        let needsSpace = !base.isEmpty && !base.hasSuffix(" ") && !base.hasSuffix("\n")
        dictationPrefix = needsSpace ? base + " " : base
        dictator.start()
    }

    // Hands-free voice mode: speak your prompt, it auto-sends on a pause, and the agent's reply is
    // read back — then it listens again. Distinct from the mic (which just dictates into the field).
    private var voiceButton: some View {
        Button {
            if dictator.isRecording { dictator.stop() } // don't run plain dictation + voice mode at once
            voice.toggle()
        } label: {
            Image(systemName: voice.active ? "waveform.circle.fill" : "waveform.circle")
                .font(.body)
                .foregroundStyle(voice.active ? palette.primary : palette.mutedForeground)
                .composerTarget(actionTarget)
        }
        .buttonStyle(.plain)
        .disabled(model.sessionID == nil)
        .help("Voice mode — talk to the agent hands-free")
        .accessibilityLabel(voice.active ? "Turn off voice mode" : "Voice mode")
    }

    /// Chips for attached documents (name + remove), mirroring the image thumbnails.
    private var fileChips: some View {
        ScrollView(.horizontal, showsIndicators: false) {
            HStack(spacing: 6) {
                ForEach(model.pendingFiles, id: \.self) { f in
                    HStack(spacing: 5) {
                        Image(systemName: "doc.text").font(.caption2)
                        Text(f.name).font(.caption2).lineLimit(1)
                        Button { model.pendingFiles.removeAll { $0 == f } } label: {
                            Image(systemName: "xmark.circle.fill").font(.footnote)
                                .composerTarget(32, circular: true)
                        }
                        .buttonStyle(.plain).foregroundStyle(palette.mutedForeground)
                        .accessibilityLabel("Remove \(f.name)")
                    }
                    .padding(.horizontal, 8).padding(.vertical, 4)
                    .background(Capsule().fill(palette.muted.opacity(0.5)))
                    .foregroundStyle(palette.foreground)
                }
            }
        }
    }

    private var attachButton: some View {
        #if os(iOS)
        return Menu {
            Button { showPhotoPicker = true } label: { Label("Photo…", systemImage: "photo") }
            Button { showFileImporter = true } label: { Label("File…", systemImage: "doc") }
        } label: {
            Image(systemName: "paperclip").font(.body).foregroundStyle(palette.mutedForeground)
                .composerTarget(actionTarget)
        }
        .accessibilityLabel("Attach a photo or file")
        .photosPicker(isPresented: $showPhotoPicker, selection: $photoItem, matching: .images)
        .onChange(of: photoItem) { item in
            guard let item else { return }
            Task {
                if let data = try? await item.loadTransferable(type: Data.self),
                   let jpeg = toJPEG(data) {
                    await MainActor.run { model.attachImage(mime: "image/jpeg", data: jpeg) }
                }
                await MainActor.run { photoItem = nil }
            }
        }
        #else
        return Menu {
            Button { showFileImporter = true } label: { Label("File…", systemImage: "doc") }
            Divider()
            Button { captureScreen(interactive: true) } label: {
                Label("Capture area…", systemImage: "viewfinder")
            }
            Button { captureScreen(interactive: false) } label: {
                Label("Capture window…", systemImage: "macwindow")
            }
        } label: {
            Image(systemName: "paperclip").font(.body).foregroundStyle(palette.mutedForeground)
                .composerTarget(actionTarget)
        }
        .menuStyle(.borderlessButton)
        .fixedSize()
        .help("Attach a file, or capture part of the screen to show the agent")
        .accessibilityLabel("Attach a file or screen capture")
        #endif
    }

    #if os(macOS)
    /// Grabs a region or window straight into the prompt.
    ///
    /// Showing an agent the broken thing is far more direct than describing it, and the alternative
    /// today is a four-step detour: screenshot to disk, find it in Finder, drag it in, delete it
    /// later. This uses the system's own capture UI (`screencapture -i`), so the selection
    /// affordances are the ones the user already knows, and the file lives in a temp dir we delete
    /// as soon as it's attached.
    private func captureScreen(interactive: Bool) {
        let url = FileManager.default.temporaryDirectory
            .appendingPathComponent("ironrain-capture-\(UUID().uuidString).png")
        let task = Process()
        task.executableURL = URL(fileURLWithPath: "/usr/sbin/screencapture")
        // -i interactive (drag a region, or press space for window mode); -w window picker;
        // -o omits the window shadow, which otherwise wastes most of the image.
        task.arguments = interactive ? ["-i", url.path] : ["-i", "-w", "-o", url.path]
        task.terminationHandler = { _ in
            defer { try? FileManager.default.removeItem(at: url) }
            // A cancelled capture writes no file — that's a normal outcome, not an error.
            guard let data = try? Data(contentsOf: url), !data.isEmpty else { return }
            Task { @MainActor in model.attachImage(mime: "image/png", data: data) }
        }
        do {
            try task.run()
        } catch {
            model.actionError = "Couldn't start a screen capture.\n\n\(error.localizedDescription)"
        }
    }
    #endif
}

private extension View {
    /// Gives a small glyph a real tap target without changing what's drawn.
    ///
    /// `.buttonStyle(.plain)` strips the system's own hit padding, so this row — the one cluster a
    /// phone user touches every single turn, STOP included — was a set of ~20pt targets against
    /// HIG's 44pt minimum. Circular for the filled send/stop discs so the corners of their target
    /// don't overlap the neighbour's.
    func composerTarget(_ size: CGFloat, circular: Bool = false) -> some View {
        frame(width: size, height: size)
            .contentShape(circular ? AnyShape(Circle()) : AnyShape(Rectangle()))
    }

    /// Cross-fades a symbol when its meaning changes (send → run-on-host). Availability-gated, and
    /// off entirely under Reduce Motion.
    @ViewBuilder func glyphSwap(_ enabled: Bool) -> some View {
        if enabled, #available(iOS 17, macOS 14, *) {
            self.contentTransition(.symbolEffect(.replace))
        } else {
            self
        }
    }
}
