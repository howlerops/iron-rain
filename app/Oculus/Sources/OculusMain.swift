import SwiftUI
import OculusUI

/// The universal Oculus app entry point — one `@main` shared by the iOS and macOS
/// targets. The App owns the `Model` (one daemon connection) and injects it into
/// the window and, on macOS, the menu-bar item so both stay in lockstep. The v0
/// surface lives in `OculusUI` (built on the vector-locked `OculusKit` client).
@main
struct OculusMain: App {
    @StateObject private var model = Model()
    #if os(iOS)
    @UIApplicationDelegateAdaptor(PushDelegate.self) private var pushDelegate
    #endif

    var body: some Scene {
        WindowGroup {
            ContentView(model: model)
                #if os(macOS)
                .frame(minWidth: 520, minHeight: 420)
                #endif
        }

        #if os(macOS)
        MenuBarExtra {
            MenuBarView(model: model)
        } label: {
            Image(systemName: model.menuBarSymbol)
        }
        .menuBarExtraStyle(.window)
        #endif
    }
}
