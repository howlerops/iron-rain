import SwiftUI
import OculusUI

/// macOS dev harness for the Oculus app. The full universal (iOS + macOS) app is
/// `app/Oculus.xcodeproj` (xcodegen); both share `OculusUI` and own the `Model`.
@main
struct OculusApp: App {
    @StateObject private var store = DesktopStore()

    var body: some Scene {
        WindowGroup("Oculus") {
            RootView(store: store)
                .frame(minWidth: 520, minHeight: 420)
        }

        MenuBarExtra {
            MenuBarView(store: store)
        } label: {
            Image(systemName: store.active?.menuBarSymbol ?? "bolt.horizontal.circle")
        }
        .menuBarExtraStyle(.window)
    }
}
