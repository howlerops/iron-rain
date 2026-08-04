import SwiftUI
import OculusKit

/// Everything about the open session, on one sheet — the phone's replacement for the Mac toolbar.
///
/// The Mac has room for eight separate controls across the top. A phone does not: the same eight
/// items collapsed into the navigation bar, where the cost meter truncated to "$0.0…", the model
/// name truncated to nothing useful, and every target was a few points wide. Nothing was
/// discoverable and nothing was reliably tappable.
///
/// So on iOS the navigation bar carries one legible summary and this sheet carries the controls,
/// full-width, with labels — the state you glance at stays in the bar, the state you CHANGE lives
/// where there's room to change it.
///
/// Where a control leads once this sheet closes, so the parent can present the destination in
/// `.sheet(onDismiss:)` instead of the sheet racing its own dismissal with a timer.
enum SessionControlsDestination: Hashable {
    case code, design, delegate, worktree, workspace, usage
}

struct SessionControlsSheet: View {
    @ObservedObject var model: Model
    let palette: OculusPalette
    var onClose: () -> Void
    /// Fired after the sheet dismisses itself, so the destination isn't presented underneath it.
    var onOpenCode: () -> Void
    var onOpenDesign: () -> Void
    var onDelegate: () -> Void
    var onWorktree: () -> Void
    var onWorkspace: () -> Void
    var onUsage: () -> Void
    /// Where the sheet wants to go next, published for a parent that presents in `.sheet(onDismiss:)`.
    ///
    /// Optional with a nil default so the existing call site keeps compiling and keeps today's
    /// behaviour. Bind it and `dismissThen` stops racing — see the note on `dismissThen`.
    var destination: Binding<SessionControlsDestination?>? = nil

    private var isWorktree: Bool { model.currentSession?.branch != nil }

    var body: some View {
        OculusSheet(
            title: "Session",
            subtitle: model.currentSession?.name ?? model.currentSession?.title ?? model.currentSession?.cwd,
            palette: palette,
            onClose: onClose
        ) {
            meter
            settings
            tools
        }
        // This is a stack of controls, not a document: at full height it is mostly empty space and
        // it covers the transcript you are reading the controls ABOUT. Medium first, draggable up.
        .presentationDetents([.medium, .large])
        .presentationDragIndicator(.visible)
    }

    // MARK: - What it has cost

    private var meter: some View {
        SheetCard(palette: palette) {
            // One figure and one button, not a column of a table — so the money scales with Dynamic
            // Type like everything else, and the button drops below it rather than being squeezed
            // off the card.
            ViewThatFits(in: .horizontal) {
                HStack(alignment: .firstTextBaseline, spacing: OculusSpace.md) {
                    meterFigure
                    Spacer()
                    allUsageButton
                }
                VStack(alignment: .leading, spacing: OculusSpace.sm) {
                    meterFigure
                    allUsageButton
                }
            }
            if let hb = model.sessionID.flatMap({ model.heartbeats[$0] }) {
                Divider().overlay(palette.border)
                HStack(spacing: OculusSpace.xs) {
                    RunningPulseDot(color: hb.state == "working" ? palette.success : palette.mutedForeground,
                                    active: hb.state == "working")
                    Text(hb.state == "working" ? "Working" : hb.state.capitalized)
                        .font(.caption).foregroundStyle(palette.mutedForeground)
                    Spacer()
                }
            }
        }
    }

    private var meterFigure: some View {
        VStack(alignment: .leading, spacing: 2) {
            Text("THIS SESSION")
                .font(.caption2.weight(.semibold)).tracking(0.8)
                .foregroundStyle(palette.mutedForeground)
            Text(String(format: "$%.3f", model.currentSession?.costUSD ?? 0))
                .font(.system(.title3, design: .rounded).weight(.semibold).monospacedDigit())
                .foregroundStyle(palette.foreground)
                .lineLimit(1).minimumScaleFactor(0.6)
            Text("\(tokens) tokens")
                .font(.caption2.monospacedDigit())
                .foregroundStyle(palette.mutedForeground)
        }
    }

    private var allUsageButton: some View {
        Button("All usage") { dismissThen(.usage, onUsage) }
            .buttonStyle(.bordered)
            #if os(macOS)
            .controlSize(.small)
            #endif
    }

    private var tokens: String {
        let n = (model.currentSession?.inputTokens ?? 0) + (model.currentSession?.outputTokens ?? 0)
        if n >= 1_000_000 { return String(format: "%.1fM", Double(n) / 1_000_000) }
        if n >= 1_000 { return String(format: "%.0fk", Double(n) / 1_000) }
        return "\(n)"
    }

    // MARK: - What it's allowed to do

    private var settings: some View {
        VStack(alignment: .leading, spacing: OculusSpace.xs) {
            sectionHeader("BEHAVIOUR")
            SheetCard(palette: palette) {
                if model.modelEditable {
                    row("Model", systemImage: "cpu") {
                        Menu {
                            if model.sessionModels.isEmpty {
                                Button { Task { await model.loadModels() } } label: {
                                    Label("Reload models", systemImage: "arrow.clockwise")
                                }
                            } else {
                                ForEach(model.sessionModels) { m in
                                    Button { Task { await model.setSessionModel(m) } } label: {
                                        if model.currentModel == m.id { Label(m.name, systemImage: "checkmark") }
                                        else { Text(m.name) }
                                    }
                                }
                            }
                        } label: {
                            pill(model.currentModel?.isEmpty == false ? model.currentModel! : "Default")
                        }
                    }
                    Divider().overlay(palette.border)
                }
                row("Mode", systemImage: SessionMode.isRestricted(model.sessionMode) ? "lock.shield" : "hammer") {
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
                        pill(SessionMode.label(model.sessionMode))
                    }
                }
                Divider().overlay(palette.border)
                Toggle(isOn: Binding(
                    get: { model.autonomous },
                    set: { v in Task { await model.setAutonomy(v) } }
                )) {
                    VStack(alignment: .leading, spacing: 1) {
                        Label("Keep going on its own", systemImage: "bolt.circle")
                            .font(.subheadline).foregroundStyle(palette.foreground)
                        Text("The heartbeat nudges this session until its to-dos are done.")
                            .font(.caption).foregroundStyle(palette.mutedForeground)
                            .fixedSize(horizontal: false, vertical: true)
                    }
                }
                .tint(palette.primary)
            }
        }
    }

    // MARK: - What you can do to it

    private var tools: some View {
        VStack(alignment: .leading, spacing: OculusSpace.xs) {
            sectionHeader("TOOLS")
            SheetCard(palette: palette) {
                if isWorktree {
                    action("Finish worktree", detail: model.currentSession?.branch ?? "review and merge",
                           systemImage: "arrow.triangle.branch", prominent: true) { dismissThen(.worktree, onWorktree) }
                    Divider().overlay(palette.border)
                }
                if model.currentSession?.isWorkspace == true {
                    action("Review workspace", detail: "changes across every repo",
                           systemImage: "folder.badge.magnifyingglass") { dismissThen(.workspace, onWorkspace) }
                    Divider().overlay(palette.border)
                }
                action("Code & changes", detail: "browse files and review the diff",
                       systemImage: "chevron.left.forwardslash.chevron.right") {
                    dismissThen(.code) { model.codeReviewTarget = model.sessionID }
                }
                Divider().overlay(palette.border)
                #if canImport(WebKit)
                action("Browser / Design", detail: "open a page and point at it",
                       systemImage: "safari") { dismissThen(.design, onOpenDesign) }
                Divider().overlay(palette.border)
                #endif
                // Running the suite executes a shell command on the host, so it carries the same
                // owner-only gate as the `!` escape — and says WHY rather than sitting dead.
                action(model.testRunning ? "Running tests…" : "Run tests",
                       detail: model.knownNonOwner ? model.ownerOnlyReason : "run this project's suite",
                       systemImage: ChatView.runTestsSymbol) {
                    onClose(); Task { await model.runTests() }
                }
                .disabled(model.runBusy || model.knownNonOwner)
                Divider().overlay(palette.border)
                action("Delegate subtask", detail: "hand part of this to a sub-agent",
                       systemImage: "arrowshape.turn.up.right") { dismissThen(.delegate, onDelegate) }
                Divider().overlay(palette.border)
                action("Recover session", detail: "re-sync if it looks stuck", systemImage: "bandage") {
                    onClose()
                    if let id = model.sessionID { Task { await model.recoverSession(id) } }
                }
                .disabled(model.busy)
            }
        }
    }

    // MARK: - Pieces

    /// Presenting a second sheet from inside a dismissing one races on iOS, so the destination has to
    /// wait for this sheet to actually be gone.
    ///
    /// The correct wait is the dismissal itself: the parent presents `destination` in its
    /// `.sheet(onDismiss:)`, which fires when the sheet has finished going away — however long that
    /// takes. Bind `destination` and that is what happens.
    ///
    /// Unbound, we fall back to a 250ms sleep, which is a GUESS at the dismissal animation and will
    /// lose the race on a slow device or when Reduce Motion changes the timing. The parent is
    /// ChatView, which this change does not own; adopting the binding there retires the timer.
    private func dismissThen(_ dest: SessionControlsDestination, _ go: @escaping () -> Void) {
        if let destination {
            destination.wrappedValue = dest
            onClose()
            return
        }
        onClose()
        Task { @MainActor in
            try? await Task.sleep(nanoseconds: 250_000_000)
            go()
        }
    }

    private func sectionHeader(_ t: String) -> some View {
        Text(t).font(.caption2.weight(.semibold)).tracking(0.8)
            .foregroundStyle(palette.mutedForeground)
    }

    private func row<T: View>(_ title: String, systemImage: String, @ViewBuilder trailing: () -> T) -> some View {
        HStack(spacing: OculusSpace.sm) {
            Label(title, systemImage: systemImage)
                .font(.subheadline).foregroundStyle(palette.foreground)
            Spacer(minLength: OculusSpace.sm)
            trailing()
        }
    }

    private func pill(_ s: String) -> some View {
        HStack(spacing: 4) {
            Text(s).font(.footnote).lineLimit(1).minimumScaleFactor(0.8)
            Image(systemName: "chevron.up.chevron.down").font(.caption2)
        }
        .foregroundStyle(palette.foreground)
        .padding(.horizontal, OculusSpace.sm).padding(.vertical, 5)
        .background(palette.input)
        .clipShape(OculusShape.rounded(OculusRadius.sm))
    }

    private func action(_ title: String, detail: String, systemImage: String,
                        prominent: Bool = false, go: @escaping () -> Void) -> some View {
        Button(action: go) {
            HStack(spacing: OculusSpace.sm) {
                Image(systemName: systemImage)
                    .font(.footnote)
                    .foregroundStyle(prominent ? palette.primary : palette.mutedForeground)
                    .frame(minWidth: 20)
                VStack(alignment: .leading, spacing: 1) {
                    Text(title).font(.subheadline)
                        .foregroundStyle(prominent ? palette.primary : palette.foreground)
                    // The detail line is what tells you what the action will do — it wraps.
                    Text(detail).font(.caption).foregroundStyle(palette.mutedForeground)
                        .lineLimit(2)
                }
                Spacer(minLength: 0)
                Image(systemName: "chevron.right").font(.caption2)
                    .foregroundStyle(palette.mutedForeground)
                    .accessibilityHidden(true)
            }
            .contentShape(Rectangle())
            .padding(.vertical, 3)
        }
        .buttonStyle(.plain)
    }
}

/// The navigation-bar summary on iOS: what this session is and what it's doing, in the space one
/// button used to waste on a truncated dollar figure. Tapping opens the controls.
struct SessionTitleChip: View {
    @ObservedObject var model: Model
    let palette: OculusPalette
    let status: String

    var body: some View {
        VStack(spacing: 0) {
            HStack(spacing: 4) {
                if model.autonomous {
                    Image(systemName: "bolt.circle.fill").font(.caption2)
                        .foregroundStyle(palette.primary)
                        // The ONLY indication that this session keeps going on its own — nothing
                        // else in the bar says so, so it can't be decorative.
                        .accessibilityLabel("Running autonomously")
                }
                if SessionMode.isRestricted(model.sessionMode) {
                    Image(systemName: "lock.shield").font(.caption2)
                        .foregroundStyle(palette.primary)
                        .accessibilityLabel("Restricted mode: \(SessionMode.label(model.sessionMode))")
                }
                Text(modelName).font(.footnote.weight(.semibold))
                    .lineLimit(1).minimumScaleFactor(0.8)
                    .foregroundStyle(palette.foreground)
                Image(systemName: "chevron.down").font(.caption2.weight(.semibold))
                    .foregroundStyle(palette.mutedForeground)
            }
            Text(status).font(.caption2).lineLimit(1)
                .foregroundStyle(palette.mutedForeground)
        }
        // A navigation-bar title, and the bar's height is not ours to grow. Two lines of type inside
        // 220pt is all there is, so this clamps rather than overrunning the toolbar.
        .dynamicTypeSize(...DynamicTypeSize.accessibility2)
        .frame(maxWidth: 220)
    }

    private var modelName: String {
        guard let m = model.currentModel, !m.isEmpty else { return "Default model" }
        // Provider-qualified ids ("anthropic/claude-opus-4-8") waste the width that matters.
        return m.contains("/") ? String(m.split(separator: "/").last ?? "") : m
    }
}
