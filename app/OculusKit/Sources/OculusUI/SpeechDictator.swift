import AVFoundation
import Foundation
import Speech

/// Speech-to-text for the composer mic button (iOS + macOS). Publishes a live
/// `transcript` while `isRecording`. Requires NSMicrophoneUsageDescription +
/// NSSpeechRecognitionUsageDescription in Info.plist (both platforms).
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
            Self.requestMic { granted in
                guard granted else { return }
                Task { @MainActor in self?.beginSession() }
            }
        }
    }

    private static func requestMic(_ completion: @escaping (Bool) -> Void) {
        #if os(iOS)
        AVAudioSession.sharedInstance().requestRecordPermission(completion)
        #else
        AVCaptureDevice.requestAccess(for: .audio, completionHandler: completion)
        #endif
    }

    private func beginSession() {
        guard let recognizer, recognizer.isAvailable, !isRecording else { return }
        transcript = ""
        #if os(iOS)
        let session = AVAudioSession.sharedInstance()
        try? session.setCategory(.record, mode: .measurement, options: .duckOthers)
        try? session.setActive(true, options: .notifyOthersOnDeactivation)
        #endif

        let req = SFSpeechAudioBufferRecognitionRequest()
        req.shouldReportPartialResults = true
        request = req

        let node = audioEngine.inputNode
        let format = node.outputFormat(forBus: 0)
        node.installTap(onBus: 0, bufferSize: 1024, format: format) { [weak self] buffer, _ in
            self?.request?.append(buffer)
        }
        audioEngine.prepare()
        do { try audioEngine.start() } catch { return }
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
