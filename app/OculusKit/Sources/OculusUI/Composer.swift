import SwiftUI
import OculusKit
import UniformTypeIdentifiers
#if os(iOS)
import PhotosUI
#endif

/// The sticky bottom composer: attach · multiline text · voice · send. Available on
/// iOS and macOS.
struct Composer: View {
    @ObservedObject var model: Model
    @Binding var draft: String
    let palette: OculusPalette

    @StateObject private var dictator = SpeechDictator()
    #if os(iOS)
    @State private var photoItem: PhotosPickerItem?
    #else
    @State private var showFileImporter = false
    #endif

    var body: some View {
        VStack(spacing: 0) {
            Divider().overlay(palette.border)
            HStack(alignment: .bottom, spacing: 8) {
                attachButton
                TextField("Message the agent…", text: $draft, axis: .vertical)
                    .lineLimit(1...5)
                    #if os(iOS)
                    .textInputAutocapitalization(.sentences)
                    #endif
                    .padding(.horizontal, 12).padding(.vertical, 9)
                    .background(palette.input)
                    .clipShape(RoundedRectangle(cornerRadius: 20))
                    .overlay(RoundedRectangle(cornerRadius: 20).stroke(palette.border))
                micButton
                sendButton
            }
            .padding(.horizontal, 12).padding(.vertical, 8)
        }
        .background(palette.background)
        .onChange(of: dictator.transcript) { newValue in
            if dictator.isRecording { draft = newValue }
        }
        #if os(macOS)
        .fileImporter(isPresented: $showFileImporter, allowedContentTypes: [.item]) { result in
            if case let .success(url) = result {
                draft += (draft.isEmpty ? "" : "\n") + "[attached \(url.lastPathComponent)]"
            }
        }
        #endif
    }

    private var sendButton: some View {
        Button {
            let text = draft
            draft = ""
            if dictator.isRecording { dictator.stop() }
            Task { await model.send(text) }
        } label: {
            Image(systemName: "arrow.up.circle.fill").font(.system(size: 28))
        }
        .buttonStyle(.plain)
        .foregroundStyle(canSend ? palette.primary : palette.mutedForeground)
        .disabled(!canSend)
    }

    private var canSend: Bool {
        !draft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
    }

    private var micButton: some View {
        Button {
            if dictator.isRecording { dictator.stop() } else { dictator.start() }
        } label: {
            Image(systemName: dictator.isRecording ? "mic.fill" : "mic")
                .font(.title3)
                .foregroundStyle(dictator.isRecording ? palette.primary : palette.mutedForeground)
                .padding(.bottom, 4)
        }
        .buttonStyle(.plain)
    }

    private var attachButton: some View {
        #if os(iOS)
        return PhotosPicker(selection: $photoItem, matching: .images) {
            Image(systemName: "paperclip").font(.title3)
                .foregroundStyle(palette.mutedForeground).padding(.bottom, 4)
        }
        .buttonStyle(.plain)
        .onChange(of: photoItem) { item in
            guard item != nil else { return }
            // Scaffold: image parts over the protocol are a follow-up.
            draft += (draft.isEmpty ? "" : "\n") + "[attached an image]"
            photoItem = nil
        }
        #else
        return Button { showFileImporter = true } label: {
            Image(systemName: "paperclip").font(.title3)
                .foregroundStyle(palette.mutedForeground).padding(.bottom, 4)
        }
        .buttonStyle(.plain)
        #endif
    }
}
