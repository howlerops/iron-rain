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
    @FocusState private var focused: Bool
    #if os(iOS)
    @State private var photoItem: PhotosPickerItem?
    #else
    @State private var showFileImporter = false
    #endif

    var body: some View {
        VStack(spacing: 0) {
            Divider().overlay(palette.border)
            VStack(alignment: .leading, spacing: 10) {
                if !model.pendingImages.isEmpty { attachmentChips }
                TextField("Message the agent…", text: $draft, axis: .vertical)
                    .lineLimit(1...8)
                    .textFieldStyle(.plain)
                    .font(.body)
                    .focused($focused)
                    #if os(iOS)
                    .textInputAutocapitalization(.sentences)
                    #endif
                    .onSubmit { submit() }

                HStack(spacing: 14) {
                    attachButton
                    micButton
                    if dictator.isRecording {
                        Text("Listening…").font(.caption).foregroundStyle(palette.primary)
                    }
                    Spacer()
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
        #if os(macOS)
        .fileImporter(isPresented: $showFileImporter, allowedContentTypes: [.image]) { result in
            if case let .success(url) = result {
                let scoped = url.startAccessingSecurityScopedResource()
                defer { if scoped { url.stopAccessingSecurityScopedResource() } }
                if let data = try? Data(contentsOf: url), let jpeg = toJPEG(data) {
                    model.attachImage(mime: "image/jpeg", data: jpeg)
                }
            }
        }
        #endif
    }

    private func submit() {
        let text = draft
        draft = ""
        if dictator.isRecording { dictator.stop() }
        Task { await model.send(text) }
    }

    private var canSend: Bool {
        !draft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty || !model.pendingImages.isEmpty
    }

    private var attachmentChips: some View {
        ScrollView(.horizontal, showsIndicators: false) {
            HStack(spacing: 8) {
                ForEach(Array(model.pendingImages.enumerated()), id: \.offset) { idx, img in
                    HStack(spacing: 5) {
                        attachmentThumb(img)
                        Text("Image \(idx + 1)").font(.caption2)
                        Button { model.pendingImages.remove(at: idx) } label: {
                            Image(systemName: "xmark.circle.fill").font(.caption2)
                        }.buttonStyle(.plain)
                    }
                    .padding(.horizontal, 8).padding(.vertical, 5)
                    .background(palette.muted.opacity(0.3)).clipShape(Capsule())
                }
            }
        }
    }

    @ViewBuilder private func attachmentThumb(_ img: ImageAttachment) -> some View {
        if let data = Data(base64Encoded: img.data), let ui = platformImage(data) {
            ui.resizable().scaledToFill().frame(width: 18, height: 18).clipShape(RoundedRectangle(cornerRadius: 4))
        } else {
            Image(systemName: "photo").font(.caption2)
        }
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
    }

    private var attachButton: some View {
        #if os(iOS)
        return PhotosPicker(selection: $photoItem, matching: .images) {
            Image(systemName: "plus").font(.system(size: 17)).foregroundStyle(palette.mutedForeground)
        }
        .buttonStyle(.plain)
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
            Image(systemName: "plus").font(.system(size: 17)).foregroundStyle(palette.mutedForeground)
        }
        .buttonStyle(.plain)
        #endif
    }
}
