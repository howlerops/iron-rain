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
    #if os(macOS)
    @StateObject private var launcher = DaemonLauncher()
    #endif

    public init(store: DesktopStore) { self.store = store }

    public var body: some View {
        Group {
            if store.isEmpty {
                DesktopOnboardView(store: store, palette: palette)
            } else if let model = store.active {
                mainSurface(model)
            }
        }
        .background(palette.background.ignoresSafeArea())
        .foregroundStyle(palette.foreground)
        .tint(palette.primary)
        .task {
            #if os(macOS)
            await launcher.ensureRunning() // start the local daemon (no terminal) if needed
            #endif
            await store.bootstrap()
        }
    }

    /// The Sessions/Issues surface. macOS uses ONE NavigationSplitView (the mode switch
    /// lives in the sidebar, the detail swaps) — a TabView wrapping a split view with
    /// per-view toolbars corrupts AppKit's toolbar bridge and crashes on window
    /// close/reopen. iOS keeps a bottom TabView, which is the right idiom there.
    @ViewBuilder private func mainSurface(_ model: Model) -> some View {
        #if os(macOS)
        NavigationSplitView {
            SessionSidebar(store: store, model: model, selection: $selection, tab: $selectedTab, searchText: $searchText)
                .navigationSplitViewColumnWidth(min: 240, ideal: 280, max: 340)
        } detail: {
            if selectedTab == 0 {
                ChatView(model: model)
            } else {
                IssuesView(model: model, palette: palette, embedded: true) { selectedTab = 0 }
            }
        }
        // Per Apple's NavigationSplitView guidance, `.searchable` goes on the split view,
        // not the sidebar — the system positions the field and it doesn't fight the
        // sidebar's own chrome for the top band.
        .searchable(text: $searchText, placement: .sidebar, prompt: "Search sessions")
        .onChange(of: selection) { handleSelection($0, model) }
        .sheet(isPresented: $showNewSession) {
            NewSessionView(model: model, palette: palette) { showNewSession = false }
        }
        #else
        TabView(selection: $selectedTab) {
            NavigationSplitView {
                SessionSidebar(store: store, model: model, selection: $selection, tab: $selectedTab, searchText: $searchText)
                    .navigationSplitViewColumnWidth(min: 240, ideal: 280, max: 340)
            } detail: {
                ChatView(model: model)
            }
            .searchable(text: $searchText, prompt: "Search sessions")
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
