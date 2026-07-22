import SwiftUI
import OculusUI

/// The universal Oculus app entry point — one `@main` shared by the iOS and macOS
/// targets. The App owns the `Model` (one daemon connection) and injects it into
/// the window and, on macOS, the menu-bar item so both stay in lockstep. The v0
/// surface lives in `OculusUI` (built on the vector-locked `OculusKit` client).
@main
struct OculusMain: App {
    @StateObject private var store = DesktopStore()
    // Appearance override lives at the SCENE root so the whole window — including sheets and the
    // AppKit-bridged toolbar — switches theme atomically. Applying it mid-tree (in RootView) let
    // sheets/toolbar lag a render, mixing old and new palette colors on a theme swap.
    @AppStorage("oculus.appearance") private var appearance: Appearance = .system
    #if os(iOS)
    @UIApplicationDelegateAdaptor(PushDelegate.self) private var pushDelegate
    #endif

    var body: some Scene {
        WindowGroup {
            RootView(store: store)
                #if os(macOS)
                .frame(minWidth: 520, minHeight: 420)
                #endif
                .preferredColorScheme(appearance.colorScheme)
        }
        #if os(macOS)
        .defaultSize(width: 1180, height: 760)
        // The window was sizing content to the NavigationSplitView's ideal height (~1884pt)
        // and centering it when the window is smaller — overflowing the sidebar. contentMinSize
        // pins only the minimum to the content's min and lets content FILL the window instead.
        .windowResizability(.contentMinSize)
        #endif

        #if os(macOS)
        MenuBarExtra {
            MenuBarView(store: store).preferredColorScheme(appearance.colorScheme)
        } label: {
            Image(systemName: store.active?.menuBarSymbol ?? "bolt.horizontal.circle")
        }
        .menuBarExtraStyle(.window)
        #endif
    }
}
