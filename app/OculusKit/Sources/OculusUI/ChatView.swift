import SwiftUI
import OculusKit
#if os(iOS)
import PhotosUI
#endif
#if canImport(AppKit)
import AppKit
#endif
#if canImport(UIKit)
import UIKit
#endif

private struct TranscriptBottomOffsetKey: PreferenceKey {
    static var defaultValue: CGFloat = .infinity
    static func reduce(value: inout CGFloat, nextValue: () -> CGFloat) {
        value = nextValue()
    }
}

// MARK: - Header status: two independent facts, never one string

/// Whether we can reach the daemon at all. Orthogonal to what the agent is doing.
enum ConnectionPhase: Equatable { case connected, connecting, offline }

/// The chat header's two separate readings.
///
/// `Model.status` is a single `String` that is written by BOTH the transport ("Disconnected",
/// "Reconnecting…", "Connect failed") and by `session.status` broadcasts ("running", "idle",
/// "error"). Rendering it in one slot meant a dead socket appeared where a session state belongs,
/// and "Disconnected" read as "this agent stopped" — the user waits instead of reconnecting.
/// This splits the two apart and marks the session word as last-known whenever the socket is down,
/// because while offline we are quoting a snapshot, not observing anything.
struct HeaderStatus: Equatable {
    var connection: ConnectionPhase
    /// The connection chip's text — nil when connected, because a healthy link says nothing.
    var connectionLabel: String?
    /// What the AGENT is doing. Never a transport word.
    var session: String
    /// True when `session` is a remembered value rather than an observed one.
    var stale: Bool
}

/// Turns the model's flat state into the two independent readings above.
///
/// Free function (not a method) so it can be exercised without a Model, a socket, or a view.
func deriveHeaderStatus(connected: Bool, connecting: Bool, rawStatus: String,
                        sessionStatus: String?, busy: Bool, awaitingApproval: Bool) -> HeaderStatus {
    let phase: ConnectionPhase = connected ? .connected : (connecting ? .connecting : .offline)
    let connectionLabel: String? = {
        switch phase {
        case .connected: return nil
        case .connecting: return "Connecting…"
        case .offline: return "Disconnected"
        }
    }()

    // Precedence is live-knowledge-first: something we are watching right now beats the last
    // broadcast snapshot, which beats a token that happens to be sitting in `rawStatus`.
    let session: String = {
        if awaitingApproval { return "awaiting approval" }
        if busy { return "working…" }
        if let s = sessionStatus, let word = sessionStatusWord(s) { return word }
        // rawStatus is only trusted when it IS a session token — anything else in there is a
        // transport message, and letting one through is the exact bug this function exists to kill.
        if let word = sessionStatusWord(rawStatus) { return word }
        return "unknown"
    }()

    return HeaderStatus(connection: phase, connectionLabel: connectionLabel,
                        session: session, stale: phase != .connected)
}

/// Maps a wire session-status token to the word shown to a human. Returns nil for anything that
/// is not a session status — that nil is what keeps connection strings out of the session slot.
func sessionStatusWord(_ raw: String) -> String? {
    switch raw {
    case SessionStatusValue.running: return "working…"
    case SessionStatusValue.idle: return "idle"
    case SessionStatusValue.awaitingApproval: return "awaiting approval"
    case SessionStatusValue.done: return "done"
    case SessionStatusValue.error, "errored": return "Error"
    case SessionStatusValue.stopped: return "stopped"
    default: return nil
    }
}

/// The session conversation surface: a streaming message list with an inline
/// approval card and a sticky composer (attach · voice · send). Sparse, dark,
/// session-first — matching the Oculus/HowlerOps design system.
public struct ChatView: View {
    @ObservedObject var model: Model
    @Environment(\.colorScheme) private var scheme
    private var palette: OculusPalette { .current(scheme) }

    /// Draft is stored on the model, keyed by session id, so switching sessions preserves each
    /// composer's unsent text (see Model.drafts). This binding reads/writes the active session's.
    private var draft: Binding<String> {
        Binding(get: { model.currentDraft }, set: { model.currentDraft = $0 })
    }
    @State private var anchorTask: Task<Void, Never>?
    /// Separate from `anchorTask` so the streaming follow and the open-time re-pin never cancel
    /// each other mid-flight.
    @State private var followTask: Task<Void, Never>?
    /// Explicit bottom re-pins only fire before this time (a short window after a session opens), so
    /// they can't fight defaultScrollAnchor during streaming and bounce the view.
    @State private var initialAnchorDeadline: Date = .distantPast
    @State private var isTranscriptBottomVisible = true
    @State private var transcriptViewportHeight: CGFloat = 0
    @State private var showWorktreePanel = false
    @State private var showHandoff = false
    @State private var showWorkspace = false
    @State private var showDelegate = false
    /// iOS: the session's controls live on a sheet instead of eight squeezed navigation-bar items.
    @State private var showSessionControls = false
    /// Where the controls sheet asked to go, acted on in its `onDismiss` so the destination isn't
    /// presented underneath a sheet that is still on screen.
    @State private var controlsDestination: SessionControlsDestination?
    @State private var showUsage = false

    public init(model: Model) { self.model = model }

    private var isWorktreeSession: Bool { model.currentSession?.branch != nil }
    /// Sub-agents delegated from the active session (the orchestration cockpit).
    private var children: [Session] {
        guard let sid = model.sessionID else { return [] }
        return model.sessions.filter { $0.parentID == sid }
    }
    /// Sub-agents actively working (drives the topbar running indicator).
    private var runningChildCount: Int {
        children.filter { model.heartbeats[$0.id]?.state == "working" || $0.status == SessionStatusValue.running }.count
    }

    /// A session the daemon couldn't re-attach after a restart — persisted, not live, restartable.
    private var isStopped: Bool { model.currentSession?.status == SessionStatusValue.stopped }

    #if !os(iOS)
    // Lifted out of the toolbar builder so each one can be written once and referenced from BOTH
    // arms of the OS-26 `sharedBackgroundVisibility` availability check (see the toolbar).
    /// A toolbar item that is a READOUT, not a control.
    ///
    /// On OS 26 the system gives every toolbar item its own glass background, which is the platform's
    /// visual language for "this is a button" — so four things you cannot tap started advertising
    /// themselves as tappable. `sharedBackgroundVisibility(.hidden)` puts them back to being text.
    ///
    /// Two separate gates are required and they are NOT interchangeable:
    ///   • `#if compiler(>=6.2)` is a COMPILE-time gate. `sharedBackgroundVisibility` does not exist
    ///     in the iOS 18 / macOS 15 SDK, so on an older toolchain the symbol cannot even be named.
    ///     CI pins Xcode 16.4 (Swift 6.1), which is exactly how this got shipped broken once: the
    ///     runtime check below compiled fine locally against the 26 SDK and failed on the runner.
    ///   • `if #available` is a RUNTIME gate, for a 26-SDK build running on an older OS.
    /// Dropping either one breaks a different set of machines.
    @ToolbarContentBuilder
    private func readoutToolbarItem<V: View>(@ViewBuilder _ content: () -> V) -> some ToolbarContent {
        #if compiler(>=6.2)
        if #available(iOS 26.0, macOS 26.0, *) {
            ToolbarItem(placement: .automatic, content: content)
                .sharedBackgroundVisibility(.hidden)
        } else {
            ToolbarItem(placement: .automatic, content: content)
        }
        #else
        ToolbarItem(placement: .automatic, content: content)
        #endif
    }

    private var toolActivityToolbarChip: some View {
        ToolActivityView(activity: model.activity, palette: palette, compact: true)
            .accessibilityElement(children: .ignore)
            .accessibilityLabel(model.activity.map { "Agent activity: \($0)" } ?? "Working")
            .help(model.activity.map { "Agent activity: \($0)" } ?? "Working")
    }

    private var runningAgentsToolbarChip: some View {
        let phrase = "\(runningChildCount) sub-agent\(runningChildCount == 1 ? " is" : "s are") running"
        return HStack(spacing: 5) {
            RunningPulseDot(color: .green, active: true)
            Text("\(runningChildCount) agent\(runningChildCount == 1 ? "" : "s")")
                .font(.caption.weight(.medium)).foregroundStyle(palette.mutedForeground)
        }
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(phrase)
        .help(phrase)
    }
    #endif

    public var body: some View {
        VStack(spacing: 0) {
            // Connection first, above everything: it explains why the rest of the pane may be
            // frozen, and it must not be mistaken for a session state (see HeaderStatus).
            connectionBanner
            if isWorktreeSession { worktreeBanner }
            if isStopped { stoppedBanner }
            // (Removed the top-of-chat "Fleet" awareness strip — it auto-appeared whenever any OTHER
            // session was active and read as if THIS session had become a fleet. Other sessions live in
            // the sidebar + the Fleet destination; the chat now stays focused on the one conversation.)
            if !children.isEmpty { SubAgentsStrip(model: model, children: children, palette: palette) }
            if !model.todos.isEmpty { TodoBar(todos: model.todos, palette: palette) }
            if model.messages.isEmpty && model.sessionID == nil {
                emptyState
            } else if model.messages.isEmpty, let err = sessionLoadError {
                sessionErrorView(err) // a broken/errored session shows WHY, not a blank pane
            } else if model.messages.isEmpty && model.sessionLoading {
                sessionLoadingView // smooth swap: a loader while the transcript replays, not white
            } else if model.messages.isEmpty, model.sessionID != nil, !model.sessionLoading, !model.transcriptSettling {
                // An OPENED session with nothing in it (e.g. a restored session that never had a turn,
                // so there is no history anywhere). A blank white pane read as "clicking does nothing" —
                // say what this is and what to do instead.
                VStack(spacing: OculusSpace.md) {
                    Image(systemName: "bubble.left.and.bubble.right").font(.largeTitle).foregroundStyle(palette.mutedForeground)
                    Text("This conversation is empty").font(.headline).foregroundStyle(palette.foreground)
                    Text("No messages were found for this session — it may never have had a turn, or its history isn't recoverable. Send a message below to start, or delete it from Manage Sessions.")
                        .font(.callout).foregroundStyle(palette.mutedForeground)
                        .multilineTextAlignment(.center).frame(maxWidth: 420).fixedSize(horizontal: false, vertical: true)
                }
                .frame(maxWidth: .infinity, maxHeight: .infinity)
                .padding(OculusSpace.xl)
            } else if model.transcriptSettling {
                // History is still bursting in. Keep the scroll view UNBUILT until it settles, then
                // create it once — fully formed and natively anchored at the bottom — instead of the
                // old behavior: an empty scroll view anchored at nothing, hundreds of appends pushing
                // content down, and per-append scrollTo calls visibly "scrolling through history".
                sessionLoadingView
            } else {
                transcript
                typingBar // pinned below the scroll so its flicker never shifts the transcript
            }
            if model.showTests {
                TestResultPanel(model: model, palette: palette)
                    .transition(.move(edge: .bottom).combined(with: .opacity))
            }
            if let ap = model.pendingApproval {
                ApprovalCard(approval: ap, palette: palette,
                             onAllow: { Task { await model.respond(Decision.allow) } },
                             onAlways: { scope in Task { await model.respond(Decision.always, scope: scope) } },
                             onDeny: { Task { await model.respond(Decision.deny) } })
                    .transition(.move(edge: .bottom).combined(with: .opacity))
            }
            if isStopped { restartFooter } else { Composer(model: model, draft: draft, palette: palette) }
        }
        // NOTE: was `.background(palette.background.ignoresSafeArea())`. In a NavigationSplitView
        // detail column on macOS 26, the ignoresSafeArea inflated ChatView's ideal height, which
        // drove the whole split view to ~1884pt and overflowed the sidebar — but ONLY on the
        // Sessions tab (IssuesView has no ignoresSafeArea and rendered at the correct ~715pt).
        // Plain background keeps the detail bounded to the window.
        .background(palette.background)
        .animation(.spring(response: 0.35, dampingFraction: 0.85), value: model.pendingApproval)
        // Handoff, publishing side. Both Info.plists have declared NSUserActivityTypes for this type
        // since the app shipped, so the OS was told Handoff was supported while nothing ever created
        // an activity — the icon simply never appeared on the other device. `isActive` is the whole
        // lifecycle: it goes false the moment no session is open, which is how the advertisement is
        // withdrawn (there is nothing to invalidate by hand), and SwiftUI re-runs the update block
        // whenever this body re-evaluates, so switching sessions re-points the activity.
        .userActivity(oculusSessionActivityType, isActive: model.sessionID != nil) { activity in
            guard let sid = model.sessionID else { return }
            activity.title = model.currentSession?.name ?? model.currentSession?.title ?? "Iron Rain session"
            activity.isEligibleForHandoff = true
            // NOT indexed: `isEligibleForSearch` would put session titles — usually a branch or a
            // ticket summary — into Spotlight, where the user never asked for them.
            activity.isEligibleForSearch = false
            // See OculusHandoffKey: identifiers only, because this payload travels via iCloud.
            activity.userInfo = [
                OculusHandoffKey.sessionID: sid,
                OculusHandoffKey.desktopID: model.id,
            ]
            activity.targetContentIdentifier = sid
        }
        .navigationTitle(model.sessionID == nil ? "New session" : statusLabel)
        #if os(iOS)
        .navigationBarTitleDisplayMode(.inline)
        #endif
        .toolbar {
            #if os(iOS)
            // A phone navigation bar fits about three things. It used to be given eight — the cost
            // meter, heartbeat, activity, agent count, handoff, model, autonomy, mode and an overflow
            // menu — so everything truncated ("$0.0…") and every target was a sliver. The state you
            // GLANCE at is now one legible chip; the state you CHANGE is one tap away with room to
            // breathe.
            if model.sessionID != nil {
                ToolbarItem(placement: .principal) {
                    Button { showSessionControls = true } label: {
                        SessionTitleChip(model: model, palette: palette, status: statusLabel)
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel("Session — \(statusLabel)")
                    .accessibilityHint("Opens session controls")
                }
                if model.activeHandoff != nil {
                    ToolbarItem(placement: .navigationBarTrailing) {
                        Button { showHandoff = true } label: { Image(systemName: "doc.text.magnifyingglass") }
                            // `.help()` populates the VoiceOver HINT, not the label, so an icon-only
                            // button with only a `.help` announces as a bare "Button". The label has to
                            // be set explicitly; it reuses the help string so the two never drift.
                            .accessibilityLabel("The agent saved its progress to a handoff file.")
                            .help("The agent saved its progress to a handoff file.")
                    }
                }
                ToolbarItem(placement: .navigationBarTrailing) {
                    Button { showSessionControls = true } label: { Image(systemName: "slider.horizontal.3") }
                        .accessibilityLabel("Session controls — model, mode, tools, usage")
                        .help("Session controls — model, mode, tools, usage")
                }
            }
            #else
            // UsageChip / HeartbeatChip / ToolActivityView / the agent count are READOUTS, not
            // controls. On OS 26 the system gives every toolbar item its own glass background, which
            // is the platform's visual language for "this is a button" — so four things you cannot tap
            // started advertising themselves as tappable. Hiding the shared background puts them back
            // to being text. The modifier returns `some ToolbarContent`, so the availability check has
            // to wrap the whole ToolbarItem, not just the modifier.
            if let s = model.currentSession, (s.costUSD ?? 0) > 0 || (s.inputTokens ?? 0) > 0 {
                readoutToolbarItem { UsageChip(session: s, palette: palette) }
            }
            if let sid = model.sessionID, let hb = model.heartbeats[sid] {
                readoutToolbarItem { HeartbeatChip(hb: hb, palette: palette) }
            }
            // The live tool-use chip (what the agent is doing NOW) replaces the old generic running
            // blob in the top bar — a real per-tool icon + word instead of an anonymous pulse.
            if model.busy, model.messages.last?.streaming != true {
                readoutToolbarItem { toolActivityToolbarChip }
            }
            if runningChildCount > 0 {
                readoutToolbarItem { runningAgentsToolbarChip }
            }
            if model.activeHandoff != nil {
                ToolbarItem(placement: .automatic) {
                    Button { showHandoff = true } label: {
                        Label("Handoff", systemImage: "doc.text.magnifyingglass")
                    }
                    .accessibilityLabel("The agent saved its progress to a handoff file. Tap to view.")
                    .help("The agent saved its progress to a handoff file. Tap to view.")
                }
            }
            // Always show the model for a model-capable session — even before its model list has
            // loaded — so the current model is visible at all times (with a Reload if the list is empty).
            if model.sessionID != nil, model.modelEditable {
                ToolbarItem(placement: .automatic) {
                    Menu {
                        if model.sessionModels.isEmpty {
                            Button { Task { await model.loadModels() } } label: { Label("Reload models", systemImage: "arrow.clockwise") }
                        } else {
                            ForEach(model.sessionModels) { m in
                                Button { Task { await model.setSessionModel(m) } } label: {
                                    if model.currentModel == m.id { Label(m.name, systemImage: "checkmark") } else { Text(m.name) }
                                }
                            }
                        }
                    } label: {
                        HStack(spacing: 4) {
                            Image(systemName: "cpu").font(.caption2)
                            Text(currentModelLabel).font(.caption).lineLimit(1)
                        }
                    }
                    .help("Model for this session")
                }
            }
            if model.sessionID != nil {
                // Autonomy stays inline — it's a MODE whose on/off state should read at a glance.
                ToolbarItem(placement: .automatic) {
                    Button { Task { await model.setAutonomy(!model.autonomous) } } label: {
                        Label(model.autonomous ? "Autonomous on" : "Autonomous off",
                              systemImage: model.autonomous ? "bolt.circle.fill" : "bolt.circle")
                    }
                    .tint(model.autonomous ? palette.primary : nil)
                    .help(model.autonomous
                          ? "The heartbeat keeps this session going until its to-dos are done. Tap to stop."
                          : "Let the heartbeat nudge this session to keep going until done.")
                }
                // Mode is its own control, not buried in the overflow: a read-only session behaves
                // very differently and the user must be able to see AND change that at a glance.
                ToolbarItem(placement: .automatic) {
                    Menu {
                        Button { Task { await model.setSessionMode(SessionMode.code) } } label: {
                            Label("Code — normal", systemImage: "hammer")
                        }
                        Button { Task { await model.setSessionMode(SessionMode.ask) } } label: {
                            Label("Ask — read-only", systemImage: "magnifyingglass")
                        }
                        Button { Task { await model.setSessionMode(SessionMode.architect) } } label: {
                            Label("Architect — plan first", systemImage: "ruler")
                        }
                    } label: {
                        Label(SessionMode.label(model.sessionMode),
                              systemImage: SessionMode.isRestricted(model.sessionMode) ? "lock.shield" : "hammer")
                    }
                    .help(SessionMode.isRestricted(model.sessionMode)
                          ? "This session is read-only — edits and commands are refused."
                          : "Normal mode. Your approval rules decide what runs without asking.")
                }
                // Everything else folds into ONE labeled menu (was 4 bare icons in the header) — the
                // items carry text labels so it's clear what each does, unlike hover-only tooltips.
                ToolbarItem(placement: .automatic) {
                    Menu {
                        Button { model.codeReviewTarget = model.sessionID } label: {
                            Label("Code & changes", systemImage: "chevron.left.forwardslash.chevron.right")
                        }
                        #if canImport(WebKit)
                        Button { model.designRequested = true } label: {
                            Label("Browser / Design", systemImage: "safari")
                        }
                        #endif
                        // Same host-shell gate as the `!` escape: this runs a command on the machine,
                        // so a non-owner gets a disabled item that explains itself instead of one
                        // that looks live and is refused by the daemon.
                        Button { Task { await model.runTests() } } label: {
                            Label(model.testRunning ? "Running tests…" : "Run tests", systemImage: Self.runTestsSymbol)
                        }
                        .disabled(model.runBusy || model.knownNonOwner)
                        .help(model.knownNonOwner ? model.ownerOnlyReason : "Run this project's test suite")
                        Button { showDelegate = true } label: {
                            Label("Delegate subtask", systemImage: "arrowshape.turn.up.right")
                        }
                        Divider()
                        Button {
                            if let id = model.sessionID { Task { await model.recoverSession(id) } }
                        } label: {
                            Label(model.busy ? "Recovering…" : "Recover session", systemImage: "bandage")
                        }
                        .disabled(model.busy)
                        if isWorktreeSession {
                            Divider()
                            Button { Task { await model.saveCheckpoint() } } label: {
                                Label("Save checkpoint", systemImage: "flag")
                            }
                            Menu {
                                if model.checkpoints.isEmpty {
                                    Text("No checkpoints yet")
                                } else {
                                    ForEach(model.checkpoints) { cp in
                                        Button {
                                            Task { await model.restoreCheckpoint(cp.sha) }
                                        } label: {
                                            Text(cp.label.isEmpty ? "Checkpoint \(cp.sha.prefix(7))" : cp.label)
                                        }
                                    }
                                }
                            } label: {
                                Label("Roll back to…", systemImage: "arrow.uturn.backward")
                            }
                            .onAppear { Task { await model.loadCheckpoints() } }
                        }
                    } label: {
                        Label("More", systemImage: "ellipsis.circle")
                    }
                    .help("Session tools — code & changes, run tests, browser, delegate, recover, checkpoints.")
                }
            }
            if isWorktreeSession {
                ToolbarItem(placement: .primaryAction) {
                    Button { showWorktreePanel = true } label: {
                        Label("Finish worktree", systemImage: "arrow.triangle.branch")
                    }
                    .help("Finish worktree — review and merge changes")
                }
            }
            if model.currentSession?.isWorkspace == true {
                ToolbarItem(placement: .primaryAction) {
                    Button {
                        showWorkspace = true
                        Task { await model.workspaceDiff() }
                    } label: {
                        Label("Review workspace", systemImage: "folder.badge.magnifyingglass")
                    }
                    .help("Review workspace changes across all repos")
                }
            }
            #endif
        }
        // `onDismiss` rather than a timer: the sheet records where it wants to go, and we act once it
        // has actually gone. The sheet's own fallback is a 250ms `Task.sleep` guessing at the dismiss
        // animation, which is a race that a slow device — or Reduce Motion changing the duration —
        // loses. Binding `destination` retires it.
        .sheet(isPresented: $showSessionControls, onDismiss: {
            guard let d = controlsDestination else { return }
            controlsDestination = nil
            switch d {
            case .code:      model.codeReviewTarget = model.sessionID
            case .design:    model.designRequested = true
            case .delegate:  showDelegate = true
            case .worktree:  showWorktreePanel = true
            case .workspace: showWorkspace = true; Task { await model.workspaceDiff() }
            case .usage:     showUsage = true
            }
        }) {
            SessionControlsSheet(
                model: model, palette: palette,
                onClose: { showSessionControls = false },
                onOpenCode: { model.codeReviewTarget = model.sessionID },
                onOpenDesign: { model.designRequested = true },
                onDelegate: { showDelegate = true },
                onWorktree: { showWorktreePanel = true },
                onWorkspace: { showWorkspace = true; Task { await model.workspaceDiff() } },
                onUsage: { showUsage = true },
                destination: $controlsDestination)
        }
        .sheet(isPresented: $showUsage) {
            UsageView(model: model, palette: palette) { showUsage = false }
        }
        .sheet(isPresented: $showWorktreePanel) {
            WorktreePanel(model: model, palette: palette) { showWorktreePanel = false }
        }
        .sheet(isPresented: $showHandoff) {
            if let h = model.activeHandoff {
                HandoffSheet(model: model, entry: h, palette: palette) { showHandoff = false }
            }
        }
        .sheet(isPresented: $showWorkspace) {
            WorkspaceReviewSheet(model: model, palette: palette) { showWorkspace = false }
        }
        .sheet(isPresented: $showDelegate) {
            DelegateSheet(model: model, palette: palette) { showDelegate = false }
        }
    }

    /// Shown when the open session is "stopped" (the daemon restarted and its provider couldn't
    /// re-attach — e.g. a CLI agent, which has no server-side session to resume). Explains the state.
    private var stoppedBanner: some View {
        HStack(spacing: 8) {
            Image(systemName: "moon.zzz.fill").font(.caption)
            Text("This session stopped when the daemon restarted. Its history isn’t restored, but you can start a fresh one in the same folder and agent.")
                .font(.caption).lineLimit(3).fixedSize(horizontal: false, vertical: true)
            Spacer(minLength: 0)
        }
        .foregroundStyle(palette.mutedForeground)
        .padding(.horizontal, 14).padding(.vertical, 8)
        .background(palette.muted.opacity(0.35))
    }

    /// Replaces the composer for a stopped session — you can't message it until it's restarted.
    private var restartFooter: some View {
        HStack(spacing: 10) {
            Button {
                if let id = model.sessionID { Task { await model.restartSession(id) } }
            } label: {
                Label(model.busy ? "Restarting…" : "Restart session", systemImage: "arrow.clockwise")
                    .frame(maxWidth: .infinity)
            }
            .buttonStyle(.borderedProminent).tint(palette.primary).controlSize(.large)
            .disabled(model.busy)
        }
        .padding(14)
        .background(palette.background)
        .overlay(Rectangle().frame(height: 1).foregroundStyle(palette.border), alignment: .top)
    }

    private var worktreeBanner: some View {
        Button { showWorktreePanel = true } label: {
            HStack(spacing: 8) {
                Image(systemName: "arrow.triangle.branch").font(.caption)
                Text(model.currentSession?.branch ?? "worktree").font(.caption).lineLimit(1)
                Spacer()
                Text("Finish").font(.caption.bold())
            }
            .foregroundStyle(palette.primaryText)
            .padding(.horizontal, 14).padding(.vertical, 7)
            .background(palette.primary.opacity(0.10))
        }
        .buttonStyle(.plain)
    }

    @ViewBuilder private var transcript: some View {
        ScrollViewReader { proxy in
            // A plain VStack (not Lazy): LazyVStack ESTIMATES off-screen row heights, and with
            // defaultScrollAnchor(.bottom) those estimate-vs-actual mismatches make the bottom jump as
            // content streams — the up/down bounce on heavy multi-agent runs. VStack lays out exact
            // heights so the anchor is stable; the per-message markdown is memoized so the cost is low.
            // The typing/activity indicator lives BELOW the scroll (see body), not in the content, so
            // its flicker can't change the scroll height and shove the bottom.
            let content = ScrollView {
                VStack(alignment: .leading, spacing: 12) {
                    // Render WINDOW: a huge (taken-over) session opens instantly at the bottom by
                    // laying out only the most recent messages; earlier history loads backwards on
                    // demand. All messages stay in memory — this bounds LAYOUT, not data.
                    // Two different "earlier": more already loaded but not laid out, or more still
                    // on the daemon. The button does whichever applies, so it always means the
                    // same thing to the user.
                    if model.messages.count > visibleWindow || model.hasEarlierHistory {
                        Button {
                            if model.messages.count > visibleWindow {
                                visibleWindow += windowStep
                            } else {
                                Task { await model.loadEarlierHistory() }
                            }
                        } label: {
                            Label(model.loadingEarlier
                                    ? "Loading earlier messages…"
                                    : (model.messages.count > visibleWindow
                                        ? "Show earlier messages (\(model.messages.count - visibleWindow) more)"
                                        : "Load earlier messages"),
                                  systemImage: "arrow.up.circle")
                                .font(.caption).foregroundStyle(palette.mutedForeground)
                                .frame(maxWidth: .infinity).padding(.vertical, 6)
                                .contentShape(Rectangle())
                        }
                        .buttonStyle(.plain)
                        .disabled(model.loadingEarlier)
                    }
                    ForEach(Array(model.messages.suffix(visibleWindow))) { msg in
                        if msg.role == .subagent, let sid = msg.subAgentID {
                            InlineSubAgentCard(model: model, subAgentID: sid, title: msg.text, palette: palette)
                        } else if msg.role == .shell {
                            // Rendered here rather than inside MessageRow because the card needs the
                            // live model: the exit code, and the explicit "hand this to the agent"
                            // action that is the ONLY way this output ever reaches the model.
                            ShellRunCard(model: model, message: msg, palette: palette)
                        } else {
                            MessageRow(message: msg, palette: palette,
                                       sessionID: model.sessionID,
                                       onRetry: msg.delivery == .failed ? { Task { await model.retryFailedMessage() } } : nil,
                                       onUIAction: { c, a, values in Task { await model.invokeUIAction(c, a, values: values) } },
                                       imageLoader: { path in
                                           guard let b = try? await model.fsReadBytes(path) else { return nil }
                                           return Data(base64Encoded: b.data)
                                       })
                                .equatable() // skip rebuilding rows whose message did not change
                        }
                    }
                    GeometryReader { geo in
                        Color.clear.preference(
                            key: TranscriptBottomOffsetKey.self,
                            value: geo.frame(in: .named("transcriptScroll")).maxY
                        )
                    }
                    .frame(height: 1)
                    .id("bottom")
                }
                .padding(16)
            }
            // Breathing room at the top edge. Without it a scrolled transcript butts straight into
            // the to-do bar above it, and the top line is sliced in half — it reads as a layout bug
            // rather than as content continuing upward.
            .modifier(TopScrollInset())
            // Dragging the transcript puts the keyboard away. On a phone the keyboard covers half the
            // conversation, and the only previous way out was tapping a target that wasn't there.
            .scrollDismissesKeyboard(.interactively)
            Group {
                if #available(macOS 14.0, iOS 17.0, *) {
                    // Native bottom anchoring keeps the view pinned as a message streams (text grows
                    // within the last row — no count change). But it does NOT cover the initial load:
                    // `.id(sessionID)` builds a fresh EMPTY ScrollView, the bottom anchor lands on that
                    // empty content, then history arrives as a burst of appends AFTER — pushing content
                    // down while the scroll stays at the (empty) top, so you open at the first message.
                    content.defaultScrollAnchor(.bottom).id(model.sessionID ?? "none")
                } else {
                    content.id(model.sessionID ?? "none")
                }
            }
            .coordinateSpace(name: "transcriptScroll")
            .background {
                GeometryReader { geo in
                    Color.clear
                        .onAppear { transcriptViewportHeight = geo.size.height }
                        .onChange(of: geo.size.height) { transcriptViewportHeight = $0 }
                }
            }
            .onPreferenceChange(TranscriptBottomOffsetKey.self) { bottomY in
                guard bottomY.isFinite, transcriptViewportHeight > 0 else { return }
                let visible = bottomY <= transcriptViewportHeight + 18
                if visible != isTranscriptBottomVisible {
                    isTranscriptBottomVisible = visible
                }
            }
            .overlay(alignment: .bottom) {
                if !isTranscriptBottomVisible {
                    jumpToBottomButton(proxy)
                        .padding(.bottom, 10)
                        .transition(.scale(scale: 0.9).combined(with: .opacity))
                }
            }
            // Re-pin to the bottom, but ONLY during a short window right after a session opens — that's
            // when the async history burst arrives and defaultScrollAnchor (which anchored the empty
            // ScrollView) hasn't caught it. Firing on EVERY message-count change forever made the
            // explicit scrollTo fight defaultScrollAnchor while streaming markdown heights fluctuate,
            // which oscillated the view up/down during heavy multi-agent runs. After the window, native
            // bottom-anchoring alone follows streaming smoothly.
            .onAppear { armInitialAnchor(); anchorBottom(proxy) }
            .onChange(of: model.sessionID) { _ in
                visibleWindow = Self.initialWindow
                isTranscriptBottomVisible = true
                armInitialAnchor()
                anchorBottom(proxy)
            }
            .onChange(of: model.messages.count) { _ in
                if Date() < initialAnchorDeadline { anchorBottom(proxy) }
            }
            // Streaming grows the LAST MESSAGE'S TEXT — `messages.count` never changes — so the
            // count-based re-pin above cannot follow a stream, and it is deadline-gated anyway. On
            // iOS 16 / macOS 13 there is no `defaultScrollAnchor` either, which left the transcript
            // simply not moving while an answer streamed in. This follows the text itself, on every
            // OS version. It fires ONLY while the bottom is already visible: that gate is what stops
            // it from yanking a user who deliberately scrolled up to read.
            .onChange(of: model.messages.last?.text.count) { _ in
                guard isTranscriptBottomVisible else { return }
                followBottom(proxy)
            }
        }
    }

    private func jumpToBottomButton(_ proxy: ScrollViewProxy) -> some View {
        Button {
            withAnimation(.easeOut(duration: 0.18)) {
                proxy.scrollTo("bottom", anchor: .bottom)
            }
            isTranscriptBottomVisible = true
        } label: {
            Image(systemName: "chevron.down")
                .font(.system(size: 15, weight: .semibold))
                .foregroundStyle(palette.foreground)
                .frame(width: 34, height: 34)
                .background(.regularMaterial, in: Circle())
                .overlay(Circle().strokeBorder(palette.border.opacity(0.8)))
                .shadow(color: .black.opacity(scheme == .dark ? 0.28 : 0.14), radius: 10, y: 4)
                .contentShape(Circle())
        }
        .buttonStyle(.plain)
        .accessibilityLabel("Scroll to latest message")
        .help("Scroll to latest message")
    }

    /// How many trailing messages the transcript LAYS OUT (see the window note above). Reset on
    /// session switch so every open starts cheap; "Show earlier" grows it backwards.
    /// How many messages are laid out at once.
    ///
    /// This VStack is deliberately NOT lazy (see the note above — laziness estimated off-screen
    /// heights and fought the bottom anchor), which means every message in the window is laid out
    /// eagerly. That's affordable on a Mac and is not on a phone: 80 rows of markdown, tool cards and
    /// images is the choppiness. A phone gets a smaller window and grows it in smaller steps; the
    /// data is all still in memory either way, this bounds LAYOUT only.
    #if os(macOS)
    static let initialWindow = 80
    private let windowStep = 150
    #else
    static let initialWindow = 25
    private let windowStep = 40
    #endif
    @State private var visibleWindow = Self.initialWindow

    private func armInitialAnchor() {
        initialAnchorDeadline = Date().addingTimeInterval(1.5) // catch the post-open history burst only
    }

    /// The "working…" indicator, OUTSIDE the scroll content — a fixed-height row so it can toggle
    /// on/off (as busy/streaming flip between sub-agent turns) without ever changing the transcript's
    /// scroll height and bouncing the view.
    @ViewBuilder private var typingBar: some View {
        HStack(spacing: 8) {
            // A stalled stream trumps the working chip: if we're busy but no bytes have arrived for a
            // while, the socket may be half-open — offer a one-tap Reconnect (replays the true state
            // from the daemon) instead of leaving an ageless spinner that only a restart would clear.
            if model.streamMaybeStalled {
                StreamStallBar(palette: palette) { Task { await model.forceResync() } }
            } else if model.busy && model.messages.last?.streaming != true {
                // Show the real tool-use indicator whenever the agent is working but not mid-stream
                // (mid-stream, the text itself is the activity). Now carries the concrete command/path
                // so you actually see WHAT it's doing, not just "Running a command".
                VStack(alignment: .leading, spacing: 3) {
                    HStack(spacing: 8) {
                        ToolActivityView(activity: model.activity, palette: palette, detail: model.activityDetail)
                        // Fan-out progress: "sub-agents 19/20" from the Turn Engine's child states, so a
                        // long fanout with one straggler reads as PROGRESS, not a stuck "Delegating…".
                        if let kids = model.turn?.children, !kids.isEmpty {
                            let done = kids.filter { $0.state != "running" }.count
                            Text("sub-agents \(done)/\(kids.count)")
                                .font(.caption.monospacedDigit()).foregroundStyle(palette.mutedForeground)
                                .padding(.horizontal, 6).padding(.vertical, 1)
                                .background(Capsule().fill(palette.muted.opacity(0.4)))
                        }
                        // Daemon-vouched patience: while the Turn Engine says the provider is alive but
                        // quiet, say so honestly ("still working · 47s since output") — never a guessed timeout.
                        if let last = model.turn?.lastEventAt, model.turn?.state == SessionStatusValue.running {
                            QuietAgeBadge(since: Date(timeIntervalSince1970: TimeInterval(last)), palette: palette)
                        }
                    }
                    // The actual reasoning as it streams (when the model emits it): the last line or two,
                    // so "Thinking" shows WHAT it's thinking, not just that it is.
                    if !model.liveThinkingTail.isEmpty {
                        Text(model.liveThinkingTail)
                            .font(.footnote).italic()
                            .foregroundStyle(palette.mutedForeground)
                            .lineLimit(2).truncationMode(.head)
                            .frame(maxWidth: 520, alignment: .leading)
                            .fixedSize(horizontal: false, vertical: true)
                    }
                }
            }
            Spacer(minLength: 0)
        }
        .frame(minHeight: 26, alignment: .leading) // reserve a line; grow for the reasoning tail
        .padding(.horizontal, 16)
        .animation(.easeInOut(duration: 0.2), value: model.streamMaybeStalled)
    }

    /// Instantly (no animation) re-pins the transcript to its last message once appends settle.
    /// Fixes "opens at the first message" without any visible scrolling.
    private func anchorBottom(_ proxy: ScrollViewProxy) {
        anchorTask?.cancel()
        anchorTask = Task { @MainActor in
            try? await Task.sleep(nanoseconds: 90_000_000) // 90ms quiet → the load/turn burst has settled
            guard !Task.isCancelled else { return }
            proxy.scrollTo("bottom", anchor: .bottom) // no withAnimation → instant, no jump
        }
    }

    /// Debounced bottom-follow for a streaming message (see the onChange that calls it). Separate task
    /// from `anchorBottom` so the two can't cancel each other, and debounced because the stream flushes
    /// every ~40ms — one scroll per flush would queue dozens of them per second.
    private func followBottom(_ proxy: ScrollViewProxy) {
        followTask?.cancel()
        followTask = Task { @MainActor in
            try? await Task.sleep(nanoseconds: 120_000_000)
            guard !Task.isCancelled else { return }
            proxy.scrollTo("bottom", anchor: .bottom) // no withAnimation → instant, no bounce
        }
    }

    private static let starters = ["Explain this project", "Find and fix a bug", "Review my changes"]

    /// Compact label for the model menu — the model id (e.g. "gpt-5.4"), or "Default" when the
    /// session is running on the provider's default model (none explicitly chosen).
    private var currentModelLabel: String {
        if let cur = model.currentModel, !cur.isEmpty { return cur }
        return "Default"
    }

    /// A clear "tests" glyph where available; a safe fallback on iOS 16 / macOS 13.
    static var runTestsSymbol: String {
        if #available(iOS 17.0, macOS 14.0, *) { return "testtube.2" }
        return "checkmark.seal.fill"
    }

    private var emptyState: some View {
        VStack(spacing: 14) {
            Spacer()
            Image("WolfMark").resizable().scaledToFit().frame(width: 44, height: 44).opacity(0.9)
            Text("Start a session").font(.title2.weight(.semibold))
            Text("Send a prompt below and an agent gets to work on your Mac — steer it, review tool calls, and approve from anywhere.")
                .font(.subheadline).foregroundStyle(palette.mutedForeground)
                .multilineTextAlignment(.center)
                .frame(maxWidth: 360).fixedSize(horizontal: false, vertical: true)
            HStack(spacing: 8) {
                ForEach(Self.starters, id: \.self) { prompt in
                    Button { draft.wrappedValue = prompt } label: {
                        Text(prompt).font(.footnote)
                            .foregroundStyle(palette.foreground)
                            .padding(.horizontal, 12).padding(.vertical, 7)
                            .background(Capsule().fill(palette.muted.opacity(0.45)))
                            .overlay(Capsule().strokeBorder(palette.border))
                    }
                    .buttonStyle(.plain)
                }
            }
            .padding(.top, 4)
            Spacer()
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .padding(.horizontal, 32)
    }

    /// The active session's load/background error, if any — so a broken session shows WHY instead of a
    /// blank pane when you switch to it.
    private var sessionLoadError: String? {
        guard let sid = model.sessionID else { return nil }
        return model.sessionErrors[sid]
    }

    /// Shown while a just-opened session's transcript is replaying — makes a swap read as "loading…"
    /// instead of a white flash.
    /// A skeleton of the conversation rather than a spinner on blank.
    ///
    /// Two reasons it's shaped like this. A spinner tells you nothing is here yet; a skeleton tells
    /// you what is ABOUT to be here, which reads as faster even at identical latency. And it is
    /// BOTTOM-ALIGNED, mirroring the real transcript — so when the actual messages swap in, the
    /// visual mass is already where they'll land and the view doesn't lurch from centre to bottom.
    private var sessionLoadingView: some View {
        TranscriptSkeleton(palette: palette)
    }

    private func sessionErrorView(_ err: String) -> some View {
        VStack(spacing: OculusSpace.md) {
            Image(systemName: "exclamationmark.triangle.fill").font(.largeTitle).foregroundStyle(palette.destructive)
            Text("Couldn’t load this session").font(.headline).foregroundStyle(palette.foreground)
            Text(err).font(.callout).foregroundStyle(palette.mutedForeground)
                .multilineTextAlignment(.center).frame(maxWidth: 420).fixedSize(horizontal: false, vertical: true)
            Button { if let id = model.sessionID { Task { await model.recoverSession(id) } } } label: {
                Label("Recover session", systemImage: "bandage")
            }
            .buttonStyle(.borderedProminent).tint(palette.primary).disabled(model.busy)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .padding(OculusSpace.xl)
    }

    /// The two header readings, derived once per body evaluation.
    private var headerStatus: HeaderStatus {
        deriveHeaderStatus(connected: model.connected, connecting: model.connecting,
                           rawStatus: model.status, sessionStatus: model.currentSession?.status,
                           busy: model.busy, awaitingApproval: model.pendingApproval != nil)
    }

    /// What the AGENT is doing — never the transport. The connection lives in `connectionBanner`.
    private var statusLabel: String {
        let h = headerStatus
        // Offline, every session word is a memory. Say so rather than presenting it as observed.
        return h.stale && h.session != "unknown" ? "last: \(h.session)" : h.session
    }

    /// The connection's own row, separate from the session status by construction. Absent when
    /// connected — a healthy link is signalled by having nothing to say.
    @ViewBuilder private var connectionBanner: some View {
        if let label = headerStatus.connectionLabel {
            HStack(spacing: 8) {
                if headerStatus.connection == .connecting {
                    ProgressView().controlSize(.mini)
                } else {
                    Circle().fill(palette.destructive).frame(width: 6, height: 6)
                }
                Text(label).font(.caption.weight(.semibold)).foregroundStyle(palette.foreground)
                if let detail = model.statusDetail, !detail.isEmpty {
                    Text(detail).font(.caption).foregroundStyle(palette.mutedForeground)
                        .lineLimit(1).truncationMode(.tail)
                }
                Spacer(minLength: 0)
                if headerStatus.connection == .offline {
                    Button("Reconnect") { Task { await model.connect() } }
                        .font(.caption.weight(.semibold)).buttonStyle(.plain)
                        .foregroundStyle(palette.primaryText)
                }
            }
            .padding(.horizontal, 14).padding(.vertical, 7)
            // Offline is a problem you can act on (red wash); connecting is just in progress, so it
            // gets the same neutral chrome as the other banners rather than an alarm colour.
            .background(headerStatus.connection == .offline
                        ? palette.destructive.opacity(0.12) : palette.muted.opacity(0.35))
        }
    }

    private func describe(_ d: Discovered) -> String {
        if d.kind == DiscoveredKind.server { return "◆ opencode \(d.url ?? "")" }
        if d.provider == "opencode" { return "  ● \(d.title ?? d.sessionID ?? "session")" }
        return "◆ claude-code \(d.cwd ?? d.sessionID ?? "")"
    }
}

// MARK: - Message row

/// Renders the images a message REFERENCES by path ("[Image: source: /abs/path.png]" — the shape
/// claude transcripts use for pasted screenshots) as inline thumbnails, loaded through the daemon
/// (fs.readbytes) so they work from any device, not just the Mac that owns the files.
struct InlineImagesView: View {
    let paths: [String]
    let palette: OculusPalette
    let load: (String) async -> Data?
    @State private var images: [String: CGImage] = [:]

    static func imagePaths(in text: String) -> [String] {
        guard text.contains("[Image: source: ") else { return [] }
        var out: [String] = []
        var rest = Substring(text)
        while let r = rest.range(of: "[Image: source: ") {
            rest = rest[r.upperBound...]
            if let end = rest.firstIndex(of: "]") {
                let p = String(rest[..<end]).trimmingCharacters(in: .whitespaces)
                if p.hasPrefix("/") { out.append(p) }
                rest = rest[rest.index(after: end)...]
            } else { break }
        }
        return Array(out.prefix(6)) // bound the fan-out
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            ForEach(paths, id: \.self) { p in
                if let cg = images[p] {
                    Image(decorative: cg, scale: 1)
                        .resizable().scaledToFit()
                        .frame(maxWidth: 420, maxHeight: 280)
                        .clipShape(OculusShape.rounded(OculusRadius.sm))
                        .overlay(OculusShape.rounded(OculusRadius.sm).strokeBorder(palette.border))
                } else {
                    HStack(spacing: 6) {
                        Image(systemName: "photo").font(.caption)
                        Text((p as NSString).lastPathComponent).font(.caption2).lineLimit(1).truncationMode(.middle)
                    }
                    .foregroundStyle(palette.mutedForeground)
                    .task {
                        if images[p] == nil, let data = await load(p),
                           let src = CGImageSourceCreateWithData(data as CFData, nil),
                           let cg = CGImageSourceCreateImageAtIndex(src, 0, nil) {
                            images[p] = cg
                        }
                    }
                }
            }
        }
    }
}

/// A transcript row.
///
/// Equatable on purpose: while a turn streams, `model.messages` changes on every flush, which
/// re-evaluates the whole non-lazy VStack. Without an equality check SwiftUI rebuilds the body of
/// every row on every frame even though only the LAST message actually changed. Conforming lets it
/// skip the untouched rows — the single biggest win while an agent is talking.
///
/// The closures are deliberately excluded from equality: they're recreated per rebuild and would
/// make every row compare unequal, defeating the whole thing. They're derived from `message` and the
/// model, so a row whose message is unchanged behaves identically with either copy.
struct MessageRow: View, Equatable {
    static func == (a: MessageRow, b: MessageRow) -> Bool {
        a.message == b.message && a.palette == b.palette && a.sessionID == b.sessionID
            && (a.onRetry == nil) == (b.onRetry == nil)
    }

    let message: ChatMessage
    let palette: OculusPalette
    var sessionID: String? = nil
    var onRetry: (() -> Void)? = nil
    /// Fired when the user activates a generative-UI component's action (choice/confirm). The
    /// transcript wires this to Model.invokeUIAction.
    /// Third argument carries a form's collected values (nil for every other component).
    var onUIAction: ((UIComponent, UIComponentAction, [String: JSONValue]?) -> Void)? = nil
    /// Loads referenced-image bytes via the daemon (nil = inline images off, e.g. child transcripts).
    var imageLoader: ((String) async -> Data?)? = nil
    // Mirror ChatMarkdownView's type prefs so the whole transcript (user bubble, thinking, streaming
    // plain text) shares the chosen font, not just finalized assistant markdown.
    @AppStorage("oculus.chatFontDesign") private var fontDesignRaw = ChatFontDesign.system.rawValue
    @AppStorage("oculus.chatFontScale") private var fontScaleRaw = ChatFontScale.standard.rawValue
    private var chosenFont: ChatFontDesign { ChatFontDesign(rawValue: fontDesignRaw) ?? .system }
    private var chatDesign: Font.Design { chosenFont.design }
    /// The AGENT-RESPONSE face. Streaming plain text and finalized markdown must use the SAME face
    /// and size, or the whole answer visibly re-sets itself the instant the turn ends — the jump this
    /// view goes to some length elsewhere to avoid.
    private var responseDesign: Font.Design { chosenFont.responseDesign }
    private var responseBody: Font { .system(size: chosenFont.responseSize(15) * chatFactor, design: responseDesign) }
    private var chatFactor: CGFloat { ChatFontScale(rawValue: fontScaleRaw)?.factor ?? 1.0 }
    private var chatBody: Font { .system(size: 15 * chatFactor, design: chatDesign) }
    // Real leading — transcript prose was rendered with default (tight) line spacing, which read
    // cramped. ~4pt (scaled) opens it up so multi-line answers breathe like a document.
    /// Leading for the non-markdown text paths (user bubble, thinking, plain streamed text). Kept in
    /// step with ChatMarkdownView so a turn doesn't change rhythm halfway down as it finalizes.
    private var chatLineSpacing: CGFloat { 6 * chatFactor }

    var body: some View {
        switch message.role {
        case .user:
            VStack(alignment: .trailing, spacing: 3) {
                // Attribution appears ONLY for a message from another device — labelling your own
                // messages with your own name is noise in the overwhelmingly common single-user case.
                if let author = message.author, !author.isEmpty {
                    HStack(spacing: 4) {
                        Image(systemName: "person.crop.circle").font(.caption2)
                        Text(author).font(.caption2.weight(.medium))
                    }
                    .foregroundStyle(palette.mutedForeground)
                    .padding(.trailing, 2)
                }
                HStack {
                    Spacer(minLength: 40)
                    Text(message.text)
                        .font(chatBody)
                        .lineSpacing(chatLineSpacing)
                        .foregroundStyle(palette.foreground)
                        .padding(.horizontal, OculusSpace.md).padding(.vertical, OculusSpace.sm)
                        .background(palette.secondary)
                        .overlay(OculusShape.rounded(OculusRadius.md)
                            .strokeBorder(message.delivery == .failed ? palette.destructive : palette.border))
                        .clipShape(OculusShape.rounded(OculusRadius.md))
                        .textSelection(.enabled)
                }
                if let load = imageLoader {
                    let paths = InlineImagesView.imagePaths(in: message.text)
                    if !paths.isEmpty { InlineImagesView(paths: paths, palette: palette, load: load) }
                }
                // Delivery badge: a failed send is visibly flagged + retryable instead of looking
                // exactly like a delivered message (the silent-loss trap).
                switch message.delivery {
                case .sending:
                    Text("Sending…").font(.caption2).foregroundStyle(palette.mutedForeground)
                case .failed:
                    HStack(spacing: 6) {
                        Image(systemName: "exclamationmark.triangle.fill").font(.caption2)
                        Text("Not delivered").font(.caption2)
                        if let onRetry {
                            Button("Retry") { onRetry() }.font(.caption2.bold()).buttonStyle(.plain)
                        }
                    }
                    .foregroundStyle(palette.destructive)
                case .ok:
                    EmptyView()
                }
            }
        case .assistant:
            if message.text.isEmpty && message.streaming {
                Text("…").font(.body).frame(maxWidth: .infinity, alignment: .leading)
            } else if message.streaming {
                // While streaming, render PLAIN text. Foundation's markdown init (AttributedString)
                // is heavy; re-parsing the whole growing message on every ~40ms flush stalls the
                // main thread and makes the stream stutter. We keep the live text flowing cheaply and
                // snap to full markdown the instant the turn ends (the `else` below). Line breaks are
                // preserved so lists/paragraphs still read naturally mid-stream.
                Text(message.text)
                    .font(responseBody) // same face/size the finalized markdown will use
                    .lineSpacing(chatLineSpacing)
                    .foregroundStyle(palette.foreground)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .textSelection(.enabled)
            } else {
                let segments = AssistantContentParser.parse(message.text, sessionID: sessionID ?? "", messageID: message.id.uuidString)
                VStack(alignment: .leading, spacing: 8) {
                    ForEach(segments) { segment in
                        switch segment.kind {
                        case .markdown(let text):
                            ChatMarkdownView(text: text, palette: palette)
                                .lineSpacing(chatLineSpacing)
                                .textSelection(.enabled)
                        case .component(let component):
                            UIComponentView(component: component, palette: palette,
                                            onAction: { action, values in onUIAction?(component, action, values) })
                        }
                    }
                    if let load = imageLoader {
                        let paths = InlineImagesView.imagePaths(in: message.text)
                        if !paths.isEmpty { InlineImagesView(paths: paths, palette: palette, load: load) }
                    }
                }
            }
        case .thinking:
            HStack(alignment: .top, spacing: 6) {
                Image(systemName: "brain").font(.caption2).padding(.top, 2)
                Text(message.text).font(.system(size: 13.5 * chatFactor, design: chatDesign)).italic().lineSpacing(chatLineSpacing)
                    .textSelection(.enabled)
            }
            .foregroundStyle(palette.mutedForeground)
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(.leading, 2)
        case .tool:
            if let call = message.tool {
                ToolCallCard(call: call, palette: palette)
            } else {
                // Legacy plain tool note (approvals etc.).
                HStack(spacing: 8) {
                    Image(systemName: "wrench.and.screwdriver.fill").font(.caption2)
                    Text(message.text).font(.system(.caption, design: .monospaced))
                }
                .foregroundStyle(palette.accentForeground)
                .padding(.horizontal, 12).padding(.vertical, 8)
                .frame(maxWidth: .infinity, alignment: .leading)
                .background(palette.accent)
                .overlay(OculusShape.rounded(10).strokeBorder(palette.primary.opacity(0.25)))
                .clipShape(OculusShape.rounded(10))
            }
        case .subagent:
            // The rich inline card is rendered at the transcript level (it needs the live model); this
            // is a minimal fallback for any context that renders a MessageRow directly.
            Label(message.text.isEmpty ? "Sub-agent" : message.text, systemImage: "person.2.fill")
                .font(.caption).foregroundStyle(palette.mutedForeground)
        case .shell:
            // Same split as .subagent: ShellRunCard needs the model, so this is the fallback for any
            // context rendering a MessageRow directly (e.g. a sub-agent's nested transcript).
            Label("! \(message.text)", systemImage: "terminal")
                .font(.system(.caption, design: .monospaced)).foregroundStyle(palette.mutedForeground)
        case .system:
            Text(message.text).font(.caption).foregroundStyle(palette.mutedForeground)
                .frame(maxWidth: .infinity, alignment: .center)
        case .ui:
            if let c = message.component {
                UIComponentView(component: c, palette: palette, onAction: { a, values in onUIAction?(c, a, values) })
            }
        }
    }
}

struct AssistantContentSegment: Identifiable {
    enum Kind {
        case markdown(String)
        case component(UIComponent)
    }

    let id = UUID()
    let kind: Kind
}

enum AssistantContentParser {
    static func parse(_ text: String, sessionID: String, messageID: String) -> [AssistantContentSegment] {
        var segments: [AssistantContentSegment] = []
        var cursor = text.startIndex
        var scan = text.startIndex

        while let open = text[scan...].firstIndex(of: "{") {
            guard let close = matchingBrace(in: text, from: open) else { break }
            let candidate = String(text[open...close])
            if isStandaloneObject(in: text, open: open, close: close),
               !isInsideFence(text, at: open),
               let component = decodeComponent(candidate, sessionID: sessionID, messageID: messageID) {
                appendMarkdown(String(text[cursor..<open]), to: &segments)
                segments.append(AssistantContentSegment(kind: .component(component)))
                cursor = text.index(after: close)
                scan = cursor
            } else {
                scan = text.index(after: open)
            }
        }

        appendMarkdown(String(text[cursor...]), to: &segments)
        return segments.isEmpty ? [AssistantContentSegment(kind: .markdown(text))] : segments
    }

    private static func appendMarkdown(_ text: String, to segments: inout [AssistantContentSegment]) {
        guard !text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty else { return }
        segments.append(AssistantContentSegment(kind: .markdown(text)))
    }

    private static func matchingBrace(in text: String, from open: String.Index) -> String.Index? {
        var depth = 0
        var inString = false
        var escaped = false
        var i = open
        while i < text.endIndex {
            let ch = text[i]
            if inString {
                if escaped {
                    escaped = false
                } else if ch == "\\" {
                    escaped = true
                } else if ch == "\"" {
                    inString = false
                }
            } else if ch == "\"" {
                inString = true
            } else if ch == "{" {
                depth += 1
            } else if ch == "}" {
                depth -= 1
                if depth == 0 { return i }
            }
            i = text.index(after: i)
        }
        return nil
    }

    private static func isStandaloneObject(in text: String, open: String.Index, close: String.Index) -> Bool {
        let lineStart = text[..<open].lastIndex(of: "\n").map { text.index(after: $0) } ?? text.startIndex
        let afterClose = text.index(after: close)
        let lineEnd = text[afterClose...].firstIndex(of: "\n") ?? text.endIndex
        return String(text[lineStart..<open]).trimmingCharacters(in: .whitespaces).isEmpty
            && String(text[afterClose..<lineEnd]).trimmingCharacters(in: .whitespaces).isEmpty
    }

    private static func isInsideFence(_ text: String, at index: String.Index) -> Bool {
        var inside = false
        for line in text[..<index].components(separatedBy: "\n") {
            if line.trimmingCharacters(in: .whitespaces).hasPrefix("```") {
                inside.toggle()
            }
        }
        return inside
    }

    private static func decodeComponent(_ json: String, sessionID: String, messageID: String) -> UIComponent? {
        guard let data = json.data(using: .utf8),
              var object = (try? JSONSerialization.jsonObject(with: data)) as? [String: Any],
              object["component"] is String,
              object["id"] is String,
              object["props"] != nil else { return nil }
        object["session_id"] = object["session_id"] ?? sessionID
        object["message_id"] = object["message_id"] ?? messageID
        object["schema_v"] = object["schema_v"] ?? 1
        object["status"] = object["status"] ?? "ready"
        object["fallback_text"] = object["fallback_text"] ?? ""
        guard JSONSerialization.isValidJSONObject(object),
              let normalized = try? JSONSerialization.data(withJSONObject: object) else { return nil }
        return try? ProtocolCoding.decoder().decode(UIComponent.self, from: normalized)
    }
}

/// Puts a string on the system pasteboard. One helper for the whole chat surface, because "copy" has
/// to behave identically whether it came from a code block, a tool card or a message context menu.
func chatCopyToPasteboard(_ text: String) {
    #if canImport(AppKit)
    NSPasteboard.general.clearContents(); NSPasteboard.general.setString(text, forType: .string)
    #elseif canImport(UIKit)
    UIPasteboard.general.string = text
    #endif
}

/// A persistent copy control for monospaced content (code blocks, tool output).
///
/// Persistent rather than hover-revealed, and sized to the 44pt minimum target: on touch there is no
/// hover, and `.textSelection` inside a horizontally-scrolling container is unusable there anyway —
/// long-press-then-drag fights the pan gesture, so selection was never a real copy path on a phone.
struct CopyContentButton: View {
    let text: String
    let palette: OculusPalette
    var label: String = "Copy"
    @State private var copied = false

    var body: some View {
        Button {
            chatCopyToPasteboard(text)
            copied = true
            Task { try? await Task.sleep(nanoseconds: 1_400_000_000); copied = false }
        } label: {
            Image(systemName: copied ? "checkmark" : "doc.on.doc")
                .font(.caption)
                .foregroundStyle(copied ? palette.success : palette.mutedForeground)
                .frame(width: 44, height: 44)
                .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .accessibilityLabel(copied ? "Copied" : label)
        .help(label)
    }
}

/// A tool call as a distinct, collapsible inline card — the invocation (icon · tool · command) reads
/// separately from the agent's prose, and the OUTPUT expands on demand rather than hiding behind a
/// "running…" chip. A running tool shows a spinner; completed/error show a result affordance.
struct ToolCallCard: View {
    let call: ToolCall
    let palette: OculusPalette
    @State private var expanded = false

    private var running: Bool { call.status == "running" }
    private var isError: Bool { call.status == "error" }
    private var hasOutput: Bool { !call.output.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty }

    /// How many trailing lines of output the card lays out. Bounds LAYOUT, not what Copy gives you.
    private static let outputTailLines = 60

    /// The TAIL of the output, plus an honest header when it was cut.
    ///
    /// The old card rendered the output from the top inside a `ScrollView(.horizontal)` capped at
    /// 220pt — a horizontal-only scroll view with a height clamp, so anything past ~17 lines was
    /// clipped with no gesture that could reach it. Expanding a failed bash card to find the error
    /// showed the first 17 lines and hid the failure. Errors live at the END of output, which is why
    /// ShellRunCard already shows the tail; this matches it.
    private var outputPreview: (text: String, truncated: Int, total: Int) {
        let trimmed = call.output.hasSuffix("\n") ? String(call.output.dropLast()) : call.output
        let all = trimmed.components(separatedBy: "\n")
        guard all.count > Self.outputTailLines else { return (trimmed, 0, all.count) }
        return (all.suffix(Self.outputTailLines).joined(separator: "\n"),
                all.count - Self.outputTailLines, all.count)
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack(spacing: 8) {
                Image(systemName: icon).font(.caption2).foregroundStyle(tint).frame(width: 14)
                Text(call.name).font(.system(.caption, design: .monospaced).bold()).foregroundStyle(palette.foreground)
                if !call.title.isEmpty {
                    Text(call.title).font(.system(.caption, design: .monospaced))
                        .foregroundStyle(palette.mutedForeground).lineLimit(1).truncationMode(.middle)
                }
                Spacer(minLength: 6)
                if running {
                    // Live elapsed so a long-running (or stuck) tool is visibly still going.
                    ElapsedLabel(since: call.startedAt, palette: palette)
                    ProgressView().controlSize(.mini)
                } else {
                    // An explicit outcome mark. "Finished" and "finished with an error" used to differ
                    // only by a tint on the icon and a border alpha — two things you have to already
                    // know to look for. Success and failure now each have their own glyph.
                    Image(systemName: isError ? "exclamationmark.triangle.fill" : "checkmark.circle.fill")
                        .font(.caption2)
                        .foregroundStyle(isError ? palette.destructive : palette.success)
                        .accessibilityLabel(isError ? "Failed" : "Succeeded")
                    if hasOutput {
                        Image(systemName: expanded ? "chevron.up" : "chevron.down")
                            .font(.caption2).foregroundStyle(palette.mutedForeground)
                    }
                }
            }
            if expanded, hasOutput {
                let preview = outputPreview
                VStack(alignment: .leading, spacing: 0) {
                    HStack(spacing: 4) {
                        if preview.truncated > 0 {
                            Text("showing the last \(Self.outputTailLines) of \(preview.total) lines — Copy takes all of it")
                                .font(.caption2).foregroundStyle(palette.mutedForeground)
                        }
                        Spacer(minLength: 0)
                        // Copy is on the FULL output, not the preview: the whole point of bounding the
                        // layout is that you can still get at what isn't drawn.
                        CopyContentButton(text: call.output, palette: palette, label: "Copy \(call.name) output")
                    }
                    // No height clamp: the row grows to fit the (already tail-bounded) preview and the
                    // transcript scrolls it, so nothing is unreachable.
                    ScrollView(.horizontal, showsIndicators: false) {
                        Text(preview.text).font(.system(.caption2, design: .monospaced))
                            .foregroundStyle(isError ? palette.destructive : palette.foreground)
                            .textSelection(.enabled)
                    }
                }
                .transition(.opacity.combined(with: .move(edge: .top)))
            }
        }
        .padding(.horizontal, 12).padding(.vertical, 8)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(palette.secondary.opacity(0.3), in: OculusShape.rounded(10))
        .overlay(OculusShape.rounded(10).strokeBorder((isError ? palette.destructive : palette.primary).opacity(running ? 0.4 : 0.2)))
        // Tap ANYWHERE on the card to expand/collapse its output. This is its own tap target, so a tool
        // card nested inside a sub-agent card handles its own taps and never collapses the parent.
        .contentShape(Rectangle())
        .onTapGesture {
            guard hasOutput else { return }
            withAnimation(.easeInOut(duration: 0.2)) { expanded.toggle() }
        }
        // A tap-gesture card is invisible to VoiceOver as a control: no trait, no label, no state. It
        // has to say what it is, what it did, and whether it's open.
        .accessibilityAddTraits(.isButton)
        .accessibilityLabel("\(call.name) tool call\(call.title.isEmpty ? "" : ", \(call.title)"), \(running ? "running" : (isError ? "failed" : "succeeded"))")
        .accessibilityValue(hasOutput ? (expanded ? "Expanded" : "Collapsed") : "No output")
    }

    private var icon: String {
        switch call.name {
        case "bash": return "terminal"
        case "read": return "doc.text"
        case "edit", "write": return "pencil"
        case "grep", "glob", "list": return "magnifyingglass"
        case "webfetch", "fetch": return "globe"
        default: return "wrench.and.screwdriver"
        }
    }
    private var tint: Color { isError ? palette.destructive : palette.primary }
}

/// A `!command` YOU ran on the host — the command, its streaming output, and how it exited.
///
/// It borrows ToolCallCard's shape deliberately (same radius, padding, mono caption, tap-to-expand)
/// so the transcript keeps one visual language for "a thing ran". What it does NOT borrow is the
/// agent's identity: the leading glyph is `!` in the primary tint with a "you" tag, and a finished
/// row says in plain words that the agent hasn't seen the output. Dressing this as an agent tool
/// call would be a lie the user would act on — they'd follow up with "fix that error" and the model
/// would have no idea what error.
struct ShellRunCard: View {
    @ObservedObject var model: Model
    let message: ChatMessage
    let palette: OculusPalette
    @State private var expanded = true // output is the reason you ran it — start open, unlike a tool card

    private var call: ToolCall? { message.tool }
    private var running: Bool { call?.status == "running" }
    private var failed: Bool { call?.status == "error" }
    private var output: String { call?.output ?? "" }
    private var hasOutput: Bool { !output.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty }
    private var exitCode: Int? { model.shellExit[message.id] }

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack(spacing: OculusSpace.sm) {
                Image(systemName: "terminal").font(.caption2).foregroundStyle(tint).frame(width: 14)
                Text("!").font(.system(.caption, design: .monospaced).bold()).foregroundStyle(tint)
                Text(message.text)
                    .font(.system(.caption, design: .monospaced))
                    .foregroundStyle(palette.foreground)
                    .lineLimit(expanded ? 4 : 1).truncationMode(.middle)
                    .textSelection(.enabled)
                Spacer(minLength: 6)
                if running {
                    ElapsedLabel(since: call?.startedAt ?? Date(), palette: palette)
                    ProgressView().controlSize(.mini)
                } else if let code = exitCode {
                    Text(BangCommand.resultLabel(ok: !failed, exitCode: code))
                        .font(.system(.caption2, design: .monospaced).weight(.semibold))
                        .foregroundStyle(failed ? palette.destructive : palette.mutedForeground)
                        .padding(.horizontal, 5).padding(.vertical, 1)
                        .background(Capsule().fill(palette.muted.opacity(0.45)))
                }
                if hasOutput {
                    Image(systemName: expanded ? "chevron.up" : "chevron.down")
                        .font(.caption2).foregroundStyle(palette.mutedForeground)
                }
            }
            // Expand/collapse hangs off the HEADER only, not the whole card. Putting it on the card
            // would make it compete with the "Send to agent" button and with selecting the output —
            // and an accidental collapse while you're reading the output is exactly the annoyance.
            .contentShape(Rectangle())
            .onTapGesture {
                guard hasOutput else { return }
                withAnimation(.easeInOut(duration: 0.2)) { expanded.toggle() }
            }
            .accessibilityElement(children: .combine)
            .accessibilityAddTraits(.isButton)
            .accessibilityLabel("Command you ran: \(message.text)\(running ? ", running" : (failed ? ", failed" : ", finished"))")
            .accessibilityValue(hasOutput ? (expanded ? "Expanded" : "Collapsed") : "No output")
            if expanded, hasOutput {
                // Horizontal scroll only (matching ToolCallCard) and NO height clamp: the row grows
                // to fit and the transcript scrolls it. A vertical clamp here would show the TOP of
                // the output and hide the end — which for a command you just ran is the only part
                // you wanted. The line count is bounded by previewOutput instead.
                ScrollView(.horizontal, showsIndicators: false) {
                    Text(BangCommand.previewOutput(output))
                        .font(.system(.caption2, design: .monospaced))
                        .foregroundStyle(failed ? palette.destructive : palette.foreground.opacity(0.9))
                        .textSelection(.enabled)
                }
            }
            if !running {
                // The agent-context boundary, stated rather than implied. Claude Code's `!` output is
                // not fed to the model unless you ask, and a user who ASSUMES it was fed will write a
                // follow-up ("fix that") that lands with no referent.
                HStack(spacing: OculusSpace.sm) {
                    Text("You ran this — the agent hasn't seen it.")
                        .font(.caption2).foregroundStyle(palette.mutedForeground)
                    Spacer(minLength: 6)
                    if hasOutput {
                        // Copy takes the FULL output, not the tail-bounded preview above it.
                        CopyContentButton(text: output, palette: palette, label: "Copy command output")
                    }
                    Button {
                        Task { await model.send(BangCommand.shareMessage(command: message.text, output: output)) }
                    } label: {
                        Label("Send to agent", systemImage: "arrowshape.turn.up.right")
                            .font(.caption2)
                    }
                    .buttonStyle(.plain).foregroundStyle(palette.primaryText)
                    .help("Paste this command and its output into the conversation as a message from you.")
                }
            }
        }
        .padding(.horizontal, OculusSpace.md).padding(.vertical, OculusSpace.sm)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(palette.secondary.opacity(0.3), in: OculusShape.rounded(OculusRadius.sm))
        .overlay(OculusShape.rounded(OculusRadius.sm)
            .strokeBorder((failed ? palette.destructive : palette.primary).opacity(running ? 0.4 : 0.2)))
    }

    private var tint: Color { failed ? palette.destructive : palette.primary }
}

/// A tiny live "elapsed" readout (ticks ~1×/s) — shows how long a running tool or sub-agent has been
/// going, so a stuck one reads as "stuck at 2:14" instead of an indefinite, ageless spinner.
struct ElapsedLabel: View {
    let since: Date
    let palette: OculusPalette
    var body: some View {
        TimelineView(.periodic(from: .now, by: 1)) { ctx in
            Text(Self.format(ctx.date.timeIntervalSince(since)))
                .font(.system(.caption2, design: .monospaced)).monospacedDigit()
                .foregroundStyle(palette.mutedForeground)
        }
    }
    static func format(_ s: TimeInterval) -> String {
        let t = max(0, Int(s))
        return t < 60 ? "\(t)s" : String(format: "%d:%02d", t / 60, t % 60)
    }
}

/// "still working · 47s since output" — shown while the daemon's Turn Engine vouches the agent is
/// alive but it hasn't produced output for a bit. Honest patience instead of a guessed timeout;
/// only renders once the quiet stretch is noticeable.
struct QuietAgeBadge: View {
    let since: Date
    let palette: OculusPalette
    var body: some View {
        TimelineView(.periodic(from: .now, by: 1)) { ctx in
            let age = ctx.date.timeIntervalSince(since)
            if age > 20 {
                Text("still working · \(ElapsedLabel.format(age)) since output")
                    .font(.caption2).foregroundStyle(palette.mutedForeground)
            }
        }
    }
}

/// Shown in the working bar when the stream has gone quiet mid-turn (possible half-open socket). It's a
/// gentle, non-error hint with a one-tap Reconnect — the manual escape from a stuck stream that used to
/// require quitting and relaunching the app.
struct StreamStallBar: View {
    let palette: OculusPalette
    let onReconnect: () -> Void
    var body: some View {
        HStack(spacing: 6) {
            // A stalled stream is a WARNING, not a failure — nothing has gone wrong yet, we just
            // stopped hearing. palette.warning carries that, and unlike the hardcoded amber it
            // darkens for light mode instead of glowing off a white background.
            Image(systemName: "wifi.exclamationmark").font(.caption).foregroundStyle(palette.warning)
            Text("No updates for a while — the stream may be stuck")
                .font(.caption).foregroundStyle(palette.foreground).lineLimit(1)
            Button(action: onReconnect) {
                Text("Reconnect").font(.caption.bold())
            }
            .buttonStyle(.plain).foregroundStyle(palette.primaryText)
        }
        .padding(.horizontal, 8).padding(.vertical, 3)
        .background(Capsule().fill(palette.warning.opacity(0.12)))
        .overlay(Capsule().strokeBorder(palette.warning.opacity(0.35)))
    }
}

/// Turns the raw activity string ("running bash", "running read", …) the daemon emits from the
/// agent's tool calls into a real tool-use indicator: a per-tool icon + a human label + a live
/// pulse — instead of a generic "working" blob. Falls back to "Thinking" when no tool is active.
struct ToolActivityView: View {
    let activity: String?
    let palette: OculusPalette
    /// compact = topbar pill (icon + short word); full = the working bar (icon + phrase + dots).
    var compact = false
    /// The concrete thing being done right now (a command line, a file path) — appended to the full
    /// bar so it reads "Running a command · npm test" instead of a contentless label. Ignored when compact.
    var detail: String? = nil
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @State private var pulse = false

    var body: some View {
        let t = Self.describe(activity)
        HStack(spacing: 6) {
            Image(systemName: t.icon)
                .font(.caption).foregroundStyle(palette.primaryText)
                .scaleEffect(pulse ? 1.0 : 0.82)
                .opacity(pulse ? 1 : 0.65)
                // With Reduce Motion the icon settles at full size/opacity rather than disappearing:
                // this chip is the only "the agent is doing something" signal in the bar, so it has to
                // stay legible when it stops moving.
                .animation(reduceMotion ? nil : .easeInOut(duration: 0.7).repeatForever(autoreverses: true),
                           value: pulse)
            Text(compact ? t.short : t.label)
                .font(.caption.weight(.medium)).foregroundStyle(palette.foreground).lineLimit(1)
            if !compact, let d = detail?.trimmingCharacters(in: .whitespacesAndNewlines), !d.isEmpty {
                Text("· \(d)")
                    .font(.system(.caption, design: .monospaced))
                    .foregroundStyle(palette.mutedForeground)
                    .lineLimit(1).truncationMode(.middle)
            }
            if !compact { TypingDots(palette: palette) }
        }
        .padding(.horizontal, 8).padding(.vertical, 3)
        .background(Capsule().fill(palette.primary.opacity(0.12)))
        .overlay(Capsule().strokeBorder(palette.primary.opacity(0.18)))
        .onAppear { pulse = true }
    }

    /// Maps a tool name to (SF Symbol, long label, short word). Kept broad so opencode/claude/pi
    /// tool names all land somewhere sensible.
    static func describe(_ activity: String?) -> (icon: String, label: String, short: String) {
        let tool = (activity ?? "")
            .lowercased()
            .replacingOccurrences(of: "running ", with: "")
            .trimmingCharacters(in: .whitespaces)
        func has(_ ss: String...) -> Bool { ss.contains { tool.contains($0) } }
        switch true {
        case tool.isEmpty:                              return ("waveform", "Thinking", "Thinking")
        case has("bash", "shell", "exec", "command"):   return ("terminal", "Running a command", "Command")
        case has("edit", "write", "patch", "apply", "create"): return ("pencil.line", "Editing files", "Editing")
        case has("read", "cat", "view", "open"):        return ("doc.text", "Reading a file", "Reading")
        case has("grep", "search", "glob", "find", "ripgrep"): return ("magnifyingglass", "Searching the code", "Searching")
        case has("fetch", "web", "http", "url", "curl"): return ("globe", "Fetching from the web", "Web")
        case has("todo"):                               return ("checklist", "Updating the plan", "Planning")
        case has("task", "agent", "delegate", "spawn"): return ("person.2.fill", "Delegating to a sub-agent", "Delegating")
        case has("list", "ls", "tree", "dir"):          return ("folder", "Listing files", "Listing")
        case has("test", "build", "run"):               return ("hammer", "Running tests/build", "Testing")
        default:                                        return ("wrench.and.screwdriver", tool.capitalized, tool.capitalized)
        }
    }
}

/// A compact three-dot pulse (the animated "…" used inline in the tool-activity chip).
struct TypingDots: View {
    let palette: OculusPalette
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @State private var animating = false

    var body: some View {
        HStack(spacing: 3) {
            ForEach(0..<3, id: \.self) { i in
                Circle().fill(palette.primary.opacity(0.7))
                    .frame(width: 4, height: 4)
                    // Was `phase == Double(i)` — an exact Double equality test against a value being
                    // continuously interpolated 0→2. That is essentially never true, so all three dots
                    // sat at 0.3 forever and the "animation" never appeared. The travelling pulse comes
                    // from three independent repeating animations offset by a staggered delay.
                    .opacity(animating ? 1 : 0.3)
                    .animation(reduceMotion
                               ? nil
                               : .easeInOut(duration: 0.5).repeatForever(autoreverses: true).delay(Double(i) * 0.15),
                               value: animating)
            }
        }
        // Under Reduce Motion the dots hold at full opacity: still a visible "…", just not moving.
        .onAppear { animating = true }
        .accessibilityHidden(true) // decorative; the chip's text already says what's happening
    }
}

// MARK: - Approval card

/// Speaks a fact to VoiceOver that has no on-screen focus change to carry it.
///
/// The transcript streams silently — there are no announcements anywhere in this app — so a
/// VoiceOver user has no way to learn that the agent stopped and is blocked waiting on them. They
/// just hear nothing and assume it's still working.
func announceToAccessibility(_ message: String) {
    #if canImport(UIKit)
    if #available(iOS 17.0, *) {
        AccessibilityNotification.Announcement(message).post()
    } else {
        UIAccessibility.post(notification: .announcement, argument: message)
    }
    #elseif canImport(AppKit)
    if #available(macOS 14.0, *) {
        AccessibilityNotification.Announcement(message).post()
    } else if let window = NSApp.keyWindow {
        NSAccessibility.post(element: window, notification: .announcementRequested, userInfo: [
            .announcement: message,
            .priority: NSAccessibilityPriorityLevel.high.rawValue,
        ])
    }
    #endif
}

struct ApprovalCard: View {
    let approval: ApprovalRequest
    let palette: OculusPalette
    let onAllow: () -> Void
    /// nil scope = the broad "this tool, everywhere" rule (what Always always meant).
    let onAlways: (ApprovalScope?) -> Void
    let onDeny: () -> Void

    @State private var showArgs: Bool
    @State private var confirmingBroadAlways = false

    /// Tools that can write, execute, or otherwise change the machine. An approval for one of these
    /// is not undone by closing a window, so the card treats them louder everywhere below.
    private static let writeCapableTools: Set<String> = [
        "bash", "write", "edit", "multiedit", "notebookedit",
    ]

    /// Fragments that make a payload dangerous no matter which tool carries it — a `Read` whose
    /// argument is a piped installer is not a read.
    private static let dangerousFragments = [
        "rm ", "rm -", "sudo", "--force", "-force", "curl", "wget", "| sh", "|sh",
        "git push", "chmod", "dd ",
    ]

    static func isWriteCapable(_ tool: String) -> Bool {
        writeCapableTools.contains(tool.lowercased())
    }

    /// Whether this request gets the loud treatment. Deliberately errs toward loud: a false alarm
    /// costs a glance, a missed one costs an `rm -rf`.
    static func isRisky(_ approval: ApprovalRequest) -> Bool {
        if isWriteCapable(approval.tool) { return true }
        let haystack = ((approval.detail ?? "") + " " + (approval.input?.prettyJSON ?? "")).lowercased()
        return dangerousFragments.contains { haystack.contains($0) }
    }

    /// Arguments start OPEN whenever the decision actually turns on them — a write-capable tool, or a
    /// payload short enough to take in at a glance. Defaulting them collapsed meant the common case
    /// was approving a command nobody ever saw, which is exactly the failure this disclosure exists
    /// to prevent. Long read-only payloads stay folded so the card doesn't swallow the screen.
    static func argsOpenByDefault(_ approval: ApprovalRequest) -> Bool {
        if isWriteCapable(approval.tool) { return true }
        guard let pretty = approval.input?.prettyJSON else { return false }
        return pretty.components(separatedBy: "\n").count <= 15
    }

    init(approval: ApprovalRequest, palette: OculusPalette,
         onAllow: @escaping () -> Void,
         onAlways: @escaping (ApprovalScope?) -> Void,
         onDeny: @escaping () -> Void) {
        self.approval = approval
        self.palette = palette
        self.onAllow = onAllow
        self.onAlways = onAlways
        self.onDeny = onDeny
        _showArgs = State(initialValue: Self.argsOpenByDefault(approval))
    }

    private var risky: Bool { Self.isRisky(approval) }
    /// The card's accent — border, header glyph, and the Allow button when the request is risky.
    private var accent: Color { risky ? palette.destructive : palette.primary }

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack(spacing: 8) {
                Image(systemName: risky ? "exclamationmark.triangle.fill" : "bell.badge.fill")
                    .foregroundStyle(accent)
                Text("Approve \(approval.tool)").font(.headline)
                Spacer()
            }
            if let d = approval.detail, !d.isEmpty {
                Text(d)
                    .font(.system(.footnote, design: .monospaced))
                    .padding(8)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .background(palette.input)
                    .clipShape(OculusShape.concentric(outer: 16, padding: 14))
                    .textSelection(.enabled)
            }
            // The exact arguments, when the harness sent them. The one-line detail is a summary;
            // approving a tool without being able to see what it will actually do is the whole
            // failure mode this fixes.
            if let args = approval.input, let pretty = args.prettyJSON, pretty != approval.detail {
                DisclosureGroup(isExpanded: $showArgs) {
                    // No height clamp, and a real reading size rather than 11pt: this was the smallest
                    // text in the app sitting on the single most consequential thing it renders, and
                    // clipping it hid the tail of the very command being approved.
                    ScrollView(.horizontal) {
                        Text(pretty)
                            .font(.system(.footnote, design: .monospaced))
                            .textSelection(.enabled)
                            .padding(8)
                    }
                    .background(palette.input)
                    .clipShape(OculusShape.concentric(outer: 16, padding: 14))
                } label: {
                    Text("Details").font(.caption.weight(.medium))
                        .foregroundStyle(palette.mutedForeground)
                }
            }
            HStack(spacing: 8) {
                if risky {
                    // For a write/exec request the SAFE choice is the prominent one. Allow drops to a
                    // bordered, destructive-tinted button so the "big button = go ahead" reflex no
                    // longer lands on the dangerous action.
                    Button("Deny", action: onDeny)
                        .buttonStyle(.borderedProminent).tint(palette.primary)
                        .keyboardShortcut(.cancelAction)
                    Spacer()
                    Button("Allow", action: onAllow)
                        .buttonStyle(.bordered).tint(palette.destructive)
                } else {
                    Button("Deny", action: onDeny)
                        .buttonStyle(.bordered).tint(palette.destructive)
                        // Allow used to carry `.keyboardShortcut(.defaultAction)`, so Return approved
                        // an agent-proposed command — on a card that animates in directly above the
                        // composer, where the user's hands already are. KeyChangeAlert makes the same
                        // call for the same reason: the destructive option must never be what a
                        // reflexive keypress selects. Here "destructive" is APPROVING, so the
                        // reflexive key is bound to Deny and nothing is bound to Allow.
                        .keyboardShortcut(.cancelAction)
                    Spacer()
                    Button("Allow", action: onAllow)
                        .buttonStyle(.borderedProminent).tint(palette.primary)
                }
            }
            // "Always" gets its own row: stating its real scope takes a full sentence, and that
            // sentence will not fit beside Deny/Allow at any Dynamic Type size.
            alwaysControl
                .frame(maxWidth: .infinity, alignment: .trailing)
        }
        .padding(14)
        .background(palette.card)
        .overlay(OculusShape.rounded(16).strokeBorder(accent.opacity(0.4)))
        .clipShape(OculusShape.rounded(16))
        .padding(.horizontal, 12).padding(.bottom, 6)
        // The run is BLOCKED on this card. Trapping VoiceOver inside it keeps a screen-reader user
        // from wandering off into the transcript behind a decision the agent is waiting on.
        .accessibilityAddTraits(.isModal)
        .onAppear {
            announceToAccessibility("The agent is waiting for your approval to run \(approval.tool).")
        }
    }

    /// "Always" is a plain button when the daemon offered no scopes (older daemon), and a menu when
    /// it did — so the common case stays one tap while "always allow *this command shape*" is
    /// available without the user inventing a pattern.
    @ViewBuilder private var alwaysControl: some View {
        if approval.suggestedScopes.isEmpty {
            // The label used to be the single word "Always", while the rule it writes is "this tool,
            // in every project, forever". One tap silently granting Bash everywhere is not something
            // a user can consent to from a word that never says it. The button now states the scope,
            // and the dialog names exactly what is about to become permanent.
            Button("Always allow \(approval.tool) everywhere") { confirmingBroadAlways = true }
                .buttonStyle(.bordered).tint(palette.primary)
                .font(.footnote)
                .confirmationDialog("Always allow \(approval.tool) everywhere?",
                                    isPresented: $confirmingBroadAlways, titleVisibility: .visible) {
                    Button("Always allow \(approval.tool)", role: .destructive) { onAlways(nil) }
                    Button("Cancel", role: .cancel) {}
                } message: {
                    Text("Every future \(approval.tool) call runs without asking — in every project, in every session on this machine, until you remove the rule.")
                }
        } else {
            Menu {
                ForEach(approval.suggestedScopes) { scope in
                    Button(scope.label) { onAlways(scope) }
                }
            } label: {
                Text("Always…")
            }
            .menuStyle(.borderlessButton)
            .fixedSize()
            .tint(palette.primary)
            .accessibilityLabel("Always allow, choose a scope")
        }
    }
}

/// The agent's live to-do list — a collapsible checklist with progress, above the transcript.
struct TodoBar: View {
    let todos: [Todo]
    let palette: OculusPalette
    @State private var expanded = false

    private var done: Int { todos.filter { $0.status == "completed" }.count }
    private var current: Todo? { todos.first { $0.status == "in_progress" } }

    var body: some View {
        VStack(spacing: 0) {
            Button { withAnimation(.easeInOut(duration: 0.15)) { expanded.toggle() } } label: {
                HStack(spacing: 8) {
                    Image(systemName: "checklist").font(.caption).foregroundStyle(palette.primaryText)
                    Text(current?.content ?? "To-dos").font(.caption).lineLimit(1)
                        .foregroundStyle(current != nil ? palette.foreground : palette.mutedForeground)
                    Spacer()
                    Text("\(done)/\(todos.count)").font(.caption2.monospacedDigit()).foregroundStyle(palette.mutedForeground)
                    Image(systemName: expanded ? "chevron.up" : "chevron.down").font(.caption2).foregroundStyle(palette.mutedForeground)
                }
                .padding(.horizontal, 14).padding(.vertical, 7).contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            if expanded {
                VStack(alignment: .leading, spacing: 3) {
                    ForEach(todos) { td in
                        HStack(alignment: .top, spacing: 7) {
                            Image(systemName: icon(td.status)).font(.caption2).foregroundStyle(color(td.status)).frame(width: 14)
                            Text(td.content).font(.caption)
                                .foregroundStyle(td.status == "completed" ? palette.mutedForeground : palette.foreground)
                                .strikethrough(td.status == "completed")
                            Spacer()
                        }
                    }
                }
                .padding(.horizontal, 14).padding(.bottom, 8)
                .frame(maxWidth: .infinity, alignment: .leading)
            }
        }
        // Opaque, not tinted-translucent: this bar sits directly above a scrolling transcript, and a
        // see-through background let text slide underneath it and show through as a smear.
        .background(palette.background)
        .background(palette.card.opacity(0.4))
        .overlay(alignment: .bottom) { Divider().overlay(palette.border) }
    }

    private func icon(_ s: String) -> String {
        s == "completed" ? "checkmark.circle.fill" : (s == "in_progress" ? "arrow.triangle.2.circlepath" : "circle")
    }
    private func color(_ s: String) -> Color {
        // "completed" is a success state, not a diff-added one — the shared token, not a hardcoded green.
        s == "completed" ? palette.success : (s == "in_progress" ? palette.primary : palette.mutedForeground)
    }
}

/// A compact cost/token meter for the active session (toolbar).
struct UsageChip: View {
    let session: Session
    let palette: OculusPalette

    var body: some View {
        HStack(spacing: 5) {
            Image(systemName: "dollarsign.circle").font(.caption2)
            Text(String(format: "$%.3f", session.costUSD ?? 0)).font(.caption2.monospacedDigit())
            if let tok = tokenText { Text("· \(tok)").font(.caption2.monospacedDigit()).foregroundStyle(palette.mutedForeground) }
        }
        .foregroundStyle(palette.mutedForeground)
        .help("\(session.inputTokens ?? 0) in / \(session.outputTokens ?? 0) out tokens · $\(String(format: "%.4f", session.costUSD ?? 0))")
    }

    private var tokenText: String? {
        let t = (session.inputTokens ?? 0) + (session.outputTokens ?? 0)
        guard t > 0 else { return nil }
        return t >= 1000 ? String(format: "%.1fk", Double(t) / 1000) : "\(t)"
    }
}

/// Compact "on-track" indicator driven by the daemon's heartbeat supervision (toolbar).
struct HeartbeatChip: View {
    let hb: SessionHeartbeat
    let palette: OculusPalette

    var body: some View {
        HStack(spacing: 5) {
            Image(systemName: icon).font(.caption2)
            Text(label).font(.caption2)
            if hb.todosTotal > 0 {
                Text("· \(hb.todosDone)/\(hb.todosTotal)").font(.caption2.monospacedDigit()).foregroundStyle(palette.mutedForeground)
            }
        }
        .foregroundStyle(color)
        .help(helpText)
    }

    private var icon: String {
        switch hb.state {
        case "working": return "waveform.path.ecg"
        case "idle_incomplete": return "arrow.triangle.2.circlepath"
        case "awaiting_input": return "hand.raised"
        case "stalled": return "exclamationmark.triangle"
        case "exhausted": return "bolt.slash"
        case "errored": return "xmark.octagon"
        case "done": return "checkmark.circle"
        default: return "waveform.path.ecg"
        }
    }
    private var label: String {
        switch hb.state {
        case "working": return "On track"
        case "idle_incomplete": return "Nudging"
        case "awaiting_input": return "Needs you"
        case "stalled": return "Stalled"
        case "exhausted": return "Budget used"
        case "errored": return "Error"
        case "done": return "Done"
        default: return hb.state
        }
    }
    private var color: Color {
        switch hb.state {
        case "stalled", "errored", "exhausted": return .orange
        case "awaiting_input": return .yellow
        case "done": return .green
        default: return palette.mutedForeground
        }
    }
    private var helpText: String {
        var s = "Supervision: \(label)"
        if hb.nudgeCount > 0 { s += " · \(hb.nudgeCount) nudges" }
        if hb.budgetUSD > 0 { s += String(format: " · $%.2f/$%.2f", hb.costUSD, hb.budgetUSD) }
        return s
    }
}

/// Shows an agent-authored handoff file — the externalized progress/state a session saves so it
/// survives context compaction. Loads the full markdown from disk via fs.read.
struct HandoffSheet: View {
    @ObservedObject var model: Model
    let entry: HandoffEntry
    let palette: OculusPalette
    let onClose: () -> Void

    @State private var content: String?
    @State private var loadError: String?

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack {
                VStack(alignment: .leading, spacing: 2) {
                    Text(entry.title.isEmpty ? "Handoff" : entry.title).font(.headline)
                    Text(entry.path).font(.caption2).foregroundStyle(palette.mutedForeground).lineLimit(1).truncationMode(.middle)
                }
                Spacer()
                Button("Done", action: onClose).keyboardShortcut(.cancelAction)
            }
            .padding(.horizontal, 16).padding(.vertical, 12)
            Divider()
            ScrollView {
                if let c = content {
                    Text(c)
                        .font(.system(.footnote, design: .monospaced))
                        .textSelection(.enabled)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .padding(16)
                } else if let e = loadError {
                    Text(e).foregroundStyle(.orange).padding(16)
                } else {
                    ProgressView().padding(24)
                }
            }
        }
        .frame(minWidth: 420, minHeight: 360)
        .background(palette.background)
        .task(id: entry.path) {
            do { content = try await model.fsRead(entry.path).content ?? "" }
            catch { loadError = "Couldn't load handoff: \(error.localizedDescription)" }
        }
    }
}

/// The orchestration cockpit: sub-agents delegated from the active session, each with its live
/// heartbeat state + to-do progress, tap to open. Lets a human drive several lanes and see which
/// need attention.
/// An inline, collapsible sub-agent card that lives in the parent transcript at the point the parent
/// delegated (opencode's `task` tool). Collapsed by default — a one-line header (agent badge · title ·
/// live tool activity · running/done) — expanding to the sub-agent's OWN streamed transcript
/// (childMessages[id]) so its work reads inline without leaving the conversation.
struct InlineSubAgentCard: View {
    @ObservedObject var model: Model
    let subAgentID: String
    let title: String
    let palette: OculusPalette

    private var running: Bool { model.subAgentStatus[subAgentID] != "done" && model.subAgentStatus[subAgentID] != "error" }
    private var expanded: Bool { model.expandedChildIDs.contains(subAgentID) }
    private var msgs: [ChatMessage] { model.childMessages[subAgentID] ?? [] }

    private func toggle() { withAnimation(.easeInOut(duration: 0.22)) { model.toggleChildExpanded(subAgentID) } }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack(spacing: 8) {
                Image(systemName: expanded ? "chevron.down" : "chevron.right")
                    .font(.caption2).foregroundStyle(palette.mutedForeground).frame(width: 10)
                Image(systemName: "person.2.fill").font(.caption2).foregroundStyle(palette.primaryText)
                Text(title).font(.caption.bold()).foregroundStyle(palette.foreground).lineLimit(1)
                if running {
                    RunningPulseDot(color: .green, active: true)
                    if let act = model.childActivity[subAgentID] {
                        ToolActivityView(activity: act, palette: palette, compact: true)
                    }
                    if let started = model.subAgentStartedAt[subAgentID] {
                        ElapsedLabel(since: started, palette: palette)
                    }
                } else {
                    Text("done").font(.caption2).foregroundStyle(palette.mutedForeground)
                }
                Spacer()
                if !msgs.isEmpty { Text("\(msgs.count)").font(.caption2.monospacedDigit()).foregroundStyle(palette.mutedForeground) }
            }
            if expanded {
                Divider().overlay(palette.border.opacity(0.5)).padding(.vertical, 6)
                if msgs.isEmpty {
                    Text(running ? "working…" : "no output").font(.caption2).italic()
                        .foregroundStyle(palette.mutedForeground).padding(.vertical, 4).padding(.leading, 4)
                } else {
                    // Cap the expanded peek + scroll internally, so even a huge sub-agent transcript
                    // never balloons the parent chat. The header stays put (one tap to collapse).
                    ScrollView {
                        VStack(alignment: .leading, spacing: 8) {
                            ForEach(msgs) { m in MessageRow(message: m, palette: palette) }
                        }
                        .padding(.leading, 4).padding(.trailing, 2)
                    }
                    .frame(maxHeight: 300)
                }
                // A full-width "Collapse" footer so getting back to the base state is always one obvious
                // tap, no matter how long the transcript is.
                Button(action: toggle) {
                    Label("Collapse", systemImage: "chevron.up").font(.caption2)
                        .frame(maxWidth: .infinity).padding(.top, 6)
                        .foregroundStyle(palette.mutedForeground).contentShape(Rectangle())
                }
                .buttonStyle(.plain)
            }
        }
        .padding(.horizontal, 12).padding(.vertical, 9)
        .background(palette.secondary.opacity(0.3), in: OculusShape.rounded(10))
        .overlay(OculusShape.rounded(10).strokeBorder(palette.primary.opacity(running ? 0.4 : 0.2)))
        // Tap anywhere on the card to expand/collapse. Nested tool cards + the Collapse button are their
        // own tap targets, so tapping INSIDE a running tool toggles that tool — it doesn't fold the
        // whole sub-agent (child taps win over this parent gesture, so inheritance is respected).
        .contentShape(Rectangle())
        .onTapGesture(perform: toggle)
        // A bare tap gesture is not a control to VoiceOver — without these the card announced as
        // unlabelled static text with no hint that it opens.
        .accessibilityAddTraits(.isButton)
        .accessibilityLabel("Sub-agent: \(title), \(running ? "running" : "done")")
        .accessibilityValue(expanded ? "Expanded" : "Collapsed")
    }
}

struct SubAgentsStrip: View {
    @ObservedObject var model: Model
    let children: [Session]
    let palette: OculusPalette
    /// Whole-strip collapse, persisted: with many lanes the strip can dominate the chat, so it folds
    /// to a one-line summary (lane count · how many are working · total spend) and expands on demand.
    @AppStorage("subAgentsStripCollapsed") private var collapsed = false

    /// Combined spend across the parent + all its sub-agents — delegation multiplies sessions, so
    /// the orchestrator watches the total, not just the active lane.
    private var totalCost: Double {
        (model.currentSession?.costUSD ?? 0) + children.reduce(0) { $0 + ($1.costUSD ?? 0) }
    }

    /// Sub-agents actively working right now — surfaced in the collapsed header so you still see
    /// "something's happening" without expanding.
    private var workingCount: Int {
        children.filter { isRunning($0, model.heartbeats[$0.id]) }.count
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            Button { withAnimation(.easeInOut(duration: 0.18)) { collapsed.toggle() } } label: {
                HStack(spacing: 6) {
                    Image(systemName: collapsed ? "chevron.right" : "chevron.down")
                        .font(.caption2).foregroundStyle(palette.mutedForeground).frame(width: 10)
                    Text("Sub-agents").font(.caption2.bold()).foregroundStyle(palette.mutedForeground)
                    if workingCount > 0 {
                        Text("\(workingCount) working")
                            .font(.caption2.bold()).foregroundStyle(palette.primaryText)
                            .padding(.horizontal, 5).padding(.vertical, 1)
                            .background(Capsule().fill(palette.primary.opacity(0.15)))
                    }
                    Spacer()
                    Text(String(format: "total $%.3f · %d lanes", totalCost, children.count + 1))
                        .font(.caption2.monospacedDigit()).foregroundStyle(palette.mutedForeground)
                }
                .padding(.horizontal, 12)
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            if !collapsed {
                VStack(spacing: 6) {
                    ForEach(children) { child in
                        childCard(child)
                    }
                }
                .padding(.horizontal, 12)
            }
        }
        .padding(.vertical, 7)
        .background(palette.secondary.opacity(0.35))
    }

    /// One sub-agent as an expandable lane: a tappable header (chevron · pulse · subtask · live
    /// tool-activity · state/todos · cost · open-full) over an inline, scrollable transcript that
    /// streams the child's tool calls + outputs while expanded.
    @ViewBuilder
    private func childCard(_ child: Session) -> some View {
        let hb = model.heartbeats[child.id]
        let expanded = model.expandedChildIDs.contains(child.id)
        VStack(alignment: .leading, spacing: 0) {
            HStack(spacing: 6) {
                Image(systemName: expanded ? "chevron.down" : "chevron.right")
                    .font(.caption2).foregroundStyle(palette.mutedForeground).frame(width: 10)
                RunningPulseDot(color: dotColor(hb), active: isRunning(child, hb))
                VStack(alignment: .leading, spacing: 1) {
                    Text(child.subtask ?? child.workspaceName ?? "subtask").font(.caption.bold())
                        .lineLimit(1).frame(maxWidth: .infinity, alignment: .leading)
                    if let hb, hb.todosTotal > 0 {
                        Text("\(stateLabel(hb.state)) · \(hb.todosDone)/\(hb.todosTotal)")
                            .font(.caption2).foregroundStyle(palette.mutedForeground)
                    } else if let hb {
                        Text(stateLabel(hb.state)).font(.caption2).foregroundStyle(palette.mutedForeground)
                    }
                }
                if isRunning(child, hb) {
                    ToolActivityView(activity: model.childActivity[child.id], palette: palette, compact: true)
                }
                if let cost = child.costUSD, cost > 0 {
                    Text(String(format: "$%.3f", cost)).font(.caption2.monospacedDigit())
                        .foregroundStyle(palette.mutedForeground)
                }
                // Distinct from expanding (an inline peek): open the child as the full active session.
                Button { Task { await model.openSession(child.id) } } label: {
                    Image(systemName: "arrow.up.forward.app").font(.caption)
                        .foregroundStyle(palette.mutedForeground)
                }
                .buttonStyle(.plain)
                .accessibilityLabel("Open as full session")
                .help("Open as full session")
            }
            .contentShape(Rectangle())
            .onTapGesture { withAnimation(.easeInOut(duration: 0.22)) { model.toggleChildExpanded(child.id) } }
            // Same reason as the tool/sub-agent cards: a tap gesture carries no button trait, label or
            // state of its own, so the lane header was silent to VoiceOver.
            .accessibilityElement(children: .combine)
            .accessibilityAddTraits(.isButton)
            .accessibilityLabel("Sub-agent lane: \(child.subtask ?? child.workspaceName ?? "subtask")")
            .accessibilityValue(expanded ? "Expanded" : "Collapsed")
            if expanded {
                childTranscript(child)
                    .transition(.opacity.combined(with: .move(edge: .top)))
            }
        }
        .padding(.horizontal, 10).padding(.vertical, 6)
        .background(palette.background, in: OculusShape.rounded(8))
        .overlay(OculusShape.rounded(8).strokeBorder(palette.border))
    }

    /// The nested lane: a bordered, scrollable compact transcript of the child's messages (reusing
    /// MessageRow), tinted secondary so it reads as a sub-conversation. A max height + internal scroll
    /// keeps it from shoving the main transcript around.
    @ViewBuilder
    private func childTranscript(_ child: Session) -> some View {
        let msgs = model.childMessages[child.id] ?? []
        VStack(spacing: 0) {
            Divider().padding(.vertical, 6)
            if msgs.isEmpty {
                Text("waiting for activity…")
                    .font(.caption2).italic().foregroundStyle(palette.mutedForeground)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .padding(.vertical, 8).padding(.horizontal, 6)
            } else {
                ScrollView {
                    VStack(alignment: .leading, spacing: 8) {
                        ForEach(msgs) { m in
                            MessageRow(message: m, palette: palette)
                        }
                    }
                    .padding(.vertical, 4).padding(.horizontal, 6)
                }
                .frame(maxHeight: 260)
            }
        }
        .background(palette.secondary.opacity(0.25), in: OculusShape.rounded(6))
    }

    private func isRunning(_ child: Session, _ hb: SessionHeartbeat?) -> Bool {
        hb?.state == "working" || child.status == SessionStatusValue.running
    }

    private func dotColor(_ hb: SessionHeartbeat?) -> Color {
        switch hb?.state {
        case "working", "idle_incomplete": return .green
        case "awaiting_input": return .yellow
        case "stalled", "errored", "exhausted": return .orange
        case "done": return palette.mutedForeground
        default: return palette.mutedForeground
        }
    }
    private func stateLabel(_ s: String) -> String {
        switch s {
        case "working": return "on track"
        case "idle_incomplete": return "nudging"
        case "awaiting_input": return "needs you"
        case "stalled": return "stalled"
        case "exhausted": return "budget used"
        case "errored": return "error"
        case "done": return "done"
        default: return s
        }
    }
}

/// Renders a finished agent message as markdown (reusing MarkdownParser): headings, bullet/ordered
/// lists, fenced code blocks (scrollable), inline emphasis/code/links, and rules. Parsed once when
/// the turn ends — streaming messages stay plain (see MessageRow) to keep typing smooth.
struct ChatMarkdownView: View {
    let text: String
    let palette: OculusPalette
    // User type preferences (Settings → Chat font). Reading them here re-renders + re-parses when the
    // user changes the font, and they participate in the cache key so a restyle doesn't serve a stale
    // AttributedString built with the old font.
    @AppStorage("oculus.chatFontDesign") private var fontDesignRaw = ChatFontDesign.system.rawValue
    @AppStorage("oculus.chatFontScale") private var fontScaleRaw = ChatFontScale.standard.rawValue
    /// Response text, not UI: Claude's own web app renders its answers in serif while keeping UI and
    /// user messages sans, and that split is most of why a long answer reads as prose.
    private var chosen: ChatFontDesign { ChatFontDesign(rawValue: fontDesignRaw) ?? .system }
    private var design: Font.Design { chosen.responseDesign }
    private var factor: CGFloat { ChatFontScale(rawValue: fontScaleRaw)?.factor ?? 1.0 }

    var body: some View {
        let blocks = MarkdownParser.parse(text)
        VStack(alignment: .leading, spacing: 0) {
            ForEach(Array(blocks.enumerated()), id: \.offset) { index, block in
                blockView(block)
                    .padding(.top, index == 0 ? 0 : gapHeight(before: block, after: blocks[index - 1]))
            }
        }
        .foregroundStyle(palette.foreground)
        .lineSpacing(6 * factor)
        .frame(maxWidth: .infinity, alignment: .leading)
        .textSelection(.enabled)
        .contextMenu {
            Button { copyAll() } label: { Label("Copy message", systemImage: "doc.on.doc") }
        }
    }

    @ViewBuilder private func blockView(_ block: MarkdownBlock) -> some View {
        switch block {
        case .heading(let level, let t):
            Text(heading(t, level: level))
                .frame(maxWidth: .infinity, alignment: .leading)
        case .paragraph(let t):
            Text(inline(t))
                .frame(maxWidth: .infinity, alignment: .leading)
        case .bullet(let items):
            VStack(alignment: .leading, spacing: 4 * factor) {
                ForEach(Array(items.enumerated()), id: \.offset) { _, item in
                    HStack(alignment: .firstTextBaseline, spacing: 7) {
                        Text("•").font(bodyFont).foregroundStyle(palette.mutedForeground)
                        Text(inline(item)).frame(maxWidth: .infinity, alignment: .leading)
                    }
                }
            }
        case .ordered(let items):
            VStack(alignment: .leading, spacing: 4 * factor) {
                ForEach(Array(items.enumerated()), id: \.offset) { _, item in
                    HStack(alignment: .firstTextBaseline, spacing: 7) {
                        Text("\(item.num).").font(bodyFont).monospacedDigit().foregroundStyle(palette.mutedForeground)
                        Text(inline(item.text)).frame(maxWidth: .infinity, alignment: .leading)
                    }
                }
            }
        case .code(let language, let code):
            ChatCodeBlockView(code: code, language: language, palette: palette, factor: factor)
        case .image(let alt, let url):
            Text(linkText(alt: alt, url: url)).frame(maxWidth: .infinity, alignment: .leading)
        case .rule:
            Rectangle().fill(palette.border).frame(height: 1).padding(.vertical, 2)
        }
    }

    private func heading(_ text: String, level: Int) -> AttributedString {
        var a = inline(text)
        a.font = scaled(headingSize(level)).bold()
        a.kern = level <= 2 ? -0.3 : -0.15
        return a
    }

    private func linkText(alt: String, url: String) -> AttributedString {
        var a = AttributedString(alt.isEmpty ? url : alt)
        a.font = bodyFont
        if let u = URL(string: url) {
            a.link = u
            a.foregroundColor = palette.primary
            a.underlineStyle = .single
        }
        return a
    }

    private func inline(_ s: String) -> AttributedString {
        var a = (try? AttributedString(markdown: s, options: .init(interpretedSyntax: .inlineOnlyPreservingWhitespace))) ?? AttributedString(s)
        a.font = bodyFont
        // `backtick code` arrived completely unstyled — identical to the prose around it, which
        // defeats the only reason anyone writes it. Style it where the parser marked it.
        for run in a.runs where run.inlinePresentationIntent?.contains(.code) == true {
            a[run.range].font = .system(size: 13 * factor, design: .monospaced)
            a[run.range].backgroundColor = palette.muted.opacity(0.6)
        }
        // Links should look like links.
        for run in a.runs where run.link != nil {
            a[run.range].foregroundColor = palette.primary
            a[run.range].underlineStyle = .single
        }
        return a
    }

    private func gapHeight(before current: MarkdownBlock, after previous: MarkdownBlock) -> CGFloat {
        switch (previous, current) {
        case (_, .heading): return 16 * factor
        case (.heading, _): return 3 * factor
        case (.bullet, .bullet), (.ordered, .ordered): return 4 * factor
        case (_, .code), (.code, _): return 11 * factor
        default: return 9 * factor
        }
    }
    /// A body-sized font in the user's chosen design + scale.
    private var bodyFont: Font { scaled(chosen.responseSize(15)) }
    private func scaled(_ base: CGFloat) -> Font { .system(size: base * factor, design: design) }
    private func headingSize(_ l: Int) -> CGFloat {
        switch l { case 1: return 22; case 2: return 19; case 3: return 16.5; default: return 15 }
    }
    private func copyAll() { chatCopyToPasteboard(text) }
}

private struct ChatCodeBlockView: View {
    let code: String
    let language: String?
    let palette: OculusPalette
    let factor: CGFloat
    @Environment(\.colorScheme) private var colorScheme

    var body: some View {
        let theme = CodeTheme.current(colorScheme)
        let attributed = SyntaxHighlighter.attributedString(code, language: codeLanguage, theme: theme)
        ScrollView(.horizontal, showsIndicators: true) {
            Text(attributed)
                .font(.system(size: 13 * factor, design: .monospaced))
                .padding(10)
                .frame(maxWidth: .infinity, alignment: .leading)
                .textSelection(.enabled)
        }
        .background(theme.background, in: OculusShape.rounded(8))
        .overlay(OculusShape.rounded(8).strokeBorder(palette.border))
        // A persistent copy control, because `.textSelection` inside a horizontally-scrolling view is
        // not a copy path on touch: long-press-then-drag fights the pan gesture, so on a phone there
        // was NO way to get a code block out of the transcript at all.
        .overlay(alignment: .topTrailing) {
            CopyContentButton(text: code, palette: palette, label: "Copy code")
        }
    }

    private var codeLanguage: CodeLanguage {
        switch language?.trimmingCharacters(in: .whitespacesAndNewlines).lowercased() {
        case "swift": return .swift
        case "go", "golang": return .go
        case "js", "javascript", "ts", "typescript", "jsx", "tsx": return .jsTs
        case "py", "python": return .python
        case "rs", "rust", "c", "cpp", "c++", "objc", "objective-c", "java", "kt", "kotlin": return .rustC
        case "json": return .json
        case "md", "markdown": return .markdown
        case "sh", "shell", "bash", "zsh": return .shell
        default: return .plain
        }
    }
}

/// A status dot that gently pulses (scale + halo) while its lane is actively running, so a live
/// sub-agent reads as alive; static when idle/done.
struct RunningPulseDot: View {
    let color: Color
    let active: Bool
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @State private var pulse = false

    var body: some View {
        ZStack {
            if active {
                Circle().fill(color.opacity(0.35))
                    .frame(width: 14, height: 14)
                    .scaleEffect(pulse ? 1.0 : 0.5)
                    .opacity(pulse ? 0 : 0.8)
                    .animation(reduceMotion ? nil : .easeOut(duration: 1.1).repeatForever(autoreverses: false),
                               value: pulse)
            }
            // The solid core is never animated: it's what still says "running" once the halo stops.
            Circle().fill(color).frame(width: 7, height: 7)
        }
        .frame(width: 14, height: 14)
        // Reduce Motion matters more here than anywhere else in this file — one of these renders per
        // running sub-agent, so a fan-out put a dozen simultaneous, unstoppable pulses on screen. With
        // motion reduced the halo simply holds still at its resting size instead of never starting.
        .onAppear { pulse = active && !reduceMotion }
        .onChange(of: active) { on in pulse = on && !reduceMotion }
    }
}

/// Delegates one subtask to a scoped sub-agent. The child is seeded from this session's handoff
/// (state + decisions) plus the subtask and an optional file allowlist — not the transcript — so
/// it starts small. It becomes the active session on launch.
struct DelegateSheet: View {
    @ObservedObject var model: Model
    let palette: OculusPalette
    let onClose: () -> Void

    @State private var subtask = ""
    @State private var filesText = ""
    @State private var autonomous = false

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            HStack {
                Text("Delegate a subtask").font(.headline)
                Spacer()
                Button("Cancel", action: onClose).keyboardShortcut(.cancelAction)
            }
            if model.activeHandoff == nil {
                Label("No handoff saved yet — the child will get the subtask and file list, but no shared state. Ask this session to save a handoff first for richer context.",
                      systemImage: "info.circle")
                    .font(.caption).foregroundStyle(palette.mutedForeground)
            }
            VStack(alignment: .leading, spacing: 4) {
                Text("Subtask").font(.caption).foregroundStyle(palette.mutedForeground)
                TextEditor(text: $subtask)
                    .font(.footnote)
                    .frame(minHeight: 80)
                    .overlay(OculusShape.rounded(6).strokeBorder(palette.border))
            }
            VStack(alignment: .leading, spacing: 4) {
                Text("Files it may change (optional, one per line)").font(.caption).foregroundStyle(palette.mutedForeground)
                TextEditor(text: $filesText)
                    .font(.system(.footnote, design: .monospaced))
                    .frame(minHeight: 54)
                    .plainInput() // paths + branch names: autocorrect turns these into nonsense
                    .overlay(OculusShape.rounded(6).strokeBorder(palette.border))
            }
            Toggle(isOn: $autonomous) {
                Text("Run autonomously (heartbeat keeps it going)").font(.footnote)
            }
            .toggleStyle(.switch).tint(palette.primary)
            HStack {
                Spacer()
                Button {
                    let files = filesText.split(whereSeparator: \.isNewline).map { $0.trimmingCharacters(in: .whitespaces) }.filter { !$0.isEmpty }
                    Task { await model.delegateSubtask(subtask: subtask, files: files.isEmpty ? nil : files, autonomous: autonomous) }
                    onClose()
                } label: { Text("Delegate").frame(minWidth: 72) }
                .buttonStyle(.borderedProminent).tint(palette.primary)
                .keyboardShortcut(.defaultAction)
                .disabled(subtask.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
            }
        }
        .padding(18)
        .frame(minWidth: 440)
        .background(palette.background)
    }
}

/// Reviews a cross-repo workspace: each member repo's branch + change summary, over the combined
/// diff (rendered by the shared DiffReviewView, which reads model.lastDiff).
struct WorkspaceReviewSheet: View {
    @ObservedObject var model: Model
    let palette: OculusPalette
    let onClose: () -> Void

    @State private var prTitle = ""

    private var anyChanges: Bool { model.workspaceDiffs.contains { !$0.diff.isEmpty } }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack {
                VStack(alignment: .leading, spacing: 2) {
                    Text("Workspace review").font(.headline)
                    Text(model.currentSession?.workspaceName ?? "\(model.workspaceDiffs.count) repos")
                        .font(.caption2).foregroundStyle(palette.mutedForeground)
                }
                Spacer()
                Button("Done", action: onClose).keyboardShortcut(.cancelAction)
            }
            .padding(.horizontal, 16).padding(.vertical, 12)
            Divider()
            if !model.workspaceDiffs.isEmpty {
                ScrollView(.horizontal, showsIndicators: false) {
                    HStack(spacing: 8) {
                        ForEach(model.workspaceDiffs) { m in
                            HStack(spacing: 5) {
                                Image(systemName: "arrow.triangle.branch").font(.caption2)
                                Text(m.name).font(.caption.bold())
                                Text(m.diff.isEmpty ? "no changes" : m.branch)
                                    .font(.caption2).foregroundStyle(palette.mutedForeground)
                            }
                            .padding(.horizontal, 9).padding(.vertical, 5)
                            .background(palette.secondary.opacity(0.5), in: Capsule())
                        }
                    }
                    .padding(.horizontal, 16).padding(.vertical, 8)
                }
            }
            DiffReviewView(model: model, palette: palette)
                .padding(.horizontal, 10)
            prBar
        }
        .frame(minWidth: 560, minHeight: 500)
        .background(palette.background)
    }

    // Coordinated multi-PR finish: one shared title → a commit + push + PR per changed repo.
    private var prBar: some View {
        VStack(alignment: .leading, spacing: 8) {
            Divider()
            if !model.workspacePRResults.isEmpty {
                ForEach(model.workspacePRResults) { r in
                    HStack(spacing: 6) {
                        Image(systemName: r.error != nil ? "xmark.circle" : (r.skipped != nil ? "minus.circle" : "checkmark.circle.fill"))
                            .font(.caption2)
                            .foregroundStyle(r.error != nil ? .orange : (r.skipped != nil ? palette.mutedForeground : .green))
                        Text(r.name).font(.caption.bold())
                        Text(r.error ?? r.skipped ?? (r.url ?? (r.pushed ? "pushed \(r.branch)" : "")))
                            .font(.caption2).foregroundStyle(palette.mutedForeground)
                            .lineLimit(1).truncationMode(.middle)
                    }
                }
            }
            HStack(spacing: 8) {
                TextField("PR title (shared across repos)", text: $prTitle)
                    .textFieldStyle(.roundedBorder)
                Button {
                    Task { await model.workspacePR(title: prTitle.isEmpty ? (model.currentSession?.workspaceName ?? "workspace") : prTitle) }
                } label: {
                    if model.workspacePRRunning { ProgressView().controlSize(.small) }
                    else { Label("Open PRs", systemImage: "arrow.up.forward.square") }
                }
                .buttonStyle(.borderedProminent).tint(palette.primary)
                .disabled(model.workspacePRRunning || !anyChanges)
            }
        }
        .padding(.horizontal, 16).padding(.vertical, 12)
    }
}

/// Streams a test/build run's output with a pass/fail header; a failure can be handed to the
/// agent to fix in one tap.
struct TestResultPanel: View {
    @ObservedObject var model: Model
    let palette: OculusPalette

    private var passed: Bool? { model.testResult.map { $0.ok } }

    var body: some View {
        VStack(spacing: 0) {
            HStack(spacing: 8) {
                if model.testRunning {
                    ProgressView().controlSize(.small)
                    Text("Running tests…").font(.caption.bold())
                } else if let r = model.testResult {
                    // Pass/fail is a success/failure reading, not a diff — the semantic tokens rather
                    // than the hardcoded GitHub greens/reds, which didn't darken for light mode.
                    Image(systemName: r.ok ? "checkmark.seal.fill" : "xmark.octagon.fill")
                        .foregroundStyle(r.ok ? palette.success : palette.destructive)
                    Text(r.ok ? "Tests passed" : "Tests failed (exit \(r.exitCode))").font(.caption.bold())
                        .foregroundStyle(r.ok ? palette.success : palette.destructive)
                }
                Spacer()
                if let r = model.testResult, !r.ok {
                    Button {
                        Task { await model.send("The tests are failing (`\(r.command)`). Please investigate and fix them.") }
                    } label: { Label("Fix with agent", systemImage: "wand.and.stars").font(.caption) }
                        .buttonStyle(.plain).foregroundStyle(palette.primaryText)
                }
                Button { model.showTests = false } label: { Image(systemName: "xmark").font(.caption2) }
                    .buttonStyle(.plain).foregroundStyle(palette.mutedForeground)
                    .accessibilityLabel("Close test results")
                    .help("Close test results")
            }
            .padding(.horizontal, 12).padding(.vertical, 7)
            Divider().overlay(palette.border)
            ScrollViewReader { proxy in
                ScrollView {
                    VStack(alignment: .leading, spacing: 0) {
                        ForEach(Array(model.testOutput.enumerated()), id: \.offset) { _, line in
                            Text(line).font(.system(.caption2, design: .monospaced))
                                .foregroundStyle(palette.foreground.opacity(0.9))
                                .frame(maxWidth: .infinity, alignment: .leading).textSelection(.enabled)
                        }
                        Color.clear.frame(height: 1).id("end")
                    }
                    .padding(.horizontal, 12).padding(.vertical, 6)
                }
                .onChange(of: model.testOutput.count) { _ in proxy.scrollTo("end", anchor: .bottom) }
            }
            .frame(maxHeight: 180)
        }
        .background(palette.input)
        .overlay(OculusShape.rounded(14).strokeBorder((passed == false ? palette.destructive : palette.border).opacity(0.5)))
        .clipShape(OculusShape.rounded(14))
        .padding(.horizontal, 12).padding(.bottom, 6)
    }
}


/// Placeholder message bubbles shown while a conversation loads.
///
/// Alternates user-side and agent-side rows with varied widths, because a uniform stack of identical
/// bars reads as a progress bar rather than as a conversation. Bottom-aligned to match the real
/// transcript so the swap is visually stable.
struct TranscriptSkeleton: View {
    let palette: OculusPalette
    @State private var shimmer = false
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    /// Width fractions + sides, fixed rather than random so the skeleton doesn't reshuffle on every
    /// redraw (which would look like content changing under you).
    private static let rows: [(frac: CGFloat, mine: Bool, lines: Int)] = [
        (0.55, true, 1), (0.85, false, 3), (0.40, true, 1),
        (0.92, false, 4), (0.62, true, 2), (0.78, false, 2),
    ]

    var body: some View {
        // One GeometryReader for the whole skeleton (containerRelativeFrame needs macOS 14; we
        // support 13) — and measuring once beats a reader per row.
        GeometryReader { geo in
            VStack(alignment: .leading, spacing: 14) {
                Spacer(minLength: 0) // push the stack to the bottom, where the real transcript sits
                ForEach(Array(Self.rows.enumerated()), id: \.offset) { _, row in
                    bubble(row, width: geo.size.width - 32)
                }
            }
            .padding(.horizontal, 16)
            .padding(.bottom, 12)
            .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .bottom)
        }
        .accessibilityElement()
        .accessibilityLabel("Loading conversation")
        .onAppear { if !reduceMotion { shimmer = true } }
    }

    private func bubble(_ row: (frac: CGFloat, mine: Bool, lines: Int), width: CGFloat) -> some View {
        HStack {
            if row.mine { Spacer(minLength: 40) }
            VStack(alignment: .leading, spacing: 6) {
                ForEach(0..<row.lines, id: \.self) { i in
                    OculusShape.rounded(5)
                        // The last line of a paragraph is short — that irregularity is most of what
                        // makes this read as text rather than as bars.
                        .frame(height: 10)
                        .frame(maxWidth: .infinity)
                        .scaleEffect(x: i == row.lines - 1 && row.lines > 1 ? 0.6 : 1, anchor: .leading)
                }
            }
            .padding(.horizontal, row.mine ? 12 : 0)
            .padding(.vertical, row.mine ? 9 : 0)
            .background(row.mine ? palette.secondary : Color.clear)
            .clipShape(OculusShape.rounded(OculusRadius.md))
            .frame(width: max(width * row.frac, 80), alignment: .leading)
            if !row.mine { Spacer(minLength: 40) }
        }
        .foregroundStyle(palette.mutedForeground)
        .opacity(shimmer ? 0.30 : 0.14)
        .animation(reduceMotion ? nil : .easeInOut(duration: 1.1).repeatForever(autoreverses: true), value: shimmer)
    }
}


/// Insets the top of a transcript's scroll content. `contentMargins` needs macOS 14 / iOS 17; on
/// older systems the transcript keeps its previous flush edge rather than faking it with padding
/// (which would move the content itself, not the scroll inset).
struct TopScrollInset: ViewModifier {
    func body(content: Content) -> some View {
        if #available(macOS 14.0, iOS 17.0, *) {
            content.contentMargins(.top, 10, for: .scrollContent)
        } else {
            content
        }
    }
}
