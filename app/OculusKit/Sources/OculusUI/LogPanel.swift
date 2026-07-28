import SwiftUI
import OculusKit
#if canImport(AppKit)
import AppKit
#endif
#if canImport(UIKit)
import UIKit
#endif

/// VS-Code-style collapsible bottom panel that tails the connected daemon's live log — local OR
/// remote — so "check the logs" never means shelling into the machine. Collapsed it's a thin status
/// strip; expanded it streams the daemon's `log` output (monospace, auto-scrolling, copyable).
/// Wired via `.safeAreaInset(edge: .bottom)` on the main surface, so it docks under everything.
struct DaemonLogPanel: View {
    @ObservedObject var model: Model
    let palette: OculusPalette
    /// Tapping the always-on activity summary jumps to the Activity destination (nil = no-op).
    var onOpenActivity: (() -> Void)? = nil

    private var runningCount: Int { model.sessions.filter { $0.status == SessionStatusValue.running }.count }

    var body: some View {
        VStack(spacing: 0) {
            if model.showLogPanel {
                Divider().overlay(palette.border)
                logBody
                    .frame(height: 240)
                    .transition(.move(edge: .bottom).combined(with: .opacity))
            }
            statusStrip
        }
        .background(palette.card)
        .animation(.easeInOut(duration: 0.18), value: model.showLogPanel)
    }

    // The always-present strip (VS Code's bottom status bar): click to toggle the panel.
    private var statusStrip: some View {
        HStack(spacing: 8) {
            // Always-on activity summary (the ticker): running / needs-you counts across all
            // sessions. Tap to drill into the Activity destination. This is the ambient glance;
            // Activity is the full inbox.
            Button { onOpenActivity?() } label: {
                HStack(spacing: 8) {
                    if runningCount > 0 {
                        HStack(spacing: 4) {
                            Circle().fill(palette.primary).frame(width: 6, height: 6)
                            Text("\(runningCount) running").font(.system(size: 11))
                        }.foregroundStyle(palette.foreground)
                    }
                    if model.needsYouCount > 0 {
                        HStack(spacing: 4) {
                            Image(systemName: "exclamationmark.triangle.fill").font(.system(size: 9))
                            Text("\(model.needsYouCount) need you").font(.system(size: 11, weight: .medium))
                        }.foregroundStyle(Color(hex: 0xE0912A))
                    }
                    if runningCount == 0 && model.needsYouCount == 0 {
                        Text("Idle").font(.system(size: 11)).foregroundStyle(palette.mutedForeground)
                    }
                }
            }
            .buttonStyle(.plain)
            .disabled(onOpenActivity == nil)

            Divider().frame(height: 12).overlay(palette.border)

            Button {
                if model.showLogPanel { model.closeLogPanel() } else { model.openLogPanel() }
            } label: {
                HStack(spacing: 6) {
                    Image(systemName: model.showLogPanel ? "chevron.down" : "chevron.up")
                        .font(.system(size: 9, weight: .bold))
                    Image(systemName: "terminal")
                        .font(.system(size: 10))
                    Text("Daemon Logs")
                        .font(.system(size: 11, weight: .medium))
                    if !model.daemonLog.isEmpty {
                        Text("\(model.daemonLog.count)")
                            .font(.system(size: 10, design: .monospaced))
                            .foregroundStyle(palette.mutedForeground)
                    }
                }
                .foregroundStyle(palette.foreground)
            }
            .buttonStyle(.plain)

            Spacer()

            if model.showLogPanel {
                Button { copyAll() } label: {
                    Image(systemName: "doc.on.doc").font(.system(size: 10))
                }
                .buttonStyle(.plain).help("Copy all")
                Button { model.clearDaemonLog() } label: {
                    Image(systemName: "trash").font(.system(size: 10))
                }
                .buttonStyle(.plain).help("Clear")
                .foregroundStyle(palette.mutedForeground)
            }
        }
        .padding(.horizontal, 12)
        .frame(height: 26)
        .background(palette.secondary)
        .foregroundStyle(palette.mutedForeground)
    }

    private var logBody: some View {
        ScrollViewReader { proxy in
            ScrollView {
                LazyVStack(alignment: .leading, spacing: 1) {
                    ForEach(Array(model.daemonLog.enumerated()), id: \.offset) { pair in
                        Text(pair.element.isEmpty ? " " : pair.element)
                            .font(.system(size: 11, design: .monospaced))
                            .foregroundStyle(color(for: pair.element))
                            .textSelection(.enabled)
                            .frame(maxWidth: .infinity, alignment: .leading)
                            .id(pair.offset)
                    }
                    Color.clear.frame(height: 1).id(bottomID)
                }
                .padding(.horizontal, 12)
                .padding(.vertical, 8)
            }
            .background(palette.background)
            .overlay(alignment: .center) {
                if model.daemonLog.isEmpty {
                    Text("Waiting for daemon output…")
                        .font(.system(size: 11))
                        .foregroundStyle(palette.mutedForeground)
                }
            }
            .onChange(of: model.daemonLog.count) { _ in
                withAnimation(.linear(duration: 0.1)) { proxy.scrollTo(bottomID, anchor: .bottom) }
            }
            .onAppear { proxy.scrollTo(bottomID, anchor: .bottom) }
        }
    }

    private let bottomID = "log-bottom"

    // Tint obvious severities so warnings/errors stand out in the stream.
    private func color(for line: String) -> Color {
        let l = line.lowercased()
        if l.contains("error") || l.contains("failed") || l.contains("panic") { return palette.destructive }
        if l.contains("warn") { return OculusPalette.brandGold }
        return palette.foreground
    }

    private func copyAll() {
        let text = model.daemonLog.joined(separator: "\n")
        #if canImport(AppKit)
        NSPasteboard.general.clearContents()
        NSPasteboard.general.setString(text, forType: .string)
        #elseif canImport(UIKit)
        UIPasteboard.general.string = text
        #endif
    }
}
