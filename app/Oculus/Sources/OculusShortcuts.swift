import AppIntents
import OculusUI

/// Exposes the app's intents to Siri / Spotlight / Shortcuts. Lives in the app
/// target (not the shared package) so Xcode's AppShortcuts NL extraction picks up
/// the Siri phrases. The intents themselves are shared in OculusUI.
@available(iOS 16.0, macOS 13.0, *)
struct OculusShortcuts: AppShortcutsProvider {
    static var appShortcuts: [AppShortcut] {
        AppShortcut(
            intent: StartSessionIntent(),
            phrases: ["Start a session in \(.applicationName)"],
            shortTitle: "Start Session",
            systemImageName: "bolt.horizontal.circle"
        )
        // Answering the blocking question is the thing people need to do away from the keyboard —
        // an agent waiting on approval is an agent doing nothing. Approve is gated on device
        // authentication by the intent itself (see ApproveToolIntent); listing it here does not
        // loosen that, and Siri will demand the unlock before it runs.
        AppShortcut(
            intent: ApproveToolIntent(),
            phrases: ["Approve the pending tool in \(.applicationName)"],
            shortTitle: "Approve Tool",
            systemImageName: "checkmark.shield"
        )
        AppShortcut(
            intent: DenyToolIntent(),
            phrases: ["Deny the pending tool in \(.applicationName)"],
            shortTitle: "Deny Tool",
            systemImageName: "hand.raised"
        )
    }
}
