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

    public init(model: Model) { self.model = model }

    public var body: some View {
        VStack(spacing: 0) {
            transcript
            if let ap = model.pendingApproval {
                ApprovalCard(approval: ap, palette: palette,
                             onAllow: { Task { await model.respond(Decision.allow) } },
                             onAlways: { Task { await model.respond(Decision.always) } },
                             onDeny: { Task { await model.respond(Decision.deny) } })
                    .transition(.move(edge: .bottom).combined(with: .opacity))
            }
            Composer(model: model, draft: $draft, palette: palette)
        }
        .background(palette.background.ignoresSafeArea())
        .animation(.spring(response: 0.35, dampingFraction: 0.85), value: model.pendingApproval)
        .navigationTitle(model.sessionID == nil ? "New session" : statusLabel)
        #if os(iOS)
        .navigationBarTitleDisplayMode(.inline)
        #endif
    }

    private var transcript: some View {
        ScrollViewReader { proxy in
            ScrollView {
                LazyVStack(alignment: .leading, spacing: 12) {
                    if model.messages.isEmpty {
                        emptyState
                    }
                    ForEach(model.messages) { msg in
                        MessageRow(message: msg, palette: palette).id(msg.id)
                    }
                    if model.busy && model.messages.last?.role != .assistant {
                        TypingIndicator(palette: palette).id("typing")
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

    private var emptyState: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text("Start a session").font(.title3.bold())
            Text("Send a prompt to launch an opencode session on your Mac. Steer it, review tool calls, and approve from anywhere.")
                .font(.subheadline).foregroundStyle(palette.mutedForeground)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(.top, 40)
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
                    .padding(.horizontal, 14).padding(.vertical, 10)
                    .background(palette.primary.opacity(0.18))
                    .overlay(RoundedRectangle(cornerRadius: 18).stroke(palette.primary.opacity(0.35)))
                    .clipShape(RoundedRectangle(cornerRadius: 18))
                    .textSelection(.enabled)
            }
        case .assistant:
            Text(LocalizedStringKey(message.text.isEmpty ? "…" : message.text))
                .font(.system(.body, design: message.text.contains("```") ? .monospaced : .default))
                .frame(maxWidth: .infinity, alignment: .leading)
                .textSelection(.enabled)
        case .tool:
            HStack(spacing: 8) {
                Image(systemName: "wrench.and.screwdriver.fill").font(.caption2)
                Text(message.text).font(.system(.caption, design: .monospaced))
            }
            .foregroundStyle(palette.accentForeground)
            .padding(.horizontal, 12).padding(.vertical, 8)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(palette.accent.opacity(0.5))
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
