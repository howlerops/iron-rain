import SwiftUI
import OculusUI

/// macOS dev harness for the Oculus app. The full universal (iOS + macOS) app is
/// `app/Oculus.xcodeproj` (xcodegen); both share `OculusUI` and own the `Model`.
@main
struct OculusApp: App {
    @StateObject private var store = DesktopStore()
    /// Mirrors the shipping app (OculusMain): on macOS this fires on app-switch rather than
    /// suspension, but the revalidate-then-reconnect path is the same one and must not drift.
    @Environment(\.scenePhase) private var scenePhase

    var body: some Scene {
        WindowGroup("Iron Rain") {
            RootView(store: store)
                .frame(minWidth: 520, minHeight: 420)
                .onChange(of: scenePhase) { phase in
                    guard phase == .active else { return }
                    let models = store.models
                    Task { @MainActor in
                        await withTaskGroup(of: Void.self) { group in
                            for m in models { group.addTask { await m.appDidBecomeActive() } }
                        }
                    }
                }
        }
        // Same main menu and Settings window as the shipping app (OculusMain) — the harness is
        // where these get exercised, so it must not diverge from what ships.
        .commands { OculusCommands(store: store) }

        Settings {
            SettingsView(store: store)
        }

        MenuBarExtra {
            MenuBarView(store: store)
        } label: {
            Image(systemName: store.active?.menuBarSymbol ?? "bolt.horizontal.circle")
        }
        .menuBarExtraStyle(.window)
    }
}
