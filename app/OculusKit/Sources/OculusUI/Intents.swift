import AppIntents
import Foundation

/// App-wide store so App Intents (which run outside the SwiftUI view tree) and
/// Handoff can hand work to the running app. `Model` observes it: a queued prompt
/// is started on the next connect; a handed-off session id is surfaced.
@MainActor
public final class OculusStore: ObservableObject {
    public static let shared = OculusStore()
    @Published public var pendingPrompt: String?
    @Published public var handoffSessionID: String?
    /// APNs device token (hex) captured by the app delegate once the OS grants one.
    @Published public var deviceToken: String?
    /// A decision (allow/deny) chosen from a notification action, to apply on connect.
    @Published public var pendingDecision: String?
    private init() {}
}

/// Siri / Shortcuts: "Start a session in Oculus". Opens the app and queues the
/// prompt; the app starts it once connected.
@available(iOS 16.0, macOS 13.0, *)
public struct StartSessionIntent: AppIntent {
    public static var title: LocalizedStringResource = "Start Oculus Session"
    public static var openAppWhenRun: Bool = true
    public init() {}

    @Parameter(title: "Prompt")
    public var prompt: String

    @MainActor
    public func perform() async throws -> some IntentResult {
        OculusStore.shared.pendingPrompt = prompt
        return .result()
    }
}

/// The Handoff activity type advertised by the app (also listed in Info.plist under
/// NSUserActivityTypes).
public let oculusSessionActivityType = "com.howlerops.oculus.session"
