#if os(macOS)
import AppKit
import SwiftUI
import OculusKit

/// The macOS Settings window (⌘,).
///
/// Roughly eighteen configuration items used to live behind a single unlabeled `⋯` button in the
/// sidebar toolbar, and each one opened as a SHEET over the main window. A sheet is a "finish this
/// or abandon it" gesture — the wrong shape for changing a preference while you keep working, and
/// it blocks the very sessions you are configuring. There was also no `Settings` scene at all, so
/// ⌘, did nothing: the one keystroke every Mac user tries first was a dead key.
///
/// Hosting the existing panels here costs nothing — they were already self-contained views — and
/// gives the system somewhere to hang ⌘,, plus a Help-menu-searchable name for each pane.
public struct SettingsView: View {
    @ObservedObject var store: DesktopStore
    @Environment(\.colorScheme) private var scheme
    private var palette: OculusPalette { .current(scheme) }
    @State private var tab: Tab = .general

    public init(store: DesktopStore) { self.store = store }

    enum Tab: Hashable { case general, agents, mcp, approvals, sharing, accounts, remotes, usage }

    public var body: some View {
        TabView(selection: $tab) {
            GeneralSettingsPane(model: store.active, palette: palette)
                .tabItem { Label("General", systemImage: "gearshape") }
                .tag(Tab.general)

            // `embedded` drops the Done button: a Settings tab has no presentation to dismiss, so it
            // would render an inert control.
            pane { ManageAgentsView(model: $0, palette: palette, embedded: true) }
                .tabItem { Label("Agents", systemImage: "cpu") }
                .tag(Tab.agents)

            pane { MCPServersView(model: $0, palette: palette) }
                .tabItem { Label("MCP", systemImage: "puzzlepiece.extension") }
                .tag(Tab.mcp)

            pane { ApprovalRulesView(model: $0, palette: palette) }
                .tabItem { Label("Approvals", systemImage: "checkmark.shield") }
                .tag(Tab.approvals)

            pane { SharingView(model: $0, palette: palette) }
                .tabItem { Label("Sharing", systemImage: "person.2") }
                .tag(Tab.sharing)

            // `onClose` is optional now; omitting it suppresses the Done button, which is what a
            // Settings pane wants — the window has its own close.
            pane { AccountsView(model: $0, palette: palette) }
                .tabItem { Label("Accounts", systemImage: "person.2.badge.key") }
                .tag(Tab.accounts)

            pane { RemotesView(model: $0, palette: palette) }
                .tabItem { Label("Remotes", systemImage: "server.rack") }
                .tag(Tab.remotes)

            pane { UsageView(model: $0, palette: palette) }
                .tabItem { Label("Usage", systemImage: "chart.bar") }
                .tag(Tab.usage)
        }
        // One size for every pane. The panels each declare their own minimums (OculusSheet asks for
        // 560×460), and letting the window resize itself per tab makes it jump as you click across
        // the tab bar — the same reason the sheets were normalised to one size in the first place.
        .frame(width: 760, height: 580)
        .background(palette.background)
    }

    /// Every pane except General is a view onto ONE paired Mac's daemon. With none paired there is
    /// nothing to configure, and an empty panel reads as a bug rather than as "pair a Mac first".
    @ViewBuilder private func pane<Content: View>(@ViewBuilder _ content: (Model) -> Content) -> some View {
        if let model = store.active { content(model) } else { unpaired }
    }

    private var unpaired: some View {
        VStack(spacing: 8) {
            Image(systemName: "desktopcomputer")
                .font(.system(size: 34)).foregroundStyle(palette.mutedForeground)
            Text("No Mac paired").font(.headline).foregroundStyle(palette.foreground)
            Text("Pair a Mac in the main window, then come back to configure it.")
                .font(.subheadline).foregroundStyle(palette.mutedForeground)
                .multilineTextAlignment(.center)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(palette.background)
    }

    /// AccountsView and RemotesView were written as sheets and draw a Done button unconditionally,
    /// so they require a close callback. In a Settings window the honest meaning of Done is "close
    /// the window" — a no-op would leave a control that visibly does nothing.
    private func closeSettingsWindow() {
        NSApp.keyWindow?.performClose(nil)
    }
}

/// Appearance, chat typography, notifications, diagnostics and updates.
///
/// These bind the SAME `@AppStorage` keys and the same daemon calls as the sidebar's `⋯` menu, so
/// the two surfaces are two front-ends onto one preference rather than two settings that can
/// disagree. The duplication is deliberate and temporary; the menu copies come out separately.
private struct GeneralSettingsPane: View {
    let model: Model?
    let palette: OculusPalette

    @AppStorage("oculus.appearance") private var appearance: Appearance = .system
    @AppStorage("oculus.chatFontDesign") private var chatFontDesign = ChatFontDesign.system.rawValue
    @AppStorage("oculus.chatFontScale") private var chatFontScale = ChatFontScale.standard.rawValue

    @StateObject private var updates = UpdateChecker()
    @State private var checkedOnce = false

    var body: some View {
        Form {
            Section("Appearance") {
                Picker("Theme", selection: $appearance) {
                    ForEach(Appearance.allCases) { a in
                        Label(a.label, systemImage: a.symbol).tag(a)
                    }
                }
                Picker("Chat font", selection: $chatFontDesign) {
                    ForEach(ChatFontDesign.displayOrder) { f in
                        Text(f.label).tag(f.rawValue)
                    }
                }
                Picker("Chat text size", selection: $chatFontScale) {
                    ForEach(ChatFontScale.allCases) { s in
                        Text(s.label).tag(s.rawValue)
                    }
                }
            }

            if let model {
                DaemonPrefsSections(model: model, palette: palette)
            } else {
                Section("Notifications") {
                    Text("Pair a Mac to choose which events notify you.")
                        .font(.callout).foregroundStyle(palette.mutedForeground)
                }
            }

            Section("Updates") {
                LabeledContent("Installed version", value: updates.currentVersion)
                HStack(spacing: 10) {
                    if updates.installing {
                        ProgressView().controlSize(.small)
                        Text(updates.installPhase)
                            .font(.callout).foregroundStyle(palette.mutedForeground)
                    } else if updates.updateAvailable {
                        Text("v\(updates.latestVersion ?? "?") is available.")
                            .font(.callout).foregroundStyle(palette.foreground)
                        Spacer()
                        Button("Update and Relaunch") { Task { await updates.installAndRelaunch() } }
                    } else {
                        Text(checkedOnce ? "You're up to date." : "")
                            .font(.callout).foregroundStyle(palette.mutedForeground)
                        Spacer()
                        Button("Check for Updates…") {
                            Task { await updates.check(); checkedOnce = true }
                        }
                    }
                }
                if let err = updates.installError {
                    Label(err, systemImage: "exclamationmark.triangle")
                        .font(.callout).foregroundStyle(.red)
                }
            }
        }
        .formStyle(.grouped)
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(palette.background)
    }
}

/// The preferences that live on the DAEMON, not in defaults. Split into its own view purely so the
/// `Model` can be observed — a `let Model?` never republishes, so the notification toggles would
/// have rendered once with whatever state happened to be loaded and then frozen.
private struct DaemonPrefsSections: View {
    @ObservedObject var model: Model
    let palette: OculusPalette
    // Shared with RootView — see `DaemonLauncher.shared`.
    @ObservedObject private var loginItem = LoginItemManager.shared
    @ObservedObject private var launcher = DaemonLauncher.shared

    var body: some View {
        Group {
            Section("Notifications") {
                if model.notifyPrefs.isEmpty {
                    HStack(spacing: 8) {
                        ProgressView().controlSize(.small)
                        Text("Loading notification types…")
                            .font(.callout).foregroundStyle(palette.mutedForeground)
                    }
                } else {
                    ForEach(model.notifyPrefs) { pref in
                        Toggle(isOn: Binding(get: { pref.enabled },
                                             set: { on in Task { await model.setNotifyPref(pref.key, enabled: on) } })) {
                            VStack(alignment: .leading, spacing: 2) {
                                Text(pref.label)
                                if let detail = pref.detail {
                                    Text(detail).font(.caption).foregroundStyle(palette.mutedForeground)
                                }
                            }
                        }
                    }
                }
            }
            .task { await model.loadNotifyPrefs() }

            Section("Privacy") {
                Toggle(isOn: Binding(get: { model.telemetryEnabled },
                                     set: { on in Task { await model.setTelemetry(on) } })) {
                    VStack(alignment: .leading, spacing: 2) {
                        Text("Send anonymous diagnostics")
                        Text("Anonymized lifecycle events and scrubbed error classes — no paths, prompts, repo names, or tokens.")
                            .font(.caption).foregroundStyle(palette.mutedForeground)
                    }
                }
            }

            Section("Startup") {
                // Safe to host here now only because the launcher is a shared instance. Built against
                // a fresh `DaemonLauncher` this toggle would report success having done nothing —
                // `managed` would read false, the app-owned daemon child would never be stopped, and
                // the launchd agent's daemon would fail to bind behind it.
                Toggle(isOn: Binding(get: { loginItem.enabled },
                                     set: { on in Task { await loginItem.setEnabled(on, launcher: launcher) } })) {
                    VStack(alignment: .leading, spacing: 2) {
                        Text("Start daemon at login")
                        Text("Keeps sessions reachable after a reboot, without leaving the app open.")
                            .font(.caption).foregroundStyle(palette.mutedForeground)
                    }
                }
                if let err = loginItem.lastError {
                    Label(err, systemImage: "exclamationmark.triangle.fill")
                        .font(.caption).foregroundStyle(palette.destructive)
                        .textSelection(.enabled)
                }
            }
            .onAppear { loginItem.refresh() }
        }
    }
}

/// The app's main menu.
///
/// Without a `.commands` block the File menu carries only the system's "New Window", so nothing the
/// app actually does is reachable from the menu bar — which also means Full Keyboard Access and the
/// Help menu's shortcut search can't find any of it.
///
/// Deliberately limited to actions reachable from the SCENE. "New Session…" and the ⌘K palette live
/// in RootView's local `@State` and have no route out of that view, so they aren't here.
public struct OculusCommands: Commands {
    @ObservedObject var store: DesktopStore

    public init(store: DesktopStore) { self.store = store }

    public var body: some Commands {
        // Replacing `.newItem` rather than appending to it: the system's "New Window" already holds
        // ⌘N, and a second window on a single-connection app isn't what ⌘N should mean here.
        CommandGroup(replacing: .newItem) {
            Button("New Chat") {
                guard let model = store.active else { return }
                Task { await model.startEphemeralChat() }
            }
            .keyboardShortcut("n", modifiers: .command)
            .disabled(store.active == nil)

            Divider()

            Button("Reconnect") {
                guard let model = store.active else { return }
                Task { await model.connect() }
            }
            .keyboardShortcut("r", modifiers: .command)
            .disabled(store.active == nil)

            Button("Refresh Sessions") {
                guard let model = store.active else { return }
                Task { await model.discover() }
            }
            .keyboardShortcut("r", modifiers: [.command, .shift])
            .disabled(store.active == nil)

            Divider()

            Button("Disconnect") { store.active?.disconnect() }
                .disabled(store.active == nil)
        }
    }
}
#endif
