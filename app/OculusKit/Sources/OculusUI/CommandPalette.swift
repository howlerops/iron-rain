import SwiftUI
import OculusKit

/// The Cmd-K command palette — one fuzzy entry point spanning destinations, sessions, loops, agents,
/// and quick actions. This is the "feels clean" spine of the Command Deck: velocity for power users
/// without adding a second nav model (the rail/tabs remain the discoverable path). macOS opens it
/// with ⌘K as a centered overlay; iOS presents it as a pull-down search sheet.
struct PaletteItem: Identifiable {
    enum Kind { case destination, session, loop, agent, action }
    let id: String
    let kind: Kind
    let title: String
    let subtitle: String
    let symbol: String
    let run: () -> Void
}

struct CommandPalette: View {
    @ObservedObject var model: Model
    let palette: OculusPalette
    let items: [PaletteItem]
    let onClose: () -> Void

    @State private var query = ""
    /// Which row Return will run. Without this the palette had no selection at all: Return ran
    /// `filtered.first` unconditionally, so every result below the first was unreachable from the
    /// keyboard — in a control whose entire reason to exist is not touching the mouse.
    @State private var selectedIndex = 0
    @FocusState private var focused: Bool

    private var filtered: [PaletteItem] {
        let q = query.trimmingCharacters(in: .whitespaces).lowercased()
        guard !q.isEmpty else { return items }
        return items.filter { fuzzy(q, $0.title.lowercased()) || $0.subtitle.lowercased().contains(q) }
    }

    /// The rows actually rendered. Selection indexes into THIS, never into `filtered`, so the
    /// highlighted row and the row Return runs can't drift apart at the 40-item cutoff.
    private var visible: [PaletteItem] { Array(filtered.prefix(40)) }

    var body: some View {
        VStack(spacing: 0) {
            HStack(spacing: 9) {
                Image(systemName: "magnifyingglass").foregroundStyle(palette.mutedForeground)
                TextField("Jump to a session, loop, agent, or action…", text: $query)
                    .textFieldStyle(.plain)
                    .font(.subheadline)
                    .plainInput()
                    .focused($focused)
                    .onSubmit { runSelected() }
                Text("esc").font(.caption2.monospaced()).foregroundStyle(palette.mutedForeground)
                    .padding(.horizontal, 5).padding(.vertical, 1)
                    .background(OculusShape.rounded(4).fill(palette.secondary))
            }
            .padding(.horizontal, 16).padding(.vertical, 14)
            Divider().overlay(palette.border)
            ScrollViewReader { scroll in
                ScrollView {
                    LazyVStack(spacing: 1) {
                        ForEach(Array(visible.enumerated()), id: \.element.id) { idx, item in
                            Button { item.run(); onClose() } label: { row(item, selected: idx == selectedIndex) }
                                .buttonStyle(.plain)
                                .id(item.id)
                        }
                        if visible.isEmpty {
                            Text("No matches").font(.callout).foregroundStyle(palette.mutedForeground)
                                .frame(maxWidth: .infinity).padding(.vertical, 30)
                        }
                    }
                    .padding(6)
                }
                .frame(maxHeight: 380)
                // Keep the keyboard selection on screen — arrowing past the fold with no scroll is
                // the same unreachable-result bug in a different costume.
                .onChange(of: selectedIndex) { i in
                    guard visible.indices.contains(i) else { return }
                    withAnimation(.easeOut(duration: 0.12)) { scroll.scrollTo(visible[i].id, anchor: .center) }
                }
            }
        }
        .background(palette.card)
        .overlay(OculusShape.rounded(14).strokeBorder(palette.border))
        .clipShape(OculusShape.rounded(14))
        .frame(maxWidth: 560)
        .shadow(color: .black.opacity(0.35), radius: 30, y: 12)
        .onAppear { focused = true }
        // Typing changes the result set under the selection; without these the highlight points at a
        // row that scrolled away or no longer exists, and Return runs the wrong command.
        .onChange(of: query) { _ in selectedIndex = 0 }
        .onChange(of: visible.count) { n in selectedIndex = min(selectedIndex, max(0, n - 1)) }
        // The palette sits over a scrim with no presentation semantics, so VoiceOver would otherwise
        // swipe straight out of it into UI the scrim has made untouchable.
        .accessibilityAddTraits(.isModal)
        .background(
            // ↑/↓ have to be key EQUIVALENTS, not `onMoveCommand`: the palette focuses its search
            // field on open, and AppKit's field editor consumes arrow keys as caret movement before
            // the SwiftUI command ever fires. Key equivalents are matched ahead of the field editor.
            // (`onKeyPress` would be the modern answer but it is macOS 14+, above this app's floor.)
            keyboardCommands.opacity(0)
        )
        #if os(macOS)
        .onMoveCommand { dir in
            switch dir {
            case .up: moveSelection(-1)
            case .down: moveSelection(1)
            default: break
            }
        }
        .onExitCommand { onClose() }
        #endif
    }

    @ViewBuilder private var keyboardCommands: some View {
        #if os(macOS)
        VStack(spacing: 0) {
            Button("") { moveSelection(-1) }.keyboardShortcut(.upArrow, modifiers: [])
            Button("") { moveSelection(1) }.keyboardShortcut(.downArrow, modifiers: [])
        }
        .accessibilityHidden(true)
        #else
        EmptyView()
        #endif
    }

    private func moveSelection(_ delta: Int) {
        guard !visible.isEmpty else { return }
        selectedIndex = min(max(0, selectedIndex + delta), visible.count - 1)
    }

    private func runSelected() {
        guard visible.indices.contains(selectedIndex) else { return }
        visible[selectedIndex].run()
        onClose()
    }

    private func row(_ item: PaletteItem, selected: Bool) -> some View {
        HStack(spacing: 11) {
            Image(systemName: item.symbol).font(.footnote).frame(width: 20)
                .foregroundStyle(item.kind == .action ? palette.primary : palette.mutedForeground)
            VStack(alignment: .leading, spacing: 1) {
                // Two lines for the title: session names are branch-shaped and a one-line clamp at a
                // large text size truncated most of them to "feature/add-…".
                Text(item.title).font(.footnote).foregroundStyle(palette.foreground).lineLimit(2)
                if !item.subtitle.isEmpty {
                    Text(item.subtitle).font(.caption.monospaced())
                        .foregroundStyle(palette.mutedForeground).lineLimit(1)
                }
            }
            Spacer(minLength: 4)
            Text(kindLabel(item.kind)).font(.caption2.weight(.semibold)).tracking(0.5)
                .foregroundStyle(palette.mutedForeground)
                .padding(.horizontal, 5).padding(.vertical, 2)
                .background(OculusShape.rounded(4).fill(palette.secondary))
        }
        .padding(.horizontal, 10).padding(.vertical, 8)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(OculusShape.rounded(6).fill(selected ? palette.primary.opacity(0.16) : .clear))
        .contentShape(Rectangle())
    }

    private func kindLabel(_ k: PaletteItem.Kind) -> String {
        switch k {
        case .destination: return "GO"
        case .session: return "SESSION"
        case .loop: return "LOOP"
        case .agent: return "AGENT"
        case .action: return "ACTION"
        }
    }

    /// Subsequence fuzzy match ("rlyfx" matches "relay-fix").
    private func fuzzy(_ needle: String, _ hay: String) -> Bool {
        if hay.contains(needle) { return true }
        var it = hay.makeIterator()
        for ch in needle {
            var found = false
            while let h = it.next() { if h == ch { found = true; break } }
            if !found { return false }
        }
        return true
    }
}
