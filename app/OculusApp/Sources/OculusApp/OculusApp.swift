import SwiftUI
import OculusUI

/// macOS dev harness for the Oculus app. The full universal (iOS + macOS) app is
/// `app/Oculus.xcodeproj` (xcodegen); both share `OculusUI.ContentView`.
@main
struct OculusApp: App {
    var body: some Scene {
        WindowGroup("Oculus") {
            ContentView()
                .frame(minWidth: 520, minHeight: 420)
        }
    }
}
