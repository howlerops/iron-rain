import SwiftUI
import OculusKit
import UniformTypeIdentifiers
#if os(iOS)
import PhotosUI
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
        .fileImporter(isPresented: $showFileImporter, allowedContentTypes: [.item]) { result in
            if case let .success(url) = result {
                draft += (draft.isEmpty ? "" : "\n") + "[attached \(url.lastPathComponent)]"
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
        !draft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
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
            guard item != nil else { return }
            draft += (draft.isEmpty ? "" : "\n") + "[attached an image]" // scaffold
            photoItem = nil
        }
        #else
        return Button { showFileImporter = true } label: {
            Image(systemName: "plus").font(.system(size: 17)).foregroundStyle(palette.mutedForeground)
        }
        .buttonStyle(.plain)
        #endif
    }
}
