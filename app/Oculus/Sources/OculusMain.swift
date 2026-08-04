import Foundation
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
    // The app's ONLY lifecycle signal. Before this existed nothing told the Model the process had
    // been suspended, so a phone taken out of a pocket showed whatever `connected` happened to be
    // when it went in — over a socket that may have died an hour earlier — and sat on a backoff
    // sleep frozen against wall-clock time it never experienced. The swap moment is the product.
    @Environment(\.scenePhase) private var scenePhase
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
                .onChange(of: scenePhase) { phase in handle(phase) }
                // Handoff, receiving side. `onContinueUserActivity` is a View modifier, not a Scene
                // one, so it hangs off the window's root rather than the WindowGroup.
                .onContinueUserActivity(oculusSessionActivityType) { activity in continueSession(activity) }
        }
        #if os(macOS)
        .defaultSize(width: 1180, height: 760)
        // The window was sizing content to the NavigationSplitView's ideal height (~1884pt)
        // and centering it when the window is smaller — overflowing the sidebar. contentMinSize
        // pins only the minimum to the content's min and lets content FILL the window instead.
        .windowResizability(.contentMinSize)
        // The app had no `.commands` at all, so the menu bar held nothing the app does — and Full
        // Keyboard Access and the Help menu's shortcut search index the MENU, not the toolbar.
        .commands { OculusCommands(store: store) }
        #endif

        #if os(macOS)
        // ⌘, was a dead key: ~18 configuration items lived behind one unlabeled `⋯` menu, each
        // opening a modal over the sessions you were configuring. SwiftUI adds the "Settings…" item
        // and its ⌘, shortcut for this scene type automatically, so no `CommandGroup(replacing:
        // .appSettings)` is wanted here — a second one would put two Settings items in the app menu.
        Settings {
            SettingsView(store: store)
                .preferredColorScheme(appearance.colorScheme)
        }

        MenuBarExtra {
            MenuBarView(store: store).preferredColorScheme(appearance.colorScheme)
        } label: {
            Image(systemName: store.active?.menuBarSymbol ?? "bolt.horizontal.circle")
        }
        .menuBarExtraStyle(.window)
        #endif
    }

    /// Continues a session handed off from another device: pick the right desktop, then open it.
    ///
    /// Every branch that can't open the session says so. Handoff advertises across a person's
    /// DEVICES, but a session belongs to a PAIRING — so the receiving device genuinely may not be
    /// able to honor the request, and an unexplained no-op there is indistinguishable from the
    /// broken state this feature just replaced (an advertised Handoff that did nothing at all).
    private func continueSession(_ activity: NSUserActivity) {
        guard let sid = activity.userInfo?[OculusHandoffKey.sessionID] as? String, !sid.isEmpty else { return }
        let desktopID = activity.userInfo?[OculusHandoffKey.desktopID] as? String

        // Prefer the Mac the activity names. An activity without a desktop id can only have come
        // from an older build, so fall back to whichever desktop is on screen rather than refusing.
        let target = (desktopID?.isEmpty == false)
            ? store.models.first { $0.id == desktopID }
            : store.active
        guard let model = target else {
            // Not paired with that Mac. Complain on the desktop the user is looking at; if there
            // are none, the window is already showing onboarding, which says the same thing better.
            if let visible = store.active {
                visible.actionErrorTitle = "That session is on another Mac"
                visible.actionError = "This device isn't paired with the Mac running that session. Pair with it first, then hand off again."
            }
            return
        }
        store.selectedID = model.id
        Task { @MainActor in
            // The common Handoff case is a cold launch, where nothing is connected yet. Calling
            // openSession now would hit its `guard let client` and return silently, dropping the
            // request; the connect path already drains this queue for notification taps.
            guard model.connected else {
                OculusStore.shared.handoffSessionID = sid
                return
            }
            // Only trust the roster when there IS one: `sessions` is filled by the post-connect
            // loads, so an empty list means "not loaded yet", not "no such session". Claiming a live
            // session was deleted is a worse error than the round trip of trying to open it.
            if !model.sessions.isEmpty, !model.sessions.contains(where: { $0.id == sid }) {
                model.actionErrorTitle = "That session is gone"
                model.actionError = "The session you handed off no longer exists on \(model.name.isEmpty ? "that Mac" : model.name). It may have been deleted or cleaned up since."
                return
            }
            await model.openSession(sid)
        }
    }

    /// Fans the lifecycle edge out to every paired desktop.
    ///
    /// Concurrently, not in a loop: each `appDidBecomeActive` may spend seconds on a probe deadline
    /// and a re-dial, and awaiting them in sequence would make the desktop you are actually looking
    /// at wait behind one that is switched off.
    private func handle(_ phase: ScenePhase) {
        let models = store.models
        switch phase {
        case .active:
            Task { @MainActor in
                await withTaskGroup(of: Void.self) { group in
                    for m in models { group.addTask { await m.appDidBecomeActive() } }
                }
            }
        case .background, .inactive:
            // .inactive is also the app switcher and an incoming call — harmless, because the next
            // .active revalidates from scratch rather than trusting anything.
            for m in models { m.appWillResignActive() }
        @unknown default:
            break
        }
    }
}
