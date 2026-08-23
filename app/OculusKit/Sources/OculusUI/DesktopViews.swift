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
            Image(systemName: "checkmark.circle.fill").foregroundStyle(palette.success).font(.subheadline)
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
            OculusShape.rounded(14)
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
                    Text(err).font(.caption).foregroundStyle(palette.destructive)
                }
            }

            // Manual fallback (offline, restricted /Applications, or if the in-app update fails).
            DisclosureGroup("Update manually instead") {
                HStack(spacing: 6) {
                    Text(DaemonLauncher.installCommand)
                        .font(.footnote.monospaced()).textSelection(.enabled)
                        .lineLimit(1).truncationMode(.middle)
                        .padding(.horizontal, 8).padding(.vertical, 6)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .background(palette.input, in: OculusShape.rounded(6))
                    Button {
                        #if canImport(AppKit)
                        NSPasteboard.general.clearContents()
                        NSPasteboard.general.setString(DaemonLauncher.installCommand, forType: .string)
                        #endif
                    } label: { Image(systemName: "doc.on.doc") }
                        .buttonStyle(.borderless).help("Copy")
                        // `.help` is only a tooltip/hint — an icon-only button still needs a name.
                        .accessibilityLabel("Copy install command")
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

/// The management panels reachable from the sidebar and the palette.
private enum PanelSheet: Int, Identifiable { case loops, agents, accounts, remotes, sessions, approvalRules, mcp, sharing, dictionary, usage; var id: Int { rawValue } }

/// EVERY modal this surface presents, in ONE `.sheet(item:)` slot.
///
/// There were four `.sheet` modifiers stacked on `mainSurface` plus a fifth inside each of the two
/// surfaces. SwiftUI only reliably drives one presentation per view and the extras don't fail loudly
/// — they just stop presenting, which is exactly how the New Session sheet went dead after the first
/// session once before. One item-valued slot makes "what is on screen" a single value that can only
/// hold one answer.
private enum DeckSheet: Identifiable {
    case newSession
    case fanoutCompose
    case fanoutCompare(FanoutSummary)
    case design
    case panel(PanelSheet)
    /// Pair another Mac. In the shared slot rather than its own `.sheet`, because a second sheet
    /// modifier on this view is what silently killed a presentation once before.
    case addDesktop

    var id: String {
        switch self {
        case .newSession:            return "new-session"
        case .fanoutCompose:         return "fanout-compose"
        case .fanoutCompare(let s):  return "fanout-compare-\(s.id)"
        case .design:                return "design"
        case .panel(let p):          return "panel-\(p.rawValue)"
        case .addDesktop:            return "add-desktop"
        }
    }
}

/// What the Sessions navigation stack can push, on the compact (phone / Slide Over) layout.
private enum SessionRoute: Hashable {
    case chat
    case code(String) // the Code & change-review surface for a session id

    var isCode: Bool { if case .code = self { return true }; return false }
}

#if os(macOS)
/// The detail column's toolbar backing.
///
/// macOS 26 draws the window toolbar itself — Liquid Glass plus the scroll edge effect that
/// separates the toolbar from content as it scrolls under. Forcing `.toolbarBackground(.visible)`
/// opts out of both and leaves a flat opaque bar, so only supply a background on systems that don't
/// provide one.
private struct DetailToolbarBackground: ViewModifier {
    func body(content: Content) -> some View {
        if #available(macOS 26.0, *) {
            content
        } else {
            content.toolbarBackground(.visible, for: .windowToolbar)
        }
    }
}
#endif

public struct RootView: View {
    @ObservedObject var store: DesktopStore
    @Environment(\.colorScheme) private var scheme
    private var palette: OculusPalette { .current(scheme) }
    @State private var selection: String?
    #if os(iOS)
    @Environment(\.horizontalSizeClass) private var hSize
    #endif
    /// The Sessions stack's push state, and the ONLY navigation path in the app.
    ///
    /// This was a `showSessionDetail` Bool bound as `navigationDestination(isPresented:)` in the
    /// Sessions, Activity AND Fleet stacks at once. One Bool cannot own three stacks: opening a
    /// session from Fleet pushed the chat onto all three, a back-swipe in one silently popped the
    /// other two, and starting a session popped every stack. Activity and Fleet are jump-off points
    /// now — they switch the tab and push here.
    @State private var sessionsPath: [SessionRoute] = []
    @State private var checkForUpdates = false   // macOS: Settings → "Check for updates" trigger
    // Every modal goes through this one slot — see DeckSheet.
    @State private var sheet: DeckSheet?
    /// True when the Sessions destination should show the INDEX rather than the open conversation.
    /// An explicit flag rather than inferring it from "no session is open": inferring meant the only
    /// way to reach the table was to destroy your place in the chat first.
    @State private var showAllSessions = false
    @State private var newSessionTakeOver = false
    // Command Deck: the active top-level destination. macOS = nav-rail selection; iOS = bottom tab.
    // iOS defaults to Activity (index-of .activity) — the phone is a triage inbox; macOS opens on Sessions.
    // `@SceneStorage`, not `@State`: a cold launch used to drop you back on the default tab no matter
    // where you had been working. The system persists this per scene and restores it, which is what
    // makes returning to the app feel like resuming rather than restarting. `Destination` is
    // Int-backed, so it round-trips without a bridge.
    #if os(macOS)
    @SceneStorage("oculus.destination") private var destination: Destination = .sessions
    #else
    @SceneStorage("oculus.destination") private var destination: Destination = .activity
    #endif
    @State private var searchText = ""
    @State private var selectedLoopID: String?     // Loops destination: which loop the detail edits (nil = new/templates)
    @State private var editingLoop = false          // Loops destination: detail shows the editor
    @State private var showPalette = false          // Cmd-K command palette
    /// Sidebar column visibility, so the toggle is a value the deck owns rather than state buried in
    /// AppKit that nothing can read or restore.
    @State private var columnVisibility: NavigationSplitViewVisibility = .automatic
    // Desktop (paired-Mac) switcher — hangs off the window title, Xcode-scheme-menu style.
    @State private var renamingDesktop = false
    @State private var desktopNewName = ""
    @AppStorage("oculus.appearance") private var appearance: Appearance = .system
    #if os(macOS)
    // Shared instances, not view-owned: the Settings window needs the SAME launcher, because
    // `managed` (did this app start the daemon child?) is what makes the login-item handoff correct.
    // See the note on `DaemonLauncher.shared`.
    @ObservedObject private var launcher = DaemonLauncher.shared
    @ObservedObject private var loginItem = LoginItemManager.shared
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
                    // Renaming a paired Mac belongs to BOTH platforms, so it lives at the root rather
                    // than on the macOS detail column where the title menu happens to sit. A desktop
                    // is named from whatever hostname it advertised, which on a second machine is
                    // often indistinguishable from the first — being able to call one "Studio" is
                    // what makes a multi-Mac setup legible.
                    .alert("Rename desktop", isPresented: $renamingDesktop) {
                        TextField("Name", text: $desktopNewName)
                        Button("Save") { if let a = store.active { store.rename(a.id, to: desktopNewName) } }
                        Button("Cancel", role: .cancel) {}
                    }
                    #if os(macOS)
                    .modifier(SoftwareUpdateModifier(palette: palette, forceCheck: $checkForUpdates, updates: updates))
                    #endif
                    // ONE sheet slot for everything — see DeckSheet. `onDismiss` clears the two
                    // model-owned REQUESTS that can open a sheet, so a swipe-to-dismiss doesn't leave
                    // a stale flag set that immediately re-presents (or blocks the next request).
                    .sheet(item: $sheet, onDismiss: {
                        model.fanoutSummary = nil
                        model.designRequested = false
                    }) { which in
                        sheetContent(which, model)
                    }
                    // The daemon can push a fan-out summary, and the chat toolbar can request Design
                    // mode, at any time — mirror those requests into the one slot.
                    .onChange(of: model.fanoutSummary?.id) { _ in
                        if let sum = model.fanoutSummary { sheet = .fanoutCompare(sum) }
                    }
                    .onChange(of: model.designRequested) { on in
                        if on { sheet = .design }
                    }
                    // macOS only. On iOS this inset sat on top of the tab bar and swallowed taps
                    // along the bottom edge — and a live-tailing log is a desk affordance anyway, not
                    // something worth a permanent strip of a phone screen.
                    #if os(macOS)
                    .safeAreaInset(edge: .bottom, spacing: 0) {
                        DaemonLogPanel(model: model, palette: palette, onOpenActivity: { destination = .activity })
                    }
                    #endif
                    // Cmd-K command palette: one fuzzy entry across destinations, sessions, loops,
                    // agents, and actions. ⌘K on macOS; a search button in the sidebar on iOS.
                    // The app has no `.commands { }` scene (that lives in OculusMain), so these
                    // invisible key equivalents are the only keyboard route to the deck's primary
                    // actions — see the note in the design report about which still need menu items.
                    .background(deckShortcuts.opacity(0))
                    .overlay {
                        if showPalette {
                            ZStack {
                                Color.black.opacity(0.35).ignoresSafeArea()
                                    .onTapGesture { showPalette = false }
                                    // A bare tap gesture is invisible to VoiceOver and Full Keyboard
                                    // Access — the scrim is the dismiss control, so name it as one.
                                    .accessibilityAddTraits(.isButton)
                                    .accessibilityLabel("Close command palette")
                                CommandPalette(model: model, palette: palette,
                                               items: paletteItems(model), onClose: { showPalette = false })
                                    .padding(.top, 80)
                                    .frame(maxHeight: .infinity, alignment: .top)
                            }
                            .transition(.opacity)
                        }
                    }
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

    /// Keyboard equivalents for the deck's own actions: ⌘K palette, ⌘N new session, and ⌘1…⌘5 for the
    /// five destinations (the order the rail shows them in). Hidden from VoiceOver — they duplicate
    /// controls that are already reachable, they are not five more things to swipe past.
    @ViewBuilder private var deckShortcuts: some View {
        VStack(spacing: 0) {
            Button("") { showPalette = true }.keyboardShortcut("k", modifiers: .command)
            #if os(macOS)
            Button("") { newSessionTakeOver = false; sheet = .newSession }
                .keyboardShortcut("n", modifiers: .command)
            ForEach(Array(Destination.allCases.enumerated()), id: \.element.id) { idx, d in
                Button("") { destination = d }
                    .keyboardShortcut(KeyEquivalent(Character("\(idx + 1)")), modifiers: .command)
            }
            #endif
        }
        .accessibilityHidden(true)
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
    /// Whether to show the full management layout (destination rail + list + detail) rather than the
    /// phone's tab layout.
    ///
    /// This is the iPad question. An iPad in full width has the room for the same rail-and-columns
    /// surface the Mac uses, and treating it as a big phone wasted that — you'd get a triage inbox on
    /// a 13" screen. So the choice is by AVAILABLE WIDTH, not by OS: an iPad in Slide Over is genuinely
    /// narrow and falls back to tabs, and an iPhone never gets the split layout regardless of
    /// orientation (some Max models report a regular width in landscape, which is not the same thing
    /// as having room for three columns).
    private var usesSplitLayout: Bool {
        #if os(macOS)
        return true
        #else
        return idiomIsPad && hSize == .regular
        #endif
    }

    #if os(iOS)
    private var idiomIsPad: Bool { UIDevice.current.userInterfaceIdiom == .pad }
    #endif

    @ViewBuilder private func mainSurface(_ model: Model) -> some View {
        if usesSplitLayout {
            splitSurface(model)
        } else {
            tabSurface(model)
        }
    }

    /// The rail + list + detail layout: macOS, and iPad at full width.
    @ViewBuilder private func splitSurface(_ model: Model) -> some View {
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
            // `columnVisibility` is bound so the sidebar toggle is state the deck owns and can drive
            // (nothing could read or set it before).
            NavigationSplitView(columnVisibility: $columnVisibility) {
                // Command Deck sidebar: the persistent destination rail on top (Sessions · Loops ·
                // Fleet · Issues · Activity + Needs-You), then the contextual list for the selected
                // destination below it — so every capability is a first-glance destination, nothing
                // is a modal sheet or a "⋯" menu item.
                VStack(spacing: 0) {
                    DestinationRail(destination: $destination,
                                    // Show the index. Deliberately does NOT close the open
                                    // conversation: an explicit flag means the table appears because
                                    // it was asked for, not as a side effect of clearing state — and
                                    // your place in the chat is still there when you click back.
                                    onShowAllSessions: { showAllSessions = true },
                                    model: model, palette: palette)
                        .padding(.top, 4)
                    Divider().overlay(palette.border)
                    // Sticky search above the session/fleet list (padded after the rail).
                    if destination == .sessions || destination == .fleet {
                        DeckSearchBar(text: $searchText, palette: palette)
                    }
                    deckList(model)
                        // Every destination's column has to be transparent, not just Sessions'.
                        // SessionSidebar hides its own list background; LoopsListColumn, ActivityView
                        // and the Issues hint did not, so switching to those destinations painted an
                        // opaque List background over the sidebar material and the lower half of the
                        // column went flat white. This is environment-propagated, so it reaches
                        // whichever scroll view the destination happens to render.
                        .scrollContentBackground(.hidden)
                        .frame(maxWidth: .infinity, maxHeight: .infinity)
                }
                // NOT an opaque fill. On macOS 26 the system floats this column on its own glass
                // material and SidebarMaterial deliberately leaves it alone; painting a solid colour
                // over it threw that away and, below 26, made the NSVisualEffectView's behind-window
                // blend vibrate against our own layer instead of the desktop.
                .sidebarMaterial()
                .navigationSplitViewColumnWidth(min: 250, ideal: 290, max: 360)
            } detail: {
                deckDetail(model)
                    // Clamp the detail column to the window height (see original note): the split view
                    // sizes to its tallest column's ideal; a flexible detail measured as a runaway
                    // ideal and inflated the sidebar. Pinning decouples them.
                    //
                    // macOS ONLY. `proxy` measures the WHOLE split view, but on iPadOS the detail
                    // column reserves a navigation bar at its top, so its real height is
                    // proxy.size.height MINUS that bar. Handing the content the full height made it
                    // taller than the space it was given, and an oversized child is CENTERED — so the
                    // content slid up under the bar by half the bar's height and the system's large
                    // `.navigationTitle` printed straight over each destination's own header row
                    // ("Fleet" on "Agent fleet", the session name on "All sessions"). The Mac has no
                    // such inset, which is why only the Designed-for-iPad build showed it.
                    #if os(macOS)
                    .frame(height: proxy.size.height)
                    #endif
                    // The window title belongs to the DETAIL, which is what macOS titles a window
                    // after. It used to hang off the sidebar column, which is why the sidebar had to
                    // know which session the detail had open.
                    .navigationTitle(pageTitle(model))
                    .toolbarTitleMenu { deckDesktopMenu }
            }
            .frame(width: proxy.size.width, height: proxy.size.height)
            .onChange(of: selection) { handleSelection($0, model) }
        }
        }
    }

    /// The compact layout: the Command Deck collapsed to a five-tab bar — the same five
    /// destinations, with Activity CENTERED and default, because a phone is a remote triage inbox
    /// ("what needs me?"). Each tab is its own NavigationStack (list → push detail), and the
    /// needs-you count badges the Activity tab. Also used by an iPad in Slide Over, which is just as
    /// narrow as a phone.
    @ViewBuilder private func tabSurface(_ model: Model) -> some View {
        // Built by iterating `Destination.mobileOrder` rather than by hand: the "Activity is centered"
        // intent lives in one array instead of in the order five copy-pasted blocks happen to sit in.
        TabView(selection: $destination) {
            ForEach(Destination.mobileOrder) { d in
                tab(d, model)
                    .tabItem { Label(d.title, systemImage: d.symbol) }
                    .tag(d)
            }
        }
    }

    @ViewBuilder private func tab(_ d: Destination, _ model: Model) -> some View {
        switch d {
        case .sessions:
            // The ONLY navigation stack with a path. Both the chat and the code surface are routes on
            // it, so a back-swipe pops exactly one thing and nothing else in the app moves.
            NavigationStack(path: $sessionsPath) {
                sessionSidebar(model)
                    .navigationDestination(for: SessionRoute.self) { route in
                        switch route {
                        case .chat: ChatView(model: model)
                        case .code(let sid): CodeSurface(model: model, sessionID: sid, reviewSessionID: sid)
                        }
                    }
                    .toolbar {
                        ToolbarItem(placement: .automatic) {
                            Button { showPalette = true } label: { Image(systemName: "magnifyingglass") }
                                .accessibilityLabel("Search")
                        }
                    }
            }
            .onChange(of: selection) { handleSelection($0, model) }
            .onChange(of: model.currentSession?.id) { if $0 != nil { pushChat() } }
            // Starting a session pops back to the list so the create skeleton — not a half-built
            // conversation — is what you watch.
            .onChange(of: model.startingSession) { if $0 { sessionsPath.removeAll() } }
            // The Review action sets `codeReviewTarget` from wherever you are; mirror it onto the path
            // in both directions so a back-swipe clears the target instead of stranding it set.
            .onChange(of: model.codeReviewTarget) { target in
                if let target {
                    if !sessionsPath.contains(.code(target)) { sessionsPath.append(.code(target)) }
                } else {
                    sessionsPath.removeAll(where: \.isCode)
                }
            }
            .onChange(of: sessionsPath) { path in
                if model.codeReviewTarget != nil, !path.contains(where: \.isCode) { model.codeReviewTarget = nil }
            }

        case .loops:
            NavigationStack {
                LoopsView(model: model, palette: palette,
                          onOpenSession: { sid in openSessionNav(sid, model) }, onClose: {})
                    .navigationTitle("Loops")
            }

        case .activity:
            // A jump-off point, not an owner of the chat: opening an item switches to Sessions and
            // pushes there. (It used to bind the same push flag as Sessions and Fleet.)
            NavigationStack {
                ActivityView(model: model, palette: palette, onOpen: { sid in openSessionNav(sid, model) })
                    .navigationTitle("Activity")
            }
            .badge(model.needsYouCount)

        case .fleet:
            NavigationStack {
                FleetView(model: model, palette: palette, onOpen: { sid in openSessionNav(sid, model) },
                          onClose: {}, onFanout: { sheet = .fanoutCompose })
                    .navigationTitle("Fleet")
            }

        case .issues:
            IssuesView(model: model, palette: palette) { destination = .sessions }
        }
    }

    /// The initial Design-Mode URL: the active session's dev-server port if a setup hook allocated
    /// one, else a sensible localhost default.
    private func designURL(_ model: Model) -> String {
        if let p = model.currentSession?.port, p > 0 { return "http://localhost:\(p)" }
        return "http://localhost:3000"
    }

    /// Show the chat as the Sessions stack's one pushed screen. An ASSIGNMENT, not an append: opening
    /// a session from anywhere means "this conversation is where I am", so a stale code surface (or a
    /// second chat) can't accumulate behind it and hand you a back button that goes somewhere odd.
    /// No-op on the split layout, where the detail column IS the chat and there is nothing to push.
    private func pushChat() {
        if sessionsPath != [.chat] { sessionsPath = [.chat] }
    }

    /// The ONE way to open a session from anywhere — palette, Activity, Fleet, Loops, a sheet. Every
    /// caller funnels here so the push has a single owner and can't be duplicated per surface.
    private func openSessionNav(_ sid: String, _ model: Model) {
        destination = .sessions
        model.codeReviewTarget = nil // a new session must not keep showing the old one's diff
        Task { await model.openSession(sid) }
        pushChat()
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
            newSessionTakeOver = false; sheet = .newSession
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
            sheet = .fanoutCompose
        })
        out.append(PaletteItem(id: "act-accounts", kind: .action, title: "Accounts & usage",
                               subtitle: "Switch credentials · token/cost meter", symbol: "person.2.badge.key") {
            sheet = .panel(.accounts)
        })
        out.append(PaletteItem(id: "act-remotes", kind: .action, title: "Remote hosts",
                               subtitle: "Run a worktree on a remote box over SSH", symbol: "server.rack") {
            sheet = .panel(.remotes)
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
                                   subtitle: a.kind, symbol: "cpu") { sheet = .panel(.agents) })
        }
        return out
    }

    /// The sidebar's contextual LIST column, per destination. Sessions & Fleet share the session
    /// list (the fleet IS the set of sessions); Loops gets a compact loop list; Issues a slim board
    /// hint (its own project picker lives in the detail board); Activity is list-dominant.

    /// The sessions/fleet sidebar. Split by platform only because launch-at-login and the
    /// self-update card are macOS concepts — iOS updates through the App Store and has no login
    /// item — not because the list itself differs.
    @ViewBuilder private func sessionSidebar(_ model: Model) -> some View {
        #if os(macOS)
        SessionSidebar(store: store, model: model, selection: $selection, searchText: $searchText,
                       onReview: { sid in model.codeReviewTarget = sid },
                       onTakeOver: { newSessionTakeOver = true; sheet = .newSession },
                       onCheckForUpdates: { checkForUpdates = true },
                       loginAtLogin: loginItem.enabled,
                       loginAtLoginError: loginItem.lastError,
                       onToggleLoginAtLogin: { on in Task { await loginItem.setEnabled(on, launcher: launcher) } },
                       updates: updates,
                       onOpenLoops: { destination = .loops },
                       onOpenAgents: { sheet = .panel(.agents) },
                       onOpenApprovalRules: { sheet = .panel(.approvalRules) },
                       onOpenMCP: { sheet = .panel(.mcp) },
                       onOpenSharing: { sheet = .panel(.sharing) },
                       onOpenDictionary: { sheet = .panel(.dictionary) },
                       onOpenUsage: { sheet = .panel(.usage) },
                       onOpenAccounts: { sheet = .panel(.accounts) },
                       onOpenRemotes: { sheet = .panel(.remotes) },
                       onManageSessions: { sheet = .panel(.sessions) })
        #else
        SessionSidebar(store: store, model: model, selection: $selection, searchText: $searchText,
                       onReview: { sid in model.codeReviewTarget = sid },
                       onTakeOver: { newSessionTakeOver = true; sheet = .newSession },
                       onOpenLoops: { destination = .loops },
                       onOpenAgents: { sheet = .panel(.agents) },
                       onOpenApprovalRules: { sheet = .panel(.approvalRules) },
                       onOpenMCP: { sheet = .panel(.mcp) },
                       onOpenSharing: { sheet = .panel(.sharing) },
                       onOpenDictionary: { sheet = .panel(.dictionary) },
                       onOpenUsage: { sheet = .panel(.usage) },
                       onOpenAccounts: { sheet = .panel(.accounts) },
                       onOpenRemotes: { sheet = .panel(.remotes) },
                       onManageSessions: { sheet = .panel(.sessions) },
                       // The same menu macOS puts in the window title. On iOS it is the only way to
                       // reach a second paired Mac, or to rename one so it reads as "Studio" rather
                       // than whatever hostname it happened to advertise.
                       desktopMenu: AnyView(deckDesktopMenu))
        #endif
    }

    @ViewBuilder private func deckList(_ model: Model) -> some View {
        switch destination {
        case .sessions, .fleet:
            sessionSidebar(model)

        case .loops:
            LoopsListColumn(model: model, palette: palette, selected: $selectedLoopID,
                            onOpen: { id in selectedLoopID = id; editingLoop = true },
                            onNew: { selectedLoopID = nil; editingLoop = true })
        case .issues:
            DestinationHint(palette: palette, symbol: "checklist", title: "Issues",
                            message: "Your tracker board is in the detail pane — filter by project, drag cards between real statuses, and open in Jira or Linear.")
        case .activity:
            ActivityView(model: model, palette: palette, onOpen: { sid in openSessionNav(sid, model) })

        }
    }

    /// The DETAIL column, per destination. This is where the primary surface for each destination
    /// lives — no modal sheets.
    @ViewBuilder private func deckDetail(_ model: Model) -> some View {
        Group {
            switch destination {
            case .sessions:
                if showAllSessions {
                    AllSessionsView(model: model, palette: palette, onClose: { showAllSessions = false },
                                    onOpen: { sid in
                                        showAllSessions = false
                                        openSessionNav(sid, model)
                                    },
                                    embedded: true)
                } else if let codeSession = codeTarget(model) {
                    CodeSurface(model: model, sessionID: codeSession, reviewSessionID: model.codeReviewTarget)
                        .id((codeSession) + (model.codeReviewTarget != nil ? ":review" : ""))
                } else if model.sessionID == nil {
                    // Nothing open: the index is a better landing than an empty chat pane.
                    AllSessionsView(model: model, palette: palette, onClose: {},
                                    onOpen: { sid in openSessionNav(sid, model) },
                                    embedded: true)
                } else {
                    ChatView(model: model)
                }
            case .fleet:
                FleetView(model: model, palette: palette, onOpen: { sid in openSessionNav(sid, model) }, onClose: {}, onFanout: { sheet = .fanoutCompose })
            case .loops:
                LoopDetail(model: model, palette: palette, loopID: selectedLoopID, editing: editingLoop,
                           onOpenSession: { sid in openSessionNav(sid, model) },
                           onDone: { editingLoop = false })
            case .issues:
                IssuesView(model: model, palette: palette, embedded: true) { destination = .sessions }
            case .activity:
                if model.currentSession != nil {
                    ChatView(model: model)
                } else {
                    // Not a hint. The feed on the left is history; this pane is the present tense —
                    // how many agents are working right now and which ones. See ActivityOverview.
                    ActivityOverview(model: model, palette: palette, onOpen: { sid in openSessionNav(sid, model) })
                }
            }
        }
        // Keyed on the CONTENT, not the destination. Sessions and Activity both render ChatView for
        // the same session, so `.id(destination)` tore the transcript down and rebuilt it every time
        // you switched between them — scroll position, the initial anchor deadline and the settling
        // machinery all reset, and you watched the loading skeleton for a conversation you were
        // reading a second ago.
        .id(detailIdentity(model))
        .background(palette.background)
        // .windowToolbar is macOS-only; the detail pane now compiles for iPad too.
        #if os(macOS)
        .modifier(DetailToolbarBackground())
        #endif
        .onChange(of: model.currentSession?.id) { _ in
            // Switching sessions (id changed) must drop any open Code/Review detail — otherwise the
            // pane keeps rendering the PREVIOUS session's code surface, which reads as a stale/white
            // screen when going between sessions. (Was only cleared when the session went to nil.)
            if destination == .sessions { model.codeReviewTarget = nil }
        }
    }

    /// The detail column's identity: what would have to be REBUILT if it changed. The two
    /// chat-showing destinations share one identity so switching between them keeps the live view.
    private func detailIdentity(_ model: Model) -> String {
        switch destination {
        case .sessions, .activity:
            if let code = model.codeReviewTarget { return "code-\(code)" }
            return "chat-\(model.sessionID ?? "none")"
        default:
            return "dest-\(destination.rawValue)"
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
        Button { sheet = .addDesktop } label: { Label("Add desktop…", systemImage: "plus") }
        if let a = store.active {
            Button { desktopNewName = a.name; renamingDesktop = true } label: { Label("Rename…", systemImage: "pencil") }
            Button(role: .destructive) { store.remove(a.id) } label: { Label("Remove desktop", systemImage: "trash") }
        }
    }

    /// Code view is a per-session sub-mode of Sessions, reached via the Review action.
    private func codeTarget(_ model: Model) -> String? {
        model.codeReviewTarget
    }

    private func hasSession(_ model: Model) -> Bool {
        model.currentSession != nil || model.codeReviewTarget != nil
    }

    private func handleSelection(_ sel: String?, _ model: Model) {
        guard let sel else { return }
        showAllSessions = false // picking a session means you want that session, not the index
        if sel == SessionSidebar.newSessionTag {
            newSessionTakeOver = false // the ✎ / empty-state "New session" opens in Start-new mode
            sheet = .newSession
            selection = nil
        } else if model.sessions.contains(where: { $0.id == sel }) {
            openSessionNav(sel, model)
            selection = nil          // reset so tapping the SAME session again re-triggers
        } else if let d = model.discovered.first(where: { $0.sessionID == sel }) {
            Task { await model.attach(d) }
            pushChat()
            selection = nil
        }
    }

    /// The one sheet slot's content. Every close handler nils the SAME state — there is no second
    /// presentation to leave dangling.
    @ViewBuilder private func sheetContent(_ which: DeckSheet, _ model: Model) -> some View {
        switch which {
        case .newSession:
            NewSessionView(model: model, palette: palette, initialTakeOver: newSessionTakeOver) { sheet = nil }
        case .fanoutCompose:
            FanoutSheet(model: model, palette: palette, onClose: { sheet = nil })
        case .fanoutCompare(let sum):
            FanoutCompareView(model: model, summary: sum, palette: palette,
                              onOpenSession: { sid in
                                  sheet = nil
                                  openSessionNav(sid, model)
                              },
                              onClose: { sheet = nil })
        case .design:
            #if canImport(WebKit)
            DesignModeView(model: model, palette: palette, initialURL: designURL(model), onClose: { sheet = nil })
            #else
            EmptyView()
            #endif
        case .panel(let which):
            panelContent(which, model)
        case .addDesktop:
            AddDesktopView(store: store, palette: palette) { sheet = nil }
        }
    }

    @ViewBuilder private func panelContent(_ which: PanelSheet, _ model: Model) -> some View {
        switch which {
        case .loops:
            LoopsView(model: model, palette: palette,
                      onOpenSession: { sid in
                          sheet = nil
                          openSessionNav(sid, model)
                      },
                      onClose: { sheet = nil })
        case .agents:
            ManageAgentsView(model: model, palette: palette)
        case .approvalRules:
            ApprovalRulesView(model: model, palette: palette, onClose: { sheet = nil })
        case .mcp:
            MCPServersView(model: model, palette: palette, onClose: { sheet = nil })
        case .sharing:
            SharingView(model: model, palette: palette, onClose: { sheet = nil })
        case .dictionary:
            DictionaryView(palette: palette, onClose: { sheet = nil })
        case .usage:
            UsageView(model: model, palette: palette, onClose: { sheet = nil })
        case .accounts:
            AccountsView(model: model, palette: palette, onClose: { sheet = nil })
        case .remotes:
            RemotesView(model: model, palette: palette, onClose: { sheet = nil })
        case .sessions:
            AllSessionsView(model: model, palette: palette, onClose: { sheet = nil },
                            onOpen: { sid in
                                sheet = nil
                                openSessionNav(sid, model)
                            })
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
    /// An optional override for the name the pairing link advertises.
    @State private var customName = ""
    #if os(iOS)
    @State private var showScanner = false
    #endif

    /// The name the link itself carries, offered as the placeholder so the field shows what you
    /// would get by leaving it blank rather than making you guess.
    private var suggestedName: String { PairingPayload(pasteURL)?.name ?? "" }

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

                // Name it here, while you know which machine you are holding.
                //
                // The name otherwise comes from whatever hostname the daemon advertised, and two
                // Macs set up the same way advertise the same thing — so the list ends up
                // distinguishable only by the address in the pairing link. Naming it at the moment
                // you pair is the one time you unambiguously know which one this is.
                TextField(suggestedName.isEmpty ? "Name (optional)" : "Name — e.g. \(suggestedName)",
                          text: $customName)
                    .textFieldStyle(.roundedBorder)
                    #if os(iOS)
                    .autocorrectionDisabled()
                    #endif

                Button("Add desktop") {
                    // Stay open when the pairing is staged for confirmation — dismissing here would
                    // tear down the alert's host before the user ever sees the question.
                    guard var p = PairingPayload(pasteURL) else { return }
                    let chosen = customName.trimmingCharacters(in: .whitespacesAndNewlines)
                    if !chosen.isEmpty { p.name = chosen }
                    if store.add(p) != nil { onClose() }
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
                    if let p = PairingPayload(payload), store.add(p) != nil { onClose() }
                }
                .ignoresSafeArea()
            }
            #endif
            // A scanned code claiming to be a Mac we already know, under a different key, stops here.
            .keyChangeAlert(
                store.pendingKeyChange.map {
                    KeyChangeAlertContent(machine: $0.existingName,
                                          currentFingerprint: $0.existingFingerprint,
                                          newFingerprint: $0.newFingerprint,
                                          matchedOn: $0.matchedOn)
                },
                onReplace: { store.confirmKeyChange(); onClose() },
                onKeep: { store.cancelKeyChange() }
            )
        }
    }
}

/// First-run screen when no desktops are paired yet.
struct DesktopOnboardView: View {
    @ObservedObject var store: DesktopStore
    let palette: OculusPalette
    @State private var showAdd = false

    var body: some View {
        // Left-aligned and bold, per the OS 26 onboarding treatment — and because this is the entire
        // explanation of what the app is. It used to be one centred `.subheadline` line in
        // `mutedForeground`, i.e. the app introduced itself in its de-emphasised colour, and never
        // mentioned the two things a new user actually needs to know: the daemon has to already be
        // running on the Mac, and the QR code lives in that Mac's ⋯ menu.
        VStack(alignment: .leading, spacing: 20) {
            Spacer()
            VStack(alignment: .leading, spacing: 14) {
                Image("WolfMark").resizable().scaledToFit().frame(width: 64, height: 64)
                IronRainWordmark(size: 28)
                Text("Pair your Mac")
                    .font(.largeTitle.bold())
                    .foregroundStyle(palette.foreground)
                Text("Iron Rain drives coding agents running on your Mac. Open Iron Rain there, then choose **Pair a phone…** from the ⋯ menu to show a QR code.")
                    .font(.body)
                    .foregroundStyle(palette.foreground)
                    .fixedSize(horizontal: false, vertical: true)
            }
            Button { showAdd = true } label: {
                Label("Add a desktop", systemImage: "plus.circle")
                    .frame(maxWidth: .infinity)
                    // `.borderedProminent` derives its label colour from the tint, and it picks a dark
                    // one against this gold — which the darkened light-mode gold made worse, not
                    // better. State the pairing explicitly so it tracks the palette.
                    .foregroundStyle(palette.primaryForeground)
            }
            .buttonStyle(.borderedProminent).tint(palette.primary)
            .controlSize(.large)
            Spacer()
        }
        .padding(.horizontal, 28)
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
                    .foregroundStyle(launcher.running ? palette.success : palette.warning)
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
        .background(palette.secondary.opacity(0.5), in: OculusShape.rounded(10))
        .overlay(OculusShape.rounded(10).strokeBorder(palette.border))
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
                Text(cmd).font(.footnote.monospaced()).textSelection(.enabled)
                    .lineLimit(1).truncationMode(.middle)
                    .padding(.horizontal, 8).padding(.vertical, 5)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .background(palette.input, in: OculusShape.rounded(6))
                Button { copyCommand(cmd) } label: { Image(systemName: "doc.on.doc") }
                    .buttonStyle(.borderless).help("Copy")
                    .accessibilityLabel("Copy command")
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
