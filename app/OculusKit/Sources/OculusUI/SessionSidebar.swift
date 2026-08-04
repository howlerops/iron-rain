import SwiftUI
import OculusKit
#if os(macOS)
// For the pre-Sonoma route into the Settings scene (`NSApp.sendAction`). SwiftUI does not
// re-export AppKit, so without this the fallback below fails to compile on macOS 13.
import AppKit
#endif

/// A normalized session for the sidebar list, unifying hub-managed sessions and
/// discovered-on-host sessions into one row model.
private struct SidebarSession: Identifiable {
    let id: String
    let title: String
    let provider: String
    let projectName: String // the natural (project) group this session belongs to
    let branch: String?
    let isRunning: Bool
    let stopped: Bool // persisted but not live after a daemon restart (restartable)
    let viewOnly: Bool
    /// True when this session is owned by our daemon (started from the app) — it can be
    /// stopped/managed. False for sessions discovered from a terminal (view-only lifecycle).
    let managed: Bool
    let updatedAt: Date?
    var isChild: Bool = false // delegated sub-agent (shown with a ↳ marker)
    var hasError: Bool = false // a background session whose sends stopped landing (no-response/error)
    var conflicted: Bool = false // worktree branch would conflict with the default branch
    /// The remote host this session's agent runs on; nil/empty means this Mac. Read from the
    /// session's own execution fields, never from its name — the name is the user's to change.
    var execHost: String? = nil
}

private struct SessionGroup: Identifiable {
    let name: String
    let items: [SidebarSession]
    let showProvider: Bool // only when a group actually mixes providers
    let showProject: Bool  // the "Recent" group spans projects, so show each row's project
    let hasRunning: Bool
    let runningCount: Int   // rolled-up status: agents running in this project
    let needsYouCount: Int  // rolled-up status: agents in this project needing attention
    var id: String { name }
}

/// Sidebar list filter — All, only running, only daemon-managed, or only view-only
/// (terminal-owned). Lets you hide the view-only clutter or focus on live work.
private enum SessionFilter: String, CaseIterable, Identifiable {
    case all, running, managed, viewOnly
    var id: String { rawValue }
    /// Kept short — four chips have to fit inside a ~260pt sidebar, not a menu.
    var label: String {
        switch self {
        case .all: return "All"
        case .running: return "Running"
        case .managed: return "Managed"
        case .viewOnly: return "Terminal"
        }
    }
    func matches(_ s: SidebarSession) -> Bool {
        switch self {
        case .all: return true
        case .running: return s.isRunning
        case .managed: return s.managed
        case .viewOnly: return !s.managed
        }
    }
}

/// The session sidebar: a device switcher + status, a Sessions/Issues switch, and the
/// live agent sessions grouped by project. One accent (gold) is used only for state —
/// selection, the running indicator, and primary actions — never for decoration.
struct SessionSidebar: View {
    @ObservedObject var store: DesktopStore
    @ObservedObject var model: Model
    @Binding var selection: String?
    @Environment(\.colorScheme) private var scheme
    private var palette: OculusPalette { .current(scheme) }
    @State private var showPairingQR = false
    @State private var showAddDesktop = false
    @State private var renamingDesktop = false
    @State private var desktopNewName = ""
    /// Driven by `.searchable` on the NavigationSplitView (RootView), per Apple's guidance;
    /// it filters `filteredGroups` here.
    @Binding var searchText: String
    /// Opens the Code detail in review mode for a session's changes (macOS).
    var onReview: ((String) -> Void)? = nil
    /// Opens the New Session sheet straight into "Take over" mode (empty-state action).
    var onTakeOver: (() -> Void)? = nil
    // The next four are macOS-only and no longer reached from this view: "Check for updates" and
    // "Start daemon at login" are the Settings window's General/Startup sections now (see
    // SettingsScene.swift), which is the single place that owns them. They stay on the initializer
    // because DesktopViews.swift constructs this view with them; drop them there first.
    /// macOS: Settings → "Check for updates". The banner (RootView-level) owns the actual check.
    var onCheckForUpdates: (() -> Void)? = nil
    /// macOS: whether the daemon is set to start at login (a launchd LaunchAgent). RootView owns
    /// the LoginItemManager; this is its current state + a toggle handler.
    var loginAtLogin: Bool = false
    var loginAtLoginError: String? = nil
    var onToggleLoginAtLogin: ((Bool) -> Void)? = nil
    #if os(macOS)
    /// The self-update checker (RootView-owned) — drives the "Relaunch to update" card pinned at the
    /// bottom of the sidebar (Claude-Code style). macOS only: iOS updates via TestFlight/App Store.
    @ObservedObject var updates: UpdateChecker
    #endif
    /// Opens the Loops (recurring autonomous workflows) destination. No longer in the `⋯` menu on
    /// either platform — Loops is a peer destination on the rail and the tab bar, and CommandDeck
    /// exists precisely so a destination is not ALSO an overflow-menu item (see the Fleet note below).
    var onOpenLoops: (() -> Void)? = nil
    // The panel callbacks below are iOS-only routes now: on macOS each of these is a tab in the
    // Settings window, so the menu no longer offers a second, modal way in. `onOpenDictionary` is
    // the exception — it has no Settings tab yet, so macOS still needs it here.
    var onOpenAgents: (() -> Void)? = nil
    var onOpenApprovalRules: (() -> Void)? = nil
    var onOpenMCP: (() -> Void)? = nil
    var onOpenSharing: (() -> Void)? = nil
    var onOpenDictionary: (() -> Void)? = nil
    var onOpenUsage: (() -> Void)? = nil
    var onOpenAccounts: (() -> Void)? = nil
    var onOpenRemotes: (() -> Void)? = nil
    var onManageSessions: (() -> Void)? = nil
    /// Set false when the HOST pins `ActiveSessionBar` itself (iOS: on the TabView, so "an agent is
    /// working" is visible from every tab, not just Sessions). Defaults to true so the sidebar keeps
    /// carrying it wherever nothing else does.
    var showsActiveSessionBar: Bool = true
    // Appearance + chat typography are the Settings window's General tab on macOS, so this view no
    // longer reads or writes those defaults there. On iOS the `⋯` menu is the only surface that has
    // them, so the bindings stay — scoped to the platform that still renders the controls.
    #if !os(macOS)
    @AppStorage("oculus.appearance") private var appearance: Appearance = .system
    @AppStorage("oculus.chatFontDesign") private var chatFontDesign = ChatFontDesign.system.rawValue
    @AppStorage("oculus.chatFontScale") private var chatFontScale = ChatFontScale.standard.rawValue
    #endif
    @State private var filter: SessionFilter = .all
    @State private var renamingSessionID: String?
    @State private var renameText = ""
    /// The LIST's own selection, so macOS draws the highlight, arrow keys walk the sessions, and
    /// VoiceOver announces which row is selected. Deliberately not `$selection` itself: the host
    /// treats that binding as a one-shot "open this" command and nils it out a beat later, which
    /// would blank the highlight after every click. This mirrors `model.sessionID` — the session
    /// that is actually open — and forwards user-driven changes back out through `$selection`, so
    /// the write is idempotent and the host needs no change.
    @State private var listSelection: String?
    /// Status is carried by colour in several dense chips; when the user has asked the system not to
    /// rely on colour, those chips grow their text label back.
    @Environment(\.accessibilityDifferentiateWithoutColor) private var differentiateWithoutColor
    /// A live terminal row from the strip awaiting confirmation before we take it over.
    /// The row whose `claude --resume` command was just copied — a one-shot "Copied" acknowledgement.
    @State private var copiedResumeFor: String?

    static let newSessionTag = "__new__"

    var body: some View {
        sessionsList
            .overlay {
                // A pairing that never came up is the ONLY thing this user can act on, so it takes
                // the whole surface. Showing the usual empty list plus a one-line grey status would
                // tell a first-run user that they have no sessions, when the truth is that we never
                // reached the Mac that has them.
                if isUnreachable {
                    connectionFailure
                } else if model.connected && searchText.isEmpty && filter == .all && filteredGroups.isEmpty {
                    emptyState
                }
            }
            #if os(macOS)
            .safeAreaInset(edge: .bottom) { updateCard }
            #endif
            .tint(palette.primary)
        // Takeover is only "proactive" if the list is already there when you look. Rescan whenever the
        // link comes up (a fresh connect, or a reconnect after the Mac woke) rather than waiting for
        // someone to find the manual Scan button.
        .task(id: model.connected) {
            guard model.connected else { return }
            await model.discover()
        }
        // macOS: the window title + desktop switcher live on the DECK (RootView), showing the current
        // PAGE consistently across destinations; search is the sticky DeckSearchBar. iOS keeps a
        // per-tab title + native search (applied at the END of this chain — see below).
        .toolbar { sidebarToolbar }
        .sheet(isPresented: $showPairingQR) {
            PairingQRView(model: model, palette: palette) {
                showPairingQR = false
                model.clearPairingCode() // a code left on screen is a live credential
            }
        }
        .sheet(isPresented: $showAddDesktop) {
            AddDesktopView(store: store, palette: palette) { showAddDesktop = false }
        }
        // NOTE: a toolbar button used to open FleetView as a SHEET from here. Fleet is a peer
        // destination (macOS rail / iOS tab bar), and the same view reached two ways — one of them
        // modal, with a different action in its header — is exactly what CommandDeck.swift:94-97
        // says the deck redesign exists to remove.
        .alert("Rename desktop", isPresented: $renamingDesktop) {
            TextField("Name", text: $desktopNewName)
            Button("Save") { if let a = store.active { store.rename(a.id, to: desktopNewName) } }
            Button("Cancel", role: .cancel) {}
        }
        .alert("Rename session", isPresented: Binding(get: { renamingSessionID != nil },
                                                      set: { if !$0 { renamingSessionID = nil } })) {
            TextField("Session name", text: $renameText)
            Button("Save") {
                if let id = renamingSessionID { Task { await model.renameSession(id, to: renameText) } }
                renamingSessionID = nil
            }
            Button("Cancel", role: .cancel) { renamingSessionID = nil }
        } message: {
            Text("Give this session a name. Leave blank to reset to its default title.")
        }
        // iOS-only: per-tab title + native pull-down search. On macOS these live on the deck.
        // (A trailing #if is safe — nothing follows it in the chain.)
        #if os(iOS)
        .navigationTitle(desktopName)
        .searchable(text: $searchText, prompt: "Search sessions")
        #endif
    }

    /// The sidebar body — a plain session `List`, styled by the system as a sidebar. Search
    /// is on the split view; the Sessions/Issues switch is on the detail toolbar. The window
    /// sizing is handled in RootView (windowResizability + detail clamp), so no inset hacks
    /// are needed here.
    private var sessionsList: some View {
        // The list owns selection. It used to be a bare `List { }` with the "selected" state
        // hand-drawn behind rows, which meant the sidebar had no selection as far as the system was
        // concerned: arrow keys did nothing, Tab skipped the list, and VoiceOver read a stack of
        // unlabelled buttons with no selected item — on macOS, where a sidebar's selection IS the
        // app's primary navigation state. The old objection was the accent-BLUE highlight, which is
        // a tint problem: `.listStyle(.sidebar)` + `.tint(palette.primary)` (applied below) draws it
        // in the brand gold.
        List(selection: $listSelection) {
            // Prominent New Session action right under the search bar — the primary way to start
            // one, so you don't have to hunt for the titlebar button.
            HStack(spacing: 4) {
                Button { selection = Self.newSessionTag } label: {
                    Label("New session", systemImage: "plus")
                        .font(.callout.weight(.medium))
                        .foregroundStyle(palette.primary)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .padding(.vertical, 6).padding(.horizontal, 8)
                        .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                // Ephemeral "just chat" — no project, not saved. A quick scratch conversation.
                Button { Task { await model.startEphemeralChat() } } label: {
                    Label("Chat", systemImage: "bubble.left.and.text.bubble.right")
                        .font(.callout.weight(.medium)).foregroundStyle(palette.mutedForeground)
                        .padding(.vertical, 6).padding(.horizontal, 8)
                        .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                .help("Ephemeral chat — no project, not saved")
            }
            .listRowInsets(EdgeInsets(top: 2, leading: 6, bottom: 4, trailing: 6))
            .listRowSeparator(.hidden)
            .listRowBackground(Color.clear)
            // The filter scopes THIS LIST, so it lives in the list. As a toolbar item it was one of
            // four trailing controls sharing a window toolbar with the chat's own — and it claimed
            // window scope for a list-scoped control. Chips rather than a segmented picker because
            // they carry live counts: "Running 3" tells you whether it is worth tapping.
            ScrollView(.horizontal, showsIndicators: false) {
                FilterChips(selection: $filter, options: filterOptions, palette: palette)
                    .padding(.horizontal, 2)
            }
            .listRowInsets(EdgeInsets(top: 0, leading: 6, bottom: 6, trailing: 6))
            .listRowSeparator(.hidden)
            .listRowBackground(Color.clear)
            // Connected over a relay: say so. A healthy link normally says nothing, and that's right
            // for the LAN — but a relay round-trips every keystroke through Cloudflare, and a user who
            // can't see the difference blames the agent for the latency (or assumes they're safely on
            // the home network when they're reaching the Mac from outside it).
            if model.onRelay {
                HStack(spacing: 6) {
                    Image(systemName: "antenna.radiowaves.left.and.right")
                        .font(.caption2).foregroundStyle(palette.mutedForeground)
                    // No lineLimit: at large type this clipped to "Connected …", which reads as a
                    // healthy LAN link — the exact confusion the row exists to prevent.
                    Text("Connected · relay")
                        .font(.caption).foregroundStyle(palette.mutedForeground)
                        .fixedSize(horizontal: false, vertical: true)
                    Spacer()
                }
                .help(model.connectionRouteHost.isEmpty ? "" : "via \(model.connectionRouteHost)")
                .listRowSeparator(.hidden)
                .listRowBackground(Color.clear)
            }
            // This row is the ONLY place a connection failure is rendered anywhere in the app, so it
            // has to be legible as a failure: a glyph rather than a bare dot, destructive rather than
            // muted, and NO lineLimit — the daemon's message is the user's entire diagnosis, and
            // truncating it to one grey line threw the diagnosis away.
            if !model.connected {
                HStack(alignment: .top, spacing: 6) {
                    if model.connecting {
                        ProgressView().controlSize(.mini)          // in progress — not an error
                    } else {
                        Image(systemName: "exclamationmark.triangle.fill")
                            .font(.caption).foregroundStyle(palette.destructive)
                    }
                    Text(model.connecting ? "Connecting…" : (model.statusDetail ?? model.status))
                        .font(.footnote)
                        .foregroundStyle(model.connecting ? palette.mutedForeground : palette.destructive)
                        .fixedSize(horizontal: false, vertical: true)
                    Spacer(minLength: 4)
                    if !model.connecting {
                        Button("Retry") { Task { await model.connect() } }
                            .font(.footnote).buttonStyle(.plain).foregroundStyle(palette.primary)
                    }
                }
                .listRowSeparator(.hidden)
                .listRowBackground(Color.clear)
            }
            // NOTE: the "Continue from terminal" strip used to live here. It belongs in the New
            // Session flow instead — the sidebar is your RECENT sessions, and an offer to adopt
            // something you haven't started yet is a creation step, not a recent. See
            // NewSessionView, which already owns take-over, and AllSessionsView for the full list.
            // Both platforms group by project, with "Recent" pinned first. The phone used to get ONE
            // flat SESSIONS section sorted by recency, which threw away the rolled-up "3 running · 1
            // needs you" counts — the very thing that makes twenty sessions scannable on a small
            // screen. Sections cost nothing in a List, and content that is grouped on the desktop
            // should stay grouped when the layout adapts. `groups` already pulls recents out of their
            // project buckets, so nothing appears twice.
            ForEach(filteredGroups) { group in
                Section {
                    ForEach(group.items) { item in
                        sessionRow(item, showProvider: group.showProvider, showProject: group.showProject)
                    }
                } header: {
                    sectionHeader(group.name, running: group.runningCount, needsYou: group.needsYouCount)
                }
            }
        }
        .listStyle(.sidebar)
        // Keep the list's selection pointed at the session that is actually open, in both directions:
        // a click routes through `$selection` (the host opens it, then nils the binding), a keyboard
        // move routes through the same activation path, and `model.sessionID` is the single truth
        // that settles both. Writing the value that is already there is a no-op, so this cannot loop.
        .onChange(of: listSelection) { sel in
            guard let sel, sel != model.sessionID else { return }
            activate(sel)
        }
        .onChange(of: model.sessionID) { listSelection = $0 }
        .onAppear { listSelection = model.sessionID }
        // The open conversation, pinned. The recents list scrolls, and on a long list the session you
        // are actually in scrolls out of sight — so the one row that answers "where am I?" was the
        // one row you could lose. Always visible, always the way back.
        .safeAreaInset(edge: .bottom) {
            if showsActiveSessionBar {
                ActiveSessionBar(model: model, palette: palette) { selection = $0 }
            }
        }
        #if os(macOS)
        // Show the system's translucent sidebar material (the "floating glass") instead of an opaque
        // fill — the list body was painting over it, making the sidebar a solid block.
        .scrollContentBackground(.hidden)
        // …and supply that material ourselves on systems that don't provide one. Without this the
        // same build looked glassy on macOS 26 and flat grey on anything older.
        .sidebarMaterial()
        #endif
    }

    /// What a row does when it is chosen — by click, or by an arrow key moving the list selection.
    /// One path for both, so keyboard navigation cannot drift from the pointer: a broken session
    /// RECOVERS and a stopped one RESTARTS rather than opening a dead conversation.
    private func activate(_ id: String) {
        guard let item = filteredGroups.flatMap(\.items).first(where: { $0.id == id }) else {
            selection = id
            return
        }
        if item.hasError {
            Task { await model.recoverSession(item.id) }
        } else if item.stopped {
            Task { await model.restartSession(item.id) }
        } else {
            selection = item.id
        }
    }

    private func attach(_ c: TakeoverCandidate) {
        guard let d = model.discovered.first(where: { $0.discoveryID == c.id }) else { return }
        Task {
            await model.attach(d)
            selection = c.sessionID
        }
    }

    /// Copies a command and flashes an acknowledgement — a menu item that silently succeeds is
    /// indistinguishable from one that silently failed.
    private func copy(_ text: String, for id: String) {
        copyToPasteboard(text)
        copiedResumeFor = id
        Task {
            try? await Task.sleep(nanoseconds: 2_000_000_000)
            if copiedResumeFor == id { copiedResumeFor = nil }
        }
    }

    /// The `claude --resume <uuid>` handback for a managed session, when we can name it honestly.
    ///
    /// LIMITATION: `Session` carries no claude UUID, so this only fires for a session whose id IS
    /// the UUID (a claude row taken over straight from discovery). Once the daemon rewrites it to a
    /// `cc_…` id, only the daemon's resume map knows the UUID — see the report note on exposing it
    /// in `session.info` (roadmap Phase 3 item 2, daemon half).
    private func resumeCommand(_ item: SidebarSession) -> String? {
        TerminalTakeover.resumeCommand(provider: item.provider, sessionID: item.id)
    }

    /// First-run empty state: no in-app sessions yet, so guide the two ways to get one —
    /// start fresh, or take over a session already running in a terminal.
    private var emptyState: some View {
        VStack(spacing: 14) {
            VStack(spacing: 4) {
                Text("No sessions yet").font(.subheadline.weight(.semibold))
                Text("Start an agent on one of your projects, or take over a session already running in a terminal.")
                    .font(.footnote).foregroundStyle(palette.mutedForeground)
                    .multilineTextAlignment(.center)
            }
            VStack(spacing: 8) {
                Button { selection = Self.newSessionTag } label: {
                    Label("New session", systemImage: "plus").frame(maxWidth: .infinity)
                }
                .buttonStyle(.borderedProminent).tint(palette.primary).controlSize(.large)
                Button { onTakeOver?() } label: {
                    Label("Take over a terminal session", systemImage: "arrow.down.left.circle").frame(maxWidth: .infinity)
                }
                .buttonStyle(.bordered).tint(palette.primary)
            }
            .padding(.top, 2)
        }
        .padding(.horizontal, 22)
        .frame(maxWidth: .infinity)
    }

    /// True when this Mac is the ONLY paired desktop and we are not talking to it. With no second
    /// desktop to fall back to there is nothing else in the app to look at, so the failure earns the
    /// whole surface.
    private var isUnreachable: Bool {
        store.models.count <= 1 && !model.connected && !model.connecting
    }

    /// The full-surface failure state: what went wrong, in full, plus the only two things that can
    /// fix it. Previously a first-run user whose pairing failed saw an empty session list and a
    /// truncated grey sentence.
    private var connectionFailure: some View {
        VStack(spacing: 14) {
            Image(systemName: "bolt.horizontal.circle")
                .font(.largeTitle).foregroundStyle(palette.destructive)
            VStack(spacing: 5) {
                Text("Can't reach your Mac").font(.subheadline.weight(.semibold))
                Text(model.statusDetail ?? model.status)
                    .font(.footnote).foregroundStyle(palette.mutedForeground)
                    .multilineTextAlignment(.center)
                    .fixedSize(horizontal: false, vertical: true)
                    .textSelection(.enabled) // the error is the thing people paste into a bug report
            }
            VStack(spacing: 8) {
                Button { Task { await model.connect() } } label: {
                    Label("Try again", systemImage: "arrow.clockwise").frame(maxWidth: .infinity)
                }
                .buttonStyle(.borderedProminent).tint(palette.primary).controlSize(.large)
                Button { showAddDesktop = true } label: {
                    #if os(iOS)
                    Label("Scan a new code", systemImage: "qrcode.viewfinder").frame(maxWidth: .infinity)
                    #else
                    Label("Pair a new desktop", systemImage: "qrcode.viewfinder").frame(maxWidth: .infinity)
                    #endif
                }
                .buttonStyle(.bordered).tint(palette.primary)
            }
            .padding(.top, 2)
        }
        .padding(.horizontal, 22)
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(palette.background)
    }

    /// The brand read on the selected row.
    ///
    /// On macOS the LIST now draws selection (gold, because the sidebar is tinted with the palette's
    /// primary), so painting a second gold card behind it stacks gold on gold. The row's own gold
    /// left bar carries the brand there. iOS keeps the card: a plain List's selection is inert
    /// outside edit mode, so without it the phone would show no selected state at all.
    @ViewBuilder private func rowSelectionBackground(_ selected: Bool) -> some View {
        #if os(iOS)
        if selected {
            // strokeBorder (not stroke) draws the border INSIDE the shape, so its outer half doesn't
            // spill past the row bounds.
            //
            // The DROP SHADOW had to go. A List cell clips to its own bounds, and a shadow by
            // definition renders outside the shape that casts it — so the glow was sliced off at the
            // row edges, which is what made the selected card look like its border was cut at the
            // corners. Insetting the card by a point leaves the hairline clear of the boundary; the
            // gold wash and border carry the "raised" read on their own.
            OculusShape.rounded(OculusRadius.sm)
                .fill(palette.primary.opacity(scheme == .dark ? 0.18 : 0.12))
                .overlay(OculusShape.rounded(OculusRadius.sm)
                    .strokeBorder(palette.primary.opacity(0.30), lineWidth: 1))
                .padding(1)
        } else {
            Color.clear
        }
        #else
        Color.clear
        #endif
    }

    /// The desktop switcher — the list of paired Macs plus add/rename/remove. Hangs off the
    /// navigation title via `.toolbarTitleMenu`, so the title (the active desktop's name)
    /// gains a dropdown chevron, the way Xcode's scheme menu works.
    @ViewBuilder private var desktopSwitcherMenu: some View {
        ForEach(store.models, id: \.id) { m in
            Button { store.selectedID = m.id } label: {
                Label(m.name.isEmpty ? "Desktop" : m.name,
                      systemImage: m.id == store.selectedID ? "checkmark"
                        : (m.connected ? "circle.fill" : "circle"))
            }
        }
        Divider()
        Button { showAddDesktop = true } label: { Label("Add desktop…", systemImage: "plus") }
        if let a = store.active {
            Button { desktopNewName = a.name; renamingDesktop = true } label: { Label("Rename…", systemImage: "pencil") }
            Button(role: .destructive) { store.remove(a.id) } label: { Label("Remove desktop", systemImage: "trash") }
        }
    }

    /// Trailing titlebar actions: exactly two — the overflow menu and the new-session button.
    ///
    /// This group had FOUR controls. On macOS they share one window toolbar with the chat's own
    /// (up to eleven), and on an iPhone they land in the Sessions nav bar beside the palette button —
    /// five trailing controls, the same crowding ChatView.swift:196-201 fixes for the chat. The
    /// filter moved into the list (it scopes the list, not the window) and Fleet is a destination.
    @ToolbarContentBuilder private var sidebarToolbar: some ToolbarContent {
        ToolbarItemGroup(placement: .primaryAction) {
            Menu { overflowMenu } label: {
                Image(systemName: "ellipsis")
            }
            .help("More options")
            // `.help` is the tooltip/HINT; without a label VoiceOver reads a bare "Button".
            .accessibilityLabel("More options")
            Button { selection = Self.newSessionTag } label: {
                Image(systemName: "plus")
            }
            .help("New session")
            .accessibilityLabel("New session")
        }
    }

    /// The `⋯` menu, for BOTH platforms out of one builder.
    ///
    /// It held eighteen items behind an unlabeled glyph: every app-wide preference the app has, plus
    /// nine panels that each opened as a sheet OVER the sessions you were configuring. macOS now has
    /// a real `Settings` scene (SettingsScene.swift) that owns all of it, so on that platform this
    /// menu keeps only what is scoped to THIS connection and THIS list, and points at ⌘, for the rest.
    ///
    /// iOS keeps the whole set, because `Settings {}` is a macOS-only scene type — there is nowhere
    /// for it to go. Stripping it there would not de-duplicate anything; it would delete appearance,
    /// fonts, notifications and diagnostics from the phone with no replacement, which is a worse
    /// outcome than the duplication this change exists to remove.
    ///
    /// One builder with the branch INSIDE it, rather than a menu per platform: the shared half is
    /// written once, so the two platforms cannot quietly grow different actions under the same glyph.
    @ViewBuilder private var overflowMenu: some View {
        // Session-scoped actions — identical on both platforms, and the reason this menu still exists.
        // None of these configure the app; they act on the link to this Mac and on the list itself.
        if model.canMintPairingCode {
            // The ONLY route to the pairing QR anywhere in the app, and the phone's onboarding copy
            // names this menu when it tells you where to look. Label and placement are load-bearing.
            Button { showPairingQR = true } label: { Label("Pair a phone…", systemImage: "qrcode") }
        }
        Button { Task { await model.discover() } } label: { Label("Refresh sessions", systemImage: "arrow.clockwise") }
        if let onManageSessions {
            Button { onManageSessions() } label: { Label("Manage sessions…", systemImage: "square.stack.3d.up") }
        }
        Divider()
        #if os(macOS)
        settingsItem
        // Kept on macOS ONLY because SettingsScene has no Dictionary tab. Removing it with the rest
        // of the panels would leave the feature with no route into it at all, which is a regression,
        // not a cleanup. Delete this the moment Settings gains the tab.
        if let onOpenDictionary {
            Button { onOpenDictionary() } label: { Label("Dictionary…", systemImage: "character.book.closed") }
        }
        #else
        mobilePreferences
        #endif
        Divider()
        Button(role: .destructive) { model.disconnect() } label: { Label("Disconnect", systemImage: "bolt.horizontal.circle") }
    }

    #if os(macOS)
    /// The single route from this menu into the Settings window. The menu still has to LEAD somewhere
    /// for a user who opens `⋯` looking for a preference — discovering ⌘, is not something to assume.
    @ViewBuilder private var settingsItem: some View {
        if #available(macOS 14, *) {
            // Sonoma's purpose-built API; it knows about the scene, so it cannot miss.
            SettingsLink { Label("Settings…", systemImage: "gearshape") }
        } else {
            Button {
                // Ventura (this app's floor) renamed the responder action when Preferences became
                // Settings, so `showSettingsWindow:` is the one that fires on 13. `showPreferencesWindow:`
                // is the pre-Ventura name, tried second so a rename we guessed wrong about degrades to
                // "nothing happened" only after both have been attempted.
                if !NSApp.sendAction(Selector(("showSettingsWindow:")), to: nil, from: nil) {
                    _ = NSApp.sendAction(Selector(("showPreferencesWindow:")), to: nil, from: nil)
                }
            } label: {
                Label("Settings…", systemImage: "gearshape")
            }
        }
    }
    #else
    /// Everything the macOS Settings window owns, inline. This is the phone's ENTIRE preferences
    /// surface, so it is kept whole rather than trimmed for length — see `overflowMenu`.
    @ViewBuilder private var mobilePreferences: some View {
        if let onOpenAgents {
            Button { onOpenAgents() } label: { Label("Agents…", systemImage: "cpu") }
        }
        if let onOpenUsage {
            Button { onOpenUsage() } label: { Label("Usage & spend…", systemImage: "chart.bar") }
        }
        if let onOpenDictionary {
            Button { onOpenDictionary() } label: { Label("Dictionary…", systemImage: "character.book.closed") }
        }
        if let onOpenSharing {
            Button { onOpenSharing() } label: { Label("Sharing…", systemImage: "person.2") }
        }
        if let onOpenMCP {
            Button { onOpenMCP() } label: { Label("MCP servers…", systemImage: "puzzlepiece.extension") }
        }
        if let onOpenApprovalRules {
            Button { onOpenApprovalRules() } label: { Label("Approval rules…", systemImage: "checkmark.shield") }
        }
        if let onOpenAccounts {
            Button { onOpenAccounts() } label: { Label("Accounts & usage…", systemImage: "person.2.badge.key") }
        }
        if let onOpenRemotes {
            Button { onOpenRemotes() } label: { Label("Remote hosts…", systemImage: "server.rack") }
        }
        Divider()
        Picker(selection: $appearance) {
            ForEach(Appearance.allCases) { a in
                Label(a.label, systemImage: a.symbol).tag(a)
            }
        } label: {
            Label("Appearance", systemImage: "circle.lefthalf.filled")
        }
        Picker(selection: $chatFontDesign) {
            ForEach(ChatFontDesign.displayOrder) { f in
                Label(f.label, systemImage: f.symbol).tag(f.rawValue)
            }
        } label: {
            Label("Chat font", systemImage: "textformat")
        }
        Picker(selection: $chatFontScale) {
            ForEach(ChatFontScale.allCases) { s in
                Text(s.label).tag(s.rawValue)
            }
        } label: {
            Label("Chat text size", systemImage: "textformat.size")
        }
        Menu {
            if model.notifyPrefs.isEmpty {
                Text("Loading…")
            } else {
                ForEach(model.notifyPrefs) { p in
                    Button { Task { await model.setNotifyPref(p.key, enabled: !p.enabled) } } label: {
                        // A checkmark = enabled (menus can't host a real Toggle inline).
                        if p.enabled { Label(p.label, systemImage: "checkmark") } else { Text(p.label) }
                    }
                }
            }
        } label: {
            Label("Notifications", systemImage: "bell")
        }
        .onAppear { Task { await model.loadNotifyPrefs() } }
        Toggle(isOn: Binding(get: { model.telemetryEnabled }, set: { on in Task { await model.setTelemetry(on) } })) {
            Label("Send anonymous diagnostics", systemImage: "waveform.path.ecg")
        }
        .help("Ships anonymized lifecycle events + scrubbed error classes (no paths, prompts, repo names, or tokens) so failures can be traced.")
    }
    #endif

    #if os(macOS)
    /// Claude-Code-style "Relaunch to update" card pinned to the bottom of the sidebar. Only shows
    /// when a newer release exists; one tap installs the new app + daemon (they update together now)
    /// and relaunches, streaming progress in place.
    @ViewBuilder private var updateCard: some View {
        if updates.updateAvailable {
            VStack(spacing: 0) {
                Button {
                    if !updates.installing { Task { await updates.installAndRelaunch() } }
                } label: {
                    HStack(spacing: 10) {
                        ZStack {
                            OculusShape.rounded(OculusRadius.sm).fill(palette.primary.opacity(0.16)).frame(width: 30, height: 30)
                            if updates.installing {
                                ProgressView().controlSize(.small)
                            } else {
                                Image(systemName: "arrow.down.circle.fill").foregroundStyle(palette.primary).font(.subheadline)
                            }
                        }
                        VStack(alignment: .leading, spacing: 1) {
                            Text(updates.installing ? "Updating…" : "Relaunch to update")
                                .font(.footnote.weight(.semibold)).foregroundStyle(palette.foreground)
                            Text(updates.installing ? updates.installPhase : "v\(updates.latestVersion ?? "")")
                                .font(.caption).foregroundStyle(palette.mutedForeground).lineLimit(1)
                        }
                        Spacer(minLength: 0)
                        if !updates.installing {
                            Image(systemName: "arrow.right").font(.footnote.weight(.semibold))
                                .foregroundStyle(palette.mutedForeground)
                                .accessibilityHidden(true)
                        }
                    }
                    .padding(10)
                    .background(palette.card, in: OculusShape.rounded(OculusRadius.md))
                    .overlay(OculusShape.rounded(OculusRadius.md).strokeBorder(palette.primary.opacity(0.3)))
                    .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                .disabled(updates.installing)
                .help("Update the app + daemon and relaunch")
                if let err = updates.installError {
                    Text(err).font(.caption2).foregroundStyle(.red)
                        .frame(maxWidth: .infinity, alignment: .leading).padding(.top, 4)
                }
            }
            .padding(.horizontal, 10).padding(.bottom, 10)
        }
    }
    #endif

    private var desktopName: String {
        let n = store.active?.name ?? model.name
        return n.isEmpty ? "Desktop" : n
    }

    /// The filter's options, with live counts — the counts are the reason these are chips and not a
    /// segmented picker.
    private var filterOptions: [FilterChips<SessionFilter>.Option] {
        let all = groups.flatMap(\.items)
        return SessionFilter.allCases.map { f in
            .init(value: f, label: f.label, count: all.filter(f.matches).count)
        }
    }

    /// One session row. Still a Button as well as a tagged list row: on iOS a plain List's selection
    /// does not respond to taps outside edit mode, and re-choosing the session you are already in
    /// has to re-push the chat — neither of which a selection binding alone would do.
    @ViewBuilder private func sessionRow(_ item: SidebarSession, showProvider: Bool = true, showProject: Bool = true) -> some View {
        let selected = model.sessionID == item.id
        Button { activate(item.id) } label: {
            SessionRow(item: item, active: selected,
                       showProvider: showProvider, showProject: showProject, palette: palette)
                .padding(.horizontal, 8).padding(.vertical, 5)
                .frame(maxWidth: .infinity, alignment: .leading)
                .background(rowSelectionBackground(selected))
                .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .tag(item.id)
        .listRowInsets(EdgeInsets(top: 1, leading: 6, bottom: 1, trailing: 6))
        .listRowSeparator(.hidden)
        .listRowBackground(Color.clear)
        .contextMenu { rowMenu(item) }
        .accessibilityAddTraits(selected ? [.isButton, .isSelected] : .isButton)
    }

    private var filteredGroups: [SessionGroup] {
        let q = searchText.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !q.isEmpty || filter != .all else { return groups }
        return groups.compactMap { g in
            let hits = g.items.filter { item in
                (q.isEmpty || item.title.localizedCaseInsensitiveContains(q)) && filter.matches(item)
            }
            guard !hits.isEmpty else { return nil }
            return SessionGroup(name: g.name, items: hits,
                                showProvider: g.showProvider, showProject: g.showProject,
                                hasRunning: hits.contains { $0.isRunning },
                                runningCount: hits.filter { $0.isRunning }.count,
                                needsYouCount: hits.filter { $0.hasError }.count)
        }
    }

    /// Right-click actions for a row. Managed (daemon-owned) sessions can be stopped, which
    /// ends the agent and removes them. Terminal-owned sessions are view-only — surfaced as a
    /// disabled hint so it's clear why there's nothing to manage.
    @ViewBuilder private func rowMenu(_ item: SidebarSession) -> some View {
        // The way BACK. A takeover is only reversible if the terminal can pick the conversation up
        // again, and the command to do that has never been surfaced anywhere in the app.
        if let cmd = resumeCommand(item) {
            Button { copy(cmd, for: item.id) } label: {
                Label(copiedResumeFor == item.id ? "Copied" : "Continue in terminal",
                      systemImage: copiedResumeFor == item.id ? "checkmark" : "terminal")
            }
            Divider()
        }
        if item.managed {
            Button { renameText = item.title; renamingSessionID = item.id } label: {
                Label("Rename…", systemImage: "pencil")
            }
            if let onReview {
                Button { onReview(item.id) } label: { Label("Review changes", systemImage: "plus.forwardslash.minus") }
            }
            Divider()
            Button(role: .destructive) {
                Task { await model.stopSession(item.id) }
            } label: {
                Label("Delete session", systemImage: "trash")
            }
        } else {
            Label("Started in a terminal · click to resume", systemImage: "terminal")
                .foregroundStyle(palette.mutedForeground)
        }
    }

    /// Project header with ROLLED-UP status: a project row shows its aggregate agent states
    /// ("3 running · 1 needs you") so you read a whole project at a glance without expanding it.
    ///
    /// Title Case, not ALL CAPS: OS 26 moved every system list header to title case, so a
    /// hand-uppercased header now reads as a deliberate departure from every native list.
    private func sectionHeader(_ name: String, running: Int, needsYou: Int) -> some View {
        HStack(spacing: 6) {
            Text(name)
                .font(.caption.weight(.semibold))
                .foregroundStyle(palette.mutedForeground)
            Spacer()
            if running > 0 {
                // A glyph, not a dot: the two aggregates differ only in hue otherwise.
                // `.caption2` on the glyph as well as the count so the pair scales together — at a
                // hardcoded 8pt the glyph stayed a speck beside a count three times its height.
                HStack(spacing: 3) {
                    Image(systemName: "bolt.fill").font(.caption2)
                    Text("\(running)").font(.caption2.weight(.semibold).monospacedDigit())
                }
                .foregroundStyle(palette.primary)
                .accessibilityElement(children: .combine)
                .accessibilityLabel("\(running) running")
            }
            if needsYou > 0 {
                HStack(spacing: 3) {
                    Image(systemName: "exclamationmark.triangle.fill").font(.caption2)
                    Text("\(needsYou)").font(.caption2.weight(.semibold).monospacedDigit())
                }
                .foregroundStyle(palette.warning)
                .accessibilityElement(children: .combine)
                .accessibilityLabel("\(needsYou) need you")
            }
        }
    }

    // MARK: grouping

    private var groups: [SessionGroup] {
        let projectNames = Dictionary(model.projects.map { ($0.id, $0.name) }, uniquingKeysWith: { a, _ in a })
        let discoveredTitles = Dictionary(model.discovered.compactMap { d -> (String, String)? in
            guard let s = d.sessionID, let t = d.title, !t.isEmpty else { return nil }
            return (s, t)
        }, uniquingKeysWith: { a, _ in a })

        var buckets: [String: [SidebarSession]] = [:]
        var order: [String] = []
        func add(_ key: String, _ item: SidebarSession) {
            if buckets[key] == nil { buckets[key] = []; order.append(key) }
            buckets[key]?.append(item)
        }

        for s in model.sessions {
            let isChild = !(s.parentID?.isEmpty ?? true)
            // Split into shorter chains — one long ?? expression made the Swift type-checker time out
            // in Release builds.
            let named = clean(s.subtask) ?? clean(s.name) ?? s.workspaceName
            let titled = named ?? clean(s.title) ?? clean(discoveredTitles[s.id])
            let title = titled ?? s.folderName ?? "ses \(s.id.prefix(6))"
            let host = clean(s.execHost)
            // A remote session has no registered project, so it fell into the bucket literally headed
            // "On this Mac" — the one heading that is certainly wrong for it. Group it under the box it
            // runs on, which also answers "what else is running over there?".
            // A projectID the daemon no longer lists used to become the section heading VERBATIM, so
            // the sidebar grew headers reading "proj_ab12cd34ef56". It is reachable two ways: the
            // project was deleted after this session was persisted, or — far more often now — the
            // registry migration folded an auto-registered worktree into its parent repo, and every
            // session created under the old worktree project still carries the collapsed id. The
            // daemon resolves those ids through AbsorbedIDs, but the client only has the list, so it
            // falls back to the one name the user will actually recognise: the session's own folder.
            let unresolved = s.folderName ?? "On this Mac"
            let localKey = (s.projectID?.isEmpty ?? true) ? "On this Mac" : unresolved
            let projectKey = s.projectID.flatMap { projectNames[$0] } ?? localKey
            let key = host.map { Self.remoteGroupPrefix + $0 } ?? projectKey
            add(key, SidebarSession(id: s.id, title: title, provider: s.provider, projectName: key,
                                    branch: s.branch, isRunning: s.status == SessionStatusValue.running,
                                    stopped: s.status == SessionStatusValue.stopped,
                                    viewOnly: false, managed: true, updatedAt: date(s.updatedAt), isChild: isChild,
                                    hasError: model.sessionErrors[s.id] != nil, conflicted: s.conflicted == true,
                                    execHost: host))
        }
        // Terminal-owned sessions discovered on the host are intentionally NOT shown here —
        // the sidebar lists only sessions started/opened in the app. Discovered sessions are
        // found on demand via the Add Session search (which attaches them, making them managed).

        // Pull recently-active sessions (active within the window, or running) out of their
        // project buckets into a single "Recent" section at the top. View-only sessions stay
        // in their own section — they're a different interaction class.
        let cutoff = Date().addingTimeInterval(-recentWindow)
        var recent: [SidebarSession] = []
        for key in order where key != "View-only" {
            var kept: [SidebarSession] = []
            for it in buckets[key] ?? [] {
                if it.isRunning || (it.updatedAt.map { $0 >= cutoff } ?? false) {
                    recent.append(it)
                } else {
                    kept.append(it)
                }
            }
            buckets[key] = kept
        }

        func group(_ name: String, _ items: [SidebarSession], showProject: Bool) -> SessionGroup {
            let sorted = items.sorted { a, b in
                if a.isRunning != b.isRunning { return a.isRunning }
                return (a.updatedAt ?? .distantPast) > (b.updatedAt ?? .distantPast)
            }
            return SessionGroup(name: name, items: sorted,
                                showProvider: Set(sorted.map { $0.provider }).count > 1,
                                showProject: showProject,
                                hasRunning: sorted.contains { $0.isRunning },
                                runningCount: sorted.filter { $0.isRunning }.count,
                                needsYouCount: sorted.filter { $0.hasError }.count)
        }

        var result: [SessionGroup] = []
        if !recent.isEmpty { result.append(group("Recent", recent, showProject: true)) }
        let special = ["On this Mac", "View-only"]
        // Remote hosts sit after your projects and before the catch-all sections: they're real work
        // locations (so not buried with "View-only"), but a machine is not a project.
        let projects = order.filter { !special.contains($0) && !$0.hasPrefix(Self.remoteGroupPrefix) }.sorted()
        let remotes = order.filter { $0.hasPrefix(Self.remoteGroupPrefix) }.sorted()
        let tail = special.filter { !(buckets[$0]?.isEmpty ?? true) }
        for name in projects + remotes + tail where !(buckets[name]?.isEmpty ?? true) {
            result.append(group(name, buckets[name] ?? [], showProject: false))
        }
        return result
    }

    /// A session counts as "Recent" if it was active within this window (or is running).
    private let recentWindow: TimeInterval = 24 * 3600

    /// Section-name prefix for sessions running on an ssh host, so the ordering pass can tell a
    /// machine from a project without carrying a second parallel structure through grouping.
    static let remoteGroupPrefix = "Remote · "

    private func date(_ secs: Int?) -> Date? {
        guard let s = secs, s > 0 else { return nil }
        return Date(timeIntervalSince1970: TimeInterval(s))
    }

    /// Cleans a raw title: strips the "New session - <ISO8601>" pattern and blanks.
    private func clean(_ raw: String?) -> String? {
        guard let t = raw?.trimmingCharacters(in: .whitespacesAndNewlines), !t.isEmpty else { return nil }
        if t.hasPrefix("New session"),
           t.range(of: #"\d{4}-\d{2}-\d{2}T"#, options: .regularExpression) != nil {
            return "New session"
        }
        return t
    }
}

/// The conversation currently open — one compact row: a pulse dot, the session's name, whether it is
/// working, and what it has cost.
///
/// It lives outside SessionSidebar because it was only ever visible in the Sessions tab, which is not
/// where a phone user starts: Activity is the default tab, and from there nothing said an agent was
/// running or what it was spending. The host can pin this to the whole TabView instead. (The WWDC25
/// answer is `.tabViewBottomAccessory`, which is iOS 26 — far above this app's iOS 16 floor.)
///
/// Deliberately ONE row tall. The daemon log inset was pulled from iOS because a taller bottom
/// accessory swallowed taps along the screen edge; the tab bar has to keep owning the safe area
/// beneath this.
struct ActiveSessionBar: View {
    @ObservedObject var model: Model
    let palette: OculusPalette
    /// Re-opening the session it names — the way back into the conversation from anywhere.
    var onOpen: (String) -> Void

    var body: some View {
        if let s = model.currentSession {
            Button { onOpen(s.id) } label: {
                HStack(spacing: 8) {
                    RunningPulseDot(color: model.busy ? palette.success : palette.mutedForeground,
                                    active: model.busy)
                    VStack(alignment: .leading, spacing: 1) {
                        Text(s.name ?? s.title ?? s.id)
                            .font(.footnote.weight(.semibold))
                            .foregroundStyle(palette.foreground)
                            .lineLimit(1)
                            // This bar answers "where am I?", so the name has to stay readable at
                            // large type — shrink it rather than truncate it to an ellipsis.
                            .minimumScaleFactor(0.85)
                        Text(model.busy ? "working…" : "open")
                            .font(.caption2).foregroundStyle(palette.mutedForeground)
                    }
                    Spacer(minLength: 4)
                    if let cost = s.costUSD, cost > 0 {
                        Text(String(format: "$%.2f", cost))
                            .font(.caption2.monospacedDigit())
                            .foregroundStyle(palette.mutedForeground)
                    }
                }
                .padding(.horizontal, 10).padding(.vertical, 8)
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .background(palette.card.opacity(0.6))
            .overlay(Rectangle().frame(height: 1).foregroundStyle(palette.border), alignment: .top)
            .accessibilityHint("Returns to the open session")
        }
    }
}

/// One session row: a gold left-bar + gold title when it's the active session, a running
/// dot only while running, provider only when its group mixes providers, branch as a chip.
private struct SessionRow: View {
    let item: SidebarSession
    let active: Bool
    let showProvider: Bool
    let showProject: Bool
    let palette: OculusPalette
    @Environment(\.accessibilityDifferentiateWithoutColor) private var differentiateWithoutColor
    /// The filled chips pair a deliberately sub-caption status pip with a `.caption2` label; giving
    /// the pip `.caption2` too would render it as tall as the word beside it and turn "Live" into a
    /// bullet point. ScaledMetric keeps the proportion while still growing with the user's type size,
    /// which a hardcoded 6pt did not — at accessibility sizes the pip simply vanished.
    @ScaledMetric(relativeTo: .caption2) private var pipSize: CGFloat = 6

    var body: some View {
        HStack(spacing: 9) {
            OculusShape.rounded(2)
                .fill(active ? palette.primary : Color.clear)
                .frame(width: 3, height: 22)
            VStack(alignment: .leading, spacing: 2) {
                HStack(spacing: 4) {
                    if item.isChild {
                        Image(systemName: "arrow.turn.down.right")
                            .font(.caption2).foregroundStyle(palette.mutedForeground)
                            .accessibilityLabel("Sub-agent")
                    }
                    Text(item.title)
                        .font(.footnote.weight(active ? .semibold : .medium))
                        .foregroundStyle(palette.foreground)
                        .lineLimit(1)
                        .minimumScaleFactor(0.85) // a long name must shrink, not disappear, at large type
                }
                if let sub = secondary {
                    Text(sub)
                        .font(.caption)
                        .foregroundStyle(palette.mutedForeground)
                        .lineLimit(1)
                        .minimumScaleFactor(0.85) // "project · provider · 4m ago" — the time is the tail
                }
            }
            Spacer(minLength: 6)
            // Where this agent is EDITING FILES. Only remote sessions carry it: "on this Mac" is the
            // assumption a user already makes, so stamping it on every row would be noise, while a
            // remote session with nothing to mark it is a genuine trap — the row looks identical to a
            // local one, and the difference is which machine the next commit comes from.
            if let host = item.execHost, !host.isEmpty {
                chip(icon: "server.rack", text: host, tint: palette.mutedForeground)
            }
            if let b = item.branch, !b.isEmpty {
                chip(icon: "arrow.triangle.branch", text: b, tint: palette.mutedForeground)
            }
            // A solid chip to distinguish lifecycle at a glance: running (gold, live), or a
            // terminal glyph for sessions started outside the app (discovered — clicking
            // resumes them). Managed idle sessions carry no chip; they're the plain default.
            if item.conflicted {
                // Worktree branch conflicts with the default branch — flag it so parallel agents on
                // one repo don't silently collide.
                chip(icon: "arrow.triangle.merge", text: "Conflict", tint: palette.conflict, filled: true)
            }
            if item.hasError {
                // A background session whose last turn errored / got no response. The chip IS the
                // fix — tapping the row reconnects it (recover, keeping history).
                chip(icon: "xmark.octagon.fill", text: "Reconnect", tint: palette.destructive, filled: true)
            } else if item.isRunning {
                chip(icon: "circle.fill", text: "Live", tint: palette.primary, filled: true)
            } else if item.stopped {
                chip(icon: "moon.zzz.fill", text: "Stopped", tint: palette.mutedForeground)
            } else if !item.managed {
                // The only glyph-only chip in the row. Spell it out when the user has asked not to
                // depend on colour, and always name it for VoiceOver.
                chip(icon: "terminal", text: differentiateWithoutColor ? "Terminal" : nil,
                     tint: palette.mutedForeground)
                    .accessibilityLabel("Started in a terminal")
            }
        }
        .padding(.vertical, 3)
        .contentShape(Rectangle())
    }

    private func chip(icon: String, text: String?, tint: Color, filled: Bool = false) -> some View {
        HStack(spacing: 3) {
            Image(systemName: icon).font(filled ? .system(size: pipSize) : .caption2)
            if let text { Text(text).font(.caption2.weight(.semibold)).lineLimit(1) }
        }
        .foregroundStyle(filled ? tint : palette.mutedForeground)
        .padding(.horizontal, text == nil ? 5 : 6).padding(.vertical, 2)
        .background(Capsule().fill(tint.opacity(filled ? 0.16 : 0.12)))
    }

    /// Provider (only when its group mixes providers) joined with a compact relative time.
    /// The view-only/managed distinction is carried by the trailing chip, not this line.
    private var secondary: String? {
        var parts: [String] = []
        if showProject { parts.append(item.projectName) } // Recent section spans projects
        if showProvider || item.viewOnly { parts.append(item.provider) }
        if let t = item.updatedAt { parts.append(Self.relative(t)) }
        return parts.isEmpty ? nil : parts.joined(separator: " · ")
    }

    private static let weekdayFmt: DateFormatter = {
        let f = DateFormatter(); f.dateFormat = "EEE"; return f // Mon
    }()
    private static let dateFmt: DateFormatter = {
        let f = DateFormatter(); f.setLocalizedDateFormatFromTemplate("MMMd"); return f // Jul 3
    }()

    static func relative(_ date: Date) -> String {
        let s = Date().timeIntervalSince(date)
        if s < 45 { return "now" }
        if s < 3600 { return "\(Int(s / 60))m ago" }
        if s < 86_400 { return "\(Int(s / 3600))h ago" }
        if Calendar.current.isDateInYesterday(date) { return "Yesterday" }
        if s < 7 * 86_400 { return weekdayFmt.string(from: date) }
        return dateFmt.string(from: date)
    }
}


