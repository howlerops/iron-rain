import SwiftUI
import OculusUI

/// The universal Oculus app entry point — one `@main` shared by the iOS and macOS
/// targets. The App owns the `Model` (one daemon connection) and injects it into
/// the window and, on macOS, the menu-bar item so both stay in lockstep. The v0
/// surface lives in `OculusUI` (built on the vector-locked `OculusKit` client).
@main
struct OculusMain: App {
    @StateObject private var store = DesktopStore()
    #if os(iOS)
    @UIApplicationDelegateAdaptor(PushDelegate.self) private var pushDelegate
    #endif

    var body: some Scene {
        WindowGroup {
            RootView(store: store)
                #if os(macOS)
                .frame(minWidth: 520, minHeight: 420)
                #endif
        }

        #if os(macOS)
        MenuBarExtra {
            MenuBarView(store: store)
        } label: {
            Image(systemName: store.active?.menuBarSymbol ?? "bolt.horizontal.circle")
        }
        .menuBarExtraStyle(.window)
        #endif
    }
}
