import AppIntents
import OculusUI

/// Exposes the app's intents to Siri / Spotlight / Shortcuts. Lives in the app
/// target (not the shared package) so Xcode's AppShortcuts NL extraction picks up
/// the Siri phrases. The intent itself (`StartSessionIntent`) is shared in OculusUI.
@available(iOS 16.0, macOS 13.0, *)
struct OculusShortcuts: AppShortcutsProvider {
    static var appShortcuts: [AppShortcut] {
        AppShortcut(
            intent: StartSessionIntent(),
            phrases: ["Start a session in \(.applicationName)"],
            shortTitle: "Start Session",
            systemImageName: "bolt.horizontal.circle"
        )
    }
}
