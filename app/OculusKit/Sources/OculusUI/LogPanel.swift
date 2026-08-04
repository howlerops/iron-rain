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
                            // A glyph, not a bare dot: this strip is the ambient status for the whole
                            // app, and "running" was carried by a gold circle whose only difference
                            // from the amber needs-you state was hue.
                            Image(systemName: "bolt.fill").font(.caption2)
                                .foregroundStyle(palette.primary)
                            Text("\(runningCount) running").font(.caption)
                        }.foregroundStyle(palette.foreground)
                    }
                    if model.needsYouCount > 0 {
                        HStack(spacing: 4) {
                            Image(systemName: "exclamationmark.triangle.fill").font(.caption2)
                            Text("\(model.needsYouCount) need you").font(.caption.weight(.medium))
                        }.foregroundStyle(palette.warning)
                    }
                    if runningCount == 0 && model.needsYouCount == 0 {
                        Text("Idle").font(.caption).foregroundStyle(palette.mutedForeground)
                    }
                }
            }
            .buttonStyle(.plain)
            .disabled(onOpenActivity == nil)
            .accessibilityHint("Opens Activity")

            Divider().frame(height: 12).overlay(palette.border)

            Button {
                if model.showLogPanel { model.closeLogPanel() } else { model.openLogPanel() }
            } label: {
                HStack(spacing: 6) {
                    Image(systemName: model.showLogPanel ? "chevron.down" : "chevron.up")
                        .font(.caption2.weight(.bold))
                    Image(systemName: "terminal")
                        .font(.caption2)
                    Text("Daemon Logs")
                        .font(.caption.weight(.medium))
                    if !model.daemonLog.isEmpty {
                        Text("\(model.daemonLog.count)")
                            .font(.system(.caption2, design: .monospaced))
                            .foregroundStyle(palette.mutedForeground)
                    }
                }
                .foregroundStyle(palette.foreground)
            }
            .buttonStyle(.plain)

            Spacer()

            if model.showLogPanel {
                // `.help` supplies the tooltip/HINT only — without a label VoiceOver announces these
                // as an unnamed "Button".
                Button { copyAll() } label: {
                    Image(systemName: "doc.on.doc").font(.caption2)
                }
                .buttonStyle(.plain).help("Copy all")
                .accessibilityLabel("Copy all log output")
                Button { model.clearDaemonLog() } label: {
                    Image(systemName: "trash").font(.caption2)
                }
                .buttonStyle(.plain).help("Clear")
                .accessibilityLabel("Clear the log")
                .foregroundStyle(palette.mutedForeground)
            }
        }
        .padding(.horizontal, 12)
        // A single-line status bar docked under the whole app: it can afford to grow taller, but not
        // to grow so wide that the counts, the label and the two buttons stop sharing one line.
        .dynamicTypeSize(...DynamicTypeSize.accessibility2)
        .frame(minHeight: 26)
        .background(palette.secondary)
        .foregroundStyle(palette.mutedForeground)
    }

    private var logBody: some View {
        ScrollViewReader { proxy in
            ScrollView {
                LazyVStack(alignment: .leading, spacing: 1) {
                    ForEach(Array(model.daemonLog.enumerated()), id: \.offset) { pair in
                        // Deliberately NOT Dynamic Type: log lines are column-aligned output, and a
                        // scaling monospaced grid rewraps every timestamp prefix into noise.
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
                        .font(.caption)
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
        if l.contains("warn") { return palette.warning }
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
