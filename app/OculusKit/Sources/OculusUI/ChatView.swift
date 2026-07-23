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

/// The session conversation surface: a streaming message list with an inline
/// approval card and a sticky composer (attach · voice · send). Sparse, dark,
/// session-first — matching the Oculus/HowlerOps design system.
public struct ChatView: View {
    @ObservedObject var model: Model
    @Environment(\.colorScheme) private var scheme
    private var palette: OculusPalette { .current(scheme) }

    @State private var draft = ""
    @State private var anchorTask: Task<Void, Never>?
    /// Explicit bottom re-pins only fire before this time (a short window after a session opens), so
    /// they can't fight defaultScrollAnchor during streaming and bounce the view.
    @State private var initialAnchorDeadline: Date = .distantPast
    @State private var showWorktreePanel = false
    @State private var showHandoff = false
    @State private var showWorkspace = false
    @State private var showDelegate = false

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

    public var body: some View {
        VStack(spacing: 0) {
            if isWorktreeSession { worktreeBanner }
            if isStopped { stoppedBanner }
            if !children.isEmpty { SubAgentsStrip(model: model, children: children, palette: palette) }
            if !model.todos.isEmpty { TodoBar(todos: model.todos, palette: palette) }
            if model.messages.isEmpty && model.sessionID == nil {
                emptyState
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
                             onAlways: { Task { await model.respond(Decision.always) } },
                             onDeny: { Task { await model.respond(Decision.deny) } })
                    .transition(.move(edge: .bottom).combined(with: .opacity))
            }
            if isStopped { restartFooter } else { Composer(model: model, draft: $draft, palette: palette) }
        }
        // NOTE: was `.background(palette.background.ignoresSafeArea())`. In a NavigationSplitView
        // detail column on macOS 26, the ignoresSafeArea inflated ChatView's ideal height, which
        // drove the whole split view to ~1884pt and overflowed the sidebar — but ONLY on the
        // Sessions tab (IssuesView has no ignoresSafeArea and rendered at the correct ~715pt).
        // Plain background keeps the detail bounded to the window.
        .background(palette.background)
        .animation(.spring(response: 0.35, dampingFraction: 0.85), value: model.pendingApproval)
        .navigationTitle(model.sessionID == nil ? "New session" : statusLabel)
        #if os(iOS)
        .navigationBarTitleDisplayMode(.inline)
        #endif
        .toolbar {
            if let s = model.currentSession, (s.costUSD ?? 0) > 0 || (s.inputTokens ?? 0) > 0 {
                ToolbarItem(placement: .automatic) { UsageChip(session: s, palette: palette) }
            }
            if let sid = model.sessionID, let hb = model.heartbeats[sid] {
                ToolbarItem(placement: .automatic) { HeartbeatChip(hb: hb, palette: palette) }
            }
            // The live tool-use chip (what the agent is doing NOW) replaces the old generic running
            // blob in the top bar — a real per-tool icon + word instead of an anonymous pulse.
            if model.busy, model.messages.last?.streaming != true {
                ToolbarItem(placement: .automatic) {
                    ToolActivityView(activity: model.activity, palette: palette, compact: true)
                        .help(model.activity.map { "Agent activity: \($0)" } ?? "Working")
                }
            }
            if runningChildCount > 0 {
                ToolbarItem(placement: .automatic) {
                    HStack(spacing: 5) {
                        RunningPulseDot(color: .green, active: true)
                        Text("\(runningChildCount) agent\(runningChildCount == 1 ? "" : "s")")
                            .font(.caption.weight(.medium)).foregroundStyle(palette.mutedForeground)
                    }
                    .help("\(runningChildCount) sub-agent\(runningChildCount == 1 ? " is" : "s are") running")
                }
            }
            if model.activeHandoff != nil {
                ToolbarItem(placement: .automatic) {
                    Button { showHandoff = true } label: {
                        Label("Handoff", systemImage: "doc.text.magnifyingglass")
                    }
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
                ToolbarItem(placement: .automatic) {
                    Button { Task { await model.runTests() } } label: {
                        Label("Run tests", systemImage: Self.runTestsSymbol)
                    }
                    .disabled(model.testRunning)
                    .help("Run tests")
                }
                ToolbarItem(placement: .automatic) {
                    Button { showDelegate = true } label: {
                        Label("Delegate subtask", systemImage: "arrowshape.turn.up.right")
                    }
                    .help("Delegate a subtask — spawn a scoped sub-agent, seeded from this session's handoff.")
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
            .foregroundStyle(palette.primary)
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
                    ForEach(model.messages) { msg in
                        MessageRow(message: msg, palette: palette)
                    }
                    Color.clear.frame(height: 1).id("bottom")
                }
                .padding(16)
            }
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
            // Re-pin to the bottom, but ONLY during a short window right after a session opens — that's
            // when the async history burst arrives and defaultScrollAnchor (which anchored the empty
            // ScrollView) hasn't caught it. Firing on EVERY message-count change forever made the
            // explicit scrollTo fight defaultScrollAnchor while streaming markdown heights fluctuate,
            // which oscillated the view up/down during heavy multi-agent runs. After the window, native
            // bottom-anchoring alone follows streaming smoothly.
            .onAppear { armInitialAnchor(); anchorBottom(proxy) }
            .onChange(of: model.sessionID) { _ in armInitialAnchor(); anchorBottom(proxy) }
            .onChange(of: model.messages.count) { _ in
                if Date() < initialAnchorDeadline { anchorBottom(proxy) }
            }
        }
    }

    private func armInitialAnchor() {
        initialAnchorDeadline = Date().addingTimeInterval(1.5) // catch the post-open history burst only
    }

    /// The "working…" indicator, OUTSIDE the scroll content — a fixed-height row so it can toggle
    /// on/off (as busy/streaming flip between sub-agent turns) without ever changing the transcript's
    /// scroll height and bouncing the view.
    @ViewBuilder private var typingBar: some View {
        HStack(spacing: 8) {
            // Show the real tool-use indicator whenever the agent is working but not mid-stream
            // (mid-stream, the text itself is the activity). This is the "what's it doing right now"
            // surface — a proper tool chip, not a generic blob.
            if model.busy && model.messages.last?.streaming != true {
                ToolActivityView(activity: model.activity, palette: palette)
            }
            Spacer(minLength: 0)
        }
        .frame(height: 26) // reserved height → no layout shift when it appears/disappears
        .padding(.horizontal, 16)
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
            Text("Start a session").font(.system(size: 22, weight: .semibold))
            Text("Send a prompt below and an agent gets to work on your Mac — steer it, review tool calls, and approve from anywhere.")
                .font(.system(size: 14)).foregroundStyle(palette.mutedForeground)
                .multilineTextAlignment(.center)
                .frame(maxWidth: 360).fixedSize(horizontal: false, vertical: true)
            HStack(spacing: 8) {
                ForEach(Self.starters, id: \.self) { prompt in
                    Button { draft = prompt } label: {
                        Text(prompt).font(.system(size: 12))
                            .foregroundStyle(palette.foreground)
                            .padding(.horizontal, 12).padding(.vertical, 7)
                            .background(Capsule().fill(palette.muted.opacity(0.45)))
                            .overlay(Capsule().stroke(palette.border))
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

    private var statusLabel: String {
        if model.pendingApproval != nil { return "awaiting approval" }
        if model.busy { return "working…" }
        if model.status == SessionStatusValue.error || model.status == "errored" { return "Error" }
        return model.status
    }

    private func describe(_ d: Discovered) -> String {
        if d.kind == DiscoveredKind.server { return "◆ opencode \(d.url ?? "")" }
        if d.provider == "opencode" { return "  ● \(d.title ?? d.sessionID ?? "session")" }
        return "◆ claude-code \(d.cwd ?? d.sessionID ?? "")"
    }
}

// MARK: - Message row

struct MessageRow: View {
    let message: ChatMessage
    let palette: OculusPalette

    var body: some View {
        switch message.role {
        case .user:
            HStack {
                Spacer(minLength: 40)
                Text(message.text)
                    .foregroundStyle(palette.foreground)
                    .padding(.horizontal, 14).padding(.vertical, 9)
                    .background(palette.secondary)
                    .overlay(RoundedRectangle(cornerRadius: 16).stroke(palette.border))
                    .clipShape(RoundedRectangle(cornerRadius: 16))
                    .textSelection(.enabled)
            }
        case .assistant:
            // Render markdown LIVE as it streams (not raw-until-done) — so headings/lists/code/
            // emphasis appear as they arrive and there's no plain→markdown "jump" at the end.
            // Streaming text is coalesced every ~60ms upstream, so this parses ~16×/s, not per token.
            if message.text.isEmpty && message.streaming {
                Text("…").font(.body).frame(maxWidth: .infinity, alignment: .leading)
            } else {
                ChatMarkdownView(text: message.text, palette: palette)
                    .textSelection(.enabled)
            }
        case .thinking:
            HStack(alignment: .top, spacing: 6) {
                Image(systemName: "brain").font(.caption2).padding(.top, 2)
                Text(message.text).font(.callout).italic()
                    .textSelection(.enabled)
            }
            .foregroundStyle(palette.mutedForeground)
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(.leading, 2)
        case .tool:
            HStack(spacing: 8) {
                Image(systemName: "wrench.and.screwdriver.fill").font(.caption2)
                Text(message.text).font(.system(.caption, design: .monospaced))
            }
            .foregroundStyle(palette.accentForeground)
            .padding(.horizontal, 12).padding(.vertical, 8)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(palette.accent)
            .overlay(RoundedRectangle(cornerRadius: 10).stroke(palette.primary.opacity(0.25)))
            .clipShape(RoundedRectangle(cornerRadius: 10))
        case .system:
            Text(message.text).font(.caption).foregroundStyle(palette.mutedForeground)
                .frame(maxWidth: .infinity, alignment: .center)
        }
    }
}

struct TypingIndicator: View {
    let palette: OculusPalette
    @State private var phase = 0.0
    var body: some View {
        HStack(spacing: 4) {
            ForEach(0..<3) { i in
                Circle().fill(palette.mutedForeground)
                    .frame(width: 6, height: 6)
                    .opacity(phase == Double(i) ? 1 : 0.3)
            }
        }
        .onAppear {
            withAnimation(.easeInOut(duration: 0.5).repeatForever()) { phase = 2 }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
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
    @State private var pulse = false

    var body: some View {
        let t = Self.describe(activity)
        HStack(spacing: 6) {
            Image(systemName: t.icon)
                .font(.caption).foregroundStyle(palette.primary)
                .scaleEffect(pulse ? 1.0 : 0.82)
                .opacity(pulse ? 1 : 0.65)
            Text(compact ? t.short : t.label)
                .font(.caption.weight(.medium)).foregroundStyle(palette.foreground).lineLimit(1)
            if !compact { TypingDots(palette: palette) }
        }
        .padding(.horizontal, 8).padding(.vertical, 3)
        .background(Capsule().fill(palette.primary.opacity(0.12)))
        .overlay(Capsule().stroke(palette.primary.opacity(0.18)))
        .onAppear { withAnimation(.easeInOut(duration: 0.7).repeatForever(autoreverses: true)) { pulse = true } }
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
    @State private var phase = 0.0
    var body: some View {
        HStack(spacing: 3) {
            ForEach(0..<3) { i in
                Circle().fill(palette.primary.opacity(0.7))
                    .frame(width: 4, height: 4)
                    .opacity(phase == Double(i) ? 1 : 0.3)
            }
        }
        .onAppear { withAnimation(.easeInOut(duration: 0.5).repeatForever()) { phase = 2 } }
    }
}

// MARK: - Approval card

struct ApprovalCard: View {
    let approval: ApprovalRequest
    let palette: OculusPalette
    let onAllow: () -> Void
    let onAlways: () -> Void
    let onDeny: () -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack(spacing: 8) {
                Image(systemName: "bell.badge.fill").foregroundStyle(palette.primary)
                Text("Approve \(approval.tool)").font(.headline)
                Spacer()
            }
            if let d = approval.detail, !d.isEmpty {
                Text(d)
                    .font(.system(.footnote, design: .monospaced))
                    .padding(8)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .background(palette.input)
                    .clipShape(RoundedRectangle(cornerRadius: 8))
                    .textSelection(.enabled)
            }
            HStack(spacing: 8) {
                Button("Deny", action: onDeny)
                    .buttonStyle(.bordered).tint(palette.destructive)
                Spacer()
                Button("Always", action: onAlways)
                    .buttonStyle(.bordered).tint(palette.primary)
                Button("Allow", action: onAllow)
                    .buttonStyle(.borderedProminent).tint(palette.primary)
                    .keyboardShortcut(.defaultAction)
            }
        }
        .padding(14)
        .background(palette.card)
        .overlay(RoundedRectangle(cornerRadius: 16).stroke(palette.primary.opacity(0.4)))
        .clipShape(RoundedRectangle(cornerRadius: 16))
        .padding(.horizontal, 12).padding(.bottom, 6)
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
                    Image(systemName: "checklist").font(.caption).foregroundStyle(palette.primary)
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
        .background(palette.card.opacity(0.4))
        .overlay(alignment: .bottom) { Divider().overlay(palette.border) }
    }

    private func icon(_ s: String) -> String {
        s == "completed" ? "checkmark.circle.fill" : (s == "in_progress" ? "arrow.triangle.2.circlepath" : "circle")
    }
    private func color(_ s: String) -> Color {
        s == "completed" ? Color(hex: 0x2EA043) : (s == "in_progress" ? palette.primary : palette.mutedForeground)
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
                        .font(.system(size: 13, design: .monospaced))
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
struct SubAgentsStrip: View {
    @ObservedObject var model: Model
    let children: [Session]
    let palette: OculusPalette

    /// Combined spend across the parent + all its sub-agents — delegation multiplies sessions, so
    /// the orchestrator watches the total, not just the active lane.
    private var totalCost: Double {
        (model.currentSession?.costUSD ?? 0) + children.reduce(0) { $0 + ($1.costUSD ?? 0) }
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack {
                Text("Sub-agents").font(.caption2.bold()).foregroundStyle(palette.mutedForeground)
                Spacer()
                Text(String(format: "total $%.3f · %d lanes", totalCost, children.count + 1))
                    .font(.caption2.monospacedDigit()).foregroundStyle(palette.mutedForeground)
            }
            .padding(.horizontal, 12)
            VStack(spacing: 6) {
                ForEach(children) { child in
                    childCard(child)
                }
            }
            .padding(.horizontal, 12)
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
                .help("Open as full session")
            }
            .contentShape(Rectangle())
            .onTapGesture { model.toggleChildExpanded(child.id) }
            if expanded { childTranscript(child) }
        }
        .padding(.horizontal, 10).padding(.vertical, 6)
        .background(palette.background, in: RoundedRectangle(cornerRadius: 8))
        .overlay(RoundedRectangle(cornerRadius: 8).stroke(palette.border))
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
        .background(palette.secondary.opacity(0.25), in: RoundedRectangle(cornerRadius: 6))
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

    // ONE Text built from a single AttributedString — so a selection can span the whole message
    // (across paragraphs, lists, and code), not just one line. `.textSelection` then copies any
    // chunk. Formatting is carried by per-run fonts (headings, monospace code, bold/italic/links).
    var body: some View {
        Text(attributed())
            .foregroundStyle(palette.foreground)
            .frame(maxWidth: .infinity, alignment: .leading)
            .textSelection(.enabled)
            .contextMenu {
                Button { copyAll() } label: { Label("Copy message", systemImage: "doc.on.doc") }
            }
    }

    // Parsing markdown is not cheap, and `body` re-runs whenever LazyVStack re-lays a row —
    // which happens constantly while scrolling a long transcript. Without memoization, every
    // scroll frame re-parses every visible message, producing the "chunky" scroll. Cache the
    // built AttributedString by message text so an unchanged (finished) message is O(1) on
    // re-layout; a streaming message still re-parses each flush (its text actually changed).
    private static var cache: [String: AttributedString] = [:]
    private static let cacheLimit = 256

    private func attributed() -> AttributedString {
        if let hit = Self.cache[text] { return hit }
        let out = build()
        if Self.cache.count >= Self.cacheLimit { Self.cache.removeAll(keepingCapacity: true) }
        Self.cache[text] = out
        return out
    }

    private func build() -> AttributedString {
        var out = AttributedString()
        let blocks = MarkdownParser.parse(text)
        for (i, b) in blocks.enumerated() {
            if i > 0 { out += AttributedString("\n") } // blank line between blocks
            switch b {
            case .heading(let level, let t):
                var a = inline(t); a.font = headingFont(level).bold()
                out += a + AttributedString("\n")
            case .paragraph(let t):
                var a = inline(t); a.font = .body
                out += a + AttributedString("\n")
            case .bullet(let items):
                for it in items { var a = inline(it); a.font = .body; out += AttributedString("•  ") + a + AttributedString("\n") }
            case .ordered(let items):
                for (idx, it) in items.enumerated() { var a = inline(it); a.font = .body; out += AttributedString("\(idx + 1).  ") + a + AttributedString("\n") }
            case .code(let c):
                var a = AttributedString(c); a.font = .system(.callout, design: .monospaced)
                out += a + AttributedString("\n")
            case .image(let alt, let url):
                var a = AttributedString(alt.isEmpty ? url : alt)
                if let u = URL(string: url) { a.link = u; a.foregroundColor = palette.primary }
                out += a + AttributedString("\n")
            case .rule:
                out += AttributedString("──────────\n")
            }
        }
        return out
    }

    private func inline(_ s: String) -> AttributedString {
        (try? AttributedString(markdown: s, options: .init(interpretedSyntax: .inlineOnlyPreservingWhitespace))) ?? AttributedString(s)
    }
    private func headingFont(_ l: Int) -> Font {
        switch l { case 1: return .title2; case 2: return .title3; case 3: return .headline; default: return .subheadline }
    }
    private func copyAll() {
        #if canImport(AppKit)
        NSPasteboard.general.clearContents(); NSPasteboard.general.setString(text, forType: .string)
        #elseif canImport(UIKit)
        UIPasteboard.general.string = text
        #endif
    }
}

/// A status dot that gently pulses (scale + halo) while its lane is actively running, so a live
/// sub-agent reads as alive; static when idle/done.
struct RunningPulseDot: View {
    let color: Color
    let active: Bool
    @State private var pulse = false

    var body: some View {
        ZStack {
            if active {
                Circle().fill(color.opacity(0.35))
                    .frame(width: 14, height: 14)
                    .scaleEffect(pulse ? 1.0 : 0.5)
                    .opacity(pulse ? 0 : 0.8)
            }
            Circle().fill(color).frame(width: 7, height: 7)
        }
        .frame(width: 14, height: 14)
        .onAppear { if active { withAnimation(.easeOut(duration: 1.1).repeatForever(autoreverses: false)) { pulse = true } } }
        .onChange(of: active) { on in
            pulse = false
            if on { withAnimation(.easeOut(duration: 1.1).repeatForever(autoreverses: false)) { pulse = true } }
        }
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
                    .font(.system(size: 13))
                    .frame(minHeight: 80)
                    .overlay(RoundedRectangle(cornerRadius: 6).stroke(palette.border))
            }
            VStack(alignment: .leading, spacing: 4) {
                Text("Files it may change (optional, one per line)").font(.caption).foregroundStyle(palette.mutedForeground)
                TextEditor(text: $filesText)
                    .font(.system(size: 12, design: .monospaced))
                    .frame(minHeight: 54)
                    .overlay(RoundedRectangle(cornerRadius: 6).stroke(palette.border))
            }
            Toggle(isOn: $autonomous) {
                Text("Run autonomously (heartbeat keeps it going)").font(.system(size: 13))
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
                    Image(systemName: r.ok ? "checkmark.seal.fill" : "xmark.octagon.fill")
                        .foregroundStyle(r.ok ? Color(hex: 0x2EA043) : Color(hex: 0xF85149))
                    Text(r.ok ? "Tests passed" : "Tests failed (exit \(r.exitCode))").font(.caption.bold())
                        .foregroundStyle(r.ok ? Color(hex: 0x2EA043) : Color(hex: 0xF85149))
                }
                Spacer()
                if let r = model.testResult, !r.ok {
                    Button {
                        Task { await model.send("The tests are failing (`\(r.command)`). Please investigate and fix them.") }
                    } label: { Label("Fix with agent", systemImage: "wand.and.stars").font(.caption) }
                        .buttonStyle(.plain).foregroundStyle(palette.primary)
                }
                Button { model.showTests = false } label: { Image(systemName: "xmark").font(.caption2) }
                    .buttonStyle(.plain).foregroundStyle(palette.mutedForeground)
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
        .overlay(RoundedRectangle(cornerRadius: 14).stroke((passed == false ? Color(hex: 0xF85149) : palette.border).opacity(0.5)))
        .clipShape(RoundedRectangle(cornerRadius: 14))
        .padding(.horizontal, 12).padding(.bottom, 6)
    }
}
