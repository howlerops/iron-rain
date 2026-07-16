import SwiftUI
import OculusUI

/// The universal Oculus app entry point — one `@main` shared by the iOS and macOS
/// targets. The entire v0 surface lives in `OculusUI.ContentView` (built on the
/// vector-locked `OculusKit` client), so both platforms are identical by design.
@main
struct OculusMain: App {
    var body: some Scene {
        WindowGroup {
            ContentView()
                #if os(macOS)
                .frame(minWidth: 520, minHeight: 420)
                #endif
        }
        #if os(macOS)
        .windowResizability(.contentMinSize)
        #endif
    }
}
