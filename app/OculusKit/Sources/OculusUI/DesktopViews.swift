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
    #if os(macOS)
    @StateObject private var launcher = DaemonLauncher()
    #endif

    public init(store: DesktopStore) { self.store = store }

    public var body: some View {
        Group {
            if store.isEmpty {
                DesktopOnboardView(store: store, palette: palette)
            } else if let model = store.active {
                TabView(selection: $selectedTab) {
                    NavigationSplitView {
                        VStack(spacing: 0) {
                            DesktopBar(store: store, palette: palette)
                            Divider().overlay(palette.border)
                            SessionSidebar(model: model, selection: $selection)
                        }
                        .navigationSplitViewColumnWidth(min: 240, ideal: 280)
                    } detail: {
                        ChatView(model: model)
                    }
                    .onChange(of: selection) { sel in
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
                    .sheet(isPresented: $showNewSession) {
                        NewSessionView(model: model, palette: palette) { showNewSession = false }
                    }
                    .tabItem { Label("Sessions", systemImage: "bubble.left.and.bubble.right.fill") }
                    .tag(0)

                    IssuesView(model: model, palette: palette) { selectedTab = 0 }
                        .tabItem { Label("Issues", systemImage: "checklist") }
                        .tag(1)
                }
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
}

/// Desktop switcher shown atop the sidebar: pick the active Mac, add/rename/remove.
struct DesktopBar: View {
    @ObservedObject var store: DesktopStore
    let palette: OculusPalette
    @State private var showAdd = false
    @State private var renaming = false
    @State private var newName = ""

    var body: some View {
        Menu {
            ForEach(store.models, id: \.id) { m in
                Button {
                    store.selectedID = m.id
                } label: {
                    Label(m.name.isEmpty ? "Desktop" : m.name,
                          systemImage: m.id == store.selectedID ? "checkmark" : (m.connected ? "circle.fill" : "circle"))
                }
            }
            Divider()
            Button { showAdd = true } label: { Label("Add desktop…", systemImage: "plus") }
            if let a = store.active {
                Button { newName = a.name; renaming = true } label: { Label("Rename…", systemImage: "pencil") }
                Button(role: .destructive) { store.remove(a.id) } label: { Label("Remove desktop", systemImage: "trash") }
            }
        } label: {
            HStack(spacing: 7) {
                Circle().fill((store.active?.connected ?? false) ? Color.green : palette.mutedForeground)
                    .frame(width: 7, height: 7)
                Text((store.active?.name.isEmpty == false) ? store.active!.name : "Desktop")
                    .font(.subheadline.bold()).lineLimit(1)
                Image(systemName: "chevron.up.chevron.down").font(.caption2)
                Spacer()
            }
            .foregroundStyle(palette.foreground)
        }
        .menuStyle(.borderlessButton)
        .padding(.horizontal, 14).padding(.vertical, 9)
        .background(palette.card)
        .sheet(isPresented: $showAdd) { AddDesktopView(store: store, palette: palette) { showAdd = false } }
        .alert("Rename desktop", isPresented: $renaming) {
            TextField("Name", text: $newName)
            Button("Save") { if let a = store.active { store.rename(a.id, to: newName) } }
            Button("Cancel", role: .cancel) {}
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
