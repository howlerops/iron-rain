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
        content.alert(model.actionErrorTitle, isPresented: Binding(
            get: { model.actionError != nil },
            set: { if !$0 { model.actionError = nil } }
        )) {
            Button("OK", role: .cancel) { model.actionError = nil }
        } message: {
            Text(model.actionError ?? "")
        }
    }
}

/// Skeleton loading overlay shown while a session is being created (worktree setup + provider
/// spin-up can take a few seconds). It covers and LOCKS the surface so no half-ready UI is
/// interactive and the wait has clear feedback.
struct SessionStartingOverlay: ViewModifier {
    @ObservedObject var model: Model
    let palette: OculusPalette
    func body(content: Content) -> some View {
        content
            .overlay {
                if model.startingSession {
                    SessionSkeleton(provider: model.startingProvider, palette: palette, steps: model.createSteps)
                        .transition(.opacity)
                }
            }
            .animation(.easeInOut(duration: 0.2), value: model.startingSession)
    }
}

/// A pulsing chat skeleton + "Starting <provider>…" status. Fills the surface and swallows input.
struct SessionSkeleton: View {
    let provider: String
    let palette: OculusPalette
    var steps: [CreateStep] = []
    @State private var pulse = false
    private let rows: [(w: CGFloat, mine: Bool)] = [(0.62, false), (0.42, true), (0.80, false), (0.34, true), (0.70, false), (0.5, false)]

    var body: some View {
        VStack(spacing: 0) {
            GeometryReader { geo in
                ScrollView {
                    VStack(alignment: .leading, spacing: 20) {
                        ForEach(Array(rows.enumerated()), id: \.offset) { _, r in
                            bubble(width: max(90, geo.size.width * r.w - 40), mine: r.mine)
                        }
                    }
                    .padding(20)
                }
                .disabled(true)
            }
            Divider().overlay(palette.border)
            footer
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(.horizontal, 20).padding(.vertical, 16)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(palette.background)
        .contentShape(Rectangle())
        .onTapGesture {} // absorb taps — locks the surface while starting
        .onAppear {
            withAnimation(.easeInOut(duration: 0.9).repeatForever(autoreverses: true)) { pulse = true }
        }
    }

    /// The prescriptive checklist when the daemon is streaming create steps; the generic
    /// "Starting…" line until the first step arrives (or for providers that report none).
    @ViewBuilder private var footer: some View {
        if steps.isEmpty {
            HStack(spacing: 10) {
                ProgressView()
                Text("Starting \(provider.isEmpty ? "session" : provider)…")
                    .font(.subheadline.weight(.medium)).foregroundStyle(palette.mutedForeground)
            }
        } else {
            VStack(alignment: .leading, spacing: 9) {
                ForEach(steps) { step in
                    HStack(spacing: 9) {
                        stepIcon(step)
                        Text(stepLabel(step))
                            .font(.subheadline.weight(step.done ? .regular : .medium))
                            .foregroundStyle(step.done ? palette.mutedForeground : palette.foreground)
                        Spacer(minLength: 0)
                    }
                }
            }
        }
    }

    @ViewBuilder private func stepIcon(_ step: CreateStep) -> some View {
        if step.done {
            Image(systemName: "checkmark.circle.fill").foregroundStyle(.green).font(.subheadline)
        } else {
            ProgressView().controlSize(.small).frame(width: 16, height: 16)
        }
    }

    private func stepLabel(_ step: CreateStep) -> String {
        if step.total > 1 { return "\(step.detail)  (\(step.step)/\(step.total))" }
        return step.detail
    }

    private func bubble(width: CGFloat, mine: Bool) -> some View {
        HStack(spacing: 0) {
            if mine { Spacer(minLength: 0) }
            RoundedRectangle(cornerRadius: 14)
                .fill(palette.muted.opacity(pulse ? 0.55 : 0.28))
                .frame(width: width, height: 46)
            if !mine { Spacer(minLength: 0) }
        }
    }
}

#if os(macOS)
/// Shows the "Update available" pill (bottom) + details sheet for the curl-installed macOS app,
/// wherever it's attached — so it appears in BOTH the empty state and the session split view.
/// `forceCheck` (from a Settings "Check for updates" action) triggers a check + opens the sheet.
struct SoftwareUpdateModifier: ViewModifier {
    let palette: OculusPalette
    @Binding var forceCheck: Bool
    @ObservedObject var updates: UpdateChecker // owned by RootView so the sidebar card shares it
    @State private var showSheet = false

    // The "update available" affordance now lives as a card at the BOTTOM OF THE SIDEBAR
    // (SessionSidebar.updatePill), Claude-Code style. This modifier just drives the periodic check,
    // the "Check for updates" sheet, and the manual/error details.
    func body(content: Content) -> some View {
        content
            .sheet(isPresented: $showSheet) { updateSheet }
            .task { await updates.check() }
            .onChange(of: forceCheck) { go in
                guard go else { return }
                Task { await updates.check(); showSheet = true; forceCheck = false }
            }
    }

    private var updateSheet: some View {
        VStack(alignment: .leading, spacing: 14) {
            HStack {
                Text("Update Iron Rain").font(.headline)
                Spacer()
                Button("Done") { showSheet = false }.keyboardShortcut(.cancelAction).disabled(updates.installing)
            }
            Text(updates.updateAvailable
                 ? "You're on v\(updates.currentVersion) · latest is v\(updates.latestVersion ?? "?"). Update installs the new version and relaunches the app for you."
                 : "You're on v\(updates.currentVersion)\(updates.latestVersion.map { ", the latest release. (v\($0))" } ?? "."). You're up to date.")
                .font(.callout).foregroundStyle(palette.mutedForeground)

            if updates.updateAvailable {
                if updates.installing {
                    HStack(spacing: 8) {
                        ProgressView().controlSize(.small)
                        Text(updates.installPhase).font(.callout).foregroundStyle(palette.mutedForeground)
                    }
                } else {
                    Button {
                        Task { await updates.installAndRelaunch() }
                    } label: {
                        Label("Update & Relaunch", systemImage: "arrow.down.circle.fill").frame(maxWidth: .infinity)
                    }
                    .buttonStyle(.borderedProminent).tint(palette.primary).controlSize(.large)
                }
                if let err = updates.installError {
                    Text(err).font(.caption).foregroundStyle(.red)
                }
            }

            // Manual fallback (offline, restricted /Applications, or if the in-app update fails).
            DisclosureGroup("Update manually instead") {
                HStack(spacing: 6) {
                    Text(DaemonLauncher.installCommand)
                        .font(.system(size: 12, design: .monospaced)).textSelection(.enabled)
                        .lineLimit(1).truncationMode(.middle)
                        .padding(.horizontal, 8).padding(.vertical, 6)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .background(palette.input, in: RoundedRectangle(cornerRadius: 6))
                    Button {
                        #if canImport(AppKit)
                        NSPasteboard.general.clearContents()
                        NSPasteboard.general.setString(DaemonLauncher.installCommand, forType: .string)
                        #endif
                    } label: { Image(systemName: "doc.on.doc") }.buttonStyle(.borderless).help("Copy")
                }
                .padding(.top, 4)
            }
            .font(.caption)

            HStack { Spacer(); Link("View release notes", destination: UpdateChecker.releasesURL).font(.caption) }
        }
        .padding(18).frame(minWidth: 460).background(palette.background)
    }
}
#endif

/// The one shared sheet slot for the Loops / Agents panels (kept to a single `.sheet` so they
/// don't collide with the New Session sheet).
private enum PanelSheet: Int, Identifiable { case loops, agents, accounts, remotes, sessions; var id: Int { rawValue } }

public struct RootView: View {
    @ObservedObject var store: DesktopStore
    @Environment(\.colorScheme) private var scheme
    private var palette: OculusPalette { .current(scheme) }
    @State private var selection: String?
    @State private var showSessionDetail = false // iOS: pushes ChatView when a session opens
    @State private var checkForUpdates = false   // macOS: Settings → "Check for updates" trigger
    // Loops + Agents share ONE sheet slot — stacking two .sheet modifiers on the same view breaks
    // SwiftUI's sheet presentation (it silently killed the New Session sheet after the first session).
    @State private var panel: PanelSheet?
    @State private var showNewSession = false
    @State private var newSessionTakeOver = false
    @State private var selectedTab = 0
    // Command Deck: the active top-level destination. macOS = nav-rail selection; iOS = bottom tab.
    // iOS defaults to Activity (index-of .activity) — the phone is a triage inbox; macOS opens on Sessions.
    #if os(macOS)
    @State private var destination: Destination = .sessions
    #else
    @State private var destination: Destination = .activity
    #endif
    @State private var searchText = ""
    @State private var selectedLoopID: String?     // Loops destination: which loop the detail edits (nil = new/templates)
    @State private var editingLoop = false          // Loops destination: detail shows the editor
    @State private var showPalette = false          // Cmd-K command palette
    @State private var showFanout = false           // Fan-out composer sheet
    // Desktop (paired-Mac) switcher — hangs off the window title, Xcode-scheme-menu style.
    @State private var showAddDesktop = false
    @State private var renamingDesktop = false
    @State private var desktopNewName = ""
    @AppStorage("oculus.appearance") private var appearance: Appearance = .system
    #if os(macOS)
    @StateObject private var launcher = DaemonLauncher()
    @StateObject private var loginItem = LoginItemManager()
    @StateObject private var updates = UpdateChecker() // shared: drives the sidebar update card + the check
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
                    .modifier(SessionStartingOverlay(model: model, palette: palette))
                    .modifier(ActionErrorAlert(model: model))
                    #if os(macOS)
                    .modifier(SoftwareUpdateModifier(palette: palette, forceCheck: $checkForUpdates, updates: updates))
                    #endif
                    .sheet(item: $panel) { which in
                        switch which {
                        case .loops:
                            LoopsView(model: model, palette: palette,
                                      onOpenSession: { sid in
                                          panel = nil
                                          Task { await model.openSession(sid) }
                                          showSessionDetail = true
                                      },
                                      onClose: { panel = nil })
                        case .agents:
                            ManageAgentsView(model: model, palette: palette)
                        case .accounts:
                            AccountsView(model: model, palette: palette, onClose: { panel = nil })
                        case .remotes:
                            RemotesView(model: model, palette: palette, onClose: { panel = nil })
                        case .sessions:
                            AllSessionsView(model: model, palette: palette, onClose: { panel = nil },
                                            onOpen: { sid in
                                                panel = nil
                                                Task { await model.openSession(sid) }
                                                showSessionDetail = true
                                            })
                        }
                    }
                    .safeAreaInset(edge: .bottom, spacing: 0) {
                        DaemonLogPanel(model: model, palette: palette, onOpenActivity: { destination = .activity })
                    }
                    // Cmd-K command palette: one fuzzy entry across destinations, sessions, loops,
                    // agents, and actions. ⌘K on macOS; a search button in the sidebar on iOS.
                    .background(
                        Button("") { showPalette = true }
                            .keyboardShortcut("k", modifiers: .command).opacity(0)
                    )
                    .overlay {
                        if showPalette {
                            ZStack {
                                Color.black.opacity(0.35).ignoresSafeArea()
                                    .onTapGesture { showPalette = false }
                                CommandPalette(model: model, palette: palette,
                                               items: paletteItems(model), onClose: { showPalette = false })
                                    .padding(.top, 80)
                                    .frame(maxHeight: .infinity, alignment: .top)
                            }
                            .transition(.opacity)
                        }
                    }
                    .sheet(isPresented: $showFanout) {
                        FanoutSheet(model: model, palette: palette, onClose: { showFanout = false })
                    }
                    #if canImport(WebKit)
                    .sheet(isPresented: Binding(get: { model.designRequested }, set: { model.designRequested = $0 })) {
                        DesignModeView(model: model, palette: palette, initialURL: designURL(model), onClose: { model.designRequested = false })
                    }
                    #endif
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
        // Appearance override is applied at the scene root (OculusMain) so it switches the whole
        // window — sheets + toolbar — atomically. `appearance` here only drives the picker binding.
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
        // Command Deck always renders the split view — the destination rail (Sessions · Loops ·
        // Fleet · Issues · Activity) plus the per-destination list and detail. Empty/connecting
        // states are handled inside the sidebar list and the Sessions detail, so there's no second
        // layout to keep in sync.
        Group {
        // NavigationSplitView on macOS 26 ignores the height proposal and reports its own
        // ideal (~1884pt), which the window host then centers — so the sidebar's content
        // hangs above the viewport and can't scroll. A GeometryReader gives us the real
        // proposed height (the 720pt window), and an explicit .frame() PINS the split view
        // to it, overriding its runaway ideal.
        GeometryReader { proxy in
            NavigationSplitView {
                // Command Deck sidebar: the persistent destination rail on top (Sessions · Loops ·
                // Fleet · Issues · Activity + Needs-You), then the contextual list for the selected
                // destination below it — so every capability is a first-glance destination, nothing
                // is a modal sheet or a "⋯" menu item.
                VStack(spacing: 0) {
                    DestinationRail(destination: $destination, model: model, palette: palette)
                        .padding(.top, 4)
                    Divider().overlay(palette.border)
                    // Sticky search above the session/fleet list (padded after the rail).
                    if destination == .sessions || destination == .fleet {
                        DeckSearchBar(text: $searchText, palette: palette)
                    }
                    deckList(model)
                }
                .background(palette.background)
                .navigationSplitViewColumnWidth(min: 250, ideal: 290, max: 360)
                // The window title is the current PAGE (or the open session's name) — set at the deck
                // level so it's consistent across every destination. The desktop (paired-Mac)
                // switcher hangs off it as the title menu.
                .navigationTitle(pageTitle(model))
                .toolbarTitleMenu { deckDesktopMenu }
                .sheet(isPresented: $showAddDesktop) {
                    AddDesktopView(store: store, palette: palette) { showAddDesktop = false }
                }
                .alert("Rename desktop", isPresented: $renamingDesktop) {
                    TextField("Name", text: $desktopNewName)
                    Button("Save") { if let a = store.active { store.rename(a.id, to: desktopNewName) } }
                    Button("Cancel", role: .cancel) {}
                }
            } detail: {
                deckDetail(model)
                    // Clamp the detail column to the window height (see original note): the split view
                    // sizes to its tallest column's ideal; a flexible detail measured as a runaway
                    // ideal and inflated the sidebar. Pinning decouples them.
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
        // iPhone: the Command Deck collapses to a five-tab bar — the same five destinations, with
        // Activity CENTERED and default, because the phone is a remote triage inbox ("what needs
        // me?"). Each tab is its own NavigationStack (list → push detail). The needs-you count
        // badges the Activity tab.
        TabView(selection: $destination) {
            // Sessions
            NavigationStack {
                SessionSidebar(store: store, model: model, selection: $selection, searchText: $searchText,
                               onOpenLoops: { destination = .loops },
                               onOpenAgents: { panel = .agents },
                               onOpenAccounts: { panel = .accounts },
                               onOpenRemotes: { panel = .remotes },
                               onManageSessions: { panel = .sessions })
                    .navigationDestination(isPresented: $showSessionDetail) { ChatView(model: model) }
                    // Code & change review pushes over the chat when the toolbar button sets the target.
                    .navigationDestination(isPresented: Binding(
                        get: { model.codeReviewTarget != nil },
                        set: { if !$0 { model.codeReviewTarget = nil } })) {
                        CodeSurface(model: model, sessionID: model.codeReviewTarget, reviewSessionID: model.codeReviewTarget)
                    }
                    .toolbar {
                        ToolbarItem(placement: .topBarTrailing) {
                            Button { showPalette = true } label: { Image(systemName: "magnifyingglass") }
                        }
                    }
            }
            .onChange(of: selection) { handleSelection($0, model) }
            .onChange(of: model.currentSession?.id) { if $0 != nil { showSessionDetail = true } }
            .onChange(of: model.startingSession) { if $0 { showSessionDetail = false } }
            .sheet(isPresented: $showNewSession) {
                NewSessionView(model: model, palette: palette, initialTakeOver: newSessionTakeOver) { showNewSession = false }
            }
            .tabItem { Label("Sessions", systemImage: Destination.sessions.symbol) }
            .tag(Destination.sessions)

            // Loops
            NavigationStack {
                LoopsView(model: model, palette: palette,
                          onOpenSession: { sid in openMobile(sid, model) }, onClose: {})
                    .navigationTitle("Loops")
            }
            .tabItem { Label("Loops", systemImage: Destination.loops.symbol) }
            .tag(Destination.loops)

            // Activity (default / centered)
            NavigationStack {
                ActivityView(model: model, palette: palette, onOpen: { sid in openMobile(sid, model) })
                    .navigationTitle("Activity")
                    .navigationDestination(isPresented: $showSessionDetail) { ChatView(model: model) }
            }
            .tabItem { Label("Activity", systemImage: Destination.activity.symbol) }
            .badge(model.needsYouCount)
            .tag(Destination.activity)

            // Fleet
            NavigationStack {
                FleetView(model: model, palette: palette, onOpen: { sid in openMobile(sid, model) }, onClose: {}, onFanout: { showFanout = true })
                    .navigationTitle("Fleet")
                    .navigationDestination(isPresented: $showSessionDetail) { ChatView(model: model) }
            }
            .tabItem { Label("Fleet", systemImage: Destination.fleet.symbol) }
            .tag(Destination.fleet)

            // Issues
            IssuesView(model: model, palette: palette) { destination = .sessions }
                .tabItem { Label("Issues", systemImage: Destination.issues.symbol) }
                .tag(Destination.issues)
        }
        #endif
    }

    #if !os(macOS)
    /// Open a session from Activity/Fleet on iOS: switch to Sessions and push the chat.
    private func openMobile(_ sid: String, _ model: Model) {
        Task { await model.openSession(sid) }
        destination = .sessions
        showSessionDetail = true
    }
    #endif

    /// The initial Design-Mode URL: the active session's dev-server port if a setup hook allocated
    /// one, else a sensible localhost default.
    private func designURL(_ model: Model) -> String {
        if let p = model.currentSession?.port, p > 0 { return "http://localhost:\(p)" }
        return "http://localhost:3000"
    }

    /// Shared session-open used by the palette (both platforms): go to Sessions and open it.
    private func openSessionNav(_ sid: String, _ model: Model) {
        destination = .sessions
        #if os(macOS)
        model.codeReviewTarget = nil
        #endif
        Task { await model.openSession(sid) }
        showSessionDetail = true
    }

    /// Builds the Cmd-K index: destinations, live sessions, loops, agents, and quick actions.
    private func paletteItems(_ model: Model) -> [PaletteItem] {
        var out: [PaletteItem] = []
        // Destinations
        for d in Destination.allCases {
            out.append(PaletteItem(id: "dest-\(d.rawValue)", kind: .destination, title: d.title,
                                   subtitle: "Go to \(d.title)", symbol: d.symbol) { destination = d })
        }
        // Actions
        out.append(PaletteItem(id: "act-new", kind: .action, title: "New session",
                               subtitle: "Start an agent", symbol: "plus.circle") {
            newSessionTakeOver = false; showNewSession = true
        })
        out.append(PaletteItem(id: "act-chat", kind: .action, title: "New chat",
                               subtitle: "Ephemeral — just chat, no project", symbol: "bubble.left.and.text.bubble.right") {
            Task { await model.startEphemeralChat() }
        })
        out.append(PaletteItem(id: "act-newloop", kind: .action, title: "New loop",
                               subtitle: "Automate recurring work", symbol: "arrow.trianglehead.2.clockwise.rotate.90") {
            destination = .loops; selectedLoopID = nil; editingLoop = true
        })
        out.append(PaletteItem(id: "act-fanout", kind: .action, title: "Fan out a task",
                               subtitle: "Race N agents, merge the winner", symbol: "square.grid.2x2") {
            showFanout = true
        })
        out.append(PaletteItem(id: "act-accounts", kind: .action, title: "Accounts & usage",
                               subtitle: "Switch credentials · token/cost meter", symbol: "person.2.badge.key") {
            panel = .accounts
        })
        out.append(PaletteItem(id: "act-remotes", kind: .action, title: "Remote hosts",
                               subtitle: "Run a worktree on a remote box over SSH", symbol: "server.rack") {
            panel = .remotes
        })
        #if canImport(WebKit)
        out.append(PaletteItem(id: "act-design", kind: .action, title: "Design mode",
                               subtitle: "Pick a UI element → HTML/CSS into the prompt", symbol: "cursorarrow.rays") {
            model.designRequested = true
        })
        #endif
        if model.needsYouCount > 0 {
            out.append(PaletteItem(id: "act-markread", kind: .action, title: "Mark all activity read",
                                   subtitle: "\(model.needsYouCount) need you", symbol: "checkmark.circle") {
                Task { await model.markActivityRead() }
            })
        }
        // Sessions
        for s in model.sessions {
            let name = s.name ?? s.title ?? String(s.id.prefix(8))
            out.append(PaletteItem(id: "ses-\(s.id)", kind: .session, title: name,
                                   subtitle: [s.provider, s.branch].compactMap { $0 }.joined(separator: " · "),
                                   symbol: "bubble.left") { openSessionNav(s.id, model) })
        }
        // Loops
        for l in model.loops {
            out.append(PaletteItem(id: "loop-\(l.id)", kind: .loop, title: l.name,
                                   subtitle: l.provider, symbol: "arrow.trianglehead.2.clockwise.rotate.90") {
                destination = .loops; selectedLoopID = l.id; editingLoop = false
            })
        }
        // Agents
        for a in model.agents {
            out.append(PaletteItem(id: "agent-\(a.id)", kind: .agent, title: a.name,
                                   subtitle: a.kind, symbol: "cpu") { panel = .agents })
        }
        return out
    }

    #if os(macOS)
    /// The sidebar's contextual LIST column, per destination. Sessions & Fleet share the session
    /// list (the fleet IS the set of sessions); Loops gets a compact loop list; Issues a slim board
    /// hint (its own project picker lives in the detail board); Activity is list-dominant.
    @ViewBuilder private func deckList(_ model: Model) -> some View {
        switch destination {
        case .sessions, .fleet:
            SessionSidebar(store: store, model: model, selection: $selection, searchText: $searchText,
                           onReview: { sid in model.codeReviewTarget = sid },
                           onTakeOver: { newSessionTakeOver = true; showNewSession = true },
                           onCheckForUpdates: { checkForUpdates = true },
                           loginAtLogin: loginItem.enabled,
                           loginAtLoginError: loginItem.lastError,
                           onToggleLoginAtLogin: { on in Task { await loginItem.setEnabled(on, launcher: launcher) } },
                           updates: updates,
                           onOpenLoops: { destination = .loops },
                           onOpenAgents: { panel = .agents },
                           onOpenAccounts: { panel = .accounts },
                               onOpenRemotes: { panel = .remotes },
                               onManageSessions: { panel = .sessions })

        case .loops:
            LoopsListColumn(model: model, palette: palette, selected: $selectedLoopID,
                            onOpen: { id in selectedLoopID = id; editingLoop = true },
                            onNew: { selectedLoopID = nil; editingLoop = true })
        case .issues:
            DestinationHint(palette: palette, symbol: "checklist", title: "Issues",
                            message: "Your tracker board is in the detail pane — filter by project, drag cards between real statuses, and open in Jira or Linear.")
        case .activity:
            ActivityView(model: model, palette: palette, onOpen: { sid in openFromActivity(sid, model) })

        }
    }

    /// The DETAIL column, per destination. This is where the primary surface for each destination
    /// lives — no modal sheets.
    @ViewBuilder private func deckDetail(_ model: Model) -> some View {
        Group {
            switch destination {
            case .sessions:
                if let codeSession = codeTarget(model) {
                    CodeSurface(model: model, sessionID: codeSession, reviewSessionID: model.codeReviewTarget)
                        .id((codeSession) + (model.codeReviewTarget != nil ? ":review" : ""))
                } else {
                    ChatView(model: model)
                }
            case .fleet:
                FleetView(model: model, palette: palette, onOpen: { sid in openFromActivity(sid, model) }, onClose: {}, onFanout: { showFanout = true })
            case .loops:
                LoopDetail(model: model, palette: palette, loopID: selectedLoopID, editing: editingLoop,
                           onOpenSession: { sid in openFromActivity(sid, model) },
                           onDone: { editingLoop = false })
            case .issues:
                IssuesView(model: model, palette: palette, embedded: true) { destination = .sessions }
            case .activity:
                if model.currentSession != nil {
                    ChatView(model: model)
                } else {
                    DestinationHint(palette: palette, symbol: "waveform.path.ecg", title: "Activity",
                                    message: "Pick an item on the left to jump to that session. Needs-you items are pinned to the top.")
                }
            }
        }
        .id(destination)
        .background(palette.background)
        .toolbarBackground(.visible, for: .windowToolbar)
        .onChange(of: model.currentSession?.id) { sid in
            if destination == .sessions && sid == nil { model.codeReviewTarget = nil }
        }
    }

    /// The window title: the open session's name while chatting in Sessions, else the destination.
    private func pageTitle(_ model: Model) -> String {
        if destination == .sessions, model.codeReviewTarget == nil,
           let s = model.currentSession {
            return s.name ?? s.title ?? s.folderName ?? "Session"
        }
        return destination.title
    }

    /// Desktop (paired-Mac) switcher, shown as the window-title dropdown.
    @ViewBuilder private var deckDesktopMenu: some View {
        ForEach(store.models, id: \.id) { m in
            Button { store.selectedID = m.id } label: {
                Label(m.name.isEmpty ? "Desktop" : m.name,
                      systemImage: m.id == store.selectedID ? "checkmark" : (m.connected ? "circle.fill" : "circle"))
            }
        }
        Divider()
        Button { showAddDesktop = true } label: { Label("Add desktop…", systemImage: "plus") }
        if let a = store.active {
            Button { desktopNewName = a.name; renamingDesktop = true } label: { Label("Rename…", systemImage: "pencil") }
            Button(role: .destructive) { store.remove(a.id) } label: { Label("Remove desktop", systemImage: "trash") }
        }
    }

    /// Code view is a per-session sub-mode of Sessions, reached via the Review action.
    private func codeTarget(_ model: Model) -> String? {
        model.codeReviewTarget
    }

    /// Jump to a session from Activity/Fleet: switch to Sessions and open it.
    private func openFromActivity(_ sid: String, _ model: Model) {
        destination = .sessions
        model.codeReviewTarget = nil
        Task { await model.openSession(sid) }
        showSessionDetail = true
    }
    #endif

    private func hasSession(_ model: Model) -> Bool {
        model.currentSession != nil || model.codeReviewTarget != nil
    }

    private func handleSelection(_ sel: String?, _ model: Model) {
        guard let sel else { return }
        if sel == SessionSidebar.newSessionTag {
            newSessionTakeOver = false // the ✎ / empty-state "New session" opens in Start-new mode
            showNewSession = true
            selection = nil
        } else if model.sessions.contains(where: { $0.id == sel }) {
            Task { await model.openSession(sel) }
            showSessionDetail = true // iOS: push the chat
            selection = nil          // reset so tapping the SAME session again re-triggers
        } else if let d = model.discovered.first(where: { $0.sessionID == sel }) {
            Task { await model.attach(d) }
            showSessionDetail = true
            selection = nil
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
