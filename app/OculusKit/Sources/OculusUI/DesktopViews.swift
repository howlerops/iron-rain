import SwiftUI
import OculusKit

/// The multi-desktop root: connect to every paired Mac at once, switch between them,
/// and drive the selected one's sessions. Replaces the single-connection ContentView as
/// the app entry surface.
/// Presents `model.actionError` as an alert on the main surface. Needed because a failed action
/// (e.g. a session that couldn't start) sets the error AFTER its New Session sheet has dismissed,
/// when plain status text isn't visible — so the user would otherwise see "nothing happened".
struct ActionErrorAlert: ViewModifier {
    @ObservedObject var model: Model
    func body(content: Content) -> some View {
        content.alert("Couldn’t start the session", isPresented: Binding(
            get: { model.actionError != nil },
            set: { if !$0 { model.actionError = nil } }
        )) {
            Button("OK", role: .cancel) { model.actionError = nil }
        } message: {
            Text(model.actionError ?? "")
        }
    }
}

public struct RootView: View {
    @ObservedObject var store: DesktopStore
    @Environment(\.colorScheme) private var scheme
    private var palette: OculusPalette { .current(scheme) }
    @State private var selection: String?
    @State private var showNewSession = false
    @State private var newSessionTakeOver = false
    @State private var selectedTab = 0
    @State private var searchText = ""
    @State private var reviewSessionID: String?
    @AppStorage("oculus.appearance") private var appearance: Appearance = .system
    #if os(macOS)
    @StateObject private var launcher = DaemonLauncher()
    #endif

    public init(store: DesktopStore) { self.store = store }

    public var body: some View {
        Group {
            if !store.didBootstrap {
                // Gate the app behind the first connection attempt so the connected surface
                // isn't preceded by a flash of onboarding / disconnected default.
                loadingScreen
            } else if store.isEmpty {
                DesktopOnboardView(store: store, palette: palette)
                #if os(macOS)
                    .safeAreaInset(edge: .top) {
                        if launcher.trouble != nil {
                            DaemonStatusBanner(launcher: launcher, palette: palette) {
                                await launcher.ensureRunning()
                                await store.bootstrap()
                            }
                            .padding(.horizontal, 20).padding(.top, 14)
                        }
                    }
                #endif
            } else if let model = store.active {
                mainSurface(model)
                    .modifier(ActionErrorAlert(model: model))
            }
        }
        // CRITICAL: force the surface to FILL the window instead of sizing to the split
        // view's ideal height. The view-tree dump showed the NavigationSplitView laying out
        // at 1884pt tall in a 720pt window (centered, so ~556pt hung off the top) — that was
        // the "sidebar overflows above the viewport, can't scroll" bug all along. This frame
        // clamps it to the window; the sidebar List then caps and scrolls normally.
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(palette.background.ignoresSafeArea())
        .foregroundStyle(palette.foreground)
        .tint(palette.primary)
        .preferredColorScheme(appearance.colorScheme)
        .task {
            #if os(macOS)
            await launcher.ensureRunning() // start the local daemon (no terminal) if needed
            #endif
            await store.bootstrap()
        }
    }

    /// Shown until the first connection attempt resolves — the wolf mark over a spinner.
    private var loadingScreen: some View {
        VStack(spacing: 18) {
            Image("WolfMark").resizable().scaledToFit().frame(width: 56, height: 56).opacity(0.9)
            ProgressView().controlSize(.small)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(palette.background)
    }

    /// The Sessions/Issues surface. macOS uses ONE NavigationSplitView (the mode switch
    /// lives in the sidebar, the detail swaps) — a TabView wrapping a split view with
    /// per-view toolbars corrupts AppKit's toolbar bridge and crashes on window
    /// close/reopen. iOS keeps a bottom TabView, which is the right idiom there.
    @ViewBuilder private func mainSurface(_ model: Model) -> some View {
        #if os(macOS)
        // No sessions yet → skip the sidebar+chat split entirely and show a single, unmistakable
        // CTA (or Issues if that mode is selected). The split view returns once a session exists.
        if model.sessions.isEmpty {
            Group {
                if selectedTab == 1 {
                    IssuesView(model: model, palette: palette, embedded: true) { selectedTab = 0 }
                } else {
                    EmptySessionsCTA(palette: palette,
                                     onNew: { newSessionTakeOver = false; showNewSession = true },
                                     onTakeOver: { newSessionTakeOver = true; showNewSession = true })
                }
            }
            .frame(maxWidth: .infinity, maxHeight: .infinity)
            .background(palette.background)
            .toolbar { modeToolbar(model) }
            .sheet(isPresented: $showNewSession) {
                NewSessionView(model: model, palette: palette, initialTakeOver: newSessionTakeOver) { showNewSession = false }
            }
        } else {
        // NavigationSplitView on macOS 26 ignores the height proposal and reports its own
        // ideal (~1884pt), which the window host then centers — so the sidebar's content
        // hangs above the viewport and can't scroll. A GeometryReader gives us the real
        // proposed height (the 720pt window), and an explicit .frame() PINS the split view
        // to it, overriding its runaway ideal.
        GeometryReader { proxy in
            NavigationSplitView {
                SessionSidebar(store: store, model: model, selection: $selection, searchText: $searchText,
                               onReview: { sid in reviewSessionID = sid; selectedTab = 2 },
                               onTakeOver: { newSessionTakeOver = true; showNewSession = true })
                    .navigationSplitViewColumnWidth(min: 240, ideal: 280, max: 340)
            } detail: {
                detailPane(model)
                    // Clamp the detail column to the window height. The split view sizes to its
                    // tallest column's ideal; ChatView's flexible (maxHeight:.infinity) empty
                    // state measured as a runaway ~1884pt ideal and inflated the whole split
                    // view (and thus the sidebar), while the fixed-height IssuesView did not.
                    // Pinning the detail to proxy.size.height decouples the two so no detail
                    // content can ever blow up the sidebar.
                    .frame(height: proxy.size.height)
            }
            .frame(width: proxy.size.width, height: proxy.size.height)
            .onChange(of: selection) { handleSelection($0, model) }
            .sheet(isPresented: $showNewSession) {
                NewSessionView(model: model, palette: palette, initialTakeOver: newSessionTakeOver) { showNewSession = false }
            }
        }
        }
        #else
        TabView(selection: $selectedTab) {
            NavigationSplitView {
                SessionSidebar(store: store, model: model, selection: $selection, searchText: $searchText)
                    .navigationSplitViewColumnWidth(min: 240, ideal: 280, max: 340)
            } detail: {
                ChatView(model: model)
            }
            .onChange(of: selection) { handleSelection($0, model) }
            .sheet(isPresented: $showNewSession) {
                NewSessionView(model: model, palette: palette, initialTakeOver: newSessionTakeOver) { showNewSession = false }
            }
            .tabItem { Label("Sessions", systemImage: "bubble.left.and.bubble.right.fill") }
            .tag(0)

            IssuesView(model: model, palette: palette) { selectedTab = 0 }
                .tabItem { Label("Issues", systemImage: "checklist") }
                .tag(1)
        }
        #endif
    }

    /// The detail column plus the Sessions/Issues mode switch, which lives in the detail
    /// toolbar (a segmented control centered in the wide detail titlebar) rather than the
    /// narrow, layout-fragile sidebar top. It swaps the detail between the chat and issues.
    @ViewBuilder private func detailPane(_ model: Model) -> some View {
        Group {
            switch selectedTab {
            case 1:
                IssuesView(model: model, palette: palette, embedded: true) { selectedTab = 0 }
            case 2:
                // Scope the file tree to the active session's workspace (per-session code view);
                // fall back to browsing all roots when no session is open.
                let codeSession = reviewSessionID ?? model.currentSession?.id
                CodeSurface(model: model, sessionID: codeSession, reviewSessionID: reviewSessionID)
                    .id((codeSession ?? "browse") + (reviewSessionID != nil ? ":review" : "")) // reload on session/target change
            default:
                ChatView(model: model)
            }
        }
        #if os(macOS)
        .toolbar { modeToolbar(model) }
        // If the open session closes while Code is showing, fall back to Sessions.
        .onChange(of: model.currentSession?.id) { sid in
            if selectedTab == 2 && sid == nil && reviewSessionID == nil { selectedTab = 0 }
        }
        #endif
    }

    #if os(macOS)
    /// The Sessions/Issues/Code segmented switch (Code only while a session is open — it's a
    /// per-session view). Shared by the split view and the empty state.
    @ToolbarContentBuilder private func modeToolbar(_ model: Model) -> some ToolbarContent {
        ToolbarItem(placement: .principal) {
            Picker("View", selection: $selectedTab) {
                Text("Sessions").tag(0)
                Text("Issues").tag(1)
                if hasSession(model) {
                    Text("Code").tag(2)
                }
            }
            .pickerStyle(.segmented)
            .frame(width: hasSession(model) ? 210 : 150)
        }
    }
    #endif

    private func hasSession(_ model: Model) -> Bool {
        model.currentSession != nil || reviewSessionID != nil
    }

    private func handleSelection(_ sel: String?, _ model: Model) {
        guard let sel else { return }
        if sel == SessionSidebar.newSessionTag {
            newSessionTakeOver = false // the ✎ / empty-state "New session" opens in Start-new mode
            showNewSession = true
            selection = nil
        } else if model.sessions.contains(where: { $0.id == sel }) {
            Task { await model.openSession(sel) }
        } else if let d = model.discovered.first(where: { $0.sessionID == sel }) {
            Task { await model.attach(d) }
        }
    }
}

/// The no-sessions empty state: a single centered CTA (start a session or take over a terminal),
/// shown instead of the sidebar+chat split so the first action is unmistakable.
struct EmptySessionsCTA: View {
    let palette: OculusPalette
    let onNew: () -> Void
    let onTakeOver: () -> Void

    var body: some View {
        VStack(spacing: 16) {
            Image("WolfMark").resizable().scaledToFit().frame(width: 60, height: 60).opacity(0.9)
            VStack(spacing: 5) {
                Text("No sessions yet").font(.title2.bold())
                Text("Start an agent on one of your projects, or take over a session already running in a terminal.")
                    .font(.subheadline).foregroundStyle(palette.mutedForeground)
                    .multilineTextAlignment(.center).frame(maxWidth: 380)
            }
            VStack(spacing: 10) {
                Button(action: onNew) {
                    Label("New session", systemImage: "plus").frame(maxWidth: .infinity)
                }
                .buttonStyle(.borderedProminent).tint(palette.primary).controlSize(.large)
                Button(action: onTakeOver) {
                    Label("Take over a terminal session", systemImage: "arrow.down.right.circle").frame(maxWidth: .infinity)
                }
                .buttonStyle(.bordered).tint(palette.primary).controlSize(.large)
            }
            .frame(maxWidth: 360)
            .padding(.top, 4)
        }
        .padding(40)
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }
}

/// Adds a desktop by scanning its QR (iOS) or pasting the oculus://pair link.
struct AddDesktopView: View {
    @ObservedObject var store: DesktopStore
    let palette: OculusPalette
    let onClose: () -> Void
    @State private var pasteURL = ""
    #if os(iOS)
    @State private var showScanner = false
    #endif

    var body: some View {
        NavigationStack {
            VStack(spacing: 16) {
                Text("Pair another Mac's Iron Rain daemon.")
                    .font(.subheadline).foregroundStyle(palette.mutedForeground)
                #if os(iOS)
                Button { showScanner = true } label: {
                    Label("Scan QR code", systemImage: "qrcode.viewfinder").frame(maxWidth: .infinity)
                }
                .buttonStyle(.borderedProminent).tint(palette.primary)
                #endif
                TextField("Paste oculus://pair link", text: $pasteURL)
                    .textFieldStyle(.roundedBorder)
                    #if os(iOS)
                    .textInputAutocapitalization(.never).autocorrectionDisabled()
                    #endif
                Button("Add desktop") {
                    if let p = PairingPayload(pasteURL) { store.add(p); onClose() }
                }
                .buttonStyle(.borderedProminent).tint(palette.primary)
                .disabled(PairingPayload(pasteURL) == nil)
                Spacer()
            }
            .padding()
            .navigationTitle("Add desktop")
            .toolbar { ToolbarItem(placement: .cancellationAction) { Button("Cancel") { onClose() } } }
            #if os(iOS)
            .sheet(isPresented: $showScanner) {
                QRScannerView { payload in
                    showScanner = false
                    if let p = PairingPayload(payload) { store.add(p); onClose() }
                }
                .ignoresSafeArea()
            }
            #endif
        }
    }
}

/// First-run screen when no desktops are paired yet.
struct DesktopOnboardView: View {
    @ObservedObject var store: DesktopStore
    let palette: OculusPalette
    @State private var showAdd = false

    var body: some View {
        VStack(spacing: 20) {
            Spacer()
            Image("WolfMark").resizable().scaledToFit().frame(width: 72, height: 72)
            IronRainWordmark(size: 30)
            Text("Pair with your Mac's Iron Rain daemon to get started.")
                .font(.subheadline).foregroundStyle(palette.mutedForeground)
                .multilineTextAlignment(.center).padding(.horizontal, 32)
            Button { showAdd = true } label: {
                Label("Add a desktop", systemImage: "plus.circle").frame(maxWidth: .infinity)
            }
            .buttonStyle(.borderedProminent).tint(palette.primary)
            .padding(.horizontal, 48)
            Spacer()
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(palette.background.ignoresSafeArea())
        .sheet(isPresented: $showAdd) { AddDesktopView(store: store, palette: palette) { showAdd = false } }
    }
}

#if os(macOS)
/// Surfaces the local daemon's health on the first screen: what went wrong, an actionable fix
/// (install command, or the manual `oculusd serve` when a sandboxed app can't spawn it), and a
/// one-tap retry that re-attempts the start and re-bootstraps the connection.
struct DaemonStatusBanner: View {
    @ObservedObject var launcher: DaemonLauncher
    let palette: OculusPalette
    let onRetry: () async -> Void
    @State private var retrying = false

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack(spacing: 8) {
                Image(systemName: launcher.running ? "bolt.fill" : "bolt.slash.fill")
                    .foregroundStyle(launcher.running ? .green : .orange)
                Text(launcher.running ? "Local daemon" : "Local daemon isn't running")
                    .font(.callout.bold())
                Spacer()
                Button {
                    guard !retrying else { return }
                    retrying = true
                    Task { await onRetry(); retrying = false }
                } label: {
                    if retrying { ProgressView().controlSize(.small) }
                    else { Text(launcher.running ? "Recheck" : "Start daemon") }
                }
                .buttonStyle(.borderedProminent).tint(palette.primary)
                .disabled(retrying)
            }
            Text(launcher.status).font(.caption).foregroundStyle(palette.mutedForeground)
            if let t = launcher.trouble { troubleHelp(t) }
        }
        .padding(12)
        .background(palette.secondary.opacity(0.5), in: RoundedRectangle(cornerRadius: 10))
        .overlay(RoundedRectangle(cornerRadius: 10).stroke(palette.border))
    }

    @ViewBuilder private func troubleHelp(_ t: DaemonLauncher.Trouble) -> some View {
        switch t {
        case .notInstalled:
            commandRow("Install it, then Start daemon:", DaemonLauncher.installCommand)
        case .startFailed, .notResponding:
            commandRow("Or start it yourself in Terminal:", DaemonLauncher.manualCommand)
        }
    }

    private func commandRow(_ label: String, _ cmd: String) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            Text(label).font(.caption).foregroundStyle(palette.mutedForeground)
            HStack(spacing: 6) {
                Text(cmd).font(.system(size: 12, design: .monospaced)).textSelection(.enabled)
                    .lineLimit(1).truncationMode(.middle)
                    .padding(.horizontal, 8).padding(.vertical, 5)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .background(palette.input, in: RoundedRectangle(cornerRadius: 6))
                Button { copyCommand(cmd) } label: { Image(systemName: "doc.on.doc") }
                    .buttonStyle(.borderless).help("Copy")
            }
        }
    }

    private func copyCommand(_ cmd: String) {
        #if canImport(AppKit)
        NSPasteboard.general.clearContents()
        NSPasteboard.general.setString(cmd, forType: .string)
        #endif
    }
}
#endif
