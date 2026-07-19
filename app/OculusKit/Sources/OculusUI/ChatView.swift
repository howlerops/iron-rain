import SwiftUI
import OculusKit
#if os(iOS)
import PhotosUI
#endif

/// The session conversation surface: a streaming message list with an inline
/// approval card and a sticky composer (attach · voice · send). Sparse, dark,
/// session-first — matching the Oculus/HowlerOps design system.
public struct ChatView: View {
    @ObservedObject var model: Model
    @Environment(\.colorScheme) private var scheme
    private var palette: OculusPalette { .current(scheme) }

    @State private var draft = ""
    @State private var showWorktreePanel = false

    public init(model: Model) { self.model = model }

    private var isWorktreeSession: Bool { model.currentSession?.branch != nil }

    public var body: some View {
        VStack(spacing: 0) {
            if isWorktreeSession { worktreeBanner }
            if model.messages.isEmpty && model.sessionID == nil {
                emptyState
            } else {
                transcript
            }
            if let ap = model.pendingApproval {
                ApprovalCard(approval: ap, palette: palette,
                             onAllow: { Task { await model.respond(Decision.allow) } },
                             onAlways: { Task { await model.respond(Decision.always) } },
                             onDeny: { Task { await model.respond(Decision.deny) } })
                    .transition(.move(edge: .bottom).combined(with: .opacity))
            }
            Composer(model: model, draft: $draft, palette: palette)
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
            if isWorktreeSession {
                ToolbarItem(placement: .primaryAction) {
                    Button { showWorktreePanel = true } label: {
                        Label("Finish worktree", systemImage: "arrow.triangle.branch")
                    }
                }
            }
        }
        .sheet(isPresented: $showWorktreePanel) {
            WorktreePanel(model: model, palette: palette) { showWorktreePanel = false }
        }
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

    private var transcript: some View {
        ScrollViewReader { proxy in
            ScrollView {
                LazyVStack(alignment: .leading, spacing: 12) {
                    ForEach(model.messages) { msg in
                        MessageRow(message: msg, palette: palette)
                    }
                    if model.busy && model.messages.last?.streaming != true {
                        HStack(spacing: 8) {
                            TypingIndicator(palette: palette)
                            if let a = model.activity, !a.isEmpty {
                                Text(a).font(.system(.caption, design: .monospaced))
                                    .foregroundStyle(palette.mutedForeground)
                            }
                        }.id("typing")
                    }
                    Color.clear.frame(height: 1).id("bottom")
                }
                .padding(16)
            }
            .onChange(of: model.messages.count) { _ in withAnimation { proxy.scrollTo("bottom", anchor: .bottom) } }
            .onChange(of: model.messages.last?.text) { _ in proxy.scrollTo("bottom", anchor: .bottom) }
            .onChange(of: model.pendingApproval) { _ in withAnimation { proxy.scrollTo("bottom", anchor: .bottom) } }
        }
    }

    private static let starters = ["Explain this project", "Find and fix a bug", "Review my changes"]

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
            // Render runtime agent output as plain text. Wrapping it in
            // LocalizedStringKey forced Markdown/AttributedString parsing of the
            // whole (growing) message on every streaming token, and misread text
            // containing %, %@, or other format specifiers.
            Text(message.text.isEmpty ? "…" : message.text)
                .font(.system(.body, design: message.text.contains("```") ? .monospaced : .default))
                .frame(maxWidth: .infinity, alignment: .leading)
                .textSelection(.enabled)
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
