#if os(iOS)
import AVFoundation
import Foundation
import Speech

/// On-device (when available) speech-to-text for the composer mic button. Publishes
/// a live `transcript` while `isRecording`. Requires NSMicrophoneUsageDescription +
/// NSSpeechRecognitionUsageDescription in Info.plist.
@MainActor
final class SpeechDictator: ObservableObject {
    @Published var transcript = ""
    @Published var isRecording = false

    private let recognizer = SFSpeechRecognizer()
    private let audioEngine = AVAudioEngine()
    private var request: SFSpeechAudioBufferRecognitionRequest?
    private var task: SFSpeechRecognitionTask?

    func start() {
        SFSpeechRecognizer.requestAuthorization { [weak self] status in
            guard status == .authorized else { return }
            AVAudioSession.sharedInstance().requestRecordPermission { granted in
                guard granted else { return }
                Task { @MainActor in self?.beginSession() }
            }
        }
    }

    private func beginSession() {
        guard let recognizer, recognizer.isAvailable, !isRecording else { return }
        transcript = ""
        let session = AVAudioSession.sharedInstance()
        try? session.setCategory(.record, mode: .measurement, options: .duckOthers)
        try? session.setActive(true, options: .notifyOthersOnDeactivation)

        let req = SFSpeechAudioBufferRecognitionRequest()
        req.shouldReportPartialResults = true
        request = req

        let node = audioEngine.inputNode
        let format = node.outputFormat(forBus: 0)
        node.installTap(onBus: 0, bufferSize: 1024, format: format) { [weak self] buffer, _ in
            self?.request?.append(buffer)
        }
        audioEngine.prepare()
        try? audioEngine.start()
        isRecording = true

        task = recognizer.recognitionTask(with: req) { [weak self] result, error in
            Task { @MainActor in
                if let result { self?.transcript = result.bestTranscription.formattedString }
                if error != nil || (result?.isFinal ?? false) { self?.stop() }
            }
        }
    }

    func stop() {
        guard isRecording else { return }
        audioEngine.stop()
        audioEngine.inputNode.removeTap(onBus: 0)
        request?.endAudio()
        task?.cancel()
        request = nil
        task = nil
        isRecording = false
    }
}
#endif
