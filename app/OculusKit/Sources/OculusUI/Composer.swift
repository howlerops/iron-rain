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
    @Binding var draft: String
    let palette: OculusPalette

    @StateObject private var dictator = SpeechDictator()
    @StateObject private var voice = VoiceController()
    @FocusState private var focused: Bool
    /// Decoded thumbnails, memoized by each attachment's base64 payload so the
    /// image is decoded once (not on every keystroke that re-evaluates `body`).
    @State private var thumbCache: [String: Image] = [:]
    @State private var showFileImporter = false // both platforms: attach documents
    @State private var showPhotoPicker = false  // iOS: photo library
    /// Highlighted row in the slash-command popup (Tab completes it, ↑/↓ move it).
    @State private var cmdIndex = 0
    #if os(iOS)
    @State private var photoItem: PhotosPickerItem?
    #endif

    /// The command palette is active while the draft is a single "/token" or "$token" (no space
    /// yet). It filters to commands with the matching prefix — so codex "$" skills and "/" commands
    /// each appear under their own trigger.
    private var commandMatches: [SlashCommand] {
        guard let first = draft.first, first == "/" || first == "$",
              !draft.dropFirst().contains(" "), !model.commands.isEmpty else { return [] }
        let prefix = String(first)
        let q = draft.dropFirst().lowercased()
        return model.commands.filter { ($0.prefix ?? "/") == prefix && (q.isEmpty || $0.name.lowercased().hasPrefix(q)) }
    }

    var body: some View {
        VStack(spacing: 0) {
            if !commandMatches.isEmpty { commandPalette }
            Divider().overlay(palette.border)
            VStack(alignment: .leading, spacing: 10) {
                if !model.pendingImages.isEmpty { attachmentChips }
                if !model.pendingFiles.isEmpty { fileChips }
                messageField

                HStack(spacing: 14) {
                    attachButton
                    if !model.commands.isEmpty { slashButton }
                    micButton
                    voiceButton
                    if dictator.isRecording {
                        Text("Listening…").font(.caption).foregroundStyle(palette.primary)
                    } else if voice.active {
                        Text(voice.speaking ? "Speaking…" : (voice.listening ? "Listening…" : "Voice mode"))
                            .font(.caption).foregroundStyle(palette.primary)
                    }
                    Spacer()
                    if model.busy && model.sessionID != nil { interruptButton }
                    sendButton
                }
            }
            .padding(12)
            .background(palette.input)
            .overlay(RoundedRectangle(cornerRadius: 18).stroke(focused ? palette.primary.opacity(0.5) : palette.border))
            .clipShape(RoundedRectangle(cornerRadius: 18))
            .padding(12)
        }
        .background(palette.background)
        .onChange(of: dictator.transcript) { newValue in
            if dictator.isRecording { draft = newValue }
        }
        // Design Mode (or any tool) injects context into the draft via model.draftInsert.
        .onChange(of: model.draftInsert) { text in
            guard !text.isEmpty else { return }
            draft = draft.isEmpty ? text : draft + "\n\n" + text
            model.draftInsert = ""
        }
        // Voice mode: a finished turn (busy → false) → speak the agent's reply, which then resumes
        // listening. Wire the send closure once the view appears.
        .onChange(of: model.busy) { nowBusy in
            if voice.active && !nowBusy {
                voice.speak(model.messages.last(where: { $0.role == .assistant })?.text ?? "")
            }
        }
        .onAppear {
            refreshThumbs()
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
    /// the draft begins with "/". Tapping inserts "/name " so you can add args, then send.
    private var commandPalette: some View {
        ScrollViewReader { proxy in
            ScrollView {
                VStack(spacing: 0) {
                    ForEach(Array(commandMatches.enumerated()), id: \.element.id) { idx, cmd in
                        let selected = idx == clampedCmdIndex
                        Button { complete(cmd) } label: {
                            HStack(spacing: 8) {
                                Text("\(cmd.glyph)\(cmd.name)")
                                    .font(.system(size: 13, weight: .semibold, design: .monospaced))
                                    .foregroundStyle(palette.primary)
                                if let d = cmd.description, !d.isEmpty {
                                    Text(d).font(.caption).foregroundStyle(palette.mutedForeground).lineLimit(1)
                                }
                                Spacer(minLength: 6)
                                if selected {
                                    Text("tab").font(.system(size: 9, weight: .semibold, design: .monospaced))
                                        .foregroundStyle(palette.mutedForeground)
                                        .padding(.horizontal, 4).padding(.vertical, 1)
                                        .background(RoundedRectangle(cornerRadius: 3).fill(palette.muted.opacity(0.5)))
                                }
                                if cmd.isCustom {
                                    Text("custom").font(.system(size: 9, weight: .semibold)).foregroundStyle(palette.mutedForeground)
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
            .frame(maxHeight: 220)
            .onChange(of: clampedCmdIndex) { i in withAnimation(.linear(duration: 0.08)) { proxy.scrollTo(i, anchor: .center) } }
        }
        .background(palette.input)
        .overlay(RoundedRectangle(cornerRadius: 12).strokeBorder(palette.border))
        .clipShape(RoundedRectangle(cornerRadius: 12))
        .padding(.horizontal, 12)
        .padding(.bottom, 6)
    }

    /// The highlighted command index, clamped to the current matches.
    private var clampedCmdIndex: Int {
        guard !commandMatches.isEmpty else { return 0 }
        return min(max(0, cmdIndex), commandMatches.count - 1)
    }

    /// Completes a command into the draft (full name + trailing space, which closes the popup).
    private func complete(_ cmd: SlashCommand) {
        draft = "\(cmd.glyph)\(cmd.name) "
        focused = true
    }

    /// The message input: a scrollable, auto-growing multiline editor (ComposerTextView) so long
    /// messages stay editable (it scrolls past ~8 lines) and Enter/Shift+Enter behave reliably —
    /// Enter sends, Shift+Enter inserts a newline. A SwiftUI overlay draws the placeholder.
    @ViewBuilder private var messageField: some View {
        ZStack(alignment: .topLeading) {
            if draft.isEmpty {
                Text("Message the agent…")
                    .font(.body).foregroundStyle(palette.mutedForeground)
                    .padding(.top, 7).padding(.leading, 2).allowsHitTesting(false)
            }
            ComposerTextView(
                text: $draft, maxHeight: 160,
                onSubmit: { submit() },
                // Tab completes the highlighted command; ↑/↓ move the highlight. Each only consumes
                // the key while the popup is open, so normal typing/tabbing is unaffected.
                onTab: { completeSelected() },
                onMoveUp: { moveSelection(-1) },
                onMoveDown: { moveSelection(1) }
            )
        }
        // Reset the highlight to the top whenever the set of matches changes (new keystroke).
        .onChange(of: draft) { _ in cmdIndex = 0 }
    }

    /// Completes the highlighted slash command when the popup is open. Returns true (consume the
    /// Tab) only in that case; otherwise Tab does nothing special.
    private func completeSelected() -> Bool {
        guard !commandMatches.isEmpty else { return false }
        complete(commandMatches[clampedCmdIndex])
        return true
    }

    /// Moves the popup highlight by `delta`, wrapping around. Returns true (consume the arrow) only
    /// while the popup is open.
    private func moveSelection(_ delta: Int) -> Bool {
        guard !commandMatches.isEmpty else { return false }
        let n = commandMatches.count
        cmdIndex = ((clampedCmdIndex + delta) % n + n) % n
        return true
    }

    private func submit() {
        // Enter with the popup open completes the highlighted command instead of sending.
        if !commandMatches.isEmpty { _ = completeSelected(); return }
        guard canSend else { return }
        let text = draft
        draft = ""
        if dictator.isRecording { dictator.stop() }
        Task { await model.send(text) }
    }

    private var canSend: Bool {
        !draft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty || !model.pendingImages.isEmpty || !model.pendingFiles.isEmpty
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
                            Image(systemName: "xmark.circle.fill").font(.caption2)
                        }.buttonStyle(.plain)
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
            image.resizable().scaledToFill().frame(width: 18, height: 18).clipShape(RoundedRectangle(cornerRadius: 4))
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
    private var interruptButton: some View {
        Button { Task { await model.interrupt() } } label: {
            Image(systemName: "stop.fill")
                .font(.system(size: 12, weight: .bold))
                .foregroundStyle(palette.primaryForeground)
                .frame(width: 28, height: 28)
                .background(palette.destructive)
                .clipShape(Circle())
        }
        .buttonStyle(.plain)
        .help("Interrupt the agent")
    }

    private var sendButton: some View {
        Button(action: submit) {
            Image(systemName: "arrow.up")
                .font(.system(size: 15, weight: .bold))
                .foregroundStyle(canSend ? palette.primaryForeground : palette.mutedForeground)
                .frame(width: 28, height: 28)
                .background(canSend ? palette.primary : palette.muted.opacity(0.4))
                .clipShape(Circle())
        }
        .buttonStyle(.plain)
        .disabled(!canSend)
        .help("Send message")
    }

    /// Opens the slash-command palette — especially handy on iPhone where "type /" isn't obvious.
    private var slashButton: some View {
        Button {
            if draft.isEmpty {
                draft = "/"
            } else if !draft.hasPrefix("/") && !draft.hasPrefix("$") {
                draft = "/" + draft
            }
            focused = true
        } label: {
            Image(systemName: "slash.circle")
                .font(.system(size: 17))
                .foregroundStyle(commandMatches.isEmpty ? palette.mutedForeground : palette.primary)
        }
        .buttonStyle(.plain)
        .help("Slash commands")
    }

    private var micButton: some View {
        Button {
            if dictator.isRecording { dictator.stop() } else { dictator.start() }
        } label: {
            Image(systemName: dictator.isRecording ? "mic.fill" : "mic")
                .font(.system(size: 17))
                .foregroundStyle(dictator.isRecording ? palette.primary : palette.mutedForeground)
        }
        .buttonStyle(.plain)
        .help("Dictate your message")
    }

    // Hands-free voice mode: speak your prompt, it auto-sends on a pause, and the agent's reply is
    // read back — then it listens again. Distinct from the mic (which just dictates into the field).
    private var voiceButton: some View {
        Button {
            if dictator.isRecording { dictator.stop() } // don't run plain dictation + voice mode at once
            voice.toggle()
        } label: {
            Image(systemName: voice.active ? "waveform.circle.fill" : "waveform.circle")
                .font(.system(size: 17))
                .foregroundStyle(voice.active ? palette.primary : palette.mutedForeground)
        }
        .buttonStyle(.plain)
        .disabled(model.sessionID == nil)
        .help("Voice mode — talk to the agent hands-free")
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
                            Image(systemName: "xmark.circle.fill").font(.caption2)
                        }.buttonStyle(.plain).foregroundStyle(palette.mutedForeground)
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
            Image(systemName: "paperclip").font(.system(size: 17)).foregroundStyle(palette.mutedForeground)
        }
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
        return Button { showFileImporter = true } label: {
            Image(systemName: "paperclip").font(.system(size: 17)).foregroundStyle(palette.mutedForeground)
        }
        .buttonStyle(.plain)
        .help("Attach an image or document")
        #endif
    }
}
