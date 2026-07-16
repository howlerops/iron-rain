import SwiftUI
import OculusUI

/// macOS dev harness for the Oculus app. The full universal (iOS + macOS) app is
/// `app/Oculus.xcodeproj` (xcodegen); both share `OculusUI` and own the `Model`.
@main
struct OculusApp: App {
    @StateObject private var model = Model()

    var body: some Scene {
        WindowGroup("Oculus") {
            ContentView(model: model)
                .frame(minWidth: 520, minHeight: 420)
        }

        MenuBarExtra {
            MenuBarView(model: model)
        } label: {
            Image(systemName: model.menuBarSymbol)
        }
        .menuBarExtraStyle(.window)
    }
}
