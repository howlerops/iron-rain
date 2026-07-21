import AVFoundation
import Foundation

/// Hands-free voice mode: listen → transcribe → auto-send on a natural pause → speak the agent's
/// reply aloud → listen again. Built on the existing SpeechDictator (STT) plus AVSpeechSynthesizer
/// (TTS). Listening and speaking are mutually exclusive, so the mic never hears the synthesized
/// voice. The Composer owns one of these; it wires `onUtterance` to send, and calls `speak(...)`
/// when a turn finishes.
@MainActor
final class VoiceController: NSObject, ObservableObject {
    @Published private(set) var active = false
    @Published private(set) var listening = false
    @Published private(set) var speaking = false

    /// Called with a finalized utterance (a pause after speech). Wire this to Model.send.
    var onUtterance: ((String) -> Void)?

    private let dictator = SpeechDictator()
    private let synth = AVSpeechSynthesizer()
    private var silenceTask: Task<Void, Never>?
    private var lastTranscript = ""

    // A stable pause: transcript unchanged for this many polls (~1.2s) with non-empty text ends the
    // utterance. Short enough to feel responsive, long enough not to cut people off mid-thought.
    private let pollInterval: UInt64 = 300_000_000
    private let stablePollsToFinalize = 4

    override init() {
        super.init()
        synth.delegate = self
    }

    func toggle() { active ? stop() : start() }

    func start() {
        guard !active else { return }
        active = true
        beginListening()
    }

    func stop() {
        active = false
        listening = false
        speaking = false
        silenceTask?.cancel()
        silenceTask = nil
        dictator.stop()
        synth.stopSpeaking(at: .immediate)
    }

    /// Speaks the agent's reply, then resumes listening. Called by the Composer when a turn ends.
    func speak(_ text: String) {
        guard active else { return }
        let trimmed = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else { beginListening(); return } // nothing to read → keep the loop going
        dictator.stop()
        listening = false
        speaking = true
        #if os(iOS)
        let session = AVAudioSession.sharedInstance()
        try? session.setCategory(.playback, mode: .spokenAudio, options: .duckOthers)
        try? session.setActive(true)
        #endif
        let u = AVSpeechUtterance(string: cleanForSpeech(trimmed))
        u.rate = AVSpeechUtteranceDefaultSpeechRate
        synth.speak(u)
    }

    private func beginListening() {
        guard active, !speaking else { return }
        listening = true
        lastTranscript = ""
        dictator.start()
        watchForPause()
    }

    private func watchForPause() {
        silenceTask?.cancel()
        silenceTask = Task { @MainActor [weak self] in
            var stable = 0
            while let self, self.active, self.listening {
                try? await Task.sleep(nanoseconds: self.pollInterval)
                guard self.active, self.listening else { return }
                let t = self.dictator.transcript
                if t != self.lastTranscript {
                    self.lastTranscript = t
                    stable = 0
                } else if !t.trimmingCharacters(in: .whitespaces).isEmpty {
                    stable += 1
                    if stable >= self.stablePollsToFinalize {
                        self.finalize(t)
                        return
                    }
                }
            }
        }
    }

    private func finalize(_ text: String) {
        silenceTask?.cancel()
        silenceTask = nil
        listening = false
        dictator.stop()
        onUtterance?(text.trimmingCharacters(in: .whitespacesAndNewlines))
        // The Composer will call speak(...) when the reply lands, which resumes listening.
    }

    // Strip markdown/code fences that read badly aloud; keep it lightweight.
    private func cleanForSpeech(_ s: String) -> String {
        var out = s.replacingOccurrences(of: "```", with: " code block ")
        for ch in ["*", "#", "`", "_", ">"] { out = out.replacingOccurrences(of: ch, with: "") }
        return out
    }
}

extension VoiceController: AVSpeechSynthesizerDelegate {
    nonisolated func speechSynthesizer(_ synthesizer: AVSpeechSynthesizer, didFinish utterance: AVSpeechUtterance) {
        Task { @MainActor [weak self] in
            guard let self else { return }
            self.speaking = false
            if self.active { self.beginListening() }
        }
    }
    nonisolated func speechSynthesizer(_ synthesizer: AVSpeechSynthesizer, didCancel utterance: AVSpeechUtterance) {
        Task { @MainActor [weak self] in self?.speaking = false }
    }
}
