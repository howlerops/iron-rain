import SwiftUI
import OculusKit

/// The multi-desktop root: connect to every paired Mac at once, switch between them,
/// and drive the selected one's sessions. Replaces the single-connection ContentView as
/// the app entry surface.
public struct RootView: View {
    @ObservedObject var store: DesktopStore
    @Environment(\.colorScheme) private var scheme
    private var palette: OculusPalette { .current(scheme) }
    @State private var selection: String?
    @State private var showNewSession = false
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
            } else if let model = store.active {
                mainSurface(model)
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
        // NavigationSplitView on macOS 26 ignores the height proposal and reports its own
        // ideal (~1884pt), which the window host then centers — so the sidebar's content
        // hangs above the viewport and can't scroll. A GeometryReader gives us the real
        // proposed height (the 720pt window), and an explicit .frame() PINS the split view
        // to it, overriding its runaway ideal.
        GeometryReader { proxy in
            NavigationSplitView {
                SessionSidebar(store: store, model: model, selection: $selection, searchText: $searchText,
                               onReview: { sid in reviewSessionID = sid; selectedTab = 2 })
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
                NewSessionView(model: model, palette: palette) { showNewSession = false }
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
                NewSessionView(model: model, palette: palette) { showNewSession = false }
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
                CodeSurface(model: model, reviewSessionID: reviewSessionID)
                    .id(reviewSessionID ?? "browse") // re-review when the target changes
            default:
                ChatView(model: model)
            }
        }
        #if os(macOS)
        .toolbar {
            ToolbarItem(placement: .principal) {
                Picker("View", selection: $selectedTab) {
                    Text("Sessions").tag(0)
                    Text("Issues").tag(1)
                    Text("Code").tag(2)
                }
                .pickerStyle(.segmented)
                .frame(width: 180)
            }
        }
        #endif
    }

    private func handleSelection(_ sel: String?, _ model: Model) {
        guard let sel else { return }
        if sel == SessionSidebar.newSessionTag {
            showNewSession = true
            selection = nil
        } else if model.sessions.contains(where: { $0.id == sel }) {
            Task { await model.openSession(sel) }
        } else if let d = model.discovered.first(where: { $0.sessionID == sel }) {
            Task { await model.attach(d) }
        }
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
            Text("Iron Rain").font(.largeTitle.bold())
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
